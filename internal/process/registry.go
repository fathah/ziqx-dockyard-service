package process

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// RootDockerConfig is the VPS administrator's Docker login (`docker login` as root).
var RootDockerConfig = "/root/.docker/config.json"

// helperDirs are searched for docker-credential-* programs.
var helperDirs = []string{"/usr/local/bin", "/usr/bin", "/bin"}

// credential runs `docker-credential-<helper> <action>` as root with root's
// real HOME, which helpers such as pass need to find their store.
var credential = func(helper, action, input string) ([]byte, error) {
	var path string
	for _, dir := range helperDirs {
		if p := filepath.Join(dir, "docker-credential-"+helper); fileExists(p) {
			path = p
			break
		}
	}
	if path == "" {
		return nil, fmt.Errorf("docker-credential-%s not found", helper)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, path, action)
	c.Env = []string{"HOME=" + filepath.Dir(filepath.Dir(RootDockerConfig)), "PATH=/usr/local/bin:/usr/bin:/bin"}
	c.Stdin = strings.NewReader(input)
	return c.Output()
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// helperAuths asks a credential helper for its stored logins and returns them
// as plain config.json "auths" entries.
func helperAuths(helper string, registries []string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	if registries == nil {
		b, err := credential(helper, "list", "")
		var listed map[string]string
		if err != nil || json.Unmarshal(b, &listed) != nil {
			return out
		}
		for registry := range listed {
			registries = append(registries, registry)
		}
	}
	for _, registry := range registries {
		b, err := credential(helper, "get", registry)
		var c struct{ Username, Secret string }
		if err != nil || json.Unmarshal(b, &c) != nil || c.Secret == "" {
			continue
		}
		auth := base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + c.Secret))
		out[registry], _ = json.Marshal(map[string]string{"auth": auth})
	}
	return out
}

// syncRegistryAuth writes runtime/config.json: Dockyard's base Docker config
// (read-only) plus the administrator's registry logins as plain entries, so
// images root can pull, Dockyard can pull too. Credential helpers are resolved
// here, as root, because Dockyard runs Docker without root's HOME. Root's login
// wins for a shared registry. It returns a one-line summary for the job log.
func syncRegistryAuth(base, runtime string) string {
	if runtime == "" {
		return ""
	}
	src, err := os.ReadFile(RootDockerConfig)
	if err != nil {
		os.Remove(filepath.Join(runtime, "config.json"))
		return "Registry logins: no root Docker login at " + RootDockerConfig + " (run `docker login` as root for private images)"
	}
	var root struct {
		Auths       map[string]json.RawMessage `json:"auths"`
		CredsStore  string                     `json:"credsStore"`
		CredHelpers map[string]string          `json:"credHelpers"`
	}
	if json.Unmarshal(src, &root) != nil {
		return "Registry logins: " + RootDockerConfig + " is not valid JSON"
	}
	logins := map[string]json.RawMessage{}
	sources := map[string]string{}
	for registry, raw := range root.Auths {
		var entry map[string]any
		if json.Unmarshal(raw, &entry) == nil && entry["auth"] != nil {
			logins[registry], sources[registry] = raw, "root config"
		}
	}
	if root.CredsStore != "" {
		for registry, raw := range helperAuths(root.CredsStore, nil) {
			logins[registry], sources[registry] = raw, "credential helper '"+root.CredsStore+"'"
		}
	}
	for registry, helper := range root.CredHelpers {
		for r, raw := range helperAuths(helper, []string{registry}) {
			logins[r], sources[r] = raw, "credential helper '"+helper+"'"
		}
	}
	if len(logins) == 0 {
		// Without root logins, Docker should read the base config again.
		os.Remove(filepath.Join(runtime, "config.json"))
		if root.CredsStore != "" {
			return fmt.Sprintf("Registry logins: root uses credential helper '%s' but it returned no logins", root.CredsStore)
		}
		return "Registry logins: root's Docker config has no saved logins"
	}

	own := map[string]json.RawMessage{}
	if b, err := os.ReadFile(filepath.Join(base, "config.json")); err == nil && json.Unmarshal(b, &own) != nil {
		return "Registry logins: Dockyard's Docker config is not valid JSON; left unchanged"
	}
	auths := map[string]json.RawMessage{}
	if raw, ok := own["auths"]; ok && json.Unmarshal(raw, &auths) != nil {
		return "Registry logins: Dockyard's Docker config has invalid auths; left unchanged"
	}
	for registry, raw := range logins {
		auths[registry] = raw
	}
	names := make([]string, 0, len(logins))
	for registry := range logins {
		names = append(names, registry+" ("+sources[registry]+")")
	}
	sort.Strings(names)
	summary := "Registry logins: " + strings.Join(names, ", ")
	b, _ := json.Marshal(auths)
	own["auths"] = b
	// Helpers would run without root's HOME; logins are plain entries now.
	delete(own, "credsStore")
	delete(own, "credHelpers")
	target := filepath.Join(runtime, "config.json")
	if current, err := os.ReadFile(target); err == nil {
		var existing map[string]json.RawMessage
		if json.Unmarshal(current, &existing) == nil && reflect.DeepEqual(normalize(existing), normalize(own)) {
			return summary
		}
	}
	if err := os.MkdirAll(runtime, 0700); err != nil {
		return "Registry logins: could not prepare " + runtime + ": " + err.Error()
	}
	if err := writePrivate(runtime, target, own); err != nil {
		return "Registry logins: could not write " + target + ": " + err.Error()
	}
	return summary
}

// normalize decodes raw values so equal JSON compares equal regardless of spacing.
func normalize(m map[string]json.RawMessage) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		var x any
		json.Unmarshal(v, &x)
		out[k] = x
	}
	return out
}

func writePrivate(dir, target string, v any) error {
	b, err := json.MarshalIndent(v, "", "\t")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(bytes.TrimSpace(b)); err == nil {
		err = tmp.Chmod(0600)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}
