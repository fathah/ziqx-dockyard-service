package engine

import (
	"context"
	"fmt"
	"reflect"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func routeOnlyJob(j model.Job) bool {
	return j.Action == "route_unlink" || j.Action == "route_setup" && j.Input.Previous != nil && j.Input.Previous.State == "stopped" && j.Input.Release == nil
}

func (e *Engine) setupRoute(ctx context.Context, j *model.Job, p *model.Project) error {
	if j.Input.Previous == nil || j.Input.Project == nil || !reflect.DeepEqual(*p, *j.Input.Previous) {
		return model.Fail("ROUTE_REVIEW_CHANGED")
	}
	next := *j.Input.Project
	if err := next.ValidateTarget(); err != nil {
		return err
	}
	pin := next.PublishedRoute
	if j.Input.RouteTarget != nil {
		pin = j.Input.RouteTarget
	}
	if p.State != "running" && p.State != "stopped" || j.Action != "route_unlink" && pin == nil {
		return model.Fail("ROUTE_SETUP_UNAVAILABLE")
	}
	routingOnly := j.Action == "route_unlink" || p.State == "stopped"
	if !routingOnly {
		r, ok := p.Current()
		if !ok {
			return model.Fail("CONFIGURATION_CHANGED")
		}
		if err := e.Docker.Healthy(ctx, *p, p.Active, r); err != nil {
			return err
		}
		if j.Input.Release != nil {
			if err := e.Routes.Ensure(ctx, *p); err != nil {
				return err
			}
			owned := append([]string{}, p.Domains...)
			for _, edit := range j.Input.RouteEdits {
				owned = append(owned, edit.Domains...)
			}
			if err := e.Routes.DomainsAvailable(ctx, next.Domains, owned); err != nil {
				return err
			}
			if len(j.Input.RouteEdits) > 0 {
				routes, ok := e.Routes.(routeImport)
				if !ok {
					return model.Fail("ROUTE_SETUP_UNAVAILABLE")
				}
				if err := routes.VerifyRouteImport(j.Input.RouteEdits); err != nil {
					return err
				}
			}
			return e.deploy(ctx, j, p)
		}
		d, ok := e.Docker.(interface {
			PublishedUpstream(context.Context, model.Project, string, int) (string, error)
		})
		if !ok {
			return model.Fail("ROUTE_SETUP_UNAVAILABLE")
		}
		upstream, err := "", error(nil)
		if pin.Service == p.RouteService && pin.ContainerPort == p.RoutePort {
			upstream = fmt.Sprintf("127.0.0.1:%d", p.Port(p.Active))
		} else {
			upstream, err = d.PublishedUpstream(ctx, *p, pin.Service, pin.ContainerPort)
		}
		if err != nil {
			return err
		}
		if upstream != fmt.Sprintf("127.0.0.1:%d", pin.HostPort) {
			return model.Fail("ROUTE_REVIEW_CHANGED")
		}
	}
	if err := e.Routes.Ensure(ctx, *p); err != nil {
		return err
	}
	if len(j.Input.RouteEdits) == 0 {
		if err := e.Routes.DomainsAvailable(ctx, next.Domains, p.Domains); err != nil {
			return err
		}
	}
	if err := e.phase(j, "route_intent"); err != nil {
		return err
	}
	if len(j.Input.RouteEdits) > 0 {
		routes, ok := e.Routes.(routeImport)
		if !ok {
			return model.Fail("ROUTE_SETUP_UNAVAILABLE")
		}
		if err := routes.ImportRoutes(ctx, j.Input.RouteEdits, next); err != nil {
			return err
		}
	} else {
		slot := next.Active
		if next.State != "running" {
			slot = ""
		}
		if err := e.Routes.Set(ctx, next, slot); err != nil {
			return err
		}
	}
	if err := e.Routes.Ensure(ctx, next); err != nil {
		return err
	}
	*p = next
	if err := e.Store.Update(*j, p); err != nil {
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	return nil
}

func (e *Engine) reconcileRouteChange(ctx context.Context, j *model.Job) (model.Project, error) {
	if j.Input.Project == nil || j.Input.Previous == nil {
		return model.Project{}, model.Uncertain("RECOVERY_UNAVAILABLE")
	}
	// A last-domain unlink has an empty snippet, so it cannot be recovered by
	// observing a routed hostname. Verify the exact reviewed snapshot instead.
	for i, p := range []*model.Project{j.Input.Project, j.Input.Previous} {
		if i == 1 && len(j.Input.RouteEdits) > 0 {
			importer, ok := e.Routes.(routeImport)
			if !ok || importer.VerifyRouteImport(j.Input.RouteEdits) != nil {
				return model.Project{}, model.Uncertain("STATE_DIVERGED")
			}
		}
		if err := e.Routes.Ensure(ctx, *p); err == nil {
			return *p, nil
		}
	}
	return model.Project{}, model.Uncertain("STATE_DIVERGED")
}
