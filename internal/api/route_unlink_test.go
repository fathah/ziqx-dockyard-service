package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func TestUnlinkServiceDomainReviewsPreserveSiblingsAndContainers(t *testing.T) {
	for _, state := range []string{"running", "stopped"} {
		for _, mode := range []string{"primary", "sibling", "last-sibling", "last-published"} {
			t.Run(state+"/"+mode, func(t *testing.T) {
				a, docker, _, send := apiFixture(t)
				a.Engine.Config.Keys[0].Projects = []string{"*"}
				a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
				a.Auth, _ = auth.New(a.Engine.Config)
				r := model.Release{ID: "original"}
				p := model.Project{ID: "demo", AppID: "tasks", Mode: "compose", Environment: model.Production, State: state, Active: "blue", RouteService: "web", RoutePort: 3000, BluePort: 3031, Domains: []string{"app.example.com", "api.example.com", "other.example.com"}, Slots: map[string]model.Release{"blue": r}, Releases: []model.Release{r}, ServiceRoutes: []model.ServiceRoute{{Service: "api", ContainerPort: 3040, HostPort: 3032, Domains: []string{"api.example.com", "other.example.com"}}}, CaddyRoutes: []model.CaddyRoute{{Domain: "api.example.com", Address: "api.example.com", Body: "reverse_proxy " + model.CaddyUpstream}}}
				input := routeSetupRequest{Unlink: true, ExpectedRelease: r.ID, Domains: []string{"api.example.com"}, Service: "api", ContainerPort: 3040}
				if mode == "primary" {
					input.Domains, input.Service, input.ContainerPort = []string{"app.example.com"}, "web", 3000
				}
				if mode == "last-sibling" {
					p.Domains = []string{"api.example.com"}
					p.ServiceRoutes[0].Domains = []string{"api.example.com"}
				}
				if mode == "last-published" {
					p.Adoption = &model.Adoption{SourceName: "tasks", ComposeProject: "tasks"}
					p.RouteService, p.RoutePort, p.ServiceRoutes, p.CaddyRoutes = "", 0, nil, nil
					p.Domains = []string{"app.example.com"}
					p.PublishedRoute = &model.PublishedRoute{Service: "web", ContainerPort: 3000, HostPort: 3031}
					input.Domains, input.Service, input.ContainerPort = p.Domains, "web", 3000
				}
				original := model.Job{ID: "original", ProjectID: p.ID, Status: "succeeded", Created: time.Now(), Input: model.Input{Project: &p}}
				if err := a.Engine.Store.Accept(original, &p, "original", "original", 10, 10); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(input)
				denied := send("POST", "/v1/projects/demo/route-setup-preview", string(raw), "sites.write", "denied")
				if denied.Code != 403 {
					t.Fatal("unscoped unlink accepted", denied.Code)
				}
				w := send("POST", "/v1/projects/demo/route-setup-preview", string(raw), "projects.write sites.write", "preview")
				var review struct {
					Hash   string `json:"review_sha256"`
					Unlink bool   `json:"unlink"`
				}
				json.Unmarshal(w.Body.Bytes(), &review)
				if w.Code != 200 || !review.Unlink || len(review.Hash) != 64 || docker.calls != 0 {
					t.Fatal(w.Code, w.Body.String(), docker.calls)
				}
				input.Review = review.Hash
				bad := input
				bad.Service = "worker"
				badRaw, _ := json.Marshal(bad)
				w = send("POST", "/v1/projects/demo/route-setup", string(badRaw), "projects.write sites.write", "wrong-service")
				if w.Code != 409 || !strings.Contains(w.Body.String(), "DOMAIN_TARGET_MISMATCH") {
					t.Fatal("wrong service unlinked", w.Code, w.Body.String())
				}
				bad = input
				bad.Review = strings.Repeat("f", 64)
				badRaw, _ = json.Marshal(bad)
				w = send("POST", "/v1/projects/demo/route-setup", string(badRaw), "projects.write sites.write", "wrong-review")
				if w.Code != 409 || !strings.Contains(w.Body.String(), "ROUTE_REVIEW_CHANGED") {
					t.Fatal("stale review unlinked", w.Code, w.Body.String())
				}
				raw, _ = json.Marshal(input)
				w = send("POST", "/v1/projects/demo/route-setup", string(raw), "projects.write sites.write", "unlink")
				if w.Code != 202 {
					t.Fatal(w.Code, w.Body.String())
				}
				var job model.Job
				json.Unmarshal(w.Body.Bytes(), &job)
				job, _ = a.Engine.Store.Job(job.ID)
				next := job.Input.Project
				if job.Action != "route_unlink" || job.Input.Release != nil || next.State != p.State || !reflect.DeepEqual(next.Slots, p.Slots) || docker.calls != 0 {
					t.Fatal("unlink changed containers or release", job)
				}
				for _, host := range next.Domains {
					if host == input.Domains[0] {
						t.Fatal("domain still assigned", next)
					}
				}
				if mode == "sibling" && (len(next.ServiceRoutes) != 1 || !reflect.DeepEqual(next.ServiceRoutes[0].Domains, []string{"other.example.com"}) || len(next.CaddyRoutes) != 0) {
					t.Fatal("sibling routes lost", next)
				}
				if mode == "last-published" && (next.PublishedRoute != nil || next.BluePort != 0 || len(next.Domains) != 0) {
					t.Fatal("empty published route retained", next)
				}
				if retry := send("POST", "/v1/projects/demo/route-setup", string(raw), "projects.write sites.write", "unlink"); retry.Body.String() != w.Body.String() {
					t.Fatal("unlink retry not idempotent")
				}
			})
		}
	}
}
