package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
)

type blueGreenRequest struct {
	ExpectedRelease string `json:"expected_release_id"`
	Compose         string `json:"compose_yaml"`
	Dotenv          string `json:"env_file"`
	Service         string `json:"route_service"`
	Port            int    `json:"route_port"`
	Readiness       string `json:"readiness_path"`
	Review          string `json:"review_sha256,omitempty"`
}
type routeImporter interface {
	PlanRouteImport(context.Context, model.Project, string) ([]model.RouteEdit, error)
}

func (a *API) blueGreen(w http.ResponseWriter, r *http.Request, principal auth.Principal, body []byte, id string, preview bool) {
	if !config.ID.MatchString(id) {
		problem(w, 400, "REQUEST_INVALID", principal.RequestID)
		return
	}
	if !require(w, principal, "deploy.environment", id) || !a.nativeAuthority(w, principal) {
		return
	}
	if !preview && (!require(w, principal, "deploy.execute", id) || !require(w, principal, "projects.write", id) || !require(w, principal, "sites.write", id)) {
		return
	}
	var input blueGreenRequest
	if secure.Decode(body, &input) != nil || !config.ServiceName.MatchString(input.Service) || input.Port < 1 || input.Port > 65535 || len(input.Compose) == 0 || len(input.Compose) > 64<<10 || len(input.Dotenv) > 64<<10 || input.Readiness != "" && (!strings.HasPrefix(input.Readiness, "/") || strings.ContainsAny(input.Readiness, "?#\r\n")) {
		problem(w, 400, "REQUEST_INVALID", principal.RequestID)
		return
	}
	e := a.Engine
	e.Admission.Lock()
	defer e.Admission.Unlock()
	fingerprint := auth.Fingerprint(a.FingerprintKey, e.Config.ServerID, r, body)
	if !preview {
		j, replayed, err := e.Store.Replay(principal.Idempotency, principal.RequestID, fingerprint)
		if err != nil {
			fail(w, err, principal.RequestID)
			return
		}
		if replayed {
			accepted(w, j)
			return
		}
	}
	if !e.Ready() {
		problem(w, 503, "RECOVERY_REQUIRED", principal.RequestID)
		return
	}
	busy, err := e.Store.Busy(id)
	if err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	if busy {
		problem(w, 409, "PROJECT_BUSY", principal.RequestID)
		return
	}
	p, err := e.Store.Project(id)
	if err != nil {
		problem(w, 404, "PROJECT_NOT_FOUND", principal.RequestID)
		return
	}
	if err = p.ValidateTarget(); err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	current, ok := p.Current()
	if !ok || current.ID != input.ExpectedRelease {
		problem(w, 409, "CONFIGURATION_CHANGED", principal.RequestID)
		return
	}
	if !p.NativeCompose() || p.ZeroDowntime || p.Environment != model.Production || p.State != "running" || p.Active != "blue" {
		problem(w, 409, "BLUE_GREEN_RUNNING_SINGLE_REQUIRED", principal.RequestID)
		return
	}
	checker, ok := e.Docker.(interface {
		CheckBlueGreenSource(context.Context, model.Project) error
	})
	if !ok {
		problem(w, 409, "BLUE_GREEN_UNAVAILABLE", principal.RequestID)
		return
	}
	if err = checker.CheckBlueGreenSource(r.Context(), p); err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	next := p
	next.Adoption = nil
	next.ExternalDomains = nil
	next.ZeroDowntime = true
	next.RouteService = input.Service
	next.RoutePort = input.Port
	next.ReadinessPath = input.Readiness
	next.Active = "green"
	next.Slots = map[string]model.Release{}
	next.Releases = []model.Release{}
	next.Domains = append([]string{}, p.Domains...)
	if p.Adoption != nil {
		next.Domains = append([]string{}, p.ExternalDomains...)
	}
	if len(next.Domains) == 0 {
		problem(w, 409, "COMPOSE_BLUE_GREEN_ROUTE_REQUIRED", principal.RequestID)
		return
	}
	if err = a.nativeDomains(next.Domains, id); err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	next.BluePort, err = e.AvailablePort(r.Context(), map[int]bool{p.BluePort: true, p.GreenPort: true})
	if err == nil {
		next.GreenPort, err = e.AvailablePort(r.Context(), map[int]bool{p.BluePort: true, p.GreenPort: true, next.BluePort: true})
	}
	if err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	var edits []model.RouteEdit
	if p.Adoption != nil {
		d, ok := e.Docker.(interface {
			PublishedUpstream(context.Context, model.Project, string, int) (string, error)
		})
		if !ok {
			problem(w, 409, "BLUE_GREEN_UNAVAILABLE", principal.RequestID)
			return
		}
		upstream, err := d.PublishedUpstream(r.Context(), p, input.Service, input.Port)
		if err != nil {
			fail(w, err, principal.RequestID)
			return
		}
		routes, ok := e.Routes.(routeImporter)
		if !ok {
			problem(w, 409, "BLUE_GREEN_UNAVAILABLE", principal.RequestID)
			return
		}
		edits, err = routes.PlanRouteImport(r.Context(), next, upstream)
		if err != nil {
			fail(w, err, principal.RequestID)
			return
		}
	} else {
		if err = e.Routes.Ensure(r.Context(), p); err != nil {
			fail(w, err, principal.RequestID)
			return
		}
	}
	d, ok := e.Docker.(interface {
		PrepareNative(context.Context, model.Project, string, string) (model.Release, error)
	})
	if !ok {
		problem(w, 409, "BLUE_GREEN_UNAVAILABLE", principal.RequestID)
		return
	}
	release, err := d.PrepareNative(r.Context(), next, input.Compose, input.Dotenv)
	if err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	if err = e.Docker.Validate(r.Context(), next, release); err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	// Bind approval to submitted files, exact old project, free ports, and route
	// source. Random snapshot IDs are excluded so a recheck can reproduce it.
	approved := input.Review
	input.Review = ""
	b, _ := json.Marshal([]any{input, p, next.BluePort, next.GreenPort, edits})
	sum := sha256.Sum256(b)
	review := hex.EncodeToString(sum[:])
	if preview {
		write(w, 200, map[string]any{"review_sha256": review, "blue_port": next.BluePort, "green_port": next.GreenPort, "domains": next.Domains, "route_service": next.RouteService, "route_port": next.RoutePort, "imports_routes": len(edits) > 0})
		return
	}
	if approved != review {
		problem(w, 409, "BLUE_GREEN_REVIEW_CHANGED", principal.RequestID)
		return
	}
	release.ID = state.NewID("rel-")
	release.Created = time.Now().UTC()
	next.Slots["green"] = release
	next.Releases = []model.Release{release}
	j := model.Job{ID: state.NewID("job-"), ProjectID: id, Action: "blue_green", Status: "queued", Phase: "accepted", Actor: principal.Actor, RequestID: principal.RequestID, Created: time.Now().UTC(), Input: model.Input{Project: &next, Previous: &p, Release: &release, RouteEdits: edits}}
	if err = runtime.IndexCompose(e.Config, e.Store, next, release); err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	if err = e.Store.Accept(j, nil, principal.Idempotency, fingerprint, e.Config.MaxQueuedJobs, e.Config.MaxProjects); err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	e.Notify()
	accepted(w, j)
}
