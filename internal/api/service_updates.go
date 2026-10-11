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

type serviceUpdateRequest struct {
	ExpectedRelease string `json:"expected_release_id"`
	Service         string `json:"service"`
	Mode            string `json:"mode"`
	ContainerPort   int    `json:"container_port,omitempty"`
	Readiness       string `json:"readiness_path,omitempty"`
	Review          string `json:"review_sha256,omitempty"`
}
type servicePlanner interface {
	PlanServiceUpdate(context.Context, model.Project, string, string, int, int, string) (model.ServiceUpdate, error)
	PublishedUpstream(context.Context, model.Project, string, int) (string, error)
}

func (a *API) serviceUpdate(w http.ResponseWriter, r *http.Request, principal auth.Principal, body []byte, id string, preview bool) {
	request := principal.RequestID
	if !config.ID.MatchString(id) {
		problem(w, 400, "REQUEST_INVALID", request)
		return
	}
	if !require(w, principal, "deploy.execute", id) || !a.nativeAuthority(w, principal) {
		return
	}
	var input serviceUpdateRequest
	if secure.Decode(body, &input) != nil || !config.ServiceName.MatchString(input.Service) || input.ExpectedRelease == "" || input.Mode != "restart" && input.Mode != "seamless" || input.Mode == "restart" && (input.ContainerPort != 0 || input.Readiness != "") || len(input.Readiness) > 2048 || input.Readiness != "" && (!strings.HasPrefix(input.Readiness, "/") || strings.ContainsAny(input.Readiness, "?#\r\n\x00")) {
		problem(w, 400, "REQUEST_INVALID", request)
		return
	}
	if input.Mode == "seamless" && (!require(w, principal, "sites.write", id) || !require(w, principal, "projects.write", id)) {
		return
	}
	e := a.Engine
	e.Admission.Lock()
	defer e.Admission.Unlock()
	fingerprint := auth.Fingerprint(a.FingerprintKey, e.Config.ServerID, r, body)
	if !preview {
		j, replayed, err := e.Store.Replay(principal.Idempotency, request, fingerprint)
		if err != nil {
			fail(w, err, request)
			return
		}
		if replayed {
			accepted(w, j)
			return
		}
	}
	if !e.Ready() {
		problem(w, 503, "RECOVERY_REQUIRED", request)
		return
	}
	busy, err := e.Store.Busy(id)
	if err != nil {
		fail(w, err, request)
		return
	}
	if busy {
		problem(w, 409, "PROJECT_BUSY", request)
		return
	}
	p, err := e.Store.Project(id)
	if err != nil {
		problem(w, 404, "PROJECT_NOT_FOUND", request)
		return
	}
	if err = p.ValidateTarget(); err != nil {
		fail(w, err, request)
		return
	}
	current, exists := p.Current()
	if !exists || current.ID != input.ExpectedRelease {
		problem(w, 409, "CONFIGURATION_CHANGED", request)
		return
	}
	d, ok := e.Docker.(servicePlanner)
	if !ok {
		problem(w, 409, "SERVICE_UPDATE_UNAVAILABLE", request)
		return
	}
	next := p
	next.ServiceInstances = map[string]model.ServiceInstance{}
	for k, v := range p.ServiceInstances {
		next.ServiceInstances[k] = v
	}
	next.ServicePorts = append([]int{}, p.ServicePorts...)
	var edits []model.RouteEdit
	domains := []string{}
	hostPort := 0
	if input.Mode == "seamless" {
		if p.Environment != model.Production || input.ContainerPort < 1 || input.ContainerPort > 65535 {
			problem(w, 409, "SERVICE_UPDATE_INVALID", request)
			return
		}
		targetPort, selectedDomains, targetErr := runtime.ServiceUpdateTarget(p, input.Service)
		if targetErr != nil {
			fail(w, targetErr, request)
			return
		}
		if targetPort > 0 && targetPort != input.ContainerPort {
			problem(w, 409, "SERVICE_ROUTE_MISMATCH", request)
			return
		}
		domains = selectedDomains
		legacyImport := p.Adoption != nil && len(p.Domains) == 0 && len(domains) > 0
		primary := p.RouteService == input.Service || p.PublishedRoute != nil && p.PublishedRoute.Service == input.Service || legacyImport
		if primary {
			next.PublishedRoute = nil
			next.RouteService = input.Service
			next.RoutePort = input.ContainerPort
			next.ReadinessPath = input.Readiness
			next.ServiceMode = true
		}
		if legacyImport {
			next.Domains = append([]string{}, p.ExternalDomains...)
			next.ExternalDomains = nil
		}
		if len(domains) == 0 {
			problem(w, 409, "SERVICE_DOMAIN_REQUIRED", request)
			return
		}
		if err = a.nativeDomains(domains, id); err != nil {
			fail(w, err, request)
			return
		}
		// Docker reserves an ephemeral loopback binding when creating the
		// candidate; there is no host-port pool or availability race.
		if legacyImport {
			upstream, err := d.PublishedUpstream(r.Context(), p, input.Service, input.ContainerPort)
			if err != nil {
				fail(w, err, request)
				return
			}
			routes, ok := e.Routes.(routeImporter)
			if !ok {
				problem(w, 409, "SERVICE_UPDATE_UNAVAILABLE", request)
				return
			}
			edits, err = routes.PlanRouteImport(r.Context(), next, upstream)
			if err != nil {
				fail(w, err, request)
				return
			}
		}
		if err = next.ValidateTarget(); err != nil {
			fail(w, err, request)
			return
		}
	}
	if err = e.Routes.Ensure(r.Context(), p); err != nil {
		fail(w, err, request)
		return
	}
	plan, err := d.PlanServiceUpdate(r.Context(), p, input.Service, input.Mode, input.ContainerPort, hostPort, input.Readiness)
	if err != nil {
		fail(w, err, request)
		return
	}
	approved := input.Review
	if planner, ok := e.Docker.(interface {
		PrepareServiceEnvironment(context.Context, model.Project, *model.ServiceUpdate) error
	}); ok {
		if !require(w, principal, "deploy.environment", id) {
			return
		}
		if err = planner.PrepareServiceEnvironment(r.Context(), p, &plan); err != nil {
			fail(w, err, request)
			return
		}
	}
	plan.PreparedImage = a.preparedImage(p, input.Service)
	input.Review = ""
	// Bind resolved environment contents, not the randomly named private .env.
	environmentRevision := ""
	if plan.SourceRelease != nil {
		environmentRevision = plan.SourceRelease.Compose
	}
	b, _ := json.Marshal([]any{input, p, next.ServicePorts, hostPort, plan.Previous, plan.NetworkAliases, plan.NetworkIDs, edits, plan.PreparedImage, environmentRevision})
	sum := sha256.Sum256(b)
	review := hex.EncodeToString(sum[:])
	if preview {
		write(w, 200, map[string]any{"review_sha256": review, "service": input.Service, "mode": input.Mode, "domains": domains, "container_port": input.ContainerPort, "host_port": hostPort, "imports_routes": len(edits) > 0, "dependencies_unchanged": true, "prepared_image": plan.PreparedImage})
		return
	}
	if approved != review {
		problem(w, 409, "SERVICE_REVIEW_CHANGED", request)
		return
	}
	j := model.Job{ID: state.NewID("job-"), ProjectID: id, Action: "service_update", Status: "queued", Phase: "accepted", Actor: principal.Actor, RequestID: request, Created: time.Now().UTC(), Input: model.Input{Project: &next, Previous: &p, ServiceUpdate: &plan, RouteEdits: edits}}
	if err = e.Store.Accept(j, nil, principal.Idempotency, fingerprint, e.Config.MaxQueuedJobs, e.Config.MaxProjects); err != nil {
		fail(w, err, request)
		return
	}
	e.Notify()
	accepted(w, j)
}
