package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
)

type routeSetupRequest struct {
	ExpectedRelease string   `json:"expected_release_id"`
	Domains         []string `json:"domains"`
	Service         string   `json:"service"`
	ContainerPort   int      `json:"container_port"`
	Review          string   `json:"review_sha256,omitempty"`
}

// Configure only Caddy around an existing, positively identified Compose stack.
// The existing published port and immutable release remain unchanged.
func (a *API) routeSetup(w http.ResponseWriter, r *http.Request, principal auth.Principal, body []byte, id string, preview bool) {
	request := principal.RequestID
	if !config.ID.MatchString(id) {
		problem(w, 400, "REQUEST_INVALID", request)
		return
	}
	if !require(w, principal, "sites.write", id) || !require(w, principal, "projects.write", id) || !a.nativeAuthority(w, principal) {
		return
	}
	var input routeSetupRequest
	if secure.Decode(body, &input) != nil || input.ExpectedRelease == "" || len(input.Domains) == 0 || !config.ServiceName.MatchString(input.Service) || input.ContainerPort < 1 || input.ContainerPort > 65535 {
		problem(w, 400, "REQUEST_INVALID", request)
		return
	}
	e := a.Engine
	e.Admission.Lock()
	defer e.Admission.Unlock()
	fingerprint := auth.Fingerprint(a.FingerprintKey, e.Config.ServerID, r, body)
	if !preview {
		job, replayed, err := e.Store.Replay(principal.Idempotency, request, fingerprint)
		if err != nil {
			fail(w, err, request)
			return
		}
		if replayed {
			accepted(w, job)
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
	if !p.NativeCompose() || p.Adoption == nil || p.ZeroDowntime || len(p.Domains) != 0 || p.ServiceMode || p.PublishedRoute != nil {
		problem(w, 409, "ROUTE_SETUP_UNAVAILABLE", request)
		return
	}
	if p.State != "running" {
		problem(w, 409, "PROJECT_NOT_RUNNING", request)
		return
	}
	current, ok := p.Current()
	if !ok || current.ID != input.ExpectedRelease {
		problem(w, 409, "CONFIGURATION_CHANGED", request)
		return
	}
	if err = a.nativeDomains(input.Domains, id); err != nil {
		fail(w, err, request)
		return
	}
	d, ok := e.Docker.(interface {
		PublishedUpstream(context.Context, model.Project, string, int) (string, error)
	})
	if !ok {
		problem(w, 409, "ROUTE_SETUP_UNAVAILABLE", request)
		return
	}
	if err = e.Docker.Healthy(r.Context(), p, p.Active, current); err != nil {
		fail(w, err, request)
		return
	}
	upstream, err := d.PublishedUpstream(r.Context(), p, input.Service, input.ContainerPort)
	if err != nil {
		fail(w, err, request)
		return
	}
	host, port, err := net.SplitHostPort(upstream)
	hostPort, parseErr := strconv.Atoi(port)
	if err != nil || parseErr != nil || host != "127.0.0.1" || hostPort < 1 || hostPort > 65535 {
		problem(w, 409, "CONTAINER_BINDING_DIVERGED", request)
		return
	}
	next := p
	next.Domains = append([]string{}, input.Domains...)
	next.BluePort = hostPort
	next.PublishedRoute = &model.PublishedRoute{Service: input.Service, ContainerPort: input.ContainerPort, HostPort: hostPort}
	var edits []model.RouteEdit
	if len(p.ExternalDomains) > 0 {
		// Import the complete observed site group; never discard unreviewed hosts.
		if len(input.Domains) != len(p.ExternalDomains) {
			problem(w, 409, "ROUTE_DOMAINS_CHANGED", request)
			return
		}
		wanted := map[string]bool{}
		for _, domain := range input.Domains {
			wanted[domain] = true
		}
		for _, domain := range p.ExternalDomains {
			if !wanted[domain] {
				problem(w, 409, "ROUTE_DOMAINS_CHANGED", request)
				return
			}
		}
		routes, ok := e.Routes.(routeImporter)
		if !ok {
			problem(w, 409, "ROUTE_SETUP_UNAVAILABLE", request)
			return
		}
		edits, err = routes.PlanRouteImport(r.Context(), next, upstream)
		if err != nil {
			fail(w, err, request)
			return
		}
		next.ExternalDomains = nil
	} else if err = e.Routes.DomainsAvailable(r.Context(), input.Domains, nil); err != nil {
		fail(w, err, request)
		return
	}
	if err = e.Routes.Ensure(r.Context(), p); err != nil {
		fail(w, err, request)
		return
	}
	if err = next.ValidateTarget(); err != nil {
		fail(w, err, request)
		return
	}
	approved := input.Review
	input.Review = ""
	encoded, _ := json.Marshal([]any{input, p, next, upstream, edits})
	sum := sha256.Sum256(encoded)
	review := hex.EncodeToString(sum[:])
	if preview {
		write(w, 200, map[string]any{"review_sha256": review, "domains": next.Domains, "service": input.Service, "container_port": input.ContainerPort, "upstream": upstream, "imports_routes": len(edits) > 0, "containers_unchanged": true})
		return
	}
	if approved != review {
		problem(w, 409, "ROUTE_REVIEW_CHANGED", request)
		return
	}
	job := model.Job{ID: state.NewID("job-"), ProjectID: id, Action: "route_setup", Status: "queued", Phase: "accepted", Actor: principal.Actor, RequestID: request, Created: time.Now().UTC(), Input: model.Input{Project: &next, Previous: &p, RouteEdits: edits}}
	if err = e.Store.Accept(job, nil, principal.Idempotency, fingerprint, e.Config.MaxQueuedJobs, e.Config.MaxProjects); err != nil {
		fail(w, err, request)
		return
	}
	e.Notify()
	accepted(w, job)
}
