package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type routeSetupDocker struct {
	*dockerStub
	upstream   string
	observeErr error
}

func (d *routeSetupDocker) PrepareServiceRoutes(ctx context.Context, p model.Project, r model.Release) (model.Release, error) {
	// A verified immutable manifest for the admission test, not a Docker call.
	return r, nil
}

func TestServiceDomainsPreserveSiblingRoutesAndRecheckReview(t *testing.T) {
	for _, mode := range []string{"published", "publish-new-port", "adopted-unpublished", "published-move", "publish-new-port-move"} {
		t.Run(mode, func(t *testing.T) {
			a, base, routes, send := apiFixture(t)
			checked := &checkedRouteSetup{routesStub: routes}
			a.Engine.Routes = checked
			var transfer *serviceTransferRoutes
			if strings.HasSuffix(mode, "-move") {
				transfer = &serviceTransferRoutes{checkedRouteSetup: checked, source: "api.example.com {\n reverse_proxy localhost:8080\n}\n"}
				a.Engine.Routes = transfer
			}
			a.Engine.Config.Keys[0].Projects = []string{"*"}
			a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
			a.Auth, _ = auth.New(a.Engine.Config)
			d := &routeSetupDocker{dockerStub: base, upstream: "127.0.0.1:3032"}
			a.Engine.Docker = d
			release := model.Release{ID: "rel-original", Compose: "cmp-original", Environment: "env-" + strings.Repeat("a", 32)}
			published := strings.HasPrefix(mode, "published")
			if !published {
				d.observeErr = model.Fail("BLUE_GREEN_ROUTE_TARGET_MISMATCH")
				dir := filepath.Join(a.Engine.Config.ProjectsRoot, "demo")
				os.MkdirAll(filepath.Join(dir, "compose"), 0700)
				os.MkdirAll(filepath.Join(dir, "env"), 0700)
				envHash := sha256.Sum256(nil)
				doc, _ := json.Marshal(map[string]any{"services": map[string]any{"web": map[string]any{"image": "web:stable"}, "api": map[string]any{"image": "api:stable"}}, "x-dockyard-env-sha256": hex.EncodeToString(envHash[:])})
				hash := sha256.Sum256(doc)
				release.Compose = "cmp-" + hex.EncodeToString(hash[:])
				os.WriteFile(filepath.Join(dir, "compose", release.Compose+".yml"), doc, 0600)
				os.WriteFile(filepath.Join(dir, "env", release.Environment+".env"), nil, 0600)
			}
			p := model.Project{ID: "demo", AppID: "tasks", Mode: "compose", Environment: model.Production, State: "running", Active: "blue", RouteService: "web", RoutePort: 3000, BluePort: 3031, Domains: []string{"app.example.com"}, Slots: map[string]model.Release{"blue": release}, Releases: []model.Release{release}}
			if mode == "adopted-unpublished" {
				p.Adoption = &model.Adoption{SourceName: "tasks", ComposeProject: "tasks"}
				p.Domains, p.RouteService, p.RoutePort, p.BluePort = nil, "", 0, 0
			}
			job := model.Job{ID: "original", ProjectID: p.ID, Status: "succeeded", Created: time.Now(), Input: model.Input{Project: &p}}
			if err := a.Engine.Store.Accept(job, &p, "original", "original", 10, 10); err != nil {
				t.Fatal(err)
			}
			input := routeSetupRequest{ExpectedRelease: release.ID, Domains: []string{"api.example.com"}, Service: "api", ContainerPort: 3040}
			raw, _ := json.Marshal(input)
			scopes := "deploy.execute projects.write sites.write"
			w := send("POST", "/v1/projects/demo/route-setup-preview", string(raw), scopes, "preview")
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var review struct {
				Hash      string `json:"review_sha256"`
				Preserved bool   `json:"containers_unchanged"`
				Imports   bool   `json:"imports_routes"`
			}
			json.Unmarshal(w.Body.Bytes(), &review)
			if review.Preserved != published || review.Imports != (transfer != nil) {
				t.Fatal("deployment impact hidden", w.Body.String())
			}
			input.Review = review.Hash
			raw, _ = json.Marshal(input)
			checked.err = model.Uncertain("STATE_DIVERGED")
			w = send("POST", "/v1/projects/demo/route-setup", string(raw), scopes, "diverged")
			if w.Code != 409 {
				t.Fatal("divergence accepted", w.Code)
			}
			checked.err = nil
			if transfer != nil {
				original := transfer.source
				transfer.source += "# concurrent operator change\n"
				stale := send("POST", "/v1/projects/demo/route-setup", string(raw), scopes, "stale-site")
				if stale.Code != 409 || !strings.Contains(stale.Body.String(), "ROUTE_REVIEW_CHANGED") {
					t.Fatal("changed site was moved", stale.Code, stale.Body.String())
				}
				transfer.source = original
			}
			w = send("POST", "/v1/projects/demo/route-setup", string(raw), scopes, "connect")
			if w.Code != 202 {
				t.Fatal(w.Code, w.Body.String())
			}
			var accepted model.Job
			json.Unmarshal(w.Body.Bytes(), &accepted)
			accepted, _ = a.Engine.Store.Job(accepted.ID)
			next := accepted.Input.Project
			if transfer != nil && (len(accepted.Input.RouteEdits) != 1 || len(next.CaddyRoutes) != 1 || next.CaddyRoutes[0].Domain != "api.example.com") {
				t.Fatal("transfer was not journaled", accepted.Input)
			}
			wantDomains := append(append([]string{}, p.Domains...), "api.example.com")
			if strings.Join(next.Domains, ",") != strings.Join(wantDomains, ",") || next.BluePort != p.BluePort || next.RouteService != p.RouteService || len(next.ServiceRoutes) != 1 || next.ServiceRoutes[0].Service != "api" || next.ServiceRoutes[0].ContainerPort != 3040 {
				t.Fatal("sibling route replaced", next)
			}
			if (accepted.Input.Release == nil) != review.Preserved {
				t.Fatal("wrong deployment plan", accepted)
			}
			before := base.calls + routes.calls
			retry := send("POST", "/v1/projects/demo/route-setup", string(raw), scopes, "connect")
			if retry.Code != 202 || retry.Body.String() != w.Body.String() || base.calls+routes.calls != before {
				t.Fatal("retry repeated planning")
			}
		})
	}
}

type serviceTransferRoutes struct {
	*checkedRouteSetup
	source string
}

func (r *serviceTransferRoutes) PlanServiceRouteImport(context.Context, model.Project, []string, bool) (runtime.ServiceRouteImport, error) {
	return runtime.ServiceRouteImport{
		Edits:     []model.RouteEdit{{Path: "/etc/caddy/Caddyfile", Before: r.source, After: "", Domains: []string{"api.example.com"}}},
		Templates: []model.CaddyRoute{{Domain: "api.example.com", Address: "api.example.com", Body: "\n reverse_proxy " + model.CaddyUpstream + "\n"}},
		Existing:  []runtime.ExistingCaddyRoute{{Domains: []string{"api.example.com"}, PreviousUpstream: "localhost:8080", RemainingDomains: []string{}, RulesPreserved: true}},
	}, nil
}

func TestPublicProjectDoesNotExposeImportedCaddyCredentials(t *testing.T) {
	p := model.Project{ID: "demo", CaddyRoutes: []model.CaddyRoute{{Domain: "api.example.com", Body: "basic_auth { user private_hash }"}}}
	data, _ := json.Marshal(publicProject(p))
	if strings.Contains(string(data), "private_hash") || len(p.CaddyRoutes) != 1 {
		t.Fatal("imported config exposed or erased", string(data))
	}
}

func (d *routeSetupDocker) PublishedUpstream(context.Context, model.Project, string, int) (string, error) {
	return d.upstream, d.observeErr
}

type routeSetupRoutes struct {
	*routesStub
	source string
}
type checkedRouteSetup struct {
	*routesStub
	err error
}

func (r *checkedRouteSetup) Ensure(ctx context.Context, p model.Project) error {
	if r.err != nil {
		return r.err
	}
	return r.routesStub.Ensure(ctx, p)
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
