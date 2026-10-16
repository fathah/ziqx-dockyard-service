package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func TestDraftBundleReadsCoherentFilesDuringSaves(t *testing.T) {
	c := config.Config{ProjectsRoot: t.TempDir()}
	p := model.Project{ID: "demo", Mode: "compose"}
	save := func(generation string) error {
		_, err := SaveDraft(c, p, generation, "GEN="+generation+"\n", map[string]string{".env.frontend": generation, ".env.backend": generation, ".env.empty": ""})
		return err
	}
	if err := save("generation-0"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 1; i <= 32; i++ {
			if err := save(fmt.Sprintf("generation-%d", i)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for {
		compose, env, files, ok, err := ReadDraftConfiguration(c, p)
		if err != nil || !ok || env != "GEN="+compose+"\n" || files[".env.frontend"] != compose || files[".env.backend"] != compose {
			t.Fatalf("read mixed saved configurations: compose=%q env=%q files=%v ok=%v error=%v", compose, env, files, ok, err)
		}
		if value, exists := files[".env.empty"]; !exists || value != "" {
			t.Fatal("empty named environment was lost")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(draftDir(c, p))
			if err != nil || len(entries) != 1 || entries[0].Name() != "draft.json" {
				t.Fatal("save did not persist one atomic bundle", entries, err)
			}
			return
		default:
		}
	}
}

func TestDraftBundleUpgradesLegacyFilesWithoutMixingInputs(t *testing.T) {
	c := config.Config{ProjectsRoot: t.TempDir()}
	p := model.Project{ID: "demo", Mode: "compose"}
	dir := draftDir(c, p)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"compose.yml": "legacy compose", ".env": "LEGACY=main\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	compose, env, files, ok, err := ReadDraftConfiguration(c, p)
	if err != nil || !ok || compose != "legacy compose" || env != "LEGACY=main\n" || len(files) != 0 {
		t.Fatal("legacy two-file draft unavailable", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env-files.json"), []byte(`{".env.frontend":"legacy frontend"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, files, ok, err = ReadDraftConfiguration(c, p)
	if err != nil || !ok || files[".env.frontend"] != "legacy frontend" {
		t.Fatal("legacy named draft unavailable", err)
	}
	latest := map[string]string{".env.frontend": "new frontend", ".env.backend": ""}
	if _, err := SaveDraft(c, p, "new compose", "NEW=main\n", latest); err != nil {
		t.Fatal(err)
	}
	compose, env, files, ok, err = ReadDraftConfiguration(c, p)
	if err != nil || !ok || compose != "new compose" || env != "NEW=main\n" || !reflect.DeepEqual(files, latest) {
		t.Fatal("bundle merged old and new inputs", err)
	}
	// Save changes just the bundle; old files remain a compatibility fallback,
	// and can never be picked independently of the new bundle's named files.
	if b, err := os.ReadFile(filepath.Join(dir, "compose.yml")); err != nil || string(b) != "legacy compose" {
		t.Fatal("save partly rewrote legacy layout", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "draft.json"), []byte("invalid interrupted data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err := ReadDraftConfiguration(c, p); err == nil || ok {
		t.Fatal("invalid bundle fell back to stale legacy files")
	}
}

func TestDraftBundleBoundsPermitEscapedInputs(t *testing.T) {
	c := config.Config{ProjectsRoot: t.TempDir()}
	p := model.Project{ID: "demo", Mode: "compose"}
	// JSON can expand one byte to six; every valid bounded draft must remain
	// readable even when both Compose and environment need maximum escaping.
	compose := strings.Repeat("\x01", 64<<10)
	files := map[string]string{".env.frontend": strings.Repeat("\x02", maxEnvBytes)}
	if _, err := SaveDraft(c, p, compose, "", files); err != nil {
		t.Fatal(err)
	}
	got, env, read, ok, err := ReadDraftConfiguration(c, p)
	if err != nil || !ok || got != compose || env != "" || !reflect.DeepEqual(read, files) {
		t.Fatal("bounded escaped draft not readable", err)
	}
}
