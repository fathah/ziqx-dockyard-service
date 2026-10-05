package process

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncRegistryAuthMergesRootLogins(t *testing.T) {
	dir := t.TempDir()
	RootDockerConfig = filepath.Join(dir, "root.json")
	own := filepath.Join(dir, "dockyard")
	os.Mkdir(own, 0700)
	os.WriteFile(RootDockerConfig, []byte(`{"auths":{"ghcr.io":{"auth":"cm9vdA=="}},"credHelpers":{"ecr.example":"ecr-login"}}`), 0600)
	os.WriteFile(filepath.Join(own, "config.json"), []byte(`{"auths":{"ghcr.io":{"auth":"b2xk"},"registry.example":{"auth":"a2VlcA=="}},"psFormat":"x"}`), 0600)
	syncRegistryAuth(own)
	b, _ := os.ReadFile(filepath.Join(own, "config.json"))
	var got struct {
		Auths       map[string]struct{ Auth string }
		CredHelpers map[string]string
		PsFormat    string
	}
	if json.Unmarshal(b, &got) != nil || got.Auths["ghcr.io"].Auth != "cm9vdA==" || got.Auths["registry.example"].Auth != "a2VlcA==" || got.CredHelpers["ecr.example"] != "ecr-login" || got.PsFormat != "x" {
		t.Fatal(string(b))
	}
	if st, _ := os.Stat(filepath.Join(own, "config.json")); st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
}

func TestSyncRegistryAuthCreatesConfigAndSkipsBadInput(t *testing.T) {
	dir := t.TempDir()
	RootDockerConfig = filepath.Join(dir, "root.json")
	own := filepath.Join(dir, "dockyard")
	os.Mkdir(own, 0700)
	os.WriteFile(RootDockerConfig, []byte(`{"auths":{"ghcr.io":{"auth":"cm9vdA=="}}}`), 0600)
	syncRegistryAuth(own)
	if b, err := os.ReadFile(filepath.Join(own, "config.json")); err != nil || len(b) == 0 {
		t.Fatal("config not created", err)
	}
	os.WriteFile(filepath.Join(own, "config.json"), []byte("not json"), 0600)
	syncRegistryAuth(own)
	if b, _ := os.ReadFile(filepath.Join(own, "config.json")); string(b) != "not json" {
		t.Fatal("overwrote an unparseable config")
	}
}
