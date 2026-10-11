package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
)

type configurationPreparer struct {
	bgPreparer
	planErr error
}

func (d configurationPreparer) PlanConfigurationUpdates(_ context.Context, p model.Project, release model.Release) ([]model.ServiceUpdate, error) {
	if d.planErr != nil {
		return nil, d.planErr
	}
	current, _ := p.Current()
	return []model.ServiceUpdate{{Service: "web", Mode: "seamless", SourceRelease: &release, Previous: &model.ServiceInstance{Name: "web", Release: current}, Instance: model.ServiceInstance{Name: "dy-update-candidate", Release: current, ContainerPort: 80}}}, nil
}

func TestConfigurationDeploymentQueuesSeamlessPlanAndReplays(t *testing.T) {
	a, d, _, send := apiFixture(t)
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
	a.Auth, _ = auth.New(a.Engine.Config)
	prep := configurationPreparer{bgPreparer: bgPreparer{d, a.Engine.Config}}
	a.Engine.Docker = prep
	old := model.Project{ID: "demo", AppID: "demo", Mode: "compose", Environment: model.Production, State: "running", Active: "blue", BluePort: 3001, RouteService: "web", RoutePort: 80, Domains: []string{"app.example.com"}}
	runtime.Compose(a.Engine.Config, old)
	release, err := prep.PrepareNative(context.Background(), old, "services: {}", "TOKEN=old")
	if err != nil {
		t.Fatal(err)
	}
	release.ID = "rel-old"
	old.Slots, old.Releases = map[string]model.Release{"blue": release}, []model.Release{release}
	original := model.Job{ID: "original", ProjectID: old.ID, Action: "project_create", Status: "succeeded", Created: time.Now()}
	if err := a.Engine.Store.Accept(original, &old, "original", "original", 10, 10); err != nil {
		t.Fatal(err)
	}
	body := `{"environment":"production","mode":"seamless","compose_yaml":"services: {}","env_file":"TOKEN=new-private","expected_release_id":"rel-old"}`
	for i, scope := range []string{"deploy.environment deploy.execute", "deploy.environment deploy.execute projects.write"} {
		w := send("POST", "/v1/projects/demo/deploy", body, scope, "denied-"+string(rune('a'+i)))
		if w.Code != 403 {
			t.Fatal("missing authority accepted", w.Code, w.Body.String())
		}
	}
	scope := "deploy.environment deploy.execute projects.write sites.write"
	stale := send("POST", "/v1/projects/demo/deploy", strings.Replace(body, "rel-old", "rel-stale", 1), scope, "stale")
	if stale.Code != 409 {
		t.Fatal("stale source accepted", stale.Code)
	}
	prep.planErr = model.Fail("CONFIGURATION_SEAMLESS_INCOMPATIBLE")
	a.Engine.Docker = prep
	blocked := send("POST", "/v1/projects/demo/deploy", body, scope, "incompatible")
	if blocked.Code != 409 || !strings.Contains(blocked.Body.String(), "CONFIGURATION_SEAMLESS_INCOMPATIBLE") {
		t.Fatal(blocked.Code, blocked.Body.String())
	}
	if busy, _ := a.Engine.Store.Busy(old.ID); busy {
		t.Fatal("incompatible edit queued a restart")
	}
	prep.planErr = nil
	a.Engine.Docker = prep
	w := send("POST", "/v1/projects/demo/deploy", body, scope, "seamless")
	if w.Code != 202 || strings.Contains(w.Body.String(), "private") {
		t.Fatal(w.Code, w.Body.String())
	}
	again := send("POST", "/v1/projects/demo/deploy", body, scope, "seamless")
	if again.Code != 202 || again.Body.String() != w.Body.String() {
		t.Fatal("replay changed", again.Code, again.Body.String())
	}
	var accepted model.Job
	json.Unmarshal(w.Body.Bytes(), &accepted)
	job, err := a.Engine.Store.Job(accepted.ID)
	if err != nil || job.Action != "configuration_update" || len(job.Input.ConfigurationUpdates) != 1 || job.Input.ConfigurationUpdates[0].SourceRelease.Environment != job.Input.Release.Environment || job.Input.DraftSHA == "" {
		t.Fatal(job, err)
	}
	p, _ := a.Engine.Store.Project(old.ID)
	current, _ := p.Current()
	if p.State != "running" || current.ID != "rel-old" {
		t.Fatal("admission changed live state", p)
	}
	read := send("GET", "/v1/jobs/"+accepted.ID, "", "deploy.read", "read")
	if strings.Contains(read.Body.String(), "source_release") || strings.Contains(read.Body.String(), "new-private") {
		t.Fatal("private plan leaked", read.Body.String())
	}
}
