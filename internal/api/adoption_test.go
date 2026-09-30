package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
)

type adoptionStub struct {
	*dockerStub
	docker runtime.Docker
	plan   runtime.AdoptionPlan
	calls  int
}

func (d *adoptionStub) PlanAdoption(context.Context, string) (runtime.AdoptionPlan, error) {
	d.calls++
	return d.plan, nil
}
func (d *adoptionStub) PrepareNative(ctx context.Context, p model.Project, source, env string) (model.Release, error) {
	return d.docker.PrepareNative(ctx, p, source, env)
}
func TestAdoptionIsScopedDurableAndDoesNotRestartStack(t *testing.T) {
	a, d, routes, send := apiFixture(t)
	id := "existing-abc"
	path := "/v1/inventory/" + id + "/migrate"
	hash := strings.Repeat("a", 64)
	body := `{"source_sha256":"` + hash + `"}`
	if w := send("POST", path, body, "deploy.environment projects.write", "denied"); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
	var err error
	a.Auth, err = auth.New(a.Engine.Config)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(a.Engine.Config.ProjectsRoot, "legacy")
	os.Mkdir(source, 0700)
	os.WriteFile(filepath.Join(source, "compose.yml"), []byte("original\n"), 0600)
	stub := &adoptionStub{dockerStub: d, docker: runtime.Docker{Config: a.Engine.Config, Runner: nativeRunner{}}, plan: runtime.AdoptionPlan{Identity: model.Adoption{SourceName: "legacy", ComposeProject: "legacy", ContainerIDs: []string{strings.Repeat("b", 64)}, ConfigFiles: []string{filepath.Join(source, "compose.yml")}}, Source: `{"services":{"web":{"image":"nginx"}}}`, Dotenv: "TOKEN=secret", SHA256: hash, State: "running", Services: 1}}
	a.Engine.Docker = stub
	inv := model.Inventory{Projects: []model.ExistingProject{{ID: id, Name: "legacy", Present: true}}, Sites: []model.ExistingSite{{HostMatcher: "legacy.example.com", ProjectIDs: []string{id}}}}
	if err = a.Engine.Store.SyncInventory(inv, true, true, true); err != nil {
		t.Fatal(err)
	}
	stale := send("POST", path, `{"source_sha256":"`+strings.Repeat("c", 64)+`"}`, "deploy.environment projects.write", "stale")
	if stale.Code != 409 || !strings.Contains(stale.Body.String(), "MIGRATION_SOURCE_CHANGED") {
		t.Fatal(stale.Code, stale.Body.String())
	}
	w := send("GET", "/v1/inventory/"+id+"/migration", "", "deploy.read", "review")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"execution_available":true`) || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = send("POST", path, body, "deploy.environment projects.write", "adopt")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var acceptedJob model.Job
	json.Unmarshal(w.Body.Bytes(), &acceptedJob)
	calls := stub.calls
	replay := send("POST", path, body, "deploy.environment projects.write", "adopt")
	if replay.Code != 202 || stub.calls != calls {
		t.Fatal("retry repeated source work", replay.Code)
	}
	// Interrupted metadata-only adoption resumes without repeating external work.
	pending, _ := a.Engine.Store.Job(acceptedJob.ID)
	pending.Status = "running"
	if err = a.Engine.Store.Update(pending, nil); err != nil {
		t.Fatal(err)
	}
	if err = a.Engine.Initialize(); err != nil {
		t.Fatal(err)
	}
	resumed, _ := a.Engine.Store.Job(acceptedJob.ID)
	if resumed.Status != "queued" {
		t.Fatal("adoption not replay-safe", resumed.Status)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Engine.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		j, _ := a.Engine.Store.Job(acceptedJob.ID)
		if j.Status == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(j)
		}
		time.Sleep(10 * time.Millisecond)
	}
	p, err := a.Engine.Store.Project(id)
	if err != nil || p.State != "running" || p.Adoption == nil || p.ZeroDowntime {
		t.Fatal(p, err)
	}
	if _, err := os.Stat(filepath.Join(a.Engine.Config.ProjectsRoot, id, "blue.env")); err != nil {
		t.Fatal("missing active slot binding", err)
	}
	services, err := a.Engine.Store.Services(id)
	if err != nil || len(services) != 1 {
		t.Fatal(services, err)
	}
	if d.calls != 1 || routes.calls != 0 {
		t.Fatal("adoption mutated adapters", d.calls, routes.calls)
	} // one read-only Validate
	if raw, _ := os.ReadFile(filepath.Join(source, "compose.yml")); string(raw) != "original\n" {
		t.Fatal("original overwritten")
	}
	inv, err = a.Engine.Store.Inventory()
	if err != nil || !inv.Projects[0].Managed {
		t.Fatal("inventory not adopted", err)
	}
	if err = a.Engine.Initialize(); err != nil {
		t.Fatal("restart lost adoption", err)
	}
}
