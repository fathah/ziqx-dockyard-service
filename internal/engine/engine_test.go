package engine

import (
	"context"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeDocker struct {
	startErr, healthErr error
	stopped             []string
	active              string
	events              *[]string
}

func (f *fakeDocker) Validate(context.Context, model.Project, model.Release) error { return nil }
func (f *fakeDocker) Pull(context.Context, model.Project, model.Release) error {
	*f.events = append(*f.events, "pull")
	return nil
}
func (f *fakeDocker) Start(ctx context.Context, p model.Project, slot string) error {
	*f.events = append(*f.events, "start:"+slot)
	if slot == f.active && p.State == "running" {
		panic("recreated serving slot")
	}
	return f.startErr
}
func (f *fakeDocker) Healthy(ctx context.Context, p model.Project, slot string, r model.Release) error {
	*f.events = append(*f.events, "health:"+slot)
	if r.ID != "old" {
		return f.healthErr
	}
	return nil
}
func (f *fakeDocker) Stop(ctx context.Context, p model.Project, slot string) error {
	f.stopped = append(f.stopped, slot)
	*f.events = append(*f.events, "stop:"+slot)
	return nil
}
func (f *fakeDocker) Logs(context.Context, model.Project, string, string, int, string) ([]byte, bool, error) {
	return nil, false, nil
}
func (f *fakeDocker) PortFree(context.Context, int) (bool, error) { return true, nil }

type fakeRoutes struct {
	err      error
	events   *[]string
	switched bool
}

func (f *fakeRoutes) Ensure(context.Context, model.Project) error                { return nil }
func (f *fakeRoutes) DomainsAvailable(context.Context, []string, []string) error { return nil }
func (f *fakeRoutes) Set(ctx context.Context, p model.Project, slot string) error {
	*f.events = append(*f.events, "route:"+slot)
	f.switched = true
	return f.err
}

func engineFixture(t *testing.T, active string) (*Engine, model.Job, *fakeDocker, *fakeRoutes, *[]string) {
	return environmentFixture(t, active, model.Production)
}

func environmentFixture(t *testing.T, active, environment string) (*Engine, model.Job, *fakeDocker, *fakeRoutes, *[]string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "docker")
	os.Mkdir(root, 0700)
	c := config.Config{ServerID: "vps-01", ProjectsRoot: root, DrainSeconds: 1, Templates: map[string]config.Template{"node": {ContainerPort: 3000}}}
	s, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	old := model.Release{ID: "old", Image: "old-image", Environment: "env-old"}
	p := model.Project{ID: "demo", AppID: "demo", Environment: model.Production, Template: "node", TemplateRevision: TemplateHash(c.Templates["node"]), Domains: []string{"app.example.com"}, BluePort: 3001, GreenPort: 3002, ZeroDowntime: true, State: "running", Active: active, Slots: map[string]model.Release{active: old}, Releases: []model.Release{old}}
	p.Environment = environment
	if environment != model.Production {
		p.ZeroDowntime = false
		p.GreenPort = 0
	}
	if err = runtime.Compose(c, p); err != nil {
		t.Fatal(err)
	}
	env, err := runtime.Environment(c, p.ID, map[string]string{"TOKEN": "new-secret"})
	if err != nil {
		t.Fatal(err)
	}
	newRelease := model.Release{ID: "new", Image: "new-image", Environment: env}
	j := model.Job{ID: "job1", ProjectID: p.ID, Action: "deploy", Status: "queued", Phase: "accepted", RequestID: "req1", Actor: "alice", Input: model.Input{Release: &newRelease}, Created: time.Now()}
	if err = s.Accept(j, &p, "op1", "fp1", 10, 10); err != nil {
		t.Fatal(err)
	}
	j.Status = "running"
	s.Update(j, nil)
	events := []string{}
	d := &fakeDocker{active: active, events: &events}
	r := &fakeRoutes{events: &events}
	e := New(c, s, d, r, nil)
	return e, j, d, r, &events
}

func TestNonProductionReplacesOnlyTheSingleSlot(t *testing.T) {
	for _, environment := range []string{model.Development, model.Staging} {
		t.Run(environment, func(t *testing.T) {
			e, j, _, _, events := environmentFixture(t, "blue", environment)
			e.Config.DrainSeconds = 0
			e.execute(context.Background(), j)
			p, _ := e.Store.Project("demo")
			got, _ := e.Store.Job(j.ID)
			if got.Status != "succeeded" || p.Active != "blue" || len(p.Slots) != 1 || p.GreenPort != 0 {
				t.Fatal("non-production used another slot", p, got)
			}
			maintenance, stop, start := -1, -1, -1
			for i, event := range *events {
				if event == "route:" {
					maintenance = i
				}
				if event == "stop:blue" {
					stop = i
				}
				if event == "start:blue" {
					start = i
				}
				if event == "start:green" {
					t.Fatal("started second non-production instance")
				}
			}
			if maintenance < 0 || stop <= maintenance || start <= stop {
				t.Fatal("recreated a serving single slot", *events)
			}
		})
	}
}
func TestCandidateFailurePreservesServingRelease(t *testing.T) {
	for _, phase := range []string{"start", "health"} {
		t.Run(phase, func(t *testing.T) {
			e, j, d, r, _ := engineFixture(t, "blue")
			if phase == "start" {
				d.startErr = model.Fail("CONTAINER_START_FAILED")
			} else {
				d.healthErr = model.Fail("CANDIDATE_UNHEALTHY")
			}
			e.execute(context.Background(), j)
			p, _ := e.Store.Project("demo")
			got, _ := e.Store.Job(j.ID)
			old, _ := p.Current()
			if got.Status != "failed" || p.Active != "blue" || old.Image != "old-image" || old.Environment != "env-old" || r.switched || len(d.stopped) > 0 {
				t.Fatal("serving release changed after candidate failure", got, p, d.stopped)
			}
		})
	}
}
func TestBlueGreenBothDirections(t *testing.T) {
	for _, active := range []string{"blue", "green"} {
		t.Run(active, func(t *testing.T) {
			e, j, d, _, events := engineFixture(t, active)
			e.execute(context.Background(), j)
			p, _ := e.Store.Project("demo")
			got, _ := e.Store.Job(j.ID)
			if got.Status != "succeeded" || p.Active == active || len(d.stopped) != 1 || d.stopped[0] != active {
				t.Fatal(got, p, d.stopped)
			}
			routeIndex, stopIndex := -1, -1
			for i, event := range *events {
				if event == "route:"+p.Active {
					routeIndex = i
				}
				if event == "stop:"+active {
					stopIndex = i
				}
			}
			if routeIndex < 0 || stopIndex <= routeIndex {
				t.Fatal("stopped old before route activation", *events)
			}
		})
	}
}
func TestUnknownRouteKeepsBothContainers(t *testing.T) {
	e, j, d, r, _ := engineFixture(t, "blue")
	r.err = model.Uncertain("ROUTE_OUTCOME_UNKNOWN")
	e.execute(context.Background(), j)
	got, _ := e.Store.Job(j.ID)
	if got.Status != "recovery_required" || len(d.stopped) != 0 || e.Ready() {
		t.Fatal("unsafe unknown outcome", got, d.stopped)
	}
}

func TestSingleSlotFailureRestoresActivatedPair(t *testing.T) {
	e, j, d, _, _ := engineFixture(t, "blue")
	p, _ := e.Store.Project("demo")
	p.ZeroDowntime = false
	e.Store.Update(j, &p)
	// Single-slot recreation is intentionally allowed only after maintenance.
	d.healthErr = model.Fail("CANDIDATE_UNHEALTHY")
	e.execute(context.Background(), j)
	p, _ = e.Store.Project("demo")
	got, _ := e.Store.Job(j.ID)
	restore, ok := p.Current()
	if got.Status != "failed" || p.State != "stopped" || !ok || restore.Image != "old-image" || restore.Environment != "env-old" {
		t.Fatal("lost activated image/environment after failed replacement", got, p, restore)
	}
}

type observedRoutes struct {
	fakeRoutes
	slot string
}

func (r *observedRoutes) Observe(context.Context, model.Project) (string, error) { return r.slot, nil }
func TestOfflineRecoveryRecordsLiveCandidateAndPreservesContainers(t *testing.T) {
	e, j, d, r, _ := engineFixture(t, "blue")
	r.err = model.Uncertain("ROUTE_OUTCOME_UNKNOWN")
	e.execute(context.Background(), j)
	e.Routes = &observedRoutes{fakeRoutes: *r, slot: "green"}
	if err := e.Reconcile(context.Background(), j.ID); err != nil {
		t.Fatal(err)
	}
	p, _ := e.Store.Project("demo")
	recovered, _ := e.Store.Job(j.ID)
	if p.Active != "green" || p.State != "running" || !p.RecoveryDrain || len(d.stopped) != 0 || recovered.Status != "failed" || !e.Ready() {
		t.Fatal("recovery guessed or stopped containers", p, recovered, d.stopped)
	}
}

type observedCompose struct {
	*fakeDocker
	release *model.Release
	err     error
}

func (d *observedCompose) ObserveCompose(context.Context, model.Project) (*model.Release, error) {
	return d.release, d.err
}

func TestOfflineComposeRecoveryDoesNotRequireCaddyRoute(t *testing.T) {
	for _, scenario := range []string{"original-running", "candidate-running", "stopped", "drift"} {
		t.Run(scenario, func(t *testing.T) {
			e, j, d, _, events := engineFixture(t, "blue")
			p, _ := e.Store.Project(j.ProjectID)
			p.Mode, p.State, p.ZeroDowntime, p.Domains = "compose", "stopped", false, nil
			old := p.Releases[0]
			p.Slots["blue"] = *j.Input.Release
			j.Status, j.Phase, j.Error = "recovery_required", "draining", "CONTAINER_STOP_FAILED"
			if err := e.Store.Update(j, &p); err != nil {
				t.Fatal(err)
			}
			observer := &observedCompose{fakeDocker: d}
			switch scenario {
			case "original-running":
				observer.release = &old
			case "candidate-running":
				observer.release = j.Input.Release
			case "drift":
				observer.err = model.Uncertain("CONTAINER_OWNERSHIP_UNKNOWN")
			}
			e.Docker = observer
			err := e.Reconcile(context.Background(), j.ID)
			got, _ := e.Store.Job(j.ID)
			p, _ = e.Store.Project(j.ProjectID)
			if scenario == "drift" {
				if err == nil || got.Status != "recovery_required" || e.Ready() || got.Finished != nil {
					t.Fatal("cleared unverified recovery", p, got, err)
				}
			} else {
				if err != nil || got.Status != "failed" || got.Finished == nil || !e.Ready() || got.Warning != "CONTAINERS_PRESERVED" {
					t.Fatal(p, got, err)
				}
				if observer.release == nil {
					if p.State != "stopped" || len(p.Releases) != 1 {
						t.Fatal("invented deployment", p)
					}
				} else if p.State != "running" || p.Slots["blue"].ID != observer.release.ID {
					t.Fatal("recorded wrong release", p)
				}
			}
			if len(d.stopped) != 0 || len(*events) != 0 {
				t.Fatal("recovery changed containers or routes", *events)
			}
		})
	}
}
