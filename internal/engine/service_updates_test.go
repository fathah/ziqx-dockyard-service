package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
)

type updatingDocker struct {
	*fakeDocker
	checks    int
	failCheck int
	unchanged bool
	verifyErr error
	stopErr   error
}

type resolvingServiceDocker struct{ *updatingDocker }

func (d *resolvingServiceDocker) ResolveServiceRouteBindings(_ context.Context, p *model.Project, plan *model.ServiceUpdate) error {
	*d.events = append(*d.events, "resolve-service-binding")
	plan.Instance.Port = 47001
	if plan.Mode == "restart" && plan.Service == p.RouteService {
		p.BluePort = plan.Instance.Port
	}
	return nil
}

func TestServiceUpdatesJournalDockerAssignedPortBeforeHealthAndCaddy(t *testing.T) {
	for _, mode := range []string{"restart", "seamless"} {
		t.Run(mode, func(t *testing.T) {
			e, j, base, _, events := serviceFixture(t, mode)
			j.Input.Previous.Mode, j.Input.Project.Mode = "compose", "compose"
			j.Input.ServiceUpdate.Instance.Port = 0
			if err := e.Store.Update(j, j.Input.Previous); err != nil {
				t.Fatal(err)
			}
			e.Docker = &resolvingServiceDocker{base}
			e.execute(context.Background(), j)
			done, _ := e.Store.Job(j.ID)
			p, _ := e.Store.Project(j.ProjectID)
			if done.Status != "succeeded" || p.ServiceInstances["web"].Port != 47001 || done.Input.ServiceUpdate.Instance.Port != 47001 || done.Input.Project.ServiceInstances["web"].Port != 47001 {
				t.Fatal("assigned port was not committed to project and recovery journal", done, p)
			}
			order := strings.Join(*events, ",")
			if !strings.Contains(order, "resolve-service-binding,health-service:") || !strings.Contains(order, "route:blue") || strings.Contains(order, "postgres") {
				t.Fatal("routing preceded discovery or affected dependencies", order)
			}
		})
	}
}

func TestRestartRecoveryRepairsRouteFromVerifiedDockerAssignment(t *testing.T) {
	e, j, base, routes, _ := serviceFixture(t, "restart")
	j.Input.Previous.Mode, j.Input.Project.Mode = "compose", "compose"
	j.Input.ServiceUpdate.Instance.Release.Image = "new-image"
	j.Input.ServiceUpdate.Instance.Port = 0
	j.Input.Project.ServiceInstances["web"] = j.Input.ServiceUpdate.Instance
	j.Status, j.Phase = "recovery_required", "service_candidate_intent"
	if err := e.Store.Update(j, j.Input.Previous); err != nil {
		t.Fatal(err)
	}
	e.Docker = &resolvingServiceDocker{base}
	live := &snippetServiceRoutes{fakeRoutes: routes, live: string(runtime.Snippet(*j.Input.Previous, "blue"))}
	e.Routes = live
	if err := e.Reconcile(context.Background(), j.ID); err != nil {
		t.Fatal(err)
	}
	p, _ := e.Store.Project(j.ProjectID)
	if p.ServiceInstances["web"].Port != 47001 || !strings.Contains(live.live, "reverse_proxy 127.0.0.1:47001") {
		t.Fatal("recovery left Caddy on the stale port", p, live.live)
	}
}

type snippetServiceRoutes struct {
	*fakeRoutes
	live   string
	writes []string
}

func (r *snippetServiceRoutes) Ensure(_ context.Context, p model.Project) error {
	if string(runtime.Snippet(p, p.Active)) != r.live {
		return model.Uncertain("ROUTE_DIVERGED")
	}
	return nil
}
func (r *snippetServiceRoutes) Set(ctx context.Context, p model.Project, slot string) error {
	r.live = string(runtime.Snippet(p, slot))
	r.writes = append(r.writes, r.live)
	return r.fakeRoutes.Set(ctx, p, slot)
}

func assignedServiceFixture(t *testing.T) (*Engine, model.Job, *updatingDocker, *snippetServiceRoutes, *[]string) {
	e, j, d, r, events := serviceFixture(t, "seamless")
	old := j.Input.Previous
	old.Mode = "compose"
	old.Domains = []string{"api.example.com", "web.example.com"}
	old.ServiceRoutes = []model.ServiceRoute{
		{Service: "api", ContainerPort: 8080, HostPort: 3002, Domains: []string{"api.example.com"}},
		{Service: "web", ContainerPort: 3000, HostPort: 3001, Domains: []string{"web.example.com"}},
	}
	old.ServiceInstances = map[string]model.ServiceInstance{"web": {Name: "dy-web-existing", Port: 3011, ContainerPort: 3000}}
	old.ServicePorts = []int{3010, 3011}
	next := *old
	next.ServiceInstances = map[string]model.ServiceInstance{"web": old.ServiceInstances["web"]}
	j.Input.Project = &next
	plan := j.Input.ServiceUpdate
	plan.Service = "api"
	plan.Previous.Name, plan.Previous.Port = "api", 3002
	plan.Instance.ContainerPort = 8080
	if err := e.Store.Update(j, old); err != nil {
		t.Fatal(err)
	}
	routes := &snippetServiceRoutes{fakeRoutes: r, live: string(runtime.Snippet(*old, old.Active))}
	e.Routes = routes
	return e, j, d, routes, events
}

func TestAssignedServiceCutoverAndRollbackPreserveSiblingReplacement(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "rollback"}[rollback], func(t *testing.T) {
			e, j, d, r, events := assignedServiceFixture(t)
			before := r.live
			if rollback {
				d.failCheck = 3
			}
			e.execute(context.Background(), j)
			done, _ := e.Store.Job(j.ID)
			p, _ := e.Store.Project(j.ProjectID)
			if len(r.writes) == 0 || !strings.Contains(r.writes[0], "api.example.com {\n\treverse_proxy 127.0.0.1:3010") || !strings.Contains(r.writes[0], "web.example.com {\n\treverse_proxy 127.0.0.1:3011") {
				t.Fatal("cutover changed sibling", r.writes)
			}
			if p.ServiceInstances["web"].Name != "dy-web-existing" {
				t.Fatal("lost sibling instance", p)
			}
			order := strings.Join(*events, ",")
			if strings.Contains(order, "stop-service:dy-web-existing") || strings.Contains(order, "postgres") {
				t.Fatal("retired sibling", order)
			}
			if rollback {
				if done.Status != "failed" || done.Error != "SERVICE_UNHEALTHY_ROLLED_BACK" || r.live != before || len(p.ServiceInstances) != 1 || strings.Contains(order, "stop-service:api") {
					t.Fatal(done, p, order, r.live)
				}
			} else if done.Status != "succeeded" || p.ServiceInstances["api"].ContainerPort != 8080 || !strings.HasSuffix(order, "stop-service:api") {
				t.Fatal(done, p, order)
			}
		})
	}
}

func TestAssignedServiceRecoveryObservesEachBinding(t *testing.T) {
	for _, live := range []string{"old", "new", "unknown"} {
		t.Run(live, func(t *testing.T) {
			e, j, _, r, events := assignedServiceFixture(t)
			j.Input.ServiceUpdate.Instance.Release.Image = "new-image"
			j.Input.Project.ServiceInstances["api"] = j.Input.ServiceUpdate.Instance
			j.Status, j.Phase = "recovery_required", "service_route_intent"
			if live == "new" {
				r.live = string(runtime.Snippet(*j.Input.Project, j.Input.Project.Active))
			}
			if live == "unknown" {
				r.live = "unknown"
			}
			e.Store.Update(j, nil)
			err := e.Reconcile(context.Background(), j.ID)
			order := strings.Join(*events, ",")
			if live == "unknown" {
				if err == nil || order != "" {
					t.Fatal("guessed traffic", err, order)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p, _ := e.Store.Project(j.ProjectID)
			if p.ServiceInstances["web"].Name != "dy-web-existing" || strings.Contains(order, "dy-web-existing") {
				t.Fatal("recovery touched sibling", p, order)
			}
			if live == "new" && !strings.HasSuffix(order, "stop-service:api") || live == "old" && order != "stop-service:dy-update-candidate" {
				t.Fatal("retired wrong instance", order)
			}
		})
	}
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
