package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
)

type configurationDocker struct{ *updatingDocker }

func (d configurationDocker) ResolveServiceRouteBindings(_ context.Context, _ *model.Project, _ *model.ServiceUpdate) error {
	return nil
}

func configurationFixture(t *testing.T) (*Engine, model.Job, *updatingDocker, *snippetServiceRoutes, *[]string) {
	e, j, d, r, events := assignedServiceFixture(t)
	old, next := j.Input.Previous, j.Input.Project
	release := old.Slots[old.Active]
	release.ID = "configuration-new"
	j.Action = "configuration_update"
	j.Input.Release = &release
	api := *j.Input.ServiceUpdate
	api.SourceRelease = &release
	web := api
	web.Service = "web"
	web.Instance.Name = "dy-update-web"
	web.Instance.Port, web.Instance.ContainerPort = 3012, 3000
	previous := old.ServiceInstances["web"]
	web.Previous = &previous
	j.Input.ServiceUpdate = nil
	j.Input.ConfigurationUpdates = []model.ServiceUpdate{api, web}
	next.ServiceInstances = map[string]model.ServiceInstance{"web": previous}
	e.Docker = configurationDocker{d}
	if err := e.Store.Update(j, old); err != nil {
		t.Fatal(err)
	}
	return e, j, d, r, events
}

func TestConfigurationCutoverWaitsForAllAppsAndNeverStopsStack(t *testing.T) {
	e, j, _, r, events := configurationFixture(t)
	e.execute(context.Background(), j)
	done, _ := e.Store.Job(j.ID)
	p, _ := e.Store.Project(j.ProjectID)
	current, _ := p.Current()
	if done.Status != "succeeded" || current.ID != j.Input.Release.ID || len(p.ServiceInstances) != 2 || len(r.writes) != 1 {
		t.Fatal(done, p, r.writes)
	}
	order := strings.Join(*events, ",")
	route := strings.Index(order, "route:blue")
	for _, name := range []string{"dy-update-candidate", "dy-update-web"} {
		if i := strings.Index(order, "promote:"+name); i < 0 || i > route {
			t.Fatal("switched before all candidates were ready", order)
		}
	}
	for _, name := range []string{"api", "dy-web-existing"} {
		if i := strings.Index(order, "stop-service:"+name); i < route {
			t.Fatal("stopped serving app early", order)
		}
	}
	if strings.Contains(order, "postgres") || strings.Contains(order, "stop:blue") || strings.Contains(order, "route:,") {
		t.Fatal("stopped dependencies or entered maintenance", order)
	}
}

func TestConfigurationReadinessFailureAndRollbackKeepOldRelease(t *testing.T) {
	for _, check := range []int{2, 5} {
		t.Run(map[int]string{2: "before switch", 5: "after switch"}[check], func(t *testing.T) {
			e, j, d, r, events := configurationFixture(t)
			before := r.live
			d.failCheck = check
			e.execute(context.Background(), j)
			done, _ := e.Store.Job(j.ID)
			p, _ := e.Store.Project(j.ProjectID)
			current, _ := p.Current()
			if done.Status != "failed" || current.ID == j.Input.Release.ID || r.live != before {
				t.Fatal(done, current, r.live)
			}
			order := strings.Join(*events, ",")
			if strings.Contains(order, "stop-service:api") || strings.Contains(order, "stop-service:dy-web-existing") {
				t.Fatal("retired serving version", order)
			}
			if !strings.Contains(order, "stop-service:dy-update-candidate") || !strings.Contains(order, "stop-service:dy-update-web") {
				t.Fatal("leaked candidate", order)
			}
		})
	}
}

func TestConfigurationRecoveryObservesWholeCutover(t *testing.T) {
	for _, live := range []string{"old", "new", "unknown"} {
		t.Run(live, func(t *testing.T) {
			e, j, _, r, events := configurationFixture(t)
			next := j.Input.Project
			next.Slots = map[string]model.Release{next.Active: *j.Input.Release}
			for _, plan := range j.Input.ConfigurationUpdates {
				next.ServiceInstances[plan.Service] = plan.Instance
			}
			j.Status, j.Phase = "recovery_required", "configuration_route_intent"
			if live == "new" {
				r.live = string(runtime.Snippet(*next, next.Active))
			}
			if live == "unknown" {
				r.live = "unknown"
			}
			if err := e.Store.Update(j, nil); err != nil {
				t.Fatal(err)
			}
			err := e.Reconcile(context.Background(), j.ID)
			order := strings.Join(*events, ",")
			if live == "unknown" {
				if err == nil || order != "" {
					t.Fatal("guessed live routes", err, order)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p, _ := e.Store.Project(j.ProjectID)
			current, _ := p.Current()
			if (live == "new") != (current.ID == j.Input.Release.ID) {
				t.Fatal("recovered wrong source", current)
			}
			if live == "old" && strings.Contains(order, "stop-service:api") || live == "new" && strings.Contains(order, "stop-service:dy-update-candidate") {
				t.Fatal("retired serving app", order)
			}
		})
	}
}
