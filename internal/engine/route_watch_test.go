package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
)

type driftDocker struct {
	fakeDocker
	live int // the host port Docker currently publishes for the web service
	err  error
}

func (d *driftDocker) PortsFingerprint(context.Context) (string, error) { return "", nil }
func (d *driftDocker) LiveRoutes(_ context.Context, p model.Project) (model.Project, error) {
	if d.err != nil {
		return p, d.err
	}
	p.BluePort = d.live
	return p, nil
}

type driftRoutes struct {
	ensureErr, setErr error
	set               []model.Project
}

func (r *driftRoutes) Ensure(context.Context, model.Project) error                { return r.ensureErr }
func (r *driftRoutes) DomainsAvailable(context.Context, []string, []string) error { return nil }
func (r *driftRoutes) Set(_ context.Context, p model.Project, slot string) error {
	r.set = append(r.set, p)
	return r.setErr
}

func driftFixture(t *testing.T) (*Engine, *driftDocker, *driftRoutes) {
	t.Helper()
	dir := t.TempDir()
	s, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	release := model.Release{ID: "rel", Image: "image", Environment: "env"}
	p := model.Project{ID: "site", AppID: "site", Mode: "compose", Environment: model.Production, RouteService: "web", RoutePort: 3000, BluePort: 32876, Domains: []string{"ziqx.example.com"}, State: "running", Active: "blue", Slots: map[string]model.Release{"blue": release}, Releases: []model.Release{release}}
	j := model.Job{ID: "job-create", ProjectID: p.ID, Action: "project_create", Status: "succeeded", RequestID: "req-create", Created: time.Now()}
	if err := s.Accept(j, &p, "create", "fp", 10, 10); err != nil {
		t.Fatal(err)
	}
	events := []string{}
	d := &driftDocker{fakeDocker: fakeDocker{events: &events}, live: 32876}
	r := &driftRoutes{}
	e := New(config.Config{ServerID: "vps-01", MaxQueuedJobs: 10, MaxProjects: 10, ProjectsRoot: dir}, s, d, r, nil)
	return e, d, r
}

func runQueued(t *testing.T, e *Engine) (model.Job, bool) {
	t.Helper()
	j, err := e.Store.Next()
	if err != nil {
		return model.Job{}, false
	}
	j.Status = "running"
	e.Store.Update(j, nil)
	e.execute(context.Background(), j)
	j, _ = e.Store.Job(j.ID)
	return j, true
}

func TestRestartedContainerPortIsRepairedThroughAJob(t *testing.T) {
	e, d, r := driftFixture(t)
	attempts, logged := map[string]routeAttempt{}, map[string]string{}
	if !e.checkRoutes(context.Background(), d, attempts, logged) {
		t.Fatal("idle check incomplete")
	}
	if _, queued := runQueued(t, e); queued {
		t.Fatal("queued a repair without drift")
	}
	d.live = 32773 // Docker restarted the container onto a new port.
	if !e.checkRoutes(context.Background(), d, attempts, logged) {
		t.Fatal("check incomplete")
	}
	j, queued := runQueued(t, e)
	if !queued || j.Action != "route_repair" || j.Status != "succeeded" || j.Actor != "dockyard" {
		t.Fatal("repair did not run", j)
	}
	if len(r.set) != 1 || r.set[0].BluePort != 32773 {
		t.Fatal("Caddy was not pointed at the live port", r.set)
	}
	p, _ := e.Store.Project("site")
	if p.BluePort != 32773 || p.State != "running" || p.RecoveryDrain {
		t.Fatal("saved routing not updated", p)
	}
	e.checkRoutes(context.Background(), d, attempts, logged)
	if _, queued := runQueued(t, e); queued {
		t.Fatal("repaired project queued again")
	}
}

func TestFailedRouteRepairIsNotRetriedEveryTick(t *testing.T) {
	e, d, r := driftFixture(t)
	r.ensureErr = model.Uncertain("STATE_DIVERGED")
	d.live = 32773
	attempts, logged := map[string]routeAttempt{}, map[string]string{}
	e.checkRoutes(context.Background(), d, attempts, logged)
	j, queued := runQueued(t, e)
	// Nothing was changed yet, so a failed precheck must not block the server.
	if !queued || j.Status != "failed" || j.Error != "STATE_DIVERGED" || !e.Ready() || len(r.set) != 0 {
		t.Fatal("precheck failure escalated or changed routes", j)
	}
	e.checkRoutes(context.Background(), d, attempts, logged)
	if _, queued := runQueued(t, e); queued {
		t.Fatal("retried the same failed repair immediately")
	}
	d.live = 32774 // A new assignment is a new attempt.
	e.checkRoutes(context.Background(), d, attempts, logged)
	if _, queued := runQueued(t, e); !queued {
		t.Fatal("new port change ignored")
	}
}

func TestRouteWatcherLeavesBusyAndUnreadableProjects(t *testing.T) {
	e, d, _ := driftFixture(t)
	d.live = 32773
	busy := model.Job{ID: "job-busy", ProjectID: "site", Action: "restart", Status: "queued", RequestID: "req-busy", Created: time.Now()}
	if err := e.Store.Accept(busy, nil, "busy", "fp", 10, 10); err != nil {
		t.Fatal(err)
	}
	if e.checkRoutes(context.Background(), d, map[string]routeAttempt{}, map[string]string{}) {
		t.Fatal("busy project reported as checked")
	}
	if j, _ := e.Store.Next(); j.ID != "job-busy" {
		t.Fatal("queued a repair beside an active job", j)
	}
	e2, d2, _ := driftFixture(t)
	d2.err = model.Fail("CONTAINER_UNAVAILABLE")
	if !e2.checkRoutes(context.Background(), d2, map[string]routeAttempt{}, map[string]string{}) {
		t.Fatal("stopped container should wait for the next port change")
	}
	if _, queued := runQueued(t, e2); queued {
		t.Fatal("queued a repair for an unavailable container")
	}
}

func TestUnknownCaddyOutcomeJournalsObservedRoute(t *testing.T) {
	e, d, r := driftFixture(t)
	d.live = 32773
	r.setErr = model.Uncertain("ROUTE_OUTCOME_UNKNOWN")
	e.checkRoutes(context.Background(), d, map[string]routeAttempt{}, map[string]string{})
	j, _ := runQueued(t, e)
	if j.Status != "recovery_required" || j.Input.Project == nil || j.Input.Project.BluePort != 32773 {
		t.Fatal("unknown Caddy outcome must journal the candidate for recovery", j)
	}
}
