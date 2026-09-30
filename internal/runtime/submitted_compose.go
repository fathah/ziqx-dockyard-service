package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
	"go.yaml.in/yaml/v3"
)

// Submitted files are parsed in memory, never passed to Compose's loader.
// This positive list excludes every host-file, plugin and host-access feature.
type ComposeService struct {
	Image       string            `json:"image"`
	Template    string            `json:"x-dockyard-template,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Command     []string          `json:"command,omitempty"`
	Entrypoint  []string          `json:"entrypoint,omitempty"`
	DependsOn   []string          `json:"depends_on,omitempty"`
	Volumes     []string          `json:"volumes,omitempty"`
	Ports       []string          `json:"ports,omitempty"`
	Expose      []string          `json:"expose,omitempty"`
}
type ComposePlan struct {
	Services map[string]ComposeService `json:"services"`
	Volumes  map[string]map[string]any `json:"volumes,omitempty"`
}

func templateHash(t config.Template) string {
	b, _ := json.Marshal(t)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func ValidateVariables(t config.Template, values map[string]string) error {
	if len(values) > 100 {
		return model.Fail("ENVIRONMENT_INVALID")
	}
	allowed := map[string]bool{}
	for _, key := range t.AllowedEnvironment {
		allowed[key] = true
	}
	for key, value := range values {
		if !allowed[key] || !config.EnvKey.MatchString(key) || len(value) > 8192 || strings.ContainsAny(value, "\r\n\x00") {
			return model.Fail("ENVIRONMENT_INVALID")
		}
	}
	for _, key := range t.RequiredEnvironment {
		if values[key] == "" {
			return model.Fail("ENVIRONMENT_INVALID")
		}
	}
	return nil
}

func ParseCompose(c config.Config, p model.Project, source string) (ComposePlan, error) {
	var plan ComposePlan
	bad := model.Fail("COMPOSE_INVALID")
	if len(source) == 0 || len(source) > 64<<10 || strings.ContainsRune(source, 0) {
		return plan, bad
	}
	d := yaml.NewDecoder(strings.NewReader(source))
	var doc, extra yaml.Node
	if d.Decode(&doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || d.Decode(&extra) != io.EOF {
		return plan, bad
	}
	nodes := 0
	var check func(*yaml.Node, int) bool
	check = func(n *yaml.Node, depth int) bool {
		nodes++
		if nodes > 8192 || depth > 32 || n.Anchor != "" || n.Kind == yaml.AliasNode {
			return false
		}
		switch n.Kind {
		case yaml.MappingNode:
			if n.Tag != "!!map" {
				return false
			}
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i]
				if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Anchor != "" || key.Value == "<<" || seen[key.Value] {
					return false
				}
				seen[key.Value] = true
			}
		case yaml.SequenceNode:
			if n.Tag != "!!seq" {
				return false
			}
		case yaml.ScalarNode:
			if n.Tag != "!!str" && n.Tag != "!!int" && n.Tag != "!!bool" && n.Tag != "!!float" && n.Tag != "!!null" {
				return false
			}
		default:
			return false
		}
		for _, child := range n.Content {
			if !check(child, depth+1) {
				return false
			}
		}
		return true
	}
	if !check(doc.Content[0], 0) {
		return plan, bad
	}
	var tree any
	if doc.Content[0].Decode(&tree) != nil {
		return plan, bad
	}
	b, err := json.Marshal(tree)
	if err != nil || secure.Decode(b, &plan) != nil || len(plan.Services) < 1 || len(plan.Services) > 8 {
		return plan, bad
	}
	if _, ok := plan.Services["app"]; !ok || len(plan.Volumes) > 8 {
		return plan, bad
	}
	if p.ZeroDowntime && len(plan.Volumes) != 0 {
		return plan, model.Fail("COMPOSE_PERSISTENT_BLUE_GREEN")
	}
	usedVolumes := map[string]bool{}
	// Preserve the previous single-service resource ceiling unless the root
	// operator explicitly grants a larger aggregate budget for stacks.
	maxMemory, maxCPUs := c.MaxStackMemoryMB, c.MaxStackCPUs
	for _, t := range c.Templates {
		if c.MaxStackMemoryMB == 0 && t.MemoryMB > maxMemory {
			maxMemory = t.MemoryMB
		}
		if c.MaxStackCPUs == 0 && t.CPUs > maxCPUs {
			maxCPUs = t.CPUs
		}
	}
	memory, cpus := 0, float64(0)
	for name, declaration := range plan.Volumes {
		if !config.ID.MatchString(name) || declaration == nil || len(declaration) != 0 {
			return plan, bad
		}
	}
	for name, service := range plan.Services {
		if !config.ID.MatchString(name) {
			return plan, bad
		}
		if name == "app" {
			if service.Template != "" && service.Template != p.Template {
				return plan, bad
			}
			service.Template = p.Template
		}
		t, ok := c.Templates[service.Template]
		if !ok || !config.Digest.MatchString(service.Image) || !strings.HasPrefix(service.Image, t.ImageRepository+"@sha256:") {
			return plan, model.Fail("IMAGE_INVALID")
		}
		memory += t.MemoryMB
		cpus += t.CPUs
		if service.Environment != nil || name != "app" {
			if err := ValidateVariables(t, service.Environment); err != nil {
				return plan, err
			}
		}
		for _, argv := range [][]string{service.Command, service.Entrypoint} {
			if len(argv) > 32 {
				return plan, bad
			}
			for _, arg := range argv {
				if len(arg) > 1024 || strings.ContainsAny(arg, "\r\n\x00") {
					return plan, bad
				}
			}
		}
		if len(service.Ports) > 1 || name != "app" && len(service.Ports) > 0 || len(service.Expose) > 1 {
			return plan, bad
		}
		for _, port := range service.Ports {
			if port != strconv.Itoa(t.ContainerPort) && port != fmt.Sprintf("127.0.0.1:%d:%d", p.BluePort, t.ContainerPort) {
				return plan, bad
			}
		}
		for _, port := range service.Expose {
			if port != strconv.Itoa(t.ContainerPort) {
				return plan, bad
			}
		}
		if len(service.Volumes) > 8 || p.ZeroDowntime && len(service.Volumes) > 0 {
			return plan, model.Fail("COMPOSE_PERSISTENT_BLUE_GREEN")
		}
		mounts := map[string]bool{}
		for _, mount := range service.Volumes {
			parts := strings.Split(mount, ":")
			if len(parts) < 2 || len(parts) > 3 || len(parts) == 3 && parts[2] != "rw" && parts[2] != "ro" {
				return plan, bad
			}
			if _, ok := plan.Volumes[parts[0]]; !ok || mounts[parts[1]] {
				return plan, bad
			}
			allowed := false
			for _, target := range t.AllowedVolumeTargets {
				allowed = allowed || target == parts[1]
			}
			if !allowed {
				return plan, bad
			}
			mounts[parts[1]] = true
			usedVolumes[parts[0]] = true
		}
		plan.Services[name] = service
	}
	if maxMemory > 0 && memory > maxMemory || maxCPUs > 0 && cpus > maxCPUs {
		return plan, model.Fail("COMPOSE_RESOURCE_LIMIT")
	}
	if len(usedVolumes) != len(plan.Volumes) {
		return plan, bad
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(name string) bool {
		if visiting[name] {
			return false
		}
		if visited[name] {
			return true
		}
		service, exists := plan.Services[name]
		if !exists || len(service.DependsOn) > 8 {
			return false
		}
		visiting[name] = true
		seen := map[string]bool{}
		for _, dependency := range service.DependsOn {
			if seen[dependency] || !visit(dependency) {
				return false
			}
			seen[dependency] = true
		}
		visiting[name], visited[name] = false, true
		return true
	}
	for name := range plan.Services {
		if !visit(name) {
			return plan, bad
		}
	}
	return plan, nil
}

func composeRevisionPath(c config.Config, id, revision string) string {
	return filepath.Join(projectDir(c, id), "compose", revision+".yml")
}

// PrepareCompose stores only our normalized, policy-enforced Compose model.
// Every service environment is outside durable job/audit metadata.
func PrepareCompose(c config.Config, p model.Project, plan ComposePlan, environment string) (string, error) {
	dir := filepath.Join(projectDir(c, p.ID), "compose")
	if err := secure.PrivateDir(dir); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) >= 500 {
		return "", model.Fail("COMPOSE_LIMIT")
	}
	services := map[string]any{}
	for name, service := range plan.Services {
		t := c.Templates[service.Template]
		compiled := hardenedService(c, p, t)
		compiled["image"] = service.Image
		compiled["expose"] = []string{strconv.Itoa(t.ContainerPort)}
		labels := compiled["labels"].(map[string]string)
		labels["io.ziqx.dockyard.template"] = service.Template
		labels["io.ziqx.dockyard.template-revision"] = templateHash(t)
		if name != "app" {
			delete(compiled, "ports")
			// Each revision owns a private directory of companion env files.
			companionDir := filepath.Join(projectDir(c, p.ID), "env", environment)
			if err := secure.PrivateDir(companionDir); err != nil {
				return "", err
			}
			var content strings.Builder
			keys := []string{}
			for key := range service.Environment {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				fmt.Fprintf(&content, "%s=%s\n", key, service.Environment[key])
			}
			// Never overwrite companion files associated with a retained release.
			path := filepath.Join(companionDir, name+".env")
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				return "", model.Fail("ENVIRONMENT_INVALID")
			}
			if err := secure.Atomic(path, []byte(content.String()), 0600); err != nil {
				return "", err
			}
			compiled["env_file"] = []any{map[string]any{"path": "${DOCKYARD_ENV_DIR:?environment directory required}/" + name + ".env", "format": "raw"}}
		}
		if len(service.Command) != 0 {
			compiled["command"] = literalArgs(service.Command)
		}
		if len(service.Entrypoint) != 0 {
			compiled["entrypoint"] = literalArgs(service.Entrypoint)
		}
		if len(service.Volumes) != 0 {
			compiled["volumes"] = service.Volumes
		}
		if len(service.DependsOn) != 0 {
			dependencies := map[string]any{}
			for _, dependency := range service.DependsOn {
				dependencies[dependency] = map[string]any{"condition": "service_healthy"}
			}
			compiled["depends_on"] = dependencies
		}
		services[name] = compiled
	}
	document := map[string]any{"services": services}
	if len(plan.Volumes) != 0 {
		document["volumes"] = plan.Volumes
	}
	b, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	revision := "cmp-" + hex.EncodeToString(h[:])
	if err := secure.Atomic(composeRevisionPath(c, p.ID, revision), b, 0600); err != nil {
		return "", err
	}
	// Older projects used compose.yml directly. Preserve their verified legacy
	// file before activation updates the operator-facing compose.yml mirror.
	legacy := filepath.Join(projectDir(c, p.ID), "compose.legacy.yml")
	if _, err := os.Lstat(legacy); os.IsNotExist(err) {
		want, err := composeBytes(c, p)
		actual, readErr := os.ReadFile(filepath.Join(projectDir(c, p.ID), "compose.yml"))
		if err != nil || readErr != nil || !bytes.Equal(want, actual) {
			return "", model.Uncertain("COMPOSE_CONFIG_DIVERGED")
		}
		if err := secure.Atomic(legacy, want, 0600); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return revision, nil
}

func literalArgs(args []string) []string {
	result := make([]string, len(args))
	for i, value := range args {
		result[i] = strings.ReplaceAll(value, "$", "$$")
	}
	return result
}

var composeRevision = regexp.MustCompile(`^cmp-[0-9a-f]{64}$`)

// ReleaseCompose proves the entire revision before any Compose command.
func ReleaseCompose(c config.Config, p model.Project, r model.Release) (string, []byte, error) {
	if p.NativeCompose() {
		return nativeRelease(c, p, r)
	}
	if r.Compose == "" {
		want, err := composeBytes(c, p)
		path := filepath.Join(projectDir(c, p.ID), "compose.legacy.yml")
		if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
			path = filepath.Join(projectDir(c, p.ID), "compose.yml")
		}
		actual, readErr := os.ReadFile(path)
		if err != nil || readErr != nil || !bytes.Equal(want, actual) {
			return "", nil, model.Uncertain("COMPOSE_CONFIG_DIVERGED")
		}
		return path, actual, nil
	}
	if !composeRevision.MatchString(r.Compose) {
		return "", nil, model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	path := composeRevisionPath(c, p.ID, r.Compose)
	f, err := os.Open(path)
	if err != nil {
		return "", nil, model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	h := sha256.Sum256(b)
	if err != nil || len(b) > 256<<10 || "cmp-"+hex.EncodeToString(h[:]) != r.Compose {
		return "", nil, model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	var document struct {
		Services map[string]runtimeService `json:"services"`
	}
	if json.Unmarshal(b, &document) != nil || document.Services["app"].Image != r.Image {
		return "", nil, model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	for _, service := range document.Services {
		templateID := service.Labels["io.ziqx.dockyard.template"]
		t, exists := c.Templates[templateID]
		if !exists || templateHash(t) != service.Labels["io.ziqx.dockyard.template-revision"] {
			return "", nil, model.Uncertain("TEMPLATE_CHANGED")
		}
	}
	return path, b, nil
}

type runtimeService = model.ServiceSpec

func releaseServices(c config.Config, p model.Project, r model.Release) (map[string]runtimeService, error) {
	_, b, err := ReleaseCompose(c, p, r)
	if err != nil {
		return nil, err
	}
	if p.NativeCompose() {
		return nativeServices(b)
	}
	var document struct {
		Services map[string]runtimeService `json:"services"`
	}
	if err := json.Unmarshal(b, &document); err != nil {
		return nil, model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	return document.Services, nil
}

// IndexCompose imports verified, safe service metadata. Normal reads use SQLite;
// filesystem hashes are still verified when invoking privileged adapters.
func IndexCompose(c config.Config, store *state.Store, p model.Project, r model.Release) error {
	services, err := releaseServices(c, p, r)
	if err != nil {
		return err
	}
	for name, service := range services {
		service.Template = service.Labels["io.ziqx.dockyard.template"]
		if r.Compose == "" {
			service.Template = p.Template
		}
		services[name] = service
	}
	return store.SaveCompose(p.ID, r.Compose, services)
}
