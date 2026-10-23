package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
)

// Unlink uses the same reviewed route operation, without inspecting or changing
// containers. DNS records remain intact; the hostname becomes available again.
func (a *API) unlinkServiceDomains(w http.ResponseWriter, r *http.Request, principal auth.Principal, input routeSetupRequest, p model.Project, fingerprint string, preview bool) {
	request := principal.RequestID
	if input.ReplaceRules {
		problem(w, 400, "REQUEST_INVALID", request)
		return
	}
	remove := map[string]bool{}
	for _, host := range input.Domains {
		assigned := false
		for _, name := range p.Domains {
			assigned = assigned || name == host
		}
		if !assigned {
			problem(w, 409, "DOMAIN_NOT_ASSIGNED", request)
			return
		}
		service, port := p.RouteService, p.RoutePort
		if pin := p.PublishedRoute; pin != nil {
			service, port = pin.Service, pin.ContainerPort
		}
		for _, route := range p.ServiceRoutes {
			for _, name := range route.Domains {
				if name == host {
					service, port = route.Service, route.ContainerPort
				}
			}
		}
		if service != input.Service || port != input.ContainerPort {
			problem(w, 409, "DOMAIN_TARGET_MISMATCH", request)
			return
		}
		remove[host] = true
	}
	keep := func(domains []string) []string {
		names := []string{}
		for _, name := range domains {
			if !remove[name] {
				names = append(names, name)
			}
		}
		return names
	}
	next := p
	next.Domains = keep(p.Domains)
	next.ServiceRoutes = nil
	siblingDomains := map[string]bool{}
	for _, route := range p.ServiceRoutes {
		route.Domains = keep(route.Domains)
		if len(route.Domains) > 0 {
			next.ServiceRoutes = append(next.ServiceRoutes, route)
			for _, name := range route.Domains {
				siblingDomains[name] = true
			}
		}
	}
	next.CaddyRoutes = nil
	for _, route := range p.CaddyRoutes {
		if !remove[route.Domain] {
			next.CaddyRoutes = append(next.CaddyRoutes, route)
		}
	}
	if p.PublishedRoute != nil {
		primaryDomains := 0
		for _, host := range next.Domains {
			if !siblingDomains[host] {
				primaryDomains++
			}
		}
		if primaryDomains == 0 {
			next.PublishedRoute = nil
			next.BluePort, next.GreenPort = 0, 0
		}
	}
	if err := next.ValidateTarget(); err != nil {
		fail(w, err, request)
		return
	}
	if err := a.Engine.Routes.Ensure(r.Context(), p); err != nil {
		fail(w, err, request)
		return
	}
	approved := input.Review
	input.Review = ""
	encoded, _ := json.Marshal([]any{input, p, next})
	sum := sha256.Sum256(encoded)
	review := hex.EncodeToString(sum[:])
	if preview {
		write(w, 200, map[string]any{"review_sha256": review, "domains": input.Domains, "service": input.Service, "container_port": input.ContainerPort, "unlink": true, "containers_unchanged": true, "imports_routes": false, "upstream": ""})
		return
	}
	if approved != review {
		problem(w, 409, "ROUTE_REVIEW_CHANGED", request)
		return
	}
	job := model.Job{ID: state.NewID("job-"), ProjectID: p.ID, Action: "route_unlink", Status: "queued", Phase: "accepted", Actor: principal.Actor, RequestID: request, Created: time.Now().UTC(), Input: model.Input{Project: &next, Previous: &p}}
	if err := a.Engine.Store.Accept(job, nil, principal.Idempotency, fingerprint, a.Engine.Config.MaxQueuedJobs, a.Engine.Config.MaxProjects); err != nil {
		fail(w, err, request)
		return
	}
	a.Engine.Notify()
	accepted(w, job)
}
