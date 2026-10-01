package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

type updatingDocker struct {
	*fakeDocker
	checks    int
	failCheck int
	unchanged bool
	verifyErr error
	stopErr   error
}

func (d *updatingDocker) VerifyServiceUpdate(context.Context, model.Project, model.ServiceUpdate) error {
	return d.verifyErr
}
func (d *updatingDocker) PullServiceUpdate(_ context.Context, _ model.Project, plan *model.ServiceUpdate) (bool, error) {
	*d.events = append(*d.events, "pull:"+plan.Service)
	plan.Instance.Release.Image = "new-image"
	return !d.unchanged, nil
}
func (d *updatingDocker) StartServiceUpdate(_ context.Context, _ model.Project, plan model.ServiceUpdate) error {
	*d.events = append(*d.events, "start-service:"+plan.Instance.Name)
	return d.startErr
}
func (d *updatingDocker) HealthyServiceUpdate(_ context.Context, _ model.Project, plan model.ServiceUpdate) error {
	d.checks++
	*d.events = append(*d.events, "health-service:"+plan.Instance.Name)
	if d.checks == d.failCheck {
		return model.Fail("SERVICE_UNHEALTHY")
	}
	return nil
}
func (d *updatingDocker) PromoteServiceUpdate(_ context.Context, _ model.Project, plan model.ServiceUpdate) error {
	*d.events = append(*d.events, "promote:"+plan.Instance.Name)
	return nil
}
func (d *updatingDocker) StopServiceInstance(_ context.Context, _ model.Project, instance model.ServiceInstance) error {
	*d.events = append(*d.events, "stop-service:"+instance.Name)
	return d.stopErr
}

func serviceFixture(t *testing.T, mode string) (*Engine, model.Job, *updatingDocker, *fakeRoutes, *[]string) {
	t.Helper()
	e, j, d, r, events := engineFixture(t, "blue")
	e.Config.DrainSeconds = 0
	old, _ := e.Store.Project(j.ProjectID)
	old.ZeroDowntime = false
	old.GreenPort = 0
	old.RouteService = "web"
	old.RoutePort = 3000
	next := old
	next.ServicePorts = []int{3010, 3011}
	next.ServiceMode = true
	next.ServiceInstances = map[string]model.ServiceInstance{}
	previous := model.ServiceInstance{Name: "web", Release: old.Slots[old.Active], Port: old.BluePort}
	candidate := previous
	if mode == "seamless" {
		candidate.Name = "dy-update-candidate"
		candidate.Port = 3010
	}
	j.Action = "service_update"
	j.Input = model.Input{Previous: &old, Project: &next, ServiceUpdate: &model.ServiceUpdate{Service: "web", Mode: mode, Previous: &previous, Instance: candidate}}
	if err := e.Store.Update(j, &old); err != nil {
		t.Fatal(err)
	}
	ud := &updatingDocker{fakeDocker: d}
	e.Docker = ud
	return e, j, ud, r, events
}

func TestServiceUpdateTouchesOnlySelectedServiceAndOrdersCutover(t *testing.T) {
	for _, scenario := range []string{"success", "before-ready", "after-promotion", "route-rejected", "route-unknown", "after-switch", "retire-failed", "unchanged", "snapshot-changed"} {
		t.Run(scenario, func(t *testing.T) {
			e, j, d, r, events := serviceFixture(t, "seamless")
			switch scenario {
			case "before-ready":
				d.failCheck = 1
			case "after-promotion":
				d.failCheck = 2
			case "route-rejected":
				r.err = model.Fail("CADDY_RELOAD_REJECTED")
			case "route-unknown":
				r.err = model.Uncertain("ROUTE_OUTCOME_UNKNOWN")
			case "after-switch":
				d.failCheck = 3
			case "retire-failed":
				d.stopErr = model.Fail("SERVICE_STOP_FAILED")
			case "unchanged":
				d.unchanged = true
			case "snapshot-changed":
				p, _ := e.Store.Project(j.ProjectID)
				p.ReadinessPath = "/changed"
				e.Store.Update(j, &p)
			}
			e.execute(context.Background(), j)
			done, _ := e.Store.Job(j.ID)
			p, _ := e.Store.Project(j.ProjectID)
			order := strings.Join(*events, ",")
			if len(d.stopped) != 0 || strings.Contains(order, "start:blue") || strings.Contains(order, "pull,") {
				t.Fatal("used whole-stack Docker operation", order)
			}
			if strings.Contains(order, "postgres") {
				t.Fatal("touched database", order)
			}
			switch scenario {
			case "success":
				if done.Status != "succeeded" || p.ServiceInstances["web"].Name != "dy-update-candidate" || p.Port(p.Active) != 3010 || p.ZeroDowntime {
					t.Fatal(done, p, order)
				}
				if order != "pull:web,start-service:dy-update-candidate,health-service:dy-update-candidate,promote:dy-update-candidate,health-service:dy-update-candidate,route:blue,health-service:dy-update-candidate,stop-service:web" {
					t.Fatal("unsafe cutover order", order)
				}
			case "unchanged":
				if done.Status != "succeeded" || done.Warning != "SERVICE_ALREADY_CURRENT" || order != "pull:web" || len(p.ServiceInstances) != 0 || len(p.ServicePorts) != 2 {
					t.Fatal(done, p, order)
				}
			case "snapshot-changed":
				if done.Error != "SERVICE_REVIEW_CHANGED" || order != "" {
					t.Fatal(done, order)
				}
			case "route-unknown", "retire-failed":
				if done.Status != "recovery_required" || strings.Contains(order, "stop-service:dy-update-candidate") {
					t.Fatal("removed potentially serving candidate", done, order)
				}
			default:
				if done.Status != "failed" || strings.Contains(order, "stop-service:web") || !strings.Contains(order, "stop-service:dy-update-candidate") || len(p.ServiceInstances) != 0 {
					t.Fatal("failed update changed old service", done, p, order)
				}
				if len(p.ServicePorts) != 2 {
					t.Fatal("failed update lost reusable port pair", p)
				}
				if scenario == "after-switch" && strings.Count(order, "route:blue") != 2 {
					t.Fatal("did not restore traffic", order)
				}
			}
		})
	}
}

func TestServiceRestartNeverSwitchesTrafficOrTouchesDependencies(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "unhealthy"}[fails], func(t *testing.T) {
			e, j, d, _, events := serviceFixture(t, "restart")
			if fails {
				d.failCheck = 1
			}
			e.execute(context.Background(), j)
			done, _ := e.Store.Job(j.ID)
			p, _ := e.Store.Project(j.ProjectID)
			order := strings.Join(*events, ",")
			if order != "pull:web,start-service:web,health-service:web" {
				t.Fatal("touched sibling or route", order)
			}
			if fails {
				if done.Status != "recovery_required" || len(p.ServiceInstances) != 0 {
					t.Fatal(done, p)
				}
			} else if done.Status != "succeeded" || p.ServiceInstances["web"].Release.Image != "new-image" {
				t.Fatal(done, p)
			}
		})
	}
}

func TestFirstServiceUpdateFromPersistedJob(t *testing.T) {
	for _, mode := range []string{"seamless", "restart"} {
		t.Run(mode, func(t *testing.T) {
			e, admitted, _, _, events := serviceFixture(t, mode)
			// The worker reads accepted jobs back from SQLite. An empty map is
			// omitted from JSON and becomes nil, unlike the admission snapshot.
			queued, err := e.Store.Job(admitted.ID)
			if err != nil {
				t.Fatal(err)
			}
			if queued.Input.Project.ServiceInstances != nil {
				t.Fatal("fixture did not reproduce the first persisted service update")
			}
			e.execute(context.Background(), queued)
			done, err := e.Store.Job(admitted.ID)
			if err != nil {
				t.Fatal(err)
			}
			project, err := e.Store.Project(admitted.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if done.Status != "succeeded" || project.ServiceInstances["web"].Release.Image != "new-image" || !e.Ready() {
				t.Fatal("persisted first update did not complete", done, project)
			}
			if len(project.ServiceInstances) != 1 || len(project.ServicePorts) != 2 || strings.Contains(strings.Join(*events, ","), "postgres") {
				t.Fatal("first update changed dependencies or lost its reserved ports", project, *events)
			}
		})
	}
}

type observedServiceRoutes struct {
	*fakeRoutes
	next    bool
	unknown bool
}

func (r observedServiceRoutes) Ensure(_ context.Context, p model.Project) error {
	if r.unknown || (len(p.ServiceInstances) > 0) != r.next {
		return model.Uncertain("ROUTE_DIVERGED")
	}
	return nil
}
func TestServiceRecoveryObservesTrafficBeforeStoppingEitherVersion(t *testing.T) {
	for _, live := range []string{"old", "new", "unknown"} {
		t.Run(live, func(t *testing.T) {
			e, j, _, r, events := serviceFixture(t, "seamless")
			plan := j.Input.ServiceUpdate
			plan.Instance.Release.Image = "new-image"
			j.Input.Project.ServiceInstances["web"] = plan.Instance
			j.Phase = "service_route_intent"
			j.Status = "recovery_required"
			e.Store.Update(j, nil)
			e.Routes = observedServiceRoutes{fakeRoutes: r, next: live == "new", unknown: live == "unknown"}
			err := e.Reconcile(context.Background(), j.ID)
			order := strings.Join(*events, ",")
			if live == "unknown" {
				if err == nil || order != "" {
					t.Fatal("guessed live traffic", err, order)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p, _ := e.Store.Project(j.ProjectID)
			if live == "new" {
				if len(p.ServiceInstances) != 1 || !strings.HasSuffix(order, "stop-service:web") {
					t.Fatal(p, order)
				}
			} else if len(p.ServiceInstances) != 0 || order != "stop-service:dy-update-candidate" {
				t.Fatal(p, order)
			}
		})
	}
}

func TestServiceRecoveryBeforeCandidateIntentKeepsExistingVersion(t *testing.T) {
	for _, mode := range []string{"seamless", "restart"} {
		t.Run(mode, func(t *testing.T) {
			e, j, _, r, events := serviceFixture(t, mode)
			j.Phase = "pulling"
			j.Status = "recovery_required"
			e.Store.Update(j, nil)
			e.Routes = observedServiceRoutes{fakeRoutes: r}
			if err := e.Reconcile(context.Background(), j.ID); err != nil {
				t.Fatal(err)
			}
			p, _ := e.Store.Project(j.ProjectID)
			if len(*events) != 0 || len(p.ServiceInstances) != 0 || len(p.ServicePorts) != 2 {
				t.Fatal("pre-start recovery touched services or lost ports", p, *events)
			}
		})
	}
}
