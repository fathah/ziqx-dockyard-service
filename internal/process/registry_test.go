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

func setupRegistry(t *testing.T, rootConfig, ownConfig string) string {
	t.Helper()
	dir := t.TempDir()
	RootDockerConfig = filepath.Join(dir, "root", ".docker", "config.json")
	os.MkdirAll(filepath.Dir(RootDockerConfig), 0700)
	if rootConfig != "" {
		os.WriteFile(RootDockerConfig, []byte(rootConfig), 0600)
	}
	own := filepath.Join(dir, "dockyard")
	os.Mkdir(own, 0700)
	if ownConfig != "" {
		os.WriteFile(filepath.Join(own, "config.json"), []byte(ownConfig), 0600)
	}
	return own
}

func readAuths(t *testing.T, own string) map[string]map[string]string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(own, "config.json"))
	var got struct {
		Auths map[string]map[string]string
	}
	json.Unmarshal(b, &got)
	return got.Auths
}

func TestSyncRegistryAuthCopiesPlainLogins(t *testing.T) {
	own := setupRegistry(t, `{"auths":{"ghcr.io":{"auth":"cm9vdA=="}}}`, `{"auths":{"ghcr.io":{"auth":"b2xk"},"registry.example":{"auth":"a2VlcA=="}},"psFormat":"x"}`)
	summary := syncRegistryAuth(own)
	auths := readAuths(t, own)
	if auths["ghcr.io"]["auth"] != "cm9vdA==" || auths["registry.example"]["auth"] != "a2VlcA==" || !strings.Contains(summary, "ghcr.io (root config)") {
		t.Fatal(summary, auths)
	}
	if b, _ := os.ReadFile(filepath.Join(own, "config.json")); !strings.Contains(string(b), "psFormat") {
		t.Fatal("dropped other settings")
	}
	if st, _ := os.Stat(filepath.Join(own, "config.json")); st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
}

func TestSyncRegistryAuthResolvesCredentialHelper(t *testing.T) {
	own := setupRegistry(t, `{"auths":{"ghcr.io":{}},"credsStore":"pass"}`, `{"credsStore":"pass"}`)
	credential = func(helper, action, input string) ([]byte, error) {
		if helper != "pass" {
			return nil, errors.New("unexpected helper")
		}
		if action == "list" {
			return []byte(`{"ghcr.io":"fathah"}`), nil
		}
		return []byte(`{"ServerURL":"ghcr.io","Username":"fathah","Secret":"ghp_token"}`), nil
	}
	summary := syncRegistryAuth(own)
	want := base64.StdEncoding.EncodeToString([]byte("fathah:ghp_token"))
	if readAuths(t, own)["ghcr.io"]["auth"] != want || !strings.Contains(summary, "credential helper 'pass'") {
		t.Fatal(summary, readAuths(t, own))
	}
	if b, _ := os.ReadFile(filepath.Join(own, "config.json")); strings.Contains(string(b), "credsStore") {
		t.Fatal("left a helper Dockyard cannot run", string(b))
	}
}

func TestSyncRegistryAuthReportsMissingLoginAndKeepsBadConfig(t *testing.T) {
	own := setupRegistry(t, "", "")
	if s := syncRegistryAuth(own); !strings.Contains(s, "no root Docker login") {
		t.Fatal(s)
	}
	own = setupRegistry(t, `{"auths":{"ghcr.io":{"auth":"cm9vdA=="}}}`, "not json")
	if s := syncRegistryAuth(own); !strings.Contains(s, "not valid JSON") {
		t.Fatal(s)
	}
	if b, _ := os.ReadFile(filepath.Join(own, "config.json")); string(b) != "not json" {
		t.Fatal("overwrote an unparseable config")
	}
}
