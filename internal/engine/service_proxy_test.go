package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

type proxyUpdatingDocker struct {
	*updatingDocker
	failSwitch, activeRequests bool
}

func (d *proxyUpdatingDocker) ResolveServiceRouteBindings(_ context.Context, _ *model.Project, plan *model.ServiceUpdate) error {
	plan.Instance.Port, plan.Instance.Ports, plan.Instance.Replicas = 47001, []int{47001, 47002}, 2
	return nil
}

func (d *proxyUpdatingDocker) SwitchServiceProxy(_ context.Context, _ model.Project, _ model.ServiceUpdate, restore bool) error {
	*d.events = append(*d.events, fmt.Sprintf("proxy-switch:restore=%v", restore))
	if d.failSwitch {
		return model.Fail("SERVICE_PROXY_CONTROL_REQUIRED")
	}
	return nil
}

func (d *proxyUpdatingDocker) DrainedServiceProxy(_ context.Context, _ model.Project, _ model.ServiceUpdate, restore bool) error {
	*d.events = append(*d.events, fmt.Sprintf("proxy-drained:restore=%v", restore))
	if d.activeRequests {
		return model.Uncertain("SERVICE_PROXY_DRAIN_REQUIRED")
	}
	return nil
}

func internalProxyFixture(t *testing.T) (*Engine, model.Job, *proxyUpdatingDocker, *fakeRoutes, *[]string) {
	t.Helper()
	e, j, base, routes, events := serviceFixture(t, "seamless")
	for _, p := range []*model.Project{j.Input.Previous, j.Input.Project} {
		p.Mode, p.RouteService, p.RoutePort = "compose", "loadbalancer", 8080
	}
	plan := j.Input.ServiceUpdate
	plan.Service, plan.Previous.Name = "api", "api"
	plan.Previous.Replicas, plan.Instance.Replicas = 2, 2
	plan.Instance.ContainerPort = 3000
	plan.Proxy = &model.ServiceProxy{Service: "loadbalancer", Backend: "api_replicas", AdminPort: 9999, AppPort: 3000}
	if err := e.Store.Update(j, j.Input.Previous); err != nil {
		t.Fatal(err)
	}
	d := &proxyUpdatingDocker{updatingDocker: base}
	e.Docker = d
	return e, j, d, routes, events
}

func TestInternalProxyUpdateOrdersHealthDrainAndRetirement(t *testing.T) {
	for _, scenario := range []string{"success", "readiness-failed", "after-alias-promotion", "control-failed", "active-requests"} {
		t.Run(scenario, func(t *testing.T) {
			e, j, d, routes, events := internalProxyFixture(t)
			switch scenario {
			case "readiness-failed":
				d.failCheck = 1
			case "after-alias-promotion":
				d.failCheck = 2
			case "control-failed":
				d.failSwitch = true
			case "active-requests":
				d.activeRequests = true
			}
			e.execute(context.Background(), j)
			done, _ := e.Store.Job(j.ID)
			p, _ := e.Store.Project(j.ProjectID)
			order := strings.Join(*events, ",")
			if routes.switched || strings.Contains(order, "stop:blue") || strings.Contains(order, "start:blue") {
				t.Fatal("backend update recreated the load balancer, changed public routes or stopped dependencies", *events)
			}
			if scenario == "success" {
				if done.Status != "succeeded" || p.ServiceInstances["api"].Replicas != 2 || !slices.Equal(p.ServiceInstances["api"].Ports, []int{47001, 47002}) || !strings.Contains(order, "proxy-switch:restore=false,health-service:dy-update-candidate,proxy-drained:restore=false,stop-service:api") {
					t.Fatal("replicas stopped before proxy readiness and drain", done, p, *events)
				}
			} else if scenario == "readiness-failed" {
				if done.Status != "failed" || !strings.Contains(order, "stop-service:dy-update-candidate") || strings.Contains(order, "proxy-switch:") {
					t.Fatal("unexposed candidate was not safely cleaned up", done, *events)
				}
			} else if done.Status != "recovery_required" || strings.Contains(order, "stop-service:") {
				t.Fatal("uncertain proxy switch discarded a serving replica group", done, *events)
			}
		})
	}
}

func TestInternalProxyRecoveryUsesProxyStateDespiteIdenticalCaddyRoutes(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore=%v", restore), func(t *testing.T) {
			e, j, d, routes, events := internalProxyFixture(t)
			j.Input.ServiceUpdate.Instance.Release.Image = "new-image"
			j.Input.Project.ServiceInstances["api"] = j.Input.ServiceUpdate.Instance
			j.Status, j.Phase = "recovery_required", "service_proxy_intent"
			if restore {
				d.failCheck = 1
			}
			if err := e.Store.Update(j, j.Input.Previous); err != nil {
				t.Fatal(err)
			}
			p, err := e.reconcileService(context.Background(), &j)
			if err != nil {
				t.Fatal(err, *events)
			}
			order := strings.Join(*events, ",")
			if !strings.Contains(order, fmt.Sprintf("proxy-switch:restore=%v,proxy-drained:restore=%v", restore, restore)) || routes.switched {
				t.Fatal("recovery guessed a switch from unchanged Caddy routes", *events)
			}
			if restore {
				if len(p.ServiceInstances) != 0 || !strings.HasSuffix(order, "stop-service:dy-update-candidate") {
					t.Fatal("recovery did not retain the old backend group", p, *events)
				}
			} else if p.ServiceInstances["api"].Name != "dy-update-candidate" || !strings.HasSuffix(order, "stop-service:api") {
				t.Fatal("recovery did not finish the verified proxy switch", p, *events)
			}
		})
	}
}

func TestInternalProxyRecoveryBeforeAliasPromotionCleansOnlyCandidate(t *testing.T) {
	e, j, _, _, events := internalProxyFixture(t)
	j.Input.ServiceUpdate.Instance.Release.Image = "new-image"
	j.Input.Project.ServiceInstances["api"] = j.Input.ServiceUpdate.Instance
	j.Status, j.Phase = "recovery_required", "service_readiness"
	if err := e.Store.Update(j, j.Input.Previous); err != nil {
		t.Fatal(err)
	}
	p, err := e.reconcileService(context.Background(), &j)
	order := strings.Join(*events, ",")
	if err != nil || len(p.ServiceInstances) != 0 || strings.Contains(order, "proxy-switch") || !strings.HasSuffix(order, "stop-service:dy-update-candidate") {
		t.Fatal("unexposed candidate was treated as a completed DNS switch", p, err, *events)
	}
}
