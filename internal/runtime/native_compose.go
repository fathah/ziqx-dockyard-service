package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
)

// Native Compose is an administrator capability, not a sandbox. Docker's own
// loader validates the full Compose model, including host paths and builds.
// Resolved configurations (which can contain secrets) stay in private files.
func (d Docker) PrepareNative(ctx context.Context, p model.Project, source, dotenv string) (model.Release, error) {
	r := model.Release{}
	if len(source) == 0 || len(source) > 64<<10 || len(dotenv) > 64<<10 || strings.ContainsRune(source+dotenv, 0) {
		return r, model.Fail("COMPOSE_INPUT_INVALID")
	}
	dir := projectDir(d.Config, p.ID)
	for _, sub := range []string{"env", "compose", "source"} {
		path := filepath.Join(dir, sub)
		if err := secure.PrivateDir(path); err != nil {
			return r, err
		}
		entries, err := os.ReadDir(path)
		limit := 500
		if sub == "env" {
			limit = 1000
		}
		if err != nil || len(entries) >= limit {
			return r, model.Fail("COMPOSE_LIMIT")
		}
	}
	env := state.NewID("env-")
	saved := false
	defer func() {
		if !saved {
			os.Remove(envPath(d.Config, p.ID, env))
		}
	}()
	if err := secure.Atomic(envPath(d.Config, p.ID, env), []byte(dotenv), 0600); err != nil {
		return r, err
	}
	f, err := os.CreateTemp(dir, ".compose-review-*.yml")
	if err != nil {
		return r, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write([]byte(source))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return r, model.Fail("PROJECT_FILES_FAILED")
	}
	args := d.args(p, "blue", "config", "--format", "json", "--no-env-resolution")
	args[6], args[8] = f.Name(), envPath(d.Config, p.ID, env)
	result, err := d.Runner.Run(ctx, d.Config.DockerBinary, dir, args)
	if err != nil || result.Truncated {
		// Compose diagnostics may interpolate environment secrets or host file
		// content. Never put raw stdout/stderr into HTTP responses or audit.
		return r, model.Fail("COMPOSE_VALIDATION_FAILED")
	}
	// Resolve env_file paths after interpolation/includes, so even an alias or
	// ${ENV_FILE:-.env} uses this deployment's .env rather than the old mirror.
	var paths map[string]any
	if json.Unmarshal(result.Output, &paths) != nil {
		return r, model.Fail("COMPOSE_VALIDATION_FAILED")
	}
	for _, raw := range object(paths["services"]) {
		for _, entry := range array(object(raw)["env_file"]) {
			file := object(entry)
			if path, ok := file["path"].(string); ok && filepath.Clean(path) == filepath.Join(dir, ".env") {
				file["path"] = envPath(d.Config, p.ID, env)
			}
		}
	}
	prepared, err := json.Marshal(paths)
	if err != nil || len(prepared) > 2<<20 {
		return r, model.Fail("COMPOSE_LIMIT")
	}
	if err = os.WriteFile(f.Name(), prepared, 0600); err != nil {
		return r, err
	}
	args = args[:len(args)-1] // Resolve service env_file contents in the second pass.
	result, err = d.Runner.Run(ctx, d.Config.DockerBinary, dir, args)
	if err != nil || result.Truncated {
		return r, model.Fail("COMPOSE_VALIDATION_FAILED")
	}
	// Compose config escapes dollars for reuse. Decode that serialization
	// escape once before collecting secrets and adding slot substitutions.
	var doc map[string]any
	if json.Unmarshal([]byte(strings.ReplaceAll(string(result.Output), "$$", "$")), &doc) != nil {
		return r, model.Fail("COMPOSE_VALIDATION_FAILED")
	}
	if err := compileNative(d.Config, p, doc); err != nil {
		return r, err
	}
	secrets := nativeSecrets(doc)
	// Escape literal dollar signs once after Compose resolves interpolation.
	// Only Dockyard's generated slot port and network substitutions remain.
	escapeNative(doc)
	services := doc["services"].(map[string]any)
	if p.RouteService != "" {
		svc := services[p.RouteService].(map[string]any)
		ports, _ := svc["ports"].([]any)
		ports = append(ports, map[string]any{"target": p.RoutePort, "published": "${DOCKYARD_PORT}", "host_ip": "127.0.0.1", "protocol": "tcp"})
		svc["ports"] = ports
	}
	if p.ZeroDowntime {
		for name, raw := range object(doc["networks"]) {
			network := raw.(map[string]any)
			network["name"] = "${DOCKYARD_COMPOSE_PROJECT}_" + name
		}
	}
	h := sha256.Sum256([]byte(dotenv))
	doc["x-dockyard-env-sha256"] = hex.EncodeToString(h[:])
	h = sha256.Sum256([]byte(source))
	doc["x-dockyard-source-sha256"] = hex.EncodeToString(h[:])
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil || len(b) > 2<<20 {
		return r, model.Fail("COMPOSE_LIMIT")
	}
	h = sha256.Sum256(b)
	revision := "cmp-" + hex.EncodeToString(h[:])
	if err := secure.Atomic(composeRevisionPath(d.Config, p.ID, revision), b, 0600); err != nil {
		return r, err
	}
	if err := secure.Atomic(filepath.Join(dir, "source", revision+".yml"), []byte(source), 0600); err != nil {
		return r, err
	}
	// Redaction includes resolved inline environment and service env_file values.
	redaction, _ := json.Marshal(secrets)
	if err := secure.Atomic(filepath.Join(dir, "env", env+".secrets.json"), redaction, 0600); err != nil {
		return r, err
	}
	saved = true
	return model.Release{Environment: env, Compose: revision}, nil
}

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }

func compileNative(c config.Config, p model.Project, doc map[string]any) error {
	services := object(doc["services"])
	if len(services) == 0 {
		return model.Fail("COMPOSE_NO_ACTIVE_SERVICES")
	}
	delete(doc, "name") // Dockyard owns the Compose identity, not a supplied name.
	if p.RouteService != "" && object(services[p.RouteService]) == nil {
		return model.Fail("COMPOSE_ROUTE_SERVICE_MISSING")
	}
	if p.ZeroDowntime && (p.RouteService == "" || len(p.Domains) == 0) {
		return model.Fail("COMPOSE_BLUE_GREEN_ROUTE_REQUIRED")
	}
	for name, raw := range services {
		svc := object(raw)
		if svc == nil || !config.ServiceName.MatchString(name) {
			return model.Fail("COMPOSE_VALIDATION_FAILED")
		}
		labels := object(svc["labels"])
		if labels == nil {
			labels = map[string]any{}
		}
		labels["io.ziqx.dockyard.server"] = c.ServerID
		labels["io.ziqx.dockyard.project"] = p.ID
		svc["labels"] = labels
		delete(svc, "profiles") // Docker config has already selected active profiles.
		ports, _ := svc["ports"].([]any)
		retained := []any{}
		for _, port := range ports {
			mapping := object(port)
			if name == p.RouteService && fmt.Sprint(mapping["target"]) == strconv.Itoa(p.RoutePort) && (mapping["protocol"] == nil || mapping["protocol"] == "tcp") {
				continue
			}
			retained = append(retained, port)
		}
		svc["ports"] = retained
		if p.ZeroDowntime {
			if len(retained) > 0 || svc["container_name"] != nil || svc["network_mode"] != nil || len(array(svc["volumes"])) > 0 || len(array(svc["devices"])) > 0 || svc["privileged"] == true || svc["pid"] != nil || svc["ipc"] != nil || len(object(svc["develop"])) > 0 {
				return model.Fail("COMPOSE_BLUE_GREEN_INCOMPATIBLE")
			}
			if svc["healthcheck"] == nil {
				return model.Fail("COMPOSE_BLUE_GREEN_HEALTHCHECK_REQUIRED")
			}
			health := object(svc["healthcheck"])
			test := array(health["test"])
			if health["disable"] == true || len(test) == 0 || test[0] == "NONE" {
				return model.Fail("COMPOSE_BLUE_GREEN_HEALTHCHECK_REQUIRED")
			}
		}
	}
	if p.ZeroDowntime {
		if len(object(doc["volumes"])) > 0 {
			return model.Fail("COMPOSE_BLUE_GREEN_INCOMPATIBLE")
		}
		for name, raw := range object(doc["networks"]) {
			network := object(raw)
			if network == nil || network["external"] == true || network["name"] != "dy-"+c.ServerID+"-"+p.ID+"-blue_"+name {
				return model.Fail("COMPOSE_BLUE_GREEN_INCOMPATIBLE")
			}
		}
	}
	return nil
}

func array(v any) []any { a, _ := v.([]any); return a }

func escapeNative(v any) {
	switch n := v.(type) {
	case map[string]any:
		for k, x := range n {
			if s, ok := x.(string); ok {
				n[k] = strings.ReplaceAll(s, "$", "$$")
			} else {
				escapeNative(x)
			}
		}
	case []any:
		for i, x := range n {
			if s, ok := x.(string); ok {
				n[i] = strings.ReplaceAll(s, "$", "$$")
			} else {
				escapeNative(x)
			}
		}
	}
}

func nativeSecrets(doc map[string]any) []string {
	values := []string{}
	for _, raw := range object(doc["services"]) {
		for _, value := range object(object(raw)["environment"]) {
			if s, ok := value.(string); ok && s != "" {
				values = append(values, s)
			}
		}
	}
	for _, raw := range object(doc["secrets"]) {
		if s, ok := object(raw)["content"].(string); ok && s != "" {
			values = append(values, s)
		}
	}
	return values
}

func nativeRelease(c config.Config, p model.Project, r model.Release) (string, []byte, error) {
	if !composeRevision.MatchString(r.Compose) {
		return "", nil, model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	path := composeRevisionPath(c, p.ID, r.Compose)
	f, err := os.Open(path)
	if err != nil {
		return "", nil, model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	h := sha256.Sum256(b)
	if err != nil || len(b) > 2<<20 || "cmp-"+hex.EncodeToString(h[:]) != r.Compose {
		return "", nil, model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil || len(object(doc["services"])) == 0 {
		return "", nil, model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	if !strings.HasPrefix(r.Environment, "env-") || len(r.Environment) != 36 || strings.ContainsAny(r.Environment, "/\\.") {
		return "", nil, model.Uncertain("ENVIRONMENT_IDENTITY_UNKNOWN")
	}
	env, err := os.ReadFile(envPath(c, p.ID, r.Environment))
	h = sha256.Sum256(env)
	if err != nil || doc["x-dockyard-env-sha256"] != hex.EncodeToString(h[:]) {
		return "", nil, model.Uncertain("ENVIRONMENT_IDENTITY_UNKNOWN")
	}
	return path, b, nil
}

func nativeServices(b []byte) (map[string]model.ServiceSpec, error) {
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return nil, model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	out := map[string]model.ServiceSpec{}
	for name, raw := range object(doc["services"]) {
		s := object(raw)
		image, _ := s["image"].(string)
		out[name] = model.ServiceSpec{Image: image, Labels: map[string]string{}, Volumes: []string{}}
	}
	return out, nil
}

func ComposeMirror(c config.Config, p model.Project, r model.Release) ([]byte, error) {
	_, b, err := ReleaseCompose(c, p, r)
	if err != nil || !p.NativeCompose() {
		return b, err
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return nil, model.Fail("COMPOSE_CONFIG_DIVERGED")
	}
	source, err := os.ReadFile(filepath.Join(projectDir(c, p.ID), "source", r.Compose+".yml"))
	h := sha256.Sum256(source)
	if err != nil || doc["x-dockyard-source-sha256"] != hex.EncodeToString(h[:]) {
		return nil, model.Fail("COMPOSE_CONFIG_DIVERGED")
	}
	return source, nil
}
