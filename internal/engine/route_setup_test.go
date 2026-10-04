package engine

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

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
