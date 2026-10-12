package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func TestAdoptedMetadataConsolidatesWithoutChangingApplicationFiles(t *testing.T) {
	d, p, _ := runtimeServiceFixture(t)
	source := filepath.Join(d.Config.ProjectsRoot, "original-app")
	if err := os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"docker-compose.yml": "original source", ".env": "TOKEN=original", "data": "application data"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SaveDraft(d.Config, p, "saved draft", "TOKEN=new\n"); err != nil {
		t.Fatal(err)
	}
	old := projectDir(d.Config, p)
	p.Adoption = &model.Adoption{SourceName: "original-app", ComposeProject: "original-app", ConfigFiles: []string{filepath.Join(source, "docker-compose.yml")}}
	if err := ConsolidateAdoptedProjectDirectory(d.Config, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("second project directory remains", err)
	}
	if ProjectDirectory(d.Config, p) != filepath.Join(source, ".dockyard") {
		t.Fatal("metadata outside original folder")
	}
	for name, content := range map[string]string{"docker-compose.yml": "original source", ".env": "TOKEN=original", "data": "application data"} {
		b, err := os.ReadFile(filepath.Join(source, name))
		if err != nil || string(b) != content {
			t.Fatal("application file changed", name, err)
		}
	}
	compose, env, saved := ReadDraft(d.Config, p)
	if !saved || compose != "saved draft" || env != "TOKEN=new\n" {
		t.Fatal("saved draft lost")
	}
	for slot, release := range p.Slots {
		if _, _, err := nativeRelease(d.Config, p, release); err != nil {
			t.Fatal("pinned release lost", err)
		}
		b, err := os.ReadFile(bindingPath(d.Config, p, slot))
		if err != nil || strings.Contains(string(b), old+"/") || !strings.Contains(string(b), source+"/.dockyard/env/") {
			t.Fatal("binding retains old absolute path", err)
		}
	}
	if err := ConsolidateAdoptedProjectDirectory(d.Config, p); err != nil {
		t.Fatal("upgrade not idempotent", err)
	}
	if ProjectMirrorPath(d.Config, p, "compose.yml") != filepath.Join(source, ".dockyard", "compose.yml") {
		t.Fatal("would overwrite original Compose source")
	}
	// Subsequent preparations still use the same hidden metadata directory.
	if _, err := d.PrepareNative(context.Background(), p, "services: supplied", "TOKEN=next\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("preparation recreated old directory")
	}
	// Changing to Dockyard's blue/green Compose identity must not put the
	// project back into a second inventory-ID directory.
	next := p
	next.StorageFolder = p.Adoption.SourceName
	next.Adoption = nil
	next.ZeroDowntime = true
	if ProjectDirectory(d.Config, next) != ProjectDirectory(d.Config, p) || d.workingDir(next) != source {
		t.Fatal("identity conversion changed project folder")
	}
	if d.composeName(next, "green") == p.Adoption.ComposeProject {
		t.Fatal("conversion retained old Compose namespace")
	}
}

func TestAdoptedDirectoryConflictPreservesBothDirectories(t *testing.T) {
	d, p, _ := runtimeServiceFixture(t)
	old := ProjectDirectory(d.Config, p)
	p.Adoption = &model.Adoption{SourceName: "original-app", ComposeProject: "original-app"}
	newDir := ProjectDirectory(d.Config, p)
	if err := os.MkdirAll(newDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ConsolidateAdoptedProjectDirectory(d.Config, p); err == nil {
		t.Fatal("overwrote conflicting metadata")
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("removed old directory", err)
	}
	if b, err := os.ReadFile(filepath.Join(newDir, "keep")); err != nil || string(b) != "keep" {
		t.Fatal("changed conflicting data")
	}
}
