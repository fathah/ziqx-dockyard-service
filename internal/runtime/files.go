package runtime

import (
	"encoding/json"
	"fmt"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func projectDir(c config.Config, id string) string { return filepath.Join(c.ProjectsRoot, id) }
func envPath(c config.Config, id, revision string) string {
	return filepath.Join(projectDir(c, id), "env", revision+".env")
}
func bindingPath(c config.Config, id, slot string) string {
	return filepath.Join(projectDir(c, id), slot+".env")
}

func ReadEnvironment(c config.Config, id, revision string) (map[string]string, error) {
	if !regexp.MustCompile(`^env-[0-9a-f]{32}$`).MatchString(revision) {
		return nil, model.Fail("ENVIRONMENT_UNAVAILABLE")
	}
	b, err := os.ReadFile(envPath(c, id, revision))
	if err != nil || len(b) > 128<<10 {
		return nil, model.Fail("ENVIRONMENT_UNAVAILABLE")
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !config.EnvKey.MatchString(key) {
			return nil, model.Fail("ENVIRONMENT_UNAVAILABLE")
		}
		values[key] = value
	}
	return values, nil
}

func Environment(c config.Config, id string, variables map[string]string) (string, error) {
	dir := filepath.Join(projectDir(c, id), "env")
	if e := secure.PrivateDir(dir); e != nil {
		return "", e
	}
	keys := []string{}
	files, e := os.ReadDir(dir)
	if e != nil {
		return "", e
	}
	if len(files) >= 500 {
		return "", model.Fail("ENVIRONMENT_LIMIT")
	}
	for k := range variables {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	// env_file.format: raw (Compose >=2.30) prevents $ expansion and quote parsing.
	// V1 accepts single-line values only; no ambiguous multiline dotenv encoding.
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, variables[k])
	}
	idRev := state.NewID("env-")
	if e := secure.Atomic(envPath(c, id, idRev), []byte(b.String()), 0600); e != nil {
		return "", e
	}
	return idRev, nil
}

func hardenedService(c config.Config, p model.Project, t config.Template) map[string]any {
	hc := append([]string{"CMD"}, t.HealthCommand...)
	// JSON is a YAML subset accepted by Compose. Only local policy selects commands.
	return map[string]any{
		"image":       "${DOCKYARD_IMAGE:?immutable image required}",
		"ports":       []string{fmt.Sprintf("127.0.0.1:${DOCKYARD_PORT:?port required}:%d", t.ContainerPort)},
		"env_file":    []any{map[string]any{"path": "${DOCKYARD_ENV:?environment required}", "format": "raw"}},
		"healthcheck": map[string]any{"test": hc, "interval": "3s", "timeout": "2s", "start_period": "10s", "retries": 10},
		"user":        t.User, "read_only": true, "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"},
		"tmpfs": []string{"/tmp:rw,noexec,nosuid,size=67108864"}, "pids_limit": 256, "mem_limit": fmt.Sprintf("%dm", t.MemoryMB), "cpus": t.CPUs,
		"init": true, "restart": "unless-stopped", "stop_grace_period": "30s",
		"logging": map[string]any{"driver": "json-file", "options": map[string]string{"max-size": "10m", "max-file": "3"}},
		"labels":  map[string]string{"io.ziqx.dockyard.server": c.ServerID, "io.ziqx.dockyard.project": p.ID},
	}
}
func composeBytes(c config.Config, p model.Project) ([]byte, error) {
	v := map[string]any{"services": map[string]any{"app": hardenedService(c, p, c.Templates[p.Template])}}
	return json.MarshalIndent(v, "", "  ")
}
func Compose(c config.Config, p model.Project) error {
	if p.NativeCompose() {
		return secure.PrivateDir(projectDir(c, p.ID))
	}
	b, e := composeBytes(c, p)
	if e != nil {
		return e
	}
	dir := projectDir(c, p.ID)
	if e = secure.PrivateDir(dir); e != nil {
		return e
	}
	if e = secure.Atomic(filepath.Join(dir, "compose.legacy.yml"), b, 0600); e != nil {
		return e
	}
	return secure.Atomic(filepath.Join(dir, "compose.yml"), b, 0600)
}
func Binding(c config.Config, p model.Project, slot string, r model.Release) error {
	if p.NativeCompose() {
		if _, _, err := nativeRelease(c, p, r); err != nil {
			return err
		}
	}
	env := envPath(c, p.ID, r.Environment)
	if _, e := os.Lstat(env); e != nil {
		return e
	}
	b := bindingBytes(c, p, slot, r)
	return secure.Atomic(bindingPath(c, p.ID, slot), []byte(b), 0600)
}

func bindingBytes(c config.Config, p model.Project, slot string, r model.Release) []byte {
	return []byte(fmt.Sprintf("DOCKYARD_COMPOSE_PROJECT=dy-%s-%s-%s\nDOCKYARD_IMAGE=%s\nDOCKYARD_PORT=%d\nDOCKYARD_ENV=%s\nDOCKYARD_ENV_DIR=%s\n", c.ServerID, p.ID, slot, r.Image, p.Port(slot), envPath(c, p.ID, r.Environment), filepath.Join(projectDir(c, p.ID), "env", r.Environment)))
}

func Snippet(p model.Project, slot string) []byte {
	var b strings.Builder
	for _, d := range p.Domains {
		fmt.Fprintf(&b, "%s {\n", d)
		if slot == "" {
			b.WriteString("\theader Retry-After 30\n\trespond \"Application unavailable\" 503\n")
		} else {
			fmt.Fprintf(&b, "\treverse_proxy 127.0.0.1:%d\n", p.Port(slot))
		}
		b.WriteString("}\n")
	}
	return []byte(b.String())
}
