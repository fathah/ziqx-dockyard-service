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
	hostPort := 0
	if input.Mode == "seamless" {
		if p.Environment != model.Production || input.ContainerPort < 1 || input.ContainerPort > 65535 {
			problem(w, 409, "SERVICE_UPDATE_INVALID", request)
			return
		}
		if p.RouteService != "" && p.RouteService != input.Service {
			problem(w, 409, "SERVICE_ROUTE_MISMATCH", request)
			return
		}
		next.RouteService = input.Service
		next.RoutePort = input.ContainerPort
		next.ReadinessPath = input.Readiness
		next.ServiceMode = true
		if p.Adoption != nil && len(p.Domains) == 0 {
			next.Domains = append([]string{}, p.ExternalDomains...)
			next.ExternalDomains = nil
		}
		if len(next.Domains) == 0 {
			problem(w, 409, "SERVICE_DOMAIN_REQUIRED", request)
			return
		}
		if err = a.nativeDomains(next.Domains, id); err != nil {
			fail(w, err, request)
			return
		}
		if len(next.ServicePorts) == 0 {
			first, err := e.AvailablePort(r.Context(), nil)
			if err != nil {
				fail(w, err, request)
				return
			}
			second, err := e.AvailablePort(r.Context(), map[int]bool{first: true})
			if err != nil {
				fail(w, err, request)
				return
			}
			next.ServicePorts = []int{first, second}
		}
		for _, port := range next.ServicePorts {
			free, err := e.Docker.PortFree(r.Context(), port)
			if err != nil {
				fail(w, err, request)
				return
			}
			if free {
				hostPort = port
				break
			}
		}
		if hostPort == 0 {
			problem(w, 409, "PORT_IN_USE", request)
			return
		}
		if next.BluePort == 0 {
			next.BluePort = next.ServicePorts[0]
		}
		if p.Adoption != nil && len(p.Domains) == 0 {
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
	input.Review = ""
	b, _ := json.Marshal([]any{input, p, next.ServicePorts, hostPort, plan.Previous, plan.NetworkAliases, plan.NetworkIDs, edits})
	sum := sha256.Sum256(b)
	review := hex.EncodeToString(sum[:])
	if preview {
		write(w, 200, map[string]any{"review_sha256": review, "service": input.Service, "mode": input.Mode, "domains": next.Domains, "container_port": input.ContainerPort, "host_port": hostPort, "imports_routes": len(edits) > 0, "dependencies_unchanged": true})
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
