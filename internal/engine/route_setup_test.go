package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
)

type publishingRouteDocker struct {
	*fakeDocker
	ports    []int
	occupied int
}

type recordingRouteChanges struct {
	*fakeRoutes
	snippet string
}

type observedRouteChange struct {
	*fakeRoutes
	snippet string
}

func (r *observedRouteChange) Ensure(_ context.Context, p model.Project) error {
	slot := ""
	if p.State == "running" {
		slot = p.Active
	}
	if r.snippet != string(runtime.Snippet(p, slot)) {
		return model.Uncertain("STATE_DIVERGED")
	}
	return nil
}

func TestRecoverPendingDomainAndLastDomainUnlink(t *testing.T) {
	for _, action := range []string{"route_setup", "route_unlink"} {
		for _, observed := range []string{"previous", "next", "diverged"} {
			t.Run(action+"/"+observed, func(t *testing.T) {
				e, j, _, routes, events := engineFixture(t, "blue")
				previous, _ := e.Store.Project(j.ProjectID)
				previous.Mode, previous.ZeroDowntime, previous.GreenPort = "compose", false, 0
				previous.RouteService, previous.RoutePort = "web", 3000
				if action == "route_setup" {
					previous.State = "stopped"
				}
				next := previous
				next.Domains = []string{}
				if action == "route_setup" {
					next.Domains = append(append([]string{}, previous.Domains...), "api.example.com")
					next.ServiceRoutes = []model.ServiceRoute{{Service: "api", ContainerPort: 3040, Domains: []string{"api.example.com"}, Managed: true}}
				}
				j.Action, j.Status, j.Input = action, "recovery_required", model.Input{Previous: &previous, Project: &next}
				if err := e.Store.Update(j, &previous); err != nil {
					t.Fatal(err)
				}
				want := previous
				if observed == "next" {
					want = next
				}
				slot := ""
				if want.State == "running" {
					slot = want.Active
				}
				snippet := string(runtime.Snippet(want, slot))
				if observed == "diverged" {
					snippet = "manual change"
				}
				e.Routes = &observedRouteChange{fakeRoutes: routes, snippet: snippet}
				err := e.Reconcile(context.Background(), j.ID)
				if observed == "diverged" {
					if err == nil {
						t.Fatal("reconciled an unknown route")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				saved, _ := e.Store.Project(j.ProjectID)
				if !reflect.DeepEqual(saved, want) || len(*events) != 0 {
					t.Fatal("recovery changed containers, state or drain intent", saved, want, *events)
				}
			})
		}
	}

}

func (r *recordingRouteChanges) Set(ctx context.Context, p model.Project, slot string) error {
	r.snippet = string(runtime.Snippet(p, slot))
	return r.fakeRoutes.Set(ctx, p, slot)
}

func TestStoppedDomainConnectionActivatesOnlyAfterHealthyStart(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		t.Run(map[bool]string{true: "healthy", false: "unhealthy"}[healthy], func(t *testing.T) {
			e, j, docker, routes, events := engineFixture(t, "blue")
			e.Config.DrainSeconds = 0
			previous, _ := e.Store.Project(j.ProjectID)
			previous.Mode, previous.ZeroDowntime, previous.GreenPort, previous.State = "compose", false, 0, "stopped"
			previous.RouteService, previous.RoutePort = "web", 3000
			next := previous
			next.Domains = append(append([]string{}, previous.Domains...), "api.example.com")
			next.ServiceRoutes = []model.ServiceRoute{{Service: "api", ContainerPort: 3040, Domains: []string{"api.example.com"}, Managed: true}}
			candidate := *j.Input.Release
			j.Action = "route_setup"
			j.Input = model.Input{Previous: &previous, Project: &next, RouteTarget: &model.PublishedRoute{Service: "api", ContainerPort: 3040}}
			if err := e.Store.Update(j, &previous); err != nil {
				t.Fatal(err)
			}
			d := &publishingRouteDocker{fakeDocker: docker}
			e.Docker = d
			r := &recordingRouteChanges{fakeRoutes: routes}
			e.Routes = r
			e.execute(context.Background(), j)
			done, _ := e.Store.Job(j.ID)
			saved, _ := e.Store.Project(j.ProjectID)
			if done.Status != "succeeded" || saved.State != "stopped" || saved.ServiceRoutes[0].HostPort != 0 || strings.Join(*events, ",") != "route:" || strings.Contains(r.snippet, "reverse_proxy") || !strings.Contains(r.snippet, "api.example.com") {
				t.Fatal("saving a pending route changed containers or sent traffic", done, saved, *events, r.snippet)
			}
			if !reflect.DeepEqual(saved.Slots, previous.Slots) || !reflect.DeepEqual(saved.Releases, previous.Releases) {
				t.Fatal("pending route changed the release")
			}
			env, err := os.ReadFile(runtime.EnvironmentPath(e.Config, saved, candidate.Environment))
			if err != nil {
				t.Fatal(err)
			}
			envHash := sha256.Sum256(env)
			doc, _ := json.Marshal(map[string]any{"services": map[string]any{"web": map[string]any{"image": "web:stable"}, "api": map[string]any{"image": "api:stable"}}, "x-dockyard-env-sha256": hex.EncodeToString(envHash[:])})
			hash := sha256.Sum256(doc)
			candidate.Compose = "cmp-" + hex.EncodeToString(hash[:])
			folder := filepath.Join(e.Config.ProjectsRoot, saved.ID, "compose")
			os.MkdirAll(folder, 0700)
			os.WriteFile(filepath.Join(folder, candidate.Compose+".yml"), doc, 0600)
			start := model.Job{ID: "start-job", ProjectID: saved.ID, Action: "start", Status: "running", Created: time.Now(), Input: model.Input{Release: &candidate}}
			if err := e.Store.Accept(start, nil, "start", "start", 10, 10); err != nil {
				t.Fatal(err)
			}
			if !healthy {
				docker.healthErr = model.Fail("CANDIDATE_UNHEALTHY")
			}
			*events = nil
			e.execute(context.Background(), start)
			done, _ = e.Store.Job(start.ID)
			saved, _ = e.Store.Project(saved.ID)
			order := strings.Join(*events, ",")
			if !strings.Contains(order, "start:blue,resolve-bindings,health:blue") {
				t.Fatal("missing start checks", order)
			}
			if healthy {
				if done.Status != "succeeded" || saved.State != "running" || !strings.Contains(order, "health:blue,route:blue") || !strings.Contains(r.snippet, "reverse_proxy 127.0.0.1:45002") || saved.ServiceRoutes[0].HostPort != 45002 {
					t.Fatal("pending route failed to activate", done, saved, order, r.snippet)
				}
			} else if done.Status != "failed" || saved.State != "stopped" || strings.Contains(order, "route:blue") || strings.Contains(r.snippet, "reverse_proxy") {
				t.Fatal("unhealthy start activated traffic", done, saved, order, r.snippet)
			}
		})
	}
}

func TestUnlinkServiceDomainOnlyChangesRouting(t *testing.T) {
	for _, state := range []string{"running", "stopped"} {
		for _, outcome := range []string{"success", "changed-snapshot", "uncertain-reload"} {
			t.Run(state+"/"+outcome, func(t *testing.T) {
				e, j, _, routes, events := engineFixture(t, "blue")
				previous, _ := e.Store.Project(j.ProjectID)
				previous.Mode, previous.ZeroDowntime, previous.GreenPort, previous.State = "compose", false, 0, state
				previous.RouteService, previous.RoutePort = "web", 3000
				previous.Domains = []string{"app.example.com", "api.example.com"}
				previous.ServiceRoutes = []model.ServiceRoute{{Service: "api", ContainerPort: 3040, HostPort: 3032, Domains: []string{"api.example.com"}, Managed: true}}
				next := previous
				next.Domains, next.ServiceRoutes = []string{"app.example.com"}, nil
				j.Action, j.Input = "route_unlink", model.Input{Previous: &previous, Project: &next}
				stored := previous
				if outcome == "changed-snapshot" {
					stored.RoutePort = 3001
				}
				if outcome == "uncertain-reload" {
					routes.err = model.Uncertain("ROUTE_OUTCOME_UNKNOWN")
				}
				if err := e.Store.Update(j, &stored); err != nil {
					t.Fatal(err)
				}
				r := &recordingRouteChanges{fakeRoutes: routes}
				e.Routes = r
				e.execute(context.Background(), j)
				done, _ := e.Store.Job(j.ID)
				saved, _ := e.Store.Project(j.ProjectID)
				for _, event := range *events {
					if !strings.HasPrefix(event, "route:") {
						t.Fatal("unlink used Docker", *events)
					}
				}
				if outcome == "success" {
					if done.Status != "succeeded" || len(saved.Domains) != 1 || strings.Contains(r.snippet, "api.example.com") || saved.State != state {
						t.Fatal(done, saved, r.snippet)
					}
					if owner, _ := e.Store.DomainOwner("api.example.com"); owner != "" {
						t.Fatal("unlinked hostname still reserved", owner)
					}
				} else if !reflect.DeepEqual(saved, stored) || outcome == "uncertain-reload" && done.Status != "recovery_required" || outcome == "changed-snapshot" && routes.switched {
					t.Fatal("failed unlink changed metadata", done, saved)
				}
			})
		}
	}
}

func (d *publishingRouteDocker) PortFree(_ context.Context, port int) (bool, error) {
	d.ports = append(d.ports, port)
	return port != d.occupied, nil
}

func (d *publishingRouteDocker) ResolveRouteBindings(_ context.Context, p *model.Project, _ string) error {
	*d.events = append(*d.events, "resolve-bindings")
	p.BluePort = 45001
	p.ServiceRoutes = append([]model.ServiceRoute(nil), p.ServiceRoutes...)
	for i := range p.ServiceRoutes {
		p.ServiceRoutes[i].HostPort = 45002
	}
	return nil
}

func TestServiceDomainDeploymentDiscoversDockerPortsBeforeHealthAndRouting(t *testing.T) {
	for _, scenario := range []struct {
		occupied int
		transfer bool
	}{{0, false}, {3010, false}, {0, true}, {3010, true}} {
		occupied := scenario.occupied
		name := map[int]string{0: "success", 3010: "port-taken"}[occupied]
		if scenario.transfer {
			name += "-move-global-site"
		}
		t.Run(name, func(t *testing.T) {
			e, j, docker, routes, events := engineFixture(t, "blue")
			e.Config.DrainSeconds = 0
			previous, _ := e.Store.Project(j.ProjectID)
			previous.Mode, previous.ZeroDowntime, previous.GreenPort = "compose", false, 0
			previous.RouteService, previous.RoutePort = "web", 3000
			previous.ServiceInstances = map[string]model.ServiceInstance{"web": {Name: "old-update", Port: 34999}}
			next := previous
			next.Domains = []string{"app.example.com", "api.example.com"}
			next.ServiceRoutes = []model.ServiceRoute{{Service: "api", ContainerPort: 3040, HostPort: 3010, Domains: []string{"api.example.com"}, Managed: true}}
			env := j.Input.Release.Environment
			envBytes, err := os.ReadFile(filepath.Join(e.Config.ProjectsRoot, previous.ID, "env", env+".env"))
			if err != nil {
				t.Fatal(err)
			}
			envHash := sha256.Sum256(envBytes)
			doc, _ := json.Marshal(map[string]any{"services": map[string]any{"web": map[string]any{"image": "web:stable"}, "api": map[string]any{"image": "api:stable"}}, "x-dockyard-env-sha256": hex.EncodeToString(envHash[:])})
			hash := sha256.Sum256(doc)
			release := *j.Input.Release
			release.Compose = "cmp-" + hex.EncodeToString(hash[:])
			folder := filepath.Join(e.Config.ProjectsRoot, previous.ID, "compose")
			if err = os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(folder, release.Compose+".yml"), doc, 0600); err != nil {
				t.Fatal(err)
			}
			j.Action, j.Input = "route_setup", model.Input{Previous: &previous, Project: &next, Release: &release, RouteTarget: &model.PublishedRoute{Service: "api", ContainerPort: 3040, HostPort: 3010}}
			if scenario.transfer {
				j.Input.RouteEdits = []model.RouteEdit{{Path: "/etc/caddy/Caddyfile", Before: "reviewed site", After: "", Domains: []string{"api.example.com"}}}
				e.Routes = &importingRoutes{routes}
			}
			if err = e.Store.Update(j, &previous); err != nil {
				t.Fatal(err)
			}
			d := &publishingRouteDocker{fakeDocker: docker, occupied: occupied}
			e.Docker = d
			e.execute(context.Background(), j)
			done, _ := e.Store.Job(j.ID)
			current, _ := e.Store.Project(j.ProjectID)
			order := strings.Join(*events, ",")
			if scenario.transfer && !strings.Contains(order, "import,route:,stop:blue") {
				t.Fatal("manual site was not moved into maintenance before stopping its service", order)
			}
			if !strings.Contains(order, "route:,stop:blue") || strings.Contains(order, "pull") || len(d.ports) != 0 {
				t.Fatal("unsafe binding deployment", done, order, d.ports)
			}
			if done.Status != "succeeded" || !strings.Contains(order, "stop:blue,start:blue,resolve-bindings,health:blue,route:blue") || current.State != "running" || len(current.ServiceRoutes) != 1 || current.Domains[0] != "app.example.com" {
				t.Fatal(done, current, order)
			}
			if current.BluePort != 45001 || current.ServiceRoutes[0].HostPort != 45002 || len(current.ServiceInstances) != 0 || done.Input.Project.ServiceRoutes[0].HostPort != 45002 {
				t.Fatal("Docker assignment was not journaled or stale service instance survived", current, done.Input.Project)
			}
		})
	}
}

func containsPort(ports []int, port int) bool {
	for _, candidate := range ports {
		if candidate == port {
			return true
		}
	}
	return false
}

type routeDocker struct {
	*fakeDocker
	upstream string
}

func (d *routeDocker) PublishedUpstream(context.Context, model.Project, string, int) (string, error) {
	return d.upstream, nil
}

type importingRoutes struct{ *fakeRoutes }

func (*importingRoutes) VerifyRouteImport([]model.RouteEdit) error { return nil }
func (r *importingRoutes) ImportRoutes(ctx context.Context, _ []model.RouteEdit, p model.Project) error {
	*r.events = append(*r.events, "import")
	if p.State != "running" {
		return r.Set(ctx, p, "")
	}
	return r.Set(ctx, p, p.Active)
}

func TestRouteSetupPreservesContainersAndReleaseAndRechecksUpstream(t *testing.T) {
	for _, mode := range []string{"new", "import", "changed-upstream", "changed-snapshot", "uncertain-reload"} {
		t.Run(mode, func(t *testing.T) {
			e, j, docker, routes, events := engineFixture(t, "blue")
			previous, _ := e.Store.Project(j.ProjectID)
			previous.Mode = "compose"
			previous.Adoption = &model.Adoption{SourceName: "tasks", ComposeProject: "tasks"}
			previous.Domains = nil
			previous.BluePort = 0
			previous.GreenPort = 0
			previous.ZeroDowntime = false
			next := previous
			next.Domains = []string{"app.example.com"}
			next.BluePort = 3031
			next.PublishedRoute = &model.PublishedRoute{Service: "web", ContainerPort: 3000, HostPort: 3031}
			j.Action = "route_setup"
			j.Input = model.Input{Previous: &previous, Project: &next}
			if mode == "import" {
				j.Input.RouteEdits = []model.RouteEdit{{Path: "/etc/caddy/Caddyfile", Before: "reviewed", After: ""}}
			}
			d := &routeDocker{fakeDocker: docker, upstream: "127.0.0.1:3031"}
			e.Docker = d
			e.Routes = &importingRoutes{routes}
			if mode == "changed-upstream" {
				d.upstream = "127.0.0.1:3032"
			}
			stored := previous
			if mode == "changed-snapshot" {
				stored.State = "stopped"
			}
			if mode == "uncertain-reload" {
				routes.err = model.Uncertain("ROUTE_OUTCOME_UNKNOWN")
			}
			if err := e.Store.Update(j, &stored); err != nil {
				t.Fatal(err)
			}
			e.execute(context.Background(), j)
			actual, _ := e.Store.Job(j.ID)
			current, _ := e.Store.Project(j.ProjectID)
			for _, event := range *events {
				if strings.HasPrefix(event, "start:") || strings.HasPrefix(event, "stop:") || event == "pull" {
					t.Fatal("route setup mutated containers", *events)
				}
			}
			if !reflect.DeepEqual(current.Slots, previous.Slots) || !reflect.DeepEqual(current.Releases, previous.Releases) || !reflect.DeepEqual(current.Adoption, previous.Adoption) {
				t.Fatal("route setup changed identity or release", current)
			}
			switch mode {
			case "new", "import":
				if actual.Status != "succeeded" || current.PublishedRoute == nil || current.BluePort != 3031 || current.State != "running" {
					t.Fatal(actual, current)
				}
			case "uncertain-reload":
				if actual.Status != "recovery_required" || current.PublishedRoute != nil {
					t.Fatal("uncertain outcome not retained", actual, current)
				}
			default:
				if actual.Status != "failed" || routes.switched || current.PublishedRoute != nil {
					t.Fatal("stale review changed route", actual, current)
				}
			}
		})
	}
}
