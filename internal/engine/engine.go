package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

type Docker interface {
	Validate(context.Context, model.Project, model.Release) error
	Pull(context.Context, model.Project, model.Release) error
	Start(context.Context, model.Project, string) error
	Healthy(context.Context, model.Project, string, model.Release) error
	Stop(context.Context, model.Project, string) error
	Logs(context.Context, model.Project, string, string, int, string) ([]byte, bool, error)
	PortFree(context.Context, int) (bool, error)
}
type Routes interface {
	Ensure(context.Context, model.Project) error
	Set(context.Context, model.Project, string) error
	DomainsAvailable(context.Context, []string, []string) error
}
type DNS interface {
	Create(context.Context, string, string) (string, error)
}
type Engine struct {
	Config    config.Config
	Store     *state.Store
	Docker    Docker
	Routes    Routes
	DNS       DNS
	Admission sync.Mutex
	wake      chan struct{}
	failed    atomic.Bool
}

func New(c config.Config, s *state.Store, d Docker, r Routes, dns DNS) *Engine {
	return &Engine{Config: c, Store: s, Docker: d, Routes: r, DNS: dns, wake: make(chan struct{}, 1)}
}
func TemplateHash(t config.Template) string {
	b, _ := json.Marshal(t)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (e *Engine) Ready() bool {
	if e.failed.Load() {
		return false
	}
	b, err := e.Store.Blocked()
	return err == nil && !b
}
func (e *Engine) Notify() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *Engine) Initialize() error {
	if err := e.Store.InterruptRunning(); err != nil {
		return err
	}
	ps, err := e.Store.Projects()
	if err != nil {
		return err
	}
	for _, p := range ps {
		if err := p.ValidateTarget(); err != nil {
			return err
		}
		if TemplateHash(e.Config.Templates[p.Template]) != p.TemplateRevision {
			return errors.New("existing project template changed; restore the pinned template")
		}
		seen := map[string]bool{}
		for _, release := range p.Slots {
			if seen[release.Compose] {
				continue
			}
			seen[release.Compose] = true
			if err := runtime.IndexCompose(e.Config, e.Store, p, release); err != nil {
				return errors.New("existing slot Compose revision, metadata or service policy changed; restore its pinned state")
			}
		}
		// Import retained legacy metadata once, without rereading every historical
		// manifest on subsequent startups. Live slot integrity remains verified.
		for _, release := range p.Releases {
			if seen[release.Compose] {
				continue
			}
			seen[release.Compose] = true
			if _, err := e.Store.ComposeServices(p.ID, release.Compose); err == nil {
				continue
			} else if err != sql.ErrNoRows {
				return err
			}
			if err := runtime.IndexCompose(e.Config, e.Store, p, release); err != nil {
				return err
			}
		}
	}
	return nil
}
func (e *Engine) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if !e.Ready() {
			select {
			case <-ctx.Done():
				return nil
			case <-e.wake:
			case <-time.After(time.Second):
			}
			continue
		}
		e.Admission.Lock()
		j, err := e.Store.Next()
		if err == nil {
			j.Status = "running"
			j.Phase = "validating"
			err = e.Store.Update(j, nil)
		}
		e.Admission.Unlock()
		if err == sql.ErrNoRows {
			select {
			case <-ctx.Done():
				return nil
			case <-e.wake:
			case <-time.After(time.Second):
			}
			continue
		}
		if err != nil {
			return err
		}
		e.execute(ctx, j)
	}
}
func (e *Engine) phase(j *model.Job, phase string) error {
	j.Phase = phase
	if err := e.Store.Update(*j, nil); err != nil {
		e.failed.Store(true)
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	return nil
}
func (e *Engine) execute(ctx context.Context, j model.Job) {
	p, err := e.Store.Project(j.ProjectID)
	if err == nil {
		err = p.ValidateTarget()
	}
	if err == nil {
		switch j.Action {
		case "project_create":
			err = e.create(ctx, &j, &p)
		case "routes_update":
			err = e.routes(ctx, &j, &p)
		case "deploy", "rollback", "restart", "start":
			err = e.deploy(ctx, &j, &p)
		case "stop":
			err = e.stop(ctx, &j, &p)
		case "dns_create":
			err = e.dns(ctx, &j, &p)
		default:
			err = model.Fail("OPERATION_INVALID")
		}
	}
	if err != nil {
		j.Status = "failed"
		j.Error = "DEPENDENCY_FAILED"
		var fault *model.Fault
		if errors.As(err, &fault) {
			j.Error = fault.Code
			if fault.Recovery {
				j.Status = "recovery_required"
			}
		}
		if ctx.Err() != nil {
			j.Status = "recovery_required"
			j.Error = "JOB_INTERRUPTED"
		}
	} else {
		j.Status = "succeeded"
	}
	now := time.Now().UTC()
	j.Finished = &now
	if err = e.Store.Update(j, nil); err != nil {
		e.failed.Store(true)
		slog.Error("durable job result unavailable", "job_id", j.ID)
	}
}
func (e *Engine) create(ctx context.Context, j *model.Job, p *model.Project) error {
	if err := e.Routes.DomainsAvailable(ctx, p.Domains, nil); err != nil {
		return err
	}
	if err := runtime.Compose(e.Config, *p); err != nil {
		return model.Fail("PROJECT_FILES_FAILED")
	}
	if err := e.phase(j, "route_intent"); err != nil {
		return err
	}
	if err := e.Routes.Set(ctx, *p, ""); err != nil {
		return err
	}
	p.State = "awaiting_release"
	if err := e.Store.Update(*j, p); err != nil {
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	return nil
}
func (e *Engine) routes(ctx context.Context, j *model.Job, p *model.Project) error {
	if err := e.Routes.Ensure(ctx, *p); err != nil {
		return err
	}
	next := *j.Input.Project
	if err := e.Routes.DomainsAvailable(ctx, next.Domains, p.Domains); err != nil {
		return err
	}
	if p.State == "running" {
		r, ok := p.Current()
		if !ok {
			return model.Uncertain("STATE_DIVERGED")
		}
		if err := e.Docker.Healthy(ctx, *p, p.Active, r); err != nil {
			return err
		}
	}
	if err := e.phase(j, "route_intent"); err != nil {
		return err
	}
	slot := ""
	if next.State == "running" {
		slot = next.Active
	}
	if err := e.Routes.Set(ctx, next, slot); err != nil {
		return err
	}
	*p = next
	if err := e.Store.Update(*j, p); err != nil {
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	return nil
}
func (e *Engine) dns(ctx context.Context, j *model.Job, p *model.Project) error {
	if err := e.phase(j, "dns_intent"); err != nil {
		return err
	}
	id, err := e.DNS.Create(ctx, p.ID, j.Input.Hostname)
	if err != nil {
		return err
	}
	j.Input.DNSRecordID = id
	found := false
	for _, s := range p.DNS {
		if s == id {
			found = true
		}
	}
	if !found {
		p.DNS = append(p.DNS, id)
	}
	if err = e.Store.Update(*j, p); err != nil {
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	return nil
}
func (e *Engine) wait(ctx context.Context, j *model.Job) error {
	if err := e.phase(j, "draining"); err != nil {
		return err
	}
	t := time.NewTimer(time.Duration(e.Config.DrainSeconds) * time.Second)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return model.Uncertain("JOB_INTERRUPTED")
	case <-t.C:
		return nil
	}
}
func (e *Engine) stop(ctx context.Context, j *model.Job, p *model.Project) error {
	if err := e.Routes.Ensure(ctx, *p); err != nil {
		return err
	}
	if err := e.phase(j, "maintenance_intent"); err != nil {
		return err
	}
	if err := e.Routes.Set(ctx, *p, ""); err != nil {
		return err
	}
	p.State = "stopped"
	if err := e.Store.Update(*j, p); err != nil {
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	if err := e.wait(ctx, j); err != nil {
		return err
	}
	// No Compose down/prune/volume deletion; positively identified services only.
	for _, slot := range []string{"blue", "green"} {
		if _, ok := p.Slots[slot]; ok {
			if err := e.Docker.Stop(ctx, *p, slot); err != nil {
				return model.Uncertain("CONTAINER_STOP_FAILED")
			}
		}
	}
	return nil
}
func (e *Engine) deploy(ctx context.Context, j *model.Job, p *model.Project) error {
	if j.Input.Release == nil {
		return model.Fail("RELEASE_INVALID")
	}
	r := *j.Input.Release
	if err := e.Routes.Ensure(ctx, *p); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(e.Config.ProjectsRoot, p.ID, "env", r.Environment+".env")); err != nil {
		return model.Fail("ENVIRONMENT_UNAVAILABLE")
	}
	old := p.Active
	if p.RecoveryDrain {
		if err := e.wait(ctx, j); err != nil {
			return err
		}
		p.RecoveryDrain = false
		if err := e.Store.Update(*j, p); err != nil {
			return model.Uncertain("STATE_WRITE_FAILED")
		}
	}
	if p.State == "stopped" && old != "" {
		// Recovery can discover maintenance with old containers still present.
		// A fresh full drain precedes positively identified stops before reuse.
		if err := e.wait(ctx, j); err != nil {
			return err
		}
		for _, oldSlot := range []string{"blue", "green"} {
			if _, ok := p.Slots[oldSlot]; ok {
				if err := e.Docker.Stop(ctx, *p, oldSlot); err != nil {
					return model.Uncertain("CONTAINER_STOP_FAILED")
				}
			}
		}
	}
	if p.State == "running" {
		active, ok := p.Current()
		if !ok {
			return model.Uncertain("STATE_DIVERGED")
		}
		if err := e.Docker.Healthy(ctx, *p, old, active); err != nil {
			return err
		}
	}
	if err := e.phase(j, "pulling"); err != nil {
		return err
	}
	if err := e.Docker.Pull(ctx, *p, r); err != nil {
		return err
	}
	slot := "blue"
	if p.ZeroDowntime && old == "blue" {
		slot = "green"
	}
	if !p.ZeroDowntime && p.State == "running" {
		if err := e.phase(j, "maintenance_intent"); err != nil {
			return err
		}
		if err := e.Routes.Set(ctx, *p, ""); err != nil {
			return err
		}
		p.State = "stopped"
		if err := e.Store.Update(*j, p); err != nil {
			return model.Uncertain("STATE_WRITE_FAILED")
		}
		if err := e.wait(ctx, j); err != nil {
			return err
		}
	}
	if _, ok := p.Slots[slot]; ok {
		if err := e.Docker.Stop(ctx, *p, slot); err != nil {
			return model.Uncertain("CANDIDATE_SLOT_NOT_REUSABLE")
		}
	}
	free, err := e.Docker.PortFree(ctx, p.Port(slot))
	if err != nil {
		return err
	}
	if !free {
		return model.Fail("PORT_IN_USE")
	}
	if err = e.phase(j, "candidate_intent"); err != nil {
		return err
	}
	if err = runtime.Binding(e.Config, *p, slot, r); err != nil {
		return model.Uncertain("BINDING_WRITE_FAILED")
	}
	// Journal the candidate binding before invoking Docker, without changing active state.
	p.Slots[slot] = r
	if err = e.Store.Update(*j, p); err != nil {
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	if err = e.Docker.Start(ctx, *p, slot); err != nil {
		return err
	}
	if err = e.phase(j, "readiness"); err != nil {
		return err
	}
	if err = e.Docker.Healthy(ctx, *p, slot, r); err != nil {
		return err
	}
	if err = e.phase(j, "route_intent"); err != nil {
		return err
	}
	if err = e.Routes.Set(ctx, *p, slot); err != nil {
		return err
	}
	p.Active = slot
	p.State = "running"
	p.Releases = append(p.Releases, r)
	if err = e.Store.Update(*j, p); err != nil {
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	// Operator-facing .env mirrors only the successfully activated revision.
	// Containers always use their immutable slot revision, never this mirror.
	if b, readErr := os.ReadFile(filepath.Join(e.Config.ProjectsRoot, p.ID, "env", r.Environment+".env")); readErr != nil {
		j.Warning = "ENVIRONMENT_MIRROR_FAILED"
	} else if writeErr := secure.Atomic(filepath.Join(e.Config.ProjectsRoot, p.ID, ".env"), b, 0600); writeErr != nil {
		j.Warning = "ENVIRONMENT_MIRROR_FAILED"
	}
	// The running slots use immutable files; this is only an operator mirror.
	if _, b, readErr := runtime.ReleaseCompose(e.Config, *p, r); readErr != nil {
		j.Warning = "COMPOSE_MIRROR_FAILED"
	} else if writeErr := secure.Atomic(filepath.Join(e.Config.ProjectsRoot, p.ID, "compose.yml"), b, 0600); writeErr != nil {
		j.Warning = "COMPOSE_MIRROR_FAILED"
	}
	if old != "" && old != slot {
		if err = e.wait(ctx, j); err != nil {
			return err
		}
		if err = e.Routes.Ensure(ctx, *p); err != nil {
			return err
		}
		if err = e.Docker.Healthy(ctx, *p, slot, r); err != nil {
			j.Warning = "ACTIVE_UNHEALTHY_OLD_SLOT_RETAINED"
			return nil
		}
		if err = e.Docker.Stop(ctx, *p, old); err != nil {
			j.Warning = "OLD_SLOT_STOP_FAILED"
		}
	}
	return nil
}

func (e *Engine) AvailablePort(ctx context.Context, exclude map[int]bool) (int, error) {
	for port := e.Config.PortMin; port <= e.Config.PortMax; port++ {
		if !e.Config.PortAllowed(port) || exclude[port] {
			continue
		}
		reserved, err := e.Store.Reserved(port, "")
		if err != nil {
			return 0, err
		}
		if reserved {
			continue
		}
		free, err := e.Docker.PortFree(ctx, port)
		if err != nil {
			return 0, err
		}
		if free {
			return port, nil
		}
	}
	return 0, model.Fail("PORT_POOL_EXHAUSTED")
}

// Reconcile is a local root operation while the daemon is stopped. It never
// stops containers or guesses which healthy instance receives traffic.
func (e *Engine) Reconcile(ctx context.Context, id string) error {
	j, err := e.Store.Job(id)
	if err != nil {
		return err
	}
	if j.Status != "recovery_required" {
		return model.Fail("JOB_NOT_RECOVERABLE")
	}
	p, err := e.Store.Project(j.ProjectID)
	if err != nil {
		return err
	}
	if j.Action == "dns_create" {
		id, err := e.DNS.Create(ctx, p.ID, j.Input.Hostname)
		if err != nil {
			return err
		}
		j.Input.DNSRecordID = id
		found := false
		for _, r := range p.DNS {
			if r == id {
				found = true
			}
		}
		if !found {
			p.DNS = append(p.DNS, id)
		}
	} else {
		observer, ok := e.Routes.(interface {
			Observe(context.Context, model.Project) (string, error)
		})
		if !ok {
			return model.Uncertain("RECOVERY_UNAVAILABLE")
		}
		observed, err := observer.Observe(ctx, p)
		if j.Input.Project != nil {
			candidate := *j.Input.Project
			if slot, candidateErr := observer.Observe(ctx, candidate); candidateErr == nil {
				p = candidate
				observed = slot
				err = nil
			}
		}
		if err != nil {
			return err
		}
		if observed != "" {
			release, ok := p.Slots[observed]
			if !ok {
				return model.Uncertain("RELEASE_IDENTITY_UNKNOWN")
			}
			if err = e.Docker.Healthy(ctx, p, observed, release); err != nil {
				return err
			}
			p.Active = observed
			p.State = "running"
			found := false
			for _, r := range p.Releases {
				if r.ID == release.ID {
					found = true
				}
			}
			if !found {
				p.Releases = append(p.Releases, release)
			}
		} else {
			if p.State == "provisioning" {
				p.State = "awaiting_release"
			} else {
				p.State = "stopped"
			}
		}
	}
	// Record the observed state while retaining an honest failed operation result.
	j.Status = "failed"
	j.Error = "JOB_INTERRUPTED_RECONCILED"
	j.Phase = "reconciled"
	j.Warning = "CONTAINERS_PRESERVED"
	p.RecoveryDrain = true
	now := time.Now().UTC()
	j.Finished = &now
	return e.Store.Update(j, &p)
}
