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

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

type publishingRouteDocker struct {
	*fakeDocker
	ports    []int
	occupied int
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
