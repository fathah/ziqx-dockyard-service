package engine

import (
	"context"
	"fmt"
	"reflect"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func (e *Engine) setupRoute(ctx context.Context, j *model.Job, p *model.Project) error {
	if j.Input.Previous == nil || j.Input.Project == nil || !reflect.DeepEqual(*p, *j.Input.Previous) {
		return model.Fail("ROUTE_REVIEW_CHANGED")
	}
	next := *j.Input.Project
	if err := next.ValidateTarget(); err != nil {
		return err
	}
	pin := next.PublishedRoute
	if pin == nil || p.State != "running" || p.Adoption == nil || len(p.Domains) != 0 {
		return model.Fail("ROUTE_SETUP_UNAVAILABLE")
	}
	r, ok := p.Current()
	if !ok {
		return model.Fail("CONFIGURATION_CHANGED")
	}
	if err := e.Docker.Healthy(ctx, *p, p.Active, r); err != nil {
		return err
	}
	d, ok := e.Docker.(interface {
		PublishedUpstream(context.Context, model.Project, string, int) (string, error)
	})
	if !ok {
		return model.Fail("ROUTE_SETUP_UNAVAILABLE")
	}
	upstream, err := d.PublishedUpstream(ctx, *p, pin.Service, pin.ContainerPort)
	if err != nil {
		return err
	}
	if upstream != fmt.Sprintf("127.0.0.1:%d", pin.HostPort) {
		return model.Fail("ROUTE_REVIEW_CHANGED")
	}
	if err = e.Routes.Ensure(ctx, *p); err != nil {
		return err
	}
	if len(j.Input.RouteEdits) == 0 {
		if err = e.Routes.DomainsAvailable(ctx, next.Domains, nil); err != nil {
			return err
		}
	}
	if err = e.phase(j, "route_intent"); err != nil {
		return err
	}
	if len(j.Input.RouteEdits) > 0 {
		routes, ok := e.Routes.(routeImport)
		if !ok {
			return model.Fail("ROUTE_SETUP_UNAVAILABLE")
		}
		err = routes.ImportRoutes(ctx, j.Input.RouteEdits, next)
	} else {
		err = e.Routes.Set(ctx, next, next.Active)
	}
	if err != nil {
		return err
	}
	if err = e.Routes.Ensure(ctx, next); err != nil {
		return err
	}
	*p = next
	if err = e.Store.Update(*j, p); err != nil {
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	return nil
}
