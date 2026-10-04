package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

type routeSetupDocker struct {
	*dockerStub
	upstream   string
	observeErr error
}

func (d *routeSetupDocker) PublishedUpstream(context.Context, model.Project, string, int) (string, error) {
	return d.upstream, d.observeErr
}

type routeSetupRoutes struct {
	*routesStub
	source string
}

func (r *routeSetupRoutes) PlanRouteImport(context.Context, model.Project, string) ([]model.RouteEdit, error) {
	return []model.RouteEdit{{Path: "/etc/caddy/Caddyfile", Before: r.source, After: "", Mode: 0644}}, nil
}

func TestRouteSetupReviewAdmissionAndReplay(t *testing.T) {
	a, base, routeBase, send := apiFixture(t)
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
	a.Auth, _ = auth.New(a.Engine.Config)
	d := &routeSetupDocker{dockerStub: base, upstream: "127.0.0.1:3031"}
	routes := &routeSetupRoutes{routesStub: routeBase, source: "reviewed original site"}
	a.Engine.Docker = d
	a.Engine.Routes = routes
	release := model.Release{ID: "rel-original", Compose: "cmp-original", Environment: "env-original"}
	p := model.Project{ID: "demo", AppID: "tasks", Mode: "compose", Environment: model.Production, State: "running", Active: "blue", Adoption: &model.Adoption{SourceName: "tasks", ComposeProject: "tasks"}, ExternalDomains: []string{"app.example.com"}, Slots: map[string]model.Release{"blue": release}, Releases: []model.Release{release}}
	job := model.Job{ID: "original", ProjectID: p.ID, Status: "succeeded", Created: time.Now(), Input: model.Input{Project: &p}}
	if err := a.Engine.Store.Accept(job, &p, "original", "original", 10, 10); err != nil {
		t.Fatal(err)
	}
	input := routeSetupRequest{ExpectedRelease: release.ID, Domains: p.ExternalDomains, Service: "web", ContainerPort: 3000}
	raw, _ := json.Marshal(input)
	for i, scope := range []string{"deploy.read", "sites.write", "projects.write"} {
		w := send("POST", "/v1/projects/demo/route-setup-preview", string(raw), scope, "denied-"+string(rune('a'+i)))
		if w.Code != 403 {
			t.Fatal("unscoped review accepted", w.Code, w.Body.String())
		}
	}
	scopes := "projects.write sites.write"
	w := send("POST", "/v1/projects/demo/route-setup-preview", string(raw), scopes, "preview")
	if w.Code != 200 || strings.Contains(w.Body.String(), routes.source) {
		t.Fatal(w.Code, w.Body.String())
	}
	var review struct {
		Hash      string `json:"review_sha256"`
		Preserved bool   `json:"containers_unchanged"`
	}
	json.Unmarshal(w.Body.Bytes(), &review)
	if len(review.Hash) != 64 || !review.Preserved {
		t.Fatal(w.Body.String())
	}
	if busy, _ := a.Engine.Store.Busy(p.ID); busy {
		t.Fatal("preview queued a mutation")
	}
	if current, _ := a.Engine.Store.Project(p.ID); len(current.Domains) != 0 || current.PublishedRoute != nil {
		t.Fatal("preview altered metadata")
	}
	input.Review = review.Hash
	raw, _ = json.Marshal(input)
	routes.source = "changed site"
	w = send("POST", "/v1/projects/demo/route-setup", string(raw), scopes, "changed-route")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "ROUTE_REVIEW_CHANGED") {
		t.Fatal(w.Code, w.Body.String())
	}
	routes.source = "reviewed original site"
	d.upstream = "127.0.0.1:3032"
	w = send("POST", "/v1/projects/demo/route-setup", string(raw), scopes, "changed-port")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "ROUTE_REVIEW_CHANGED") {
		t.Fatal(w.Code, w.Body.String())
	}
	d.upstream = "127.0.0.1:3031"
	d.observeErr = model.Uncertain("CONTAINER_OWNERSHIP_UNKNOWN")
	w = send("POST", "/v1/projects/demo/route-setup", string(raw), scopes, "unknown-container")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "CONTAINER_OWNERSHIP_UNKNOWN") {
		t.Fatal(w.Code, w.Body.String())
	}
	d.observeErr = nil
	p.State = "stopped"
	a.Engine.Store.Update(job, &p)
	w = send("POST", "/v1/projects/demo/route-setup-preview", string(raw), scopes, "stopped")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "PROJECT_NOT_RUNNING") {
		t.Fatal(w.Code, w.Body.String())
	}
	p.State = "running"
	job.Status = "recovery_required"
	a.Engine.Store.Update(job, &p)
	w = send("POST", "/v1/projects/demo/route-setup", string(raw), scopes, "recovery")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "RECOVERY_REQUIRED") {
		t.Fatal(w.Code, w.Body.String())
	}
	job.Status = "succeeded"
	a.Engine.Store.Update(job, &p)
	w = send("POST", "/v1/projects/demo/route-setup", string(raw), scopes, "configure")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	before := base.calls + routeBase.calls
	retry := send("POST", "/v1/projects/demo/route-setup", string(raw), scopes, "configure")
	if retry.Code != 202 || retry.Body.String() != w.Body.String() || base.calls+routeBase.calls != before {
		t.Fatal("retry repeated planning", retry.Code)
	}
	var acceptedJob model.Job
	json.Unmarshal(w.Body.Bytes(), &acceptedJob)
	acceptedJob, err := a.Engine.Store.Job(acceptedJob.ID)
	if err != nil || acceptedJob.Input.Project.PublishedRoute.HostPort != 3031 || acceptedJob.Input.Project.RouteService != "" || acceptedJob.Input.Project.Slots["blue"] != release || acceptedJob.Input.Release != nil {
		t.Fatal("route changed release or containers", err, acceptedJob)
	}
}
