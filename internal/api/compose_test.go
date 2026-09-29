package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/engine"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
)

func seedComposeProject(t *testing.T, a *API) {
	t.Helper()
	c := a.Engine.Config
	p := model.Project{ID: "demo", AppID: "demo", Environment: model.Production, Template: "node", TemplateRevision: engine.TemplateHash(c.Templates["node"]), Domains: []string{"app.example.com"}, BluePort: 3001, GreenPort: 3002, ZeroDowntime: true, State: "awaiting_release", Slots: map[string]model.Release{}, Releases: []model.Release{}}
	if err := runtime.Compose(c, p); err != nil {
		t.Fatal(err)
	}
	j := model.Job{ID: "seed", ProjectID: "demo", Action: "project_create", Status: "succeeded", RequestID: "seed-request"}
	if err := a.Engine.Store.Accept(j, &p, "seed", "seed-fingerprint", 10, 10); err != nil {
		t.Fatal(err)
	}
}

func TestComposeAdmissionRequiresYAMLAndEnvironmentPermission(t *testing.T) {
	source := "services:\n  app:\n    image: ghcr.io/ziqx/demo@sha256:" + strings.Repeat("a", 64) + "\n    environment: {TOKEN: app-secret}\n  worker:\n    x-dockyard-template: node\n    image: ghcr.io/ziqx/demo@sha256:" + strings.Repeat("b", 64) + "\n    environment: {TOKEN: worker-secret}\n"
	cases := []struct {
		name, body, scope string
		status            int
	}{
		{"legacy image body", `{"image":"ghcr.io/ziqx/demo:latest"}`, "deploy.execute", 400},
		{"missing yaml", `{"environment":"production","variables":{"TOKEN":"secret"}}`, "deploy.environment deploy.execute", 400},
		{"unsafe yaml", "", "deploy.environment deploy.execute", 400},
		{"missing environment scope", "", "deploy.execute", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, d, routes, send := apiFixture(t)
			seedComposeProject(t, a)
			body := tc.body
			if body == "" {
				yaml := source
				if tc.name == "unsafe yaml" {
					yaml += "    privileged: true\n"
				}
				b, _ := json.Marshal(map[string]string{"environment": "production", "compose_yaml": yaml})
				body = string(b)
			}
			w := send("POST", "/v1/projects/demo/deploy", body, tc.scope, "new-deploy")
			if w.Code != tc.status || d.calls+routes.calls != 0 {
				t.Fatal("invalid Compose reached adapters", w.Code, w.Body.String())
			}
			if _, err := os.Stat(filepath.Join(a.Engine.Config.ProjectsRoot, "demo", "env")); !os.IsNotExist(err) {
				t.Fatal("rejection wrote secrets")
			}
			if busy, _ := a.Engine.Store.Busy("demo"); busy {
				t.Fatal("rejection queued deployment")
			}
		})
	}
}

func TestComposeAdmissionValidatesBeforeQueueAndDeduplicates(t *testing.T) {
	source := "services:\n  app:\n    image: ghcr.io/ziqx/demo@sha256:" + strings.Repeat("a", 64) + "\n    environment: {TOKEN: app-secret}\n  worker:\n    x-dockyard-template: node\n    image: ghcr.io/ziqx/demo@sha256:" + strings.Repeat("b", 64) + "\n    environment: {TOKEN: worker-secret}\n"
	bodyBytes, _ := json.Marshal(map[string]string{"environment": "production", "compose_yaml": source})
	body := string(bodyBytes)
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "compose parser rejects"}[reject], func(t *testing.T) {
			a, d, _, send := apiFixture(t)
			seedComposeProject(t, a)
			if reject {
				d.validationError = model.Fail("COMPOSE_INVALID")
			}
			w := send("POST", "/v1/projects/demo/deploy", body, "deploy.environment deploy.execute", "deploy-compose")
			if reject {
				if w.Code != 400 {
					t.Fatal(w.Code, w.Body.String())
				}
				if busy, _ := a.Engine.Store.Busy("demo"); busy {
					t.Fatal("queued before Compose validation")
				}
				return
			}
			if w.Code != 202 || d.calls != 1 {
				t.Fatal("did not validate exactly once before queue", w.Code, w.Body.String(), d.calls)
			}
			retry := send("POST", "/v1/projects/demo/deploy", body, "deploy.environment deploy.execute", "deploy-compose")
			if retry.Code != 202 || retry.Body.String() != w.Body.String() || d.calls != 1 {
				t.Fatal("retry restaged Compose or secrets")
			}
			var receipt map[string]string
			json.Unmarshal(w.Body.Bytes(), &receipt)
			job, err := a.Engine.Store.Job(receipt["job_id"])
			if err != nil || job.Input.Release.Compose == "" {
				t.Fatal("lost Compose identity", err)
			}
			metadata, _ := json.Marshal(job.Input)
			if strings.Contains(string(metadata), "app-secret") || strings.Contains(string(metadata), "worker-secret") {
				t.Fatal("inline Compose secrets entered durable metadata")
			}
			events, _ := a.Engine.Store.Audit(0)
			for _, event := range events {
				if strings.Contains(string(event), "secret") {
					t.Fatal("secret entered audit")
				}
			}
		})
	}
}
