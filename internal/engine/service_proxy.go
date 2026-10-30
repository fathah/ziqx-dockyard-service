package engine

import (
	"context"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

type serviceProxyDocker interface {
	SwitchServiceProxy(context.Context, model.Project, model.ServiceUpdate, bool) error
	DrainedServiceProxy(context.Context, model.Project, model.ServiceUpdate, bool) error
}

func (e *Engine) finishProxyService(ctx context.Context, j *model.Job, p *model.Project, old, next model.Project, plan model.ServiceUpdate, d serviceDocker) error {
	proxy, ok := e.Docker.(serviceProxyDocker)
	if !ok {
		return model.Uncertain("SERVICE_PROXY_RECOVERY_REQUIRED")
	}
	if err := e.phase(j, "service_proxy_intent"); err != nil {
		return err
	}
	if err := proxy.SwitchServiceProxy(ctx, next, plan, false); err != nil {
		return err
	}
	*p = next
	if err := e.Store.Update(*j, p); err != nil {
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	if err := e.wait(ctx, j); err != nil {
		return err
	}
	if err := d.HealthyServiceUpdate(ctx, next, plan); err != nil {
		return err
	}
	if err := proxy.DrainedServiceProxy(ctx, next, plan, false); err != nil {
		return err
	}
	if err := e.phase(j, "service_retire_intent"); err != nil {
		return err
	}
	return d.StopServiceInstance(ctx, old, *plan.Previous)
}

// Caddy routes are unchanged for an internal backend, so their equality cannot
// identify a completed switch. Reconcile the actual proxy and container state.
func (e *Engine) reconcileProxyService(ctx context.Context, j *model.Job, old, next model.Project, plan model.ServiceUpdate, d serviceDocker) (model.Project, error) {
	proxy, ok := e.Docker.(serviceProxyDocker)
	if !ok {
		return old, model.Uncertain("SERVICE_PROXY_RECOVERY_REQUIRED")
	}
	if err := e.Routes.Ensure(ctx, old); err != nil {
		return old, err
	}
	instance, journaled := next.ServiceInstances[plan.Service]
	journaled = journaled && instance.Name == plan.Instance.Name && instance.Release.Compose == plan.Instance.Release.Compose && instance.Release.Image == plan.Instance.Release.Image
	if !journaled || j.Phase == "service_candidate_intent" || j.Phase == "service_readiness" {
		// The network-promotion intent is always persisted before candidates
		// can receive logical service aliases and enter HAProxy discovery.
		if err := d.VerifyServiceUpdate(ctx, old, plan); err != nil {
			return old, err
		}
		if journaled {
			if err := d.StopServiceInstance(ctx, next, plan.Instance); err != nil {
				return old, err
			}
		}
		return old, nil
	}
	restore := !journaled || d.HealthyServiceUpdate(ctx, next, plan) != nil
	if restore {
		if err := d.VerifyServiceUpdate(ctx, old, plan); err != nil {
			return old, err
		}
	}
	if err := proxy.SwitchServiceProxy(ctx, next, plan, restore); err != nil {
		return old, err
	}
	if err := e.wait(ctx, j); err != nil {
		return old, err
	}
	if err := proxy.DrainedServiceProxy(ctx, next, plan, restore); err != nil {
		return old, err
	}
	if restore {
		if err := d.StopServiceInstance(ctx, next, plan.Instance); err != nil {
			return old, err
		}
		return old, nil
	}
	if err := d.HealthyServiceUpdate(ctx, next, plan); err != nil {
		return old, err
	}
	if err := d.StopServiceInstance(ctx, old, *plan.Previous); err != nil {
		return old, err
	}
	return next, nil
}
