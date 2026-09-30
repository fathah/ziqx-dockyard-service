package api

import (
	"context"
	"encoding/json"
	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"strings"
	"testing"
	"time"
)

type bgRunner struct{}

func (bgRunner) Run(context.Context, string, string, []string) (process.Result, error) {
	return process.Result{Output: []byte(`{"services":{"web":{"image":"nginx","healthcheck":{"test":["CMD","true"]}}}}`)}, nil
}

type bgPreparer struct {
	*dockerStub
	config config.Config
}

func (n bgPreparer) PrepareNative(ctx context.Context, p model.Project, s, env string) (model.Release, error) {
	return (runtime.Docker{Config: n.config, Runner: bgRunner{}}).PrepareNative(ctx, p, s, env)
}
func (n bgPreparer) CheckBlueGreenSource(context.Context, model.Project) error { return nil }
func TestBlueGreenReviewAuthorizationStalenessAndReplay(t *testing.T) {
	a, d, _, send := apiFixture(t)
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
	a.Auth, _ = auth.New(a.Engine.Config)
	prep := bgPreparer{d, a.Engine.Config}
	a.Engine.Docker = prep
	old := model.Project{ID: "demo", AppID: "demo", Mode: "compose", Environment: model.Production, State: "running", Active: "blue", BluePort: 3001, RouteService: "web", RoutePort: 80, Domains: []string{"app.example.com"}}
	runtime.Compose(a.Engine.Config, old)
	rel, err := prep.PrepareNative(context.Background(), old, "services: {}", "TOKEN=private")
	if err != nil {
		t.Fatal(err)
	}
	rel.ID = "rel-old"
	old.Slots = map[string]model.Release{"blue": rel}
	old.Releases = []model.Release{rel}
	j := model.Job{ID: "old-job", ProjectID: old.ID, Action: "project_create", Status: "succeeded", Created: time.Now(), Input: model.Input{Project: &old}}
	if err = a.Engine.Store.Accept(j, &old, "original", "original", 10, 10); err != nil {
		t.Fatal(err)
	}
	req := blueGreenRequest{ExpectedRelease: rel.ID, Compose: "services: {}", Dotenv: "TOKEN=private", Service: "web", Port: 80}
	raw, _ := json.Marshal(req)
	denied := send("POST", "/v1/projects/demo/blue-green-preview", string(raw), "deploy.read", "denied")
	if denied.Code != 403 {
		t.Fatal(denied.Code)
	}
	preview := send("POST", "/v1/projects/demo/blue-green-preview", string(raw), "deploy.environment", "review")
	if preview.Code != 200 {
		t.Fatal(preview.Code, preview.Body.String())
	}
	if strings.Contains(preview.Body.String(), "private") {
		t.Fatal("secret leaked")
	}
	var review struct {
		Hash string `json:"review_sha256"`
	}
	json.Unmarshal(preview.Body.Bytes(), &review)
	req.Review = review.Hash
	req.Dotenv = "TOKEN=changed"
	raw, _ = json.Marshal(req)
	scopes := "deploy.environment deploy.execute projects.write sites.write"
	changed := send("POST", "/v1/projects/demo/blue-green", string(raw), scopes, "changed")
	if changed.Code != 409 || !strings.Contains(changed.Body.String(), "BLUE_GREEN_REVIEW_CHANGED") {
		t.Fatal(changed.Code, changed.Body.String())
	}
	req.Dotenv = "TOKEN=private"
	raw, _ = json.Marshal(req)
	denied = send("POST", "/v1/projects/demo/blue-green", string(raw), "deploy.environment deploy.execute", "no-route-scope")
	if denied.Code != 403 {
		t.Fatal(denied.Code)
	}
	accepted := send("POST", "/v1/projects/demo/blue-green", string(raw), scopes, "enable")
	if accepted.Code != 202 {
		t.Fatal(accepted.Code, accepted.Body.String())
	}
	replay := send("POST", "/v1/projects/demo/blue-green", string(raw), scopes, "enable")
	if replay.Code != 202 || replay.Body.String() != accepted.Body.String() {
		t.Fatal("retry changed job", replay.Code, replay.Body.String())
	}
	current, _ := a.Engine.Store.Project(old.ID)
	if current.ZeroDowntime || current.Active != "blue" {
		t.Fatal("accept changed live project", current)
	}
	var out model.Job
	json.Unmarshal(accepted.Body.Bytes(), &out)
	job, _ := a.Engine.Store.Job(out.ID)
	if job.Input.Previous == nil || !job.Input.Project.ZeroDowntime || job.Input.Project.Active != "green" {
		t.Fatal("missing durable plan", job)
	}
	if strings.Contains(accepted.Body.String(), "private") || strings.Contains(accepted.Body.String(), "route_edits") {
		t.Fatal("private journal leaked")
	}
}
