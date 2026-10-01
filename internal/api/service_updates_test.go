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

type servicePreparer struct {
	bgPreparer
	network string
}

func (n *servicePreparer) PlanServiceUpdate(_ context.Context, p model.Project, service, mode string, containerPort, hostPort int, _ string) (model.ServiceUpdate, error) {
	r, _ := p.Current()
	previous := model.ServiceInstance{Name: service, Release: r, Port: p.BluePort}
	previous.Release.Image = "sha256:" + strings.Repeat("1", 64)
	return model.ServiceUpdate{Service: service, Mode: mode, Previous: &previous, Instance: model.ServiceInstance{Name: "dy-update-random", Release: r, Port: hostPort}, NetworkAliases: map[string][]string{"network": {service}}, NetworkIDs: map[string]string{"network": n.network}}, nil
}
func (n *servicePreparer) PublishedUpstream(context.Context, model.Project, string, int) (string, error) {
	return "127.0.0.1:8080", nil
}

func TestServiceUpdateReviewBindsSnapshotNetworksAndPortsAndReplaysJob(t *testing.T) {
	a, d, _, send := apiFixture(t)
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
	a.Auth, _ = auth.New(a.Engine.Config)
	prep := &servicePreparer{bgPreparer: bgPreparer{d, a.Engine.Config}, network: "network-original"}
	a.Engine.Docker = prep
	old := model.Project{ID: "demo", AppID: "demo", Mode: "compose", Environment: model.Production, State: "running", Active: "blue", BluePort: 3001, RouteService: "web", RoutePort: 80, Domains: []string{"app.example.com"}}
	runtime.Compose(a.Engine.Config, old)
	rel, err := prep.PrepareNative(context.Background(), old, "services: {}", "TOKEN=private")
	if err != nil {
		t.Fatal(err)
	}
	rel.ID = "rel-original"
	old.Slots = map[string]model.Release{"blue": rel}
	old.Releases = []model.Release{rel}
	original := model.Job{ID: "original", ProjectID: old.ID, Action: "project_create", Status: "succeeded", Created: time.Now(), Input: model.Input{Project: &old}}
	if err = a.Engine.Store.Accept(original, &old, "original", "original", 10, 10); err != nil {
		t.Fatal(err)
	}
	input := serviceUpdateRequest{ExpectedRelease: rel.ID, Service: "web", Mode: "seamless", ContainerPort: 80}
	raw, _ := json.Marshal(input)
	scope := "deploy.execute projects.write sites.write"
	for i, scopes := range []string{"deploy.read", "deploy.execute", "deploy.execute projects.write"} {
		w := send("POST", "/v1/projects/demo/service-update-preview", string(raw), scopes, "denied-"+string(rune('a'+i)))
		if w.Code != 403 {
			t.Fatal("unscoped review accepted", w.Code, w.Body.String())
		}
	}
	w := send("POST", "/v1/projects/demo/service-update-preview", string(raw), scope, "review")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "network-original") {
		t.Fatal("review leaked private plan")
	}
	if busy, _ := a.Engine.Store.Busy(old.ID); busy {
		t.Fatal("preview queued a mutation")
	}
	var review struct {
		Hash         string `json:"review_sha256"`
		Dependencies bool   `json:"dependencies_unchanged"`
	}
	json.Unmarshal(w.Body.Bytes(), &review)
	if len(review.Hash) != 64 || !review.Dependencies {
		t.Fatal(w.Body.String())
	}
	input.Review = review.Hash
	prep.network = "network-replaced"
	raw, _ = json.Marshal(input)
	w = send("POST", "/v1/projects/demo/service-update", string(raw), scope, "changed-network")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "SERVICE_REVIEW_CHANGED") {
		t.Fatal(w.Code, w.Body.String())
	}
	prep.network = "network-original"
	input.ContainerPort = 81
	raw, _ = json.Marshal(input)
	w = send("POST", "/v1/projects/demo/service-update", string(raw), scope, "changed-port")
	if w.Code != 409 {
		t.Fatal("port change bypassed review", w.Code)
	}
	input.ContainerPort = 80
	raw, _ = json.Marshal(input)
	w = send("POST", "/v1/projects/demo/service-update", string(raw), scope, "update")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	again := send("POST", "/v1/projects/demo/service-update", string(raw), scope, "update")
	if again.Code != 202 || again.Body.String() != w.Body.String() {
		t.Fatal("retry created a second update", again.Code, again.Body.String())
	}
	var acceptedJob model.Job
	json.Unmarshal(w.Body.Bytes(), &acceptedJob)
	job, _ := a.Engine.Store.Job(acceptedJob.ID)
	if job.Input.ServiceUpdate == nil || job.Input.Previous == nil || job.Input.ServiceUpdate.Service != "web" || job.Input.Project.ZeroDowntime || len(job.Input.Project.ServicePorts) != 2 {
		t.Fatal("incomplete journal", job)
	}
	for _, port := range job.Input.Project.ServicePorts {
		owner, err := a.Engine.Store.Reserved(port, "other")
		if err != nil || !owner {
			t.Fatal("candidate port not reserved", port, owner, err)
		}
	}
	current, _ := a.Engine.Store.Project(old.ID)
	if len(current.ServiceInstances) != 0 || current.ServiceMode {
		t.Fatal("accept changed running project", current)
	}
	if strings.Contains(w.Body.String(), `"service_update":`) || strings.Contains(w.Body.String(), "private") {
		t.Fatal("private journal leaked", w.Body.String())
	}
}

func TestServiceRestartNeedsDeployScopeAndRejectsAmbiguousInputs(t *testing.T) {
	a, d, _, send := apiFixture(t)
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
	a.Auth, _ = auth.New(a.Engine.Config)
	prep := &servicePreparer{bgPreparer: bgPreparer{d, a.Engine.Config}}
	a.Engine.Docker = prep
	p := model.Project{ID: "demo", AppID: "demo", Mode: "compose", Environment: model.Staging, State: "running", Active: "blue", Domains: []string{}, Slots: map[string]model.Release{"blue": {ID: "rel-original"}}}
	j := model.Job{ID: "original", ProjectID: p.ID, Status: "succeeded", Created: time.Now(), Input: model.Input{Project: &p}}
	if err := a.Engine.Store.Accept(j, &p, "original", "original", 10, 10); err != nil {
		t.Fatal(err)
	}
	input := serviceUpdateRequest{ExpectedRelease: "rel-original", Service: "postgres", Mode: "restart"}
	raw, _ := json.Marshal(input)
	w := send("POST", "/v1/projects/demo/service-update-preview", string(raw), "deploy.execute", "review")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for i, bad := range []string{`{"expected_release_id":"rel-original","service":"../postgres","mode":"restart"}`, `{"expected_release_id":"rel-original","service":"postgres","mode":"restart","container_port":5432}`, `{"expected_release_id":"rel-original","service":"postgres","mode":"restart","env_file":"SECRET=private"}`, `{"expected_release_id":"rel-original","service":"postgres","mode":"seamless","container_port":5432}`} {
		w = send("POST", "/v1/projects/demo/service-update-preview", bad, "deploy.execute projects.write sites.write", string(rune('a'+i)))
		if w.Code < 400 {
			t.Fatal("invalid service update accepted", bad, w.Code)
		}
	}
	a.Engine.Config.Keys[0].Scopes = []string{"deploy.execute"}
	a.Auth, _ = auth.New(a.Engine.Config)
	w = send("POST", "/v1/projects/demo/service-update-preview", string(raw), "deploy.execute", "no-admin")
	if w.Code != 403 || !strings.Contains(w.Body.String(), "COMPOSE_ACCESS_REQUIRED") {
		t.Fatal("native authority bypass", w.Code, w.Body.String())
	}
}
