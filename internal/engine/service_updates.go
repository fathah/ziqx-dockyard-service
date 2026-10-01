package engine

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
)

type serviceDocker interface {
	VerifyServiceUpdate(context.Context, model.Project, model.ServiceUpdate) error
	PullServiceUpdate(context.Context, model.Project, *model.ServiceUpdate) (bool, error)
	StartServiceUpdate(context.Context, model.Project, model.ServiceUpdate) error
	HealthyServiceUpdate(context.Context, model.Project, model.ServiceUpdate) error
	PromoteServiceUpdate(context.Context, model.Project, model.ServiceUpdate) error
	StopServiceInstance(context.Context, model.Project, model.ServiceInstance) error
}

func (e *Engine) updateService(ctx context.Context, j *model.Job, p *model.Project) (result error) {
	if j.Input.Previous == nil || j.Input.Project == nil || j.Input.ServiceUpdate == nil {
		return model.Fail("SERVICE_UPDATE_INVALID")
	}
	old, next := *j.Input.Previous, *j.Input.Project
	plan := j.Input.ServiceUpdate
	if !reflect.DeepEqual(*p, old) {
		return model.Fail("SERVICE_REVIEW_CHANGED")
	}
	// Admission reserved these ports. Retain the pair even when pulling or
	// readiness fails, so a retry never consumes another pair from the pool.
	p.ServicePorts = next.ServicePorts
	if err := e.Store.Update(*j, p); err != nil {
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	d, ok := e.Docker.(serviceDocker)
	if !ok {
		return model.Fail("SERVICE_UPDATE_UNAVAILABLE")
	}
	if err := e.Routes.Ensure(ctx, old); err != nil {
		return err
	}
	if err := d.VerifyServiceUpdate(ctx, old, *plan); err != nil {
		return err
	}
	if err := e.phase(j, "pulling"); err != nil {
		return err
	}
	changed, err := d.PullServiceUpdate(ctx, next, plan)
	if err != nil {
		return err
	}
	if !changed {
		// Keep the reserved pair reusable even if the registry image was unchanged.
		p.ServicePorts = next.ServicePorts
		j.Warning = "SERVICE_ALREADY_CURRENT"
		j.Phase = "unchanged"
		return e.Store.Update(*j, p)
	}
	plan.Instance.Release.ID = state.NewID("rel-")
	plan.Instance.Release.Created = time.Now().UTC()
	next.ServiceInstances[plan.Service] = plan.Instance
	j.Input.Project = &next
	if err := e.phase(j, "service_candidate_intent"); err != nil {
		return err
	}
	started, switched := false, false
	defer func() {
		var fault *model.Fault
		if result != nil && started && plan.Mode == "seamless" && !switched && ctx.Err() == nil && (!errors.As(result, &fault) || !fault.Recovery) {
			if err := d.StopServiceInstance(ctx, next, plan.Instance); err != nil {
				result = model.Uncertain("SERVICE_CLEANUP_REQUIRED")
			}
		}
	}()
	started = true
	if err := d.StartServiceUpdate(ctx, next, *plan); err != nil {
		if plan.Mode == "restart" {
			return model.Uncertain("SERVICE_UPDATE_RECOVERY_REQUIRED")
		}
		return err
	}
	if err := e.phase(j, "service_readiness"); err != nil {
		return err
	}
	if err := d.HealthyServiceUpdate(ctx, next, *plan); err != nil {
		if plan.Mode == "restart" {
			return model.Uncertain("SERVICE_UPDATE_RECOVERY_REQUIRED")
		}
		return err
	}
	if plan.Mode == "restart" {
		*p = next
		if err := e.Store.Update(*j, p); err != nil {
			return model.Uncertain("STATE_WRITE_FAILED")
		}
		return nil
	}
	if err := e.phase(j, "service_network_intent"); err != nil {
		return err
	}
	if err := d.PromoteServiceUpdate(ctx, next, *plan); err != nil {
		return err
	}
	if err := d.HealthyServiceUpdate(ctx, next, *plan); err != nil {
		return err
	}
	if err := e.Routes.Ensure(ctx, old); err != nil {
		return err
	}
	if err := e.phase(j, "service_route_intent"); err != nil {
		return err
	}
	if len(j.Input.RouteEdits) > 0 {
		routes, ok := e.Routes.(routeImport)
		if !ok {
			return model.Fail("SERVICE_UPDATE_UNAVAILABLE")
		}
		if err := routes.ImportRoutes(ctx, j.Input.RouteEdits, next); err != nil {
			return err
		}
	} else if err := e.Routes.Set(ctx, next, next.Active); err != nil {
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
	if err := d.HealthyServiceUpdate(ctx, next, *plan); err != nil {
		if err := d.VerifyServiceUpdate(ctx, old, *plan); err != nil {
			return model.Uncertain("SERVICE_ROLLBACK_REQUIRED")
		}
		if err := e.phase(j, "service_rollback_intent"); err != nil {
			return err
		}
		if len(j.Input.RouteEdits) > 0 {
			restorer, ok := e.Routes.(interface {
				RestoreImportedRoutes(context.Context, []model.RouteEdit, model.Project) error
			})
			if !ok {
				return model.Uncertain("RECOVERY_UNAVAILABLE")
			}
			if err := restorer.RestoreImportedRoutes(ctx, j.Input.RouteEdits, next); err != nil {
				return err
			}
		} else if err := e.Routes.Set(ctx, old, old.Active); err != nil {
			return model.Uncertain("SERVICE_ROLLBACK_REQUIRED")
		}
		*p = old
		p.ServicePorts = next.ServicePorts
		if err := e.Store.Update(*j, p); err != nil {
			return model.Uncertain("STATE_WRITE_FAILED")
		}
		switched = false
		return model.Fail("SERVICE_UNHEALTHY_ROLLED_BACK")
	}
	if err := e.phase(j, "service_retire_intent"); err != nil {
		return err
	}
	if err := d.StopServiceInstance(ctx, old, *plan.Previous); err != nil {
		return model.Uncertain("SERVICE_DRAIN_RECOVERY_REQUIRED")
	}
	return nil
}

func (e *Engine) reconcileService(ctx context.Context, j *model.Job) (model.Project, error) {
	if j.Input.ServiceUpdate == nil || j.Input.Previous == nil || j.Input.Project == nil {
		return model.Project{}, model.Uncertain("STATE_DIVERGED")
	}
	old, next, plan := *j.Input.Previous, *j.Input.Project, *j.Input.ServiceUpdate
	d, ok := e.Docker.(serviceDocker)
	if !ok {
		return old, model.Uncertain("RECOVERY_UNAVAILABLE")
	}
	instance, journaled := next.ServiceInstances[plan.Service]
	journaled = journaled && instance.Name == plan.Instance.Name && instance.Release.Compose == plan.Instance.Release.Compose && instance.Release.Image == plan.Instance.Release.Image
	if plan.Mode == "restart" && journaled {
		if err := d.HealthyServiceUpdate(ctx, next, plan); err != nil {
			return old, err
		}
		return next, nil
	}
	if err := e.Routes.Ensure(ctx, next); plan.Mode == "seamless" && journaled && err == nil {
		if err := d.HealthyServiceUpdate(ctx, next, plan); err != nil {
			return old, err
		}
		if err := e.wait(ctx, j); err != nil {
			return old, err
		}
		if err := d.StopServiceInstance(ctx, old, *plan.Previous); err != nil {
			return old, err
		}
		return next, nil
	}
	if len(j.Input.RouteEdits) > 0 {
		routes, ok := e.Routes.(interface {
			VerifyOriginalRoutes(context.Context, []model.RouteEdit) error
		})
		if !ok {
			return old, model.Uncertain("RECOVERY_UNAVAILABLE")
		}
		if err := routes.VerifyOriginalRoutes(ctx, j.Input.RouteEdits); err != nil {
			return old, err
		}
	} else if err := e.Routes.Ensure(ctx, old); err != nil {
		return old, err
	}
	// An interruption may have happened before the candidate existed. Validate
	// the live service first, then remove only a positively identified candidate.
	if err := d.VerifyServiceUpdate(ctx, old, plan); err != nil {
		return old, err
	}
	if journaled {
		if err := d.StopServiceInstance(ctx, next, plan.Instance); err != nil {
			return old, err
		}
	}
	old.ServicePorts = next.ServicePorts
	return old, nil
}
