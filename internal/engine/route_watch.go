package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
)

// Docker assigns automatic route bindings a new host port whenever it restarts
// a container. The watcher notices the change and queues a route_repair job so
// Caddy follows the container instead of proxying to a dead (or reused) port.
type routeObserver interface {
	PortsFingerprint(context.Context) (string, error)
	LiveRoutes(context.Context, model.Project) (model.Project, error)
}

var (
	routeWatchInterval = 5 * time.Second
	// A full pass runs even without a port change, catching any missed event.
	routeWatchFull = 5 * time.Minute
	// A failed repair for the same observed ports is not retried sooner.
	routeRepairRetry = 10 * time.Minute
)

type routeAttempt struct {
	ports string
	at    time.Time
}

// WatchRoutes runs until ctx ends. It never changes routes itself; repairs go
// through the job worker, which serializes every Caddy reload and records audit.
func (e *Engine) WatchRoutes(ctx context.Context) {
	observer, ok := e.Docker.(routeObserver)
	if !ok {
		return
	}
	attempts := map[string]routeAttempt{}
	logged := map[string]string{}
	last, full := "", time.Time{}
	t := time.NewTicker(routeWatchInterval)
	defer t.Stop()
	for {
		fingerprint, err := observer.PortsFingerprint(ctx)
		if err == nil && (fingerprint != last || time.Since(full) >= routeWatchFull) {
			if e.checkRoutes(ctx, observer, attempts, logged) {
				last, full = fingerprint, time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// checkRoutes reports false when a project could not be examined yet (busy,
// recovery pending) so the next tick looks again even if no port changed.
func (e *Engine) checkRoutes(ctx context.Context, observer routeObserver, attempts map[string]routeAttempt, logged map[string]string) bool {
	if !e.Ready() {
		return false
	}
	ps, err := e.Store.Projects()
	if err != nil {
		return false
	}
	complete := true
	for _, p := range ps {
		if ctx.Err() != nil {
			return false
		}
		if !p.NativeCompose() || p.State != "running" || len(p.Domains) == 0 {
			continue
		}
		if busy, err := e.Store.Busy(p.ID); err != nil || busy {
			complete = false
			continue
		}
		next, err := observer.LiveRoutes(ctx, p)
		if err != nil {
			// A stopped or restarting container changes the fingerprint when it
			// returns, so this is not retried every tick. Log each new reason once.
			code := faultCode(err)
			if logged[p.ID] != code {
				slog.Warn("route check skipped", "project", p.ID, "reason", code)
				logged[p.ID] = code
			}
			continue
		}
		delete(logged, p.ID)
		if reflect.DeepEqual(p, next) {
			delete(attempts, p.ID)
			continue
		}
		ports := routePorts(next)
		if a, ok := attempts[p.ID]; ok && a.ports == ports && time.Since(a.at) < routeRepairRetry {
			continue
		}
		id, err := e.queueRouteRepair(p.ID)
		if err != nil {
			complete = false
			continue
		}
		if id != "" {
			attempts[p.ID] = routeAttempt{ports: ports, at: time.Now()}
			slog.Warn("route ports changed; repair queued", "project", p.ID, "job", id, "ports", ports)
		}
	}
	return complete
}

func (e *Engine) queueRouteRepair(projectID string) (string, error) {
	e.Admission.Lock()
	defer e.Admission.Unlock()
	if !e.Ready() {
		return "", model.Fail("RECOVERY_REQUIRED")
	}
	if busy, err := e.Store.Busy(projectID); err != nil || busy {
		return "", model.Fail("PROJECT_BUSY")
	}
	id := state.NewID("job-")
	j := model.Job{ID: id, ProjectID: projectID, Action: "route_repair", Status: "queued", Phase: "accepted", Actor: "dockyard", RequestID: id, Created: time.Now().UTC()}
	if err := e.Store.Accept(j, nil, "route-repair:"+id, "dockyard", e.Config.MaxQueuedJobs, e.Config.MaxProjects); err != nil {
		return "", err
	}
	e.Notify()
	return id, nil
}

// repairRoutes points Caddy at the ports Docker assigned after a restart. Until
// Caddy is touched nothing has changed, so earlier problems fail without
// blocking the server for recovery.
func (e *Engine) repairRoutes(ctx context.Context, j *model.Job, p *model.Project) error {
	observer, ok := e.Docker.(routeObserver)
	if !ok {
		return model.Fail("ROUTE_REPAIR_UNAVAILABLE")
	}
	if !p.NativeCompose() || p.State != "running" || len(p.Domains) == 0 {
		j.Warning = "ROUTE_REPAIR_NOT_NEEDED"
		return nil
	}
	if err := e.Routes.Ensure(ctx, *p); err != nil {
		return model.Fail(faultCode(err))
	}
	next, err := observer.LiveRoutes(ctx, *p)
	if err != nil {
		return model.Fail(faultCode(err))
	}
	if reflect.DeepEqual(*p, next) {
		j.Warning = "ROUTE_REPAIR_NOT_NEEDED"
		return nil
	}
	if e.log != nil {
		fmt.Fprintf(e.log, "Docker assigned new host ports: %s -> %s\n", routePorts(*p), routePorts(next))
	}
	// Journal the observed assignment first so recovery can recognise it.
	previous := *p
	j.Input.Previous = &previous
	j.Input.Project = &next
	if err := e.phase(j, "route_intent"); err != nil {
		return err
	}
	if err := e.Routes.Set(ctx, next, next.Active); err != nil {
		return err
	}
	*p = next
	if err := e.Store.Update(*j, p); err != nil {
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	return nil
}

// routePorts summarizes only the host ports routing depends on.
func routePorts(p model.Project) string {
	type instance struct {
		Service string `json:"service"`
		Port    int    `json:"port"`
	}
	summary := struct {
		Blue      int                   `json:"blue,omitempty"`
		Green     int                   `json:"green,omitempty"`
		Published *model.PublishedRoute `json:"published,omitempty"`
		Routes    []model.ServiceRoute  `json:"routes,omitempty"`
		Instances []instance            `json:"instances,omitempty"`
	}{Blue: p.BluePort, Green: p.GreenPort, Published: p.PublishedRoute, Routes: p.ServiceRoutes}
	for name, i := range p.ServiceInstances {
		summary.Instances = append(summary.Instances, instance{name, i.Port})
	}
	sort.Slice(summary.Instances, func(a, b int) bool { return summary.Instances[a].Service < summary.Instances[b].Service })
	b, _ := json.Marshal(summary)
	return string(b)
}

func faultCode(err error) string {
	var fault *model.Fault
	if errors.As(err, &fault) {
		return fault.Code
	}
	return "DEPENDENCY_FAILED"
}
