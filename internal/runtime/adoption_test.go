package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

type adoptionRunner struct {
	t       *testing.T
	dir     string
	foreign bool
	calls   [][]string
}

func (r *adoptionRunner) Run(_ context.Context, _ string, _ string, args []string) (process.Result, error) {
	r.calls = append(r.calls, append([]string{}, args...))
	id := strings.Repeat("a", 64)
	switch args[0] {
	case "ps":
		if r.foreign && args[len(args)-1] == "label=com.docker.compose.project=legacy_app" {
			return process.Result{Output: []byte(id + "\n" + strings.Repeat("b", 64))}, nil
		}
		return process.Result{Output: []byte(id)}, nil
	case "inspect":
		c := Container{ID: id}
		c.State.Running = true
		c.Config.Labels = map[string]string{"com.docker.compose.project": "legacy_app", "com.docker.compose.service": "web", "com.docker.compose.project.working_dir": r.dir, "com.docker.compose.project.config_files": filepath.Join(r.dir, "compose.yml")}
		b, _ := json.Marshal([]Container{c})
		return process.Result{Output: b}, nil
	case "compose":
		for _, a := range args {
			if a == "up" || a == "stop" || a == "down" || a == "restart" {
				r.t.Fatal("adoption mutated Docker", args)
			}
		}
		b := `{"services":{"web":{"image":"nginx:alpine","ports":[{"target":80,"published":"3138","protocol":"tcp"}],"environment":{"TOKEN":"private-token"},"volumes":[{"type":"volume","source":"data","target":"/data"}]}},"volumes":{"data":{"name":"legacy_app_data"}},"networks":{"default":{"name":"legacy_app_default"}}}`
		return process.Result{Output: []byte(b)}, nil
	}
	r.t.Fatal("unexpected command", args)
	return process.Result{}, nil
}
func adoptionFixture(t *testing.T) (Docker, *adoptionRunner, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "legacy")
	os.Mkdir(dir, 0700)
	os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("services: {}\n"), 0600)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("TOKEN=private-token\n"), 0600)
	runner := &adoptionRunner{t: t, dir: dir}
	return Docker{Config: config.Config{ProjectsRoot: root, ServerID: "vps"}, Runner: runner}, runner, dir
}
func TestAdoptionPreservesResourcesAndBindsApproval(t *testing.T) {
	d, _, dir := adoptionFixture(t)
	ctx := context.Background()
	plan, err := d.PlanAdoption(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	again, err := d.PlanAdoption(ctx, "legacy")
	if err != nil || again.SHA256 != plan.SHA256 {
		t.Fatal("unstable plan", err)
	}
	p := model.Project{ID: "existing-abc", Mode: "compose", Adoption: &plan.Identity}
	os.Mkdir(projectDir(d.Config, p.ID), 0700)
	release, err := d.PrepareNative(ctx, p, plan.Source, plan.Dotenv)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := nativeRelease(d.Config, p, release)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"legacy_app_data", "legacy_app_default", "3138", "private-token"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("lost %s", want)
		}
	}
	args := d.args(p, "blue", "ps")
	if args[2] != "legacy_app" || args[4] != dir {
		t.Fatal(args)
	}
	for _, slot := range []string{"blue", "green"} {
		if d.composeName(p, slot) != "legacy_app" {
			t.Fatal("changed project name")
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "compose.yml"))
	if string(raw) != "services: {}\n" {
		t.Fatal("changed original file")
	}
	os.WriteFile(filepath.Join(dir, ".env"), []byte("TOKEN=changed\n"), 0600)
	changed, err := d.PlanAdoption(ctx, "legacy")
	if err != nil || changed.SHA256 == plan.SHA256 {
		t.Fatal("env changed without new approval", err)
	}
	c := Container{ID: strings.Repeat("a", 64)}
	c.Config.Labels = map[string]string{"com.docker.compose.project": "legacy_app", "com.docker.compose.project.working_dir": dir, "com.docker.compose.project.config_files": filepath.Join(dir, "compose.yml")}
	if !d.ownsNative(p, "blue", c) {
		t.Fatal("recorded container not recognized")
	}
	c.ID = strings.Repeat("b", 64)
	if !d.ownsNative(p, "blue", c) {
		t.Fatal("recreated source container not recognized")
	}
	c.Config.Labels["com.docker.compose.project.config_files"] = filepath.Join(dir, "other.yml")
	if d.ownsNative(p, "blue", c) {
		t.Fatal("container from another config accepted")
	}
	c.Config.Labels["com.docker.compose.project.config_files"] = filepath.Join(dir, "compose.yml")
	c.Config.Labels["io.ziqx.dockyard.server"] = "vps"
	c.Config.Labels["io.ziqx.dockyard.project"] = p.ID
	if !d.ownsNative(p, "blue", c) {
		t.Fatal("later managed container not recognized")
	}
}
func TestAdoptionRejectsForeignProjectAndLinks(t *testing.T) {
	d, r, dir := adoptionFixture(t)
	r.foreign = true
	if _, err := d.PlanAdoption(context.Background(), "legacy"); err == nil {
		t.Fatal("shared Compose identity accepted")
	}
	r.foreign = false
	os.Remove(filepath.Join(dir, "compose.yml"))
	os.Symlink(".env", filepath.Join(dir, "compose.yml"))
	if _, err := d.PlanAdoption(context.Background(), "legacy"); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := d.PlanAdoption(context.Background(), "../legacy"); err == nil {
		t.Fatal("path escape accepted")
	}
}
