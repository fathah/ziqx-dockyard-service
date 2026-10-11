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
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
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

// Connect a positively identified Compose service to Caddy. Existing bindings
// remain in place; missing bindings require a reviewed controlled deployment.
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
	if !p.NativeCompose() || p.ZeroDowntime {
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
	legacy := p.Adoption != nil && len(p.Domains) == 0 && !p.ServiceMode && p.PublishedRoute == nil && len(p.ServiceRoutes) == 0 && (len(p.ExternalDomains) == 0 || sameDomains(input.Domains, p.ExternalDomains))
	if !legacy {
		a.serviceRouteSetup(w, r, principal, input, p, current, fingerprint, preview, d)
		return
	}
	upstream, err := d.PublishedUpstream(r.Context(), p, input.Service, input.ContainerPort)
	if err != nil {
		if len(p.ExternalDomains) == 0 && err.Error() == "BLUE_GREEN_ROUTE_TARGET_MISMATCH" {
			a.serviceRouteSetup(w, r, principal, input, p, current, fingerprint, preview, d)
			return
		}
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

func sameDomains(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, name := range a {
		set[name] = true
	}
	for _, name := range b {
		if !set[name] {
			return false
		}
	}
	return true
}

// Add hosts to one service without replacing a sibling's route. Reuse a
// verified published binding, or review a controlled deployment that adds one.
func (a *API) serviceRouteSetup(w http.ResponseWriter, r *http.Request, principal auth.Principal, input routeSetupRequest, p model.Project, current model.Release, fingerprint string, preview bool, d interface {
	PublishedUpstream(context.Context, model.Project, string, int) (string, error)
}) {
	e := a.Engine
	request := principal.RequestID
	next := p
	next.Domains = append([]string{}, p.Domains...)
	next.ServiceRoutes = append([]model.ServiceRoute{}, p.ServiceRoutes...)
	primary := p.RouteService == input.Service && p.RoutePort == input.ContainerPort || p.PublishedRoute != nil && p.PublishedRoute.Service == input.Service && p.PublishedRoute.ContainerPort == input.ContainerPort
	index := -1
	for i, route := range p.ServiceRoutes {
		if route.Service == input.Service && route.ContainerPort == input.ContainerPort {
			index = i
		}
	}
	for _, name := range input.Domains {
		for _, owned := range p.Domains {
			if owned != name {
				continue
			}
			assigned := primary
			for i, route := range p.ServiceRoutes {
				for _, host := range route.Domains {
					if host == name {
						assigned = i == index
					}
				}
			}
			if !assigned {
				problem(w, 409, "DOMAIN_IN_USE", request)
				return
			}
		}
		found := false
		for _, owned := range next.Domains {
			found = found || owned == name
		}
		if !found {
			next.Domains = append(next.Domains, name)
		}
	}
	if err := a.nativeDomains(next.Domains, p.ID); err != nil {
		fail(w, err, request)
		return
	}
	if err := e.Routes.Ensure(r.Context(), p); err != nil {
		fail(w, err, request)
		return
	}
	if err := e.Routes.DomainsAvailable(r.Context(), input.Domains, p.Domains); err != nil {
		fail(w, err, request)
		return
	}
	upstream := ""
	var err error
	if primary {
		upstream = net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port(p.Active)))
	} else {
		upstream, err = d.PublishedUpstream(r.Context(), p, input.Service, input.ContainerPort)
	}
	managed := index >= 0 && p.ServiceRoutes[index].Managed
	var release *model.Release
	if err != nil {
		if err.Error() != "BLUE_GREEN_ROUTE_TARGET_MISMATCH" || index >= 0 {
			fail(w, err, request)
			return
		}
		if len(p.ExternalDomains) > 0 {
			problem(w, 409, "ADOPTED_ROUTES_PRESERVED", request)
			return
		}
		port, err := e.AvailablePort(r.Context(), map[int]bool{})
		if err != nil {
			fail(w, err, request)
			return
		}
		upstream = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		managed = true
	}
	host, port, parseErr := net.SplitHostPort(upstream)
	hostPort, numberErr := strconv.Atoi(port)
	if parseErr != nil || numberErr != nil || host != "127.0.0.1" || hostPort < 1 || hostPort > 65535 {
		problem(w, 409, "CONTAINER_BINDING_DIVERGED", request)
		return
	}
	if !primary {
		route := model.ServiceRoute{Service: input.Service, ContainerPort: input.ContainerPort, HostPort: hostPort, Domains: append([]string{}, input.Domains...), Managed: managed}
		if index >= 0 {
			if p.ServiceRoutes[index].HostPort != hostPort {
				problem(w, 409, "CONTAINER_BINDING_DIVERGED", request)
				return
			}
			route.Domains = append([]string{}, p.ServiceRoutes[index].Domains...)
			for _, name := range input.Domains {
				found := false
				for _, old := range route.Domains {
					found = found || old == name
				}
				if !found {
					route.Domains = append(route.Domains, name)
				}
			}
			next.ServiceRoutes[index] = route
		} else {
			next.ServiceRoutes = append(next.ServiceRoutes, route)
		}
	}
	if err := next.ValidateTarget(); err != nil {
		fail(w, err, request)
		return
	}
	if managed && index < 0 {
		if !require(w, principal, "deploy.execute", p.ID) {
			return
		}
		preparer, ok := e.Docker.(interface {
			PrepareServiceRoutes(context.Context, model.Project, model.Release) (model.Release, error)
		})
		if !ok {
			problem(w, 409, "ROUTE_SETUP_UNAVAILABLE", request)
			return
		}
		prepared, err := preparer.PrepareServiceRoutes(r.Context(), next, current)
		if err != nil {
			fail(w, err, request)
			return
		}
		if err = e.Docker.Validate(r.Context(), next, prepared); err != nil {
			fail(w, err, request)
			return
		}
		if err = runtime.IndexCompose(e.Config, e.Store, next, prepared); err != nil {
			fail(w, err, request)
			return
		}
		release = &prepared
	}
	approved := input.Review
	input.Review = ""
	encoded, _ := json.Marshal([]any{input, p, next, upstream, release})
	sum := sha256.Sum256(encoded)
	review := hex.EncodeToString(sum[:])
	if preview {
		write(w, 200, map[string]any{"review_sha256": review, "domains": input.Domains, "service": input.Service, "container_port": input.ContainerPort, "upstream": upstream, "imports_routes": false, "containers_unchanged": release == nil})
		return
	}
	if approved != review {
		problem(w, 409, "ROUTE_REVIEW_CHANGED", request)
		return
	}
	if release != nil {
		release.ID = state.NewID("rel-")
		release.Created = time.Now().UTC()
	}
	job := model.Job{ID: state.NewID("job-"), ProjectID: p.ID, Action: "route_setup", Status: "queued", Phase: "accepted", Actor: principal.Actor, RequestID: request, Created: time.Now().UTC(), Input: model.Input{Project: &next, Previous: &p, Release: release, RouteTarget: &model.PublishedRoute{Service: input.Service, ContainerPort: input.ContainerPort, HostPort: hostPort}}}
	if err := e.Store.Accept(job, nil, principal.Idempotency, fingerprint, e.Config.MaxQueuedJobs, e.Config.MaxProjects); err != nil {
		fail(w, err, request)
		return
	}
	e.Notify()
	accepted(w, job)
}
