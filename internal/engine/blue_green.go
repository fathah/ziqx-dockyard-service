package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
)

type routeImport interface {
	VerifyRouteImport([]model.RouteEdit) error
	ImportRoutes(context.Context, []model.RouteEdit, model.Project) error
}

func (e *Engine) blueGreen(ctx context.Context, j *model.Job, p *model.Project) (result error) {
	if j.Input.Previous == nil || j.Input.Project == nil || j.Input.Release == nil {
		return model.Fail("RELEASE_INVALID")
	}
	previous := *j.Input.Previous
	next := *j.Input.Project
	r := *j.Input.Release
	if !reflect.DeepEqual(*p, previous) || p.ZeroDowntime || next.Adoption != nil || !next.ZeroDowntime || next.Active != "green" {
		return model.Fail("BLUE_GREEN_REVIEW_CHANGED")
	}
	if err := next.ValidateTarget(); err != nil {
		return err
	}
	if err := e.Routes.Ensure(ctx, previous); err != nil {
		return err
	}
	if len(j.Input.RouteEdits) > 0 {
		routes, ok := e.Routes.(routeImport)
		if !ok {
			return model.Fail("BLUE_GREEN_UNAVAILABLE")
		}
		if err := routes.VerifyRouteImport(j.Input.RouteEdits); err != nil {
			return err
		}
	}
	// Candidate ownership is separate from the live blue/adopted Compose identity.
	// The old project snapshot remains authoritative until traffic is verified.
	if check, ok := e.Docker.(interface {
		CheckBlueGreenSource(context.Context, model.Project) error
	}); ok {
		if err := check.CheckBlueGreenSource(ctx, previous); err != nil {
			return err
		}
	} else {
		return model.Fail("BLUE_GREEN_UNAVAILABLE")
	}
	if err := e.Docker.Validate(ctx, next, r); err != nil {
		return err
	}
	for _, port := range []int{next.BluePort, next.GreenPort} {
		free, err := e.Docker.PortFree(ctx, port)
		if err != nil {
			return err
		}
		if !free {
			return model.Fail("PORT_IN_USE")
		}
	}
	if err := e.phase(j, "candidate_intent"); err != nil {
		return err
	}
	if err := runtime.Binding(e.Config, next, "green", r); err != nil {
		return err
	}
	started := false
	cutover := false
	defer func() {
		var fault *model.Fault
		if result != nil && started && !cutover && ctx.Err() == nil && (!errors.As(result, &fault) || !fault.Recovery) {
			if err := e.Docker.Stop(ctx, next, "green"); err != nil {
				result = model.Uncertain("CANDIDATE_CLEANUP_REQUIRED")
			}
		}
	}()
	started = true
	if err := e.Docker.Start(ctx, next, "green"); err != nil {
		return err
	}
	if err := e.phase(j, "readiness"); err != nil {
		return err
	}
	if err := e.Docker.Healthy(ctx, next, "green", r); err != nil {
		return err
	}
	if err := e.Routes.Ensure(ctx, previous); err != nil {
		return err
	}
	if err := e.phase(j, "route_intent"); err != nil {
		return err
	}
	if len(j.Input.RouteEdits) > 0 {
		if err := e.Routes.(routeImport).ImportRoutes(ctx, j.Input.RouteEdits, next); err != nil {
			return err
		}
	} else {
		if err := e.Routes.Set(ctx, next, "green"); err != nil {
			return err
		}
	}
	cutover = true
	*p = next
	if err := e.Store.Update(*j, p); err != nil {
		return model.Uncertain("STATE_WRITE_FAILED")
	}
	// Once traffic moved, retain the old instance through the drain and readiness
	// check. An unhealthy new slot keeps the old one available for recovery.
	if err := e.wait(ctx, j); err != nil {
		return err
	}
	if err := e.Routes.Ensure(ctx, next); err != nil {
		return err
	}
	if err := e.Docker.Healthy(ctx, next, "green", r); err != nil {
		if err := e.phase(j, "rollback_route_intent"); err != nil {
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
				return model.Uncertain("BLUE_GREEN_ROLLBACK_REQUIRED")
			}
		} else if err := e.Routes.Set(ctx, previous, previous.Active); err != nil {
			return model.Uncertain("BLUE_GREEN_ROLLBACK_REQUIRED")
		}
		*p = previous
		if err := e.Store.Update(*j, p); err != nil {
			return model.Uncertain("STATE_WRITE_FAILED")
		}
		cutover = false
		return model.Fail("BLUE_GREEN_HEALTH_FAILED_ROLLED_BACK")
	}
	if err := e.Docker.Stop(ctx, previous, previous.Active); err != nil {
		j.Warning = "OLD_SLOT_STOP_FAILED"
	}
	if b, err := runtime.ComposeMirror(e.Config, next, r); err != nil {
		j.Warning = "COMPOSE_MIRROR_FAILED"
	} else if secure.Atomic(filepath.Join(e.Config.ProjectsRoot, p.ID, "compose.yml"), b, 0600) != nil {
		j.Warning = "COMPOSE_MIRROR_FAILED"
	}
	if b, err := os.ReadFile(filepath.Join(e.Config.ProjectsRoot, p.ID, "env", r.Environment+".env")); err != nil {
		j.Warning = "ENVIRONMENT_MIRROR_FAILED"
	} else if secure.Atomic(filepath.Join(e.Config.ProjectsRoot, p.ID, ".env"), b, 0600) != nil {
		j.Warning = "ENVIRONMENT_MIRROR_FAILED"
	}
	return nil
}

// Reconciliation never guesses traffic ownership. Before cutover, it preserves
// the old project; after a verified cutover it commits the candidate snapshot.
func (e *Engine) reconcileBlueGreen(ctx context.Context, j *model.Job) (model.Project, error) {
	if j.Input.Project == nil || j.Input.Previous == nil {
		return model.Project{}, model.Uncertain("STATE_DIVERGED")
	}
	next, old := *j.Input.Project, *j.Input.Previous
	observer, ok := e.Routes.(interface {
		Observe(context.Context, model.Project) (string, error)
	})
	if !ok {
		return old, model.Uncertain("RECOVERY_UNAVAILABLE")
	}
	if slot, err := observer.Observe(ctx, next); err == nil && slot == "green" {
		if err = e.Docker.Healthy(ctx, next, slot, next.Slots[slot]); err != nil {
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
	return old, nil
}
