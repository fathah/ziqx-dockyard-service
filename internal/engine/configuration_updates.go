package engine

import (
	"context"
	"errors"
	"os"
	"reflect"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
)

func (e *Engine) updateConfiguration(ctx context.Context, j *model.Job, p *model.Project) (result error) {
	if j.Input.Previous == nil || j.Input.Project == nil || j.Input.Release == nil || len(j.Input.ConfigurationUpdates) == 0 {
		return model.Fail("CONFIGURATION_UPDATE_INVALID")
	}
	old, next := *j.Input.Previous, *j.Input.Project
	if next.ServiceInstances == nil {
		next.ServiceInstances = map[string]model.ServiceInstance{}
	}
	if !reflect.DeepEqual(*p, old) {
		return model.Fail("CONFIGURATION_CHANGED")
	}
	d, ok := e.Docker.(serviceDocker)
	if !ok {
		return model.Fail("SERVICE_UPDATE_UNAVAILABLE")
	}
	if err := e.Routes.Ensure(ctx, old); err != nil {
		return err
	}
	for _, plan := range j.Input.ConfigurationUpdates {
		if err := d.VerifyServiceUpdate(ctx, old, plan); err != nil {
			return err
		}
	}
	if err := e.phase(j, "pulling"); err != nil {
		return err
	}
	// Pull and prepare every manifest before starting any replacement.
	for i := range j.Input.ConfigurationUpdates {
		plan := &j.Input.ConfigurationUpdates[i]
		if _, err := d.PullServiceUpdate(ctx, old, plan); err != nil {
			return err
		}
		plan.Instance.Release.ID = state.NewID("rel-")
		plan.Instance.Release.Created = time.Now().UTC()
	}
	started, switched := 0, false
	defer func() {
		var fault *model.Fault
		if result != nil && !switched && ctx.Err() == nil && (!errors.As(result, &fault) || !fault.Recovery) {
			for _, plan := range j.Input.ConfigurationUpdates[:started] {
				if err := d.StopServiceInstance(ctx, next, plan.Instance); err != nil {
					result = model.Uncertain("SERVICE_CLEANUP_REQUIRED")
				}
			}
		}
	}()
	for i := range j.Input.ConfigurationUpdates {
		plan := &j.Input.ConfigurationUpdates[i]
		next.ServiceInstances[plan.Service] = plan.Instance
		j.Input.Project = &next
		// Persist the candidate identity before Docker can create it.
		if err := e.phase(j, "service_candidate_intent"); err != nil {
			return err
		}
		started = i + 1
		if err := d.StartServiceUpdate(ctx, next, *plan); err != nil {
			return err
		}
		resolver, ok := e.Docker.(interface {
			ResolveServiceRouteBindings(context.Context, *model.Project, *model.ServiceUpdate) error
		})
		if !ok {
			return model.Uncertain("SERVICE_UPDATE_RECOVERY_REQUIRED")
		}
		if err := resolver.ResolveServiceRouteBindings(ctx, &next, plan); err != nil {
			return model.Uncertain("SERVICE_UPDATE_RECOVERY_REQUIRED")
		}
		next.ServiceInstances[plan.Service] = plan.Instance
		if err := e.phase(j, "service_readiness"); err != nil {
			return err
		}
		if err := d.HealthyServiceUpdate(ctx, next, *plan); err != nil {
			return err
		}
	}
	if err := e.phase(j, "service_network_intent"); err != nil {
		return err
	}
	for _, plan := range j.Input.ConfigurationUpdates {
		if err := d.PromoteServiceUpdate(ctx, next, plan); err != nil {
			return err
		}
		if err := d.HealthyServiceUpdate(ctx, next, plan); err != nil {
			return err
		}
	}
	if err := e.Routes.Ensure(ctx, old); err != nil {
		return err
	}
	// The new full source revision becomes current only with the traffic switch.
	next.Slots = map[string]model.Release{}
	for slot, release := range old.Slots {
		next.Slots[slot] = release
	}
	next.Slots[next.Active] = *j.Input.Release
	next.Releases = append(append([]model.Release{}, old.Releases...), *j.Input.Release)
	if err := e.phase(j, "configuration_route_intent"); err != nil {
		return err
	}
	if err := e.Routes.Set(ctx, next, next.Active); err != nil {
		return err
	}
	switched = true
	*p = next
	if err := e.Store.Update(*j, p); err != nil {
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	if err := e.wait(ctx, j); err != nil {
		return err
	}
	if err := e.Routes.Ensure(ctx, next); err != nil {
		return err
	}
	for _, plan := range j.Input.ConfigurationUpdates {
		if err := d.HealthyServiceUpdate(ctx, next, plan); err != nil {
			for _, previous := range j.Input.ConfigurationUpdates {
				if err := d.VerifyServiceUpdate(ctx, old, previous); err != nil {
					return model.Uncertain("SERVICE_ROLLBACK_REQUIRED")
				}
			}
			if err := e.phase(j, "configuration_rollback_intent"); err != nil {
				return err
			}
			if err := e.Routes.Set(ctx, old, old.Active); err != nil {
				return model.Uncertain("SERVICE_ROLLBACK_REQUIRED")
			}
			*p = old
			if err := e.Store.Update(*j, p); err != nil {
				return model.Uncertain("STATE_WRITE_FAILED")
			}
			switched = false
			return model.Fail("SERVICE_UNHEALTHY_ROLLED_BACK")
		}
	}
	if err := e.phase(j, "configuration_retire_intent"); err != nil {
		return err
	}
	for _, plan := range j.Input.ConfigurationUpdates {
		if err := d.StopServiceInstance(ctx, old, *plan.Previous); err != nil {
			return model.Uncertain("SERVICE_DRAIN_RECOVERY_REQUIRED")
		}
	}
	e.configurationMirrors(j, next)
	return nil
}

func (e *Engine) configurationMirrors(j *model.Job, p model.Project) {
	r := *j.Input.Release
	runtime.ClearDraft(e.Config, p, j.Input.DraftSHA)
	if b, err := os.ReadFile(runtime.EnvironmentPath(e.Config, p, r.Environment)); err != nil {
		j.Warning = "ENVIRONMENT_MIRROR_FAILED"
	} else if err := secure.Atomic(runtime.ProjectMirrorPath(e.Config, p, ".env"), b, 0600); err != nil {
		j.Warning = "ENVIRONMENT_MIRROR_FAILED"
	}
	if b, err := runtime.ComposeMirror(e.Config, p, r); err != nil {
		j.Warning = "COMPOSE_MIRROR_FAILED"
	} else if err := secure.Atomic(runtime.ProjectMirrorPath(e.Config, p, "compose.yml"), b, 0600); err != nil {
		j.Warning = "COMPOSE_MIRROR_FAILED"
	}
}

func (e *Engine) reconcileConfiguration(ctx context.Context, j *model.Job) (model.Project, error) {
	if j.Input.Previous == nil || j.Input.Project == nil || j.Input.Release == nil || len(j.Input.ConfigurationUpdates) == 0 {
		return model.Project{}, model.Uncertain("STATE_DIVERGED")
	}
	old, next := *j.Input.Previous, *j.Input.Project
	d, ok := e.Docker.(serviceDocker)
	if !ok {
		return old, model.Uncertain("RECOVERY_UNAVAILABLE")
	}
	current, _ := next.Current()
	if current.ID == j.Input.Release.ID && e.Routes.Ensure(ctx, next) == nil {
		for _, plan := range j.Input.ConfigurationUpdates {
			if err := d.HealthyServiceUpdate(ctx, next, plan); err != nil {
				return old, err
			}
		}
		if err := e.wait(ctx, j); err != nil {
			return old, err
		}
		for _, plan := range j.Input.ConfigurationUpdates {
			if err := d.StopServiceInstance(ctx, old, *plan.Previous); err != nil {
				return old, err
			}
		}
		e.configurationMirrors(j, next)
		return next, nil
	}
	if err := e.Routes.Ensure(ctx, old); err != nil {
		return old, err
	}
	for _, plan := range j.Input.ConfigurationUpdates {
		if err := d.VerifyServiceUpdate(ctx, old, plan); err != nil {
			return old, err
		}
	}
	for _, plan := range j.Input.ConfigurationUpdates {
		if instance, ok := next.ServiceInstances[plan.Service]; ok && instance.Name == plan.Instance.Name {
			if err := d.StopServiceInstance(ctx, next, plan.Instance); err != nil {
				return old, err
			}
		}
	}
	return old, nil
}
