package process

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupRegistry returns (base, runtime) directories for a root and base config.
func setupRegistry(t *testing.T, rootConfig, baseConfig string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	RootDockerConfig = filepath.Join(dir, "root", ".docker", "config.json")
	os.MkdirAll(filepath.Dir(RootDockerConfig), 0700)
	if rootConfig != "" {
		os.WriteFile(RootDockerConfig, []byte(rootConfig), 0600)
	}
	base := filepath.Join(dir, "etc-docker")
	os.Mkdir(base, 0500) // read-only, like /etc under ProtectSystem=strict
	if baseConfig != "" {
		os.Chmod(base, 0700)
		os.WriteFile(filepath.Join(base, "config.json"), []byte(baseConfig), 0600)
		os.Chmod(base, 0500)
	}
	t.Cleanup(func() { os.Chmod(base, 0700) })
	return base, filepath.Join(dir, "state", "docker")
}

func readConfig(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(b, &got)
	return got
}

func auth(cfg map[string]any, registry string) string {
	a, _ := cfg["auths"].(map[string]any)[registry].(map[string]any)
	s, _ := a["auth"].(string)
	return s
}

func TestSyncRegistryAuthMergesBaseAndRootIntoRuntime(t *testing.T) {
	base, runtime := setupRegistry(t, `{"auths":{"ghcr.io":{"auth":"cm9vdA=="}}}`, `{"auths":{"ghcr.io":{"auth":"b2xk"},"registry.example":{"auth":"a2VlcA=="}},"psFormat":"x"}`)
	summary := syncRegistryAuth(base, runtime)
	cfg := readConfig(t, runtime)
	if auth(cfg, "ghcr.io") != "cm9vdA==" || auth(cfg, "registry.example") != "a2VlcA==" || cfg["psFormat"] != "x" || !strings.Contains(summary, "ghcr.io (root config)") {
		t.Fatal(summary, cfg)
	}
	if st, _ := os.Stat(filepath.Join(runtime, "config.json")); st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	if b, _ := os.ReadFile(filepath.Join(base, "config.json")); strings.Contains(string(b), "cm9vdA==") {
		t.Fatal("wrote to the read-only base config")
	}
	if (Exec{DockerConfig: base, RuntimeDockerConfig: runtime}).dockerConfig() != runtime {
		t.Fatal("Docker should read the merged runtime config")
	}
}

func TestSyncRegistryAuthResolvesCredentialHelper(t *testing.T) {
	base, runtime := setupRegistry(t, `{"auths":{"ghcr.io":{}},"credsStore":"pass"}`, "")
	credential = func(helper, action, input string) ([]byte, error) {
		if helper != "pass" {
			return nil, errors.New("unexpected helper")
		}
		if action == "list" {
			return []byte(`{"ghcr.io":"fathah"}`), nil
		}
		return []byte(`{"ServerURL":"ghcr.io","Username":"fathah","Secret":"ghp_token"}`), nil
	}
	summary := syncRegistryAuth(base, runtime)
	cfg := readConfig(t, runtime)
	if auth(cfg, "ghcr.io") != base64.StdEncoding.EncodeToString([]byte("fathah:ghp_token")) || cfg["credsStore"] != nil || !strings.Contains(summary, "credential helper 'pass'") {
		t.Fatal(summary, cfg)
	}
}

func TestSyncRegistryAuthFallsBackWithoutRootLogin(t *testing.T) {
	base, runtime := setupRegistry(t, `{"auths":{"ghcr.io":{"auth":"cm9vdA=="}}}`, "")
	syncRegistryAuth(base, runtime)
	os.Remove(RootDockerConfig)
	if s := syncRegistryAuth(base, runtime); !strings.Contains(s, "no root Docker login") {
		t.Fatal(s)
	}
	if (Exec{DockerConfig: base, RuntimeDockerConfig: runtime}).dockerConfig() != base {
		t.Fatal("stale merged config should be dropped")
	}
}

func TestSyncRegistryAuthKeepsBadBaseConfigUnmerged(t *testing.T) {
	base, runtime := setupRegistry(t, `{"auths":{"ghcr.io":{"auth":"cm9vdA=="}}}`, "not json")
	if s := syncRegistryAuth(base, runtime); !strings.Contains(s, "not valid JSON") {
		t.Fatal(s)
	}
}
