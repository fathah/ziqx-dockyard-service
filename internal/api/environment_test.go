package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
)

func TestAppDeploymentsInThreeEnvironmentsAreIsolated(t *testing.T) {
	a, _, _, send := apiFixture(t)
	a.Engine.Config.Keys[0].Projects = []string{"demo-development", "demo-staging", "demo-production", "duplicate"}
	var err error
	a.Auth, err = auth.New(a.Engine.Config)
	if err != nil {
		t.Fatal(err)
	}
	ports := map[int]bool{}
	for _, environment := range []string{model.Development, model.Staging, model.Production} {
		id := "demo-" + environment
		body, _ := json.Marshal(map[string]any{"id": id, "app_id": "demo", "environment": environment, "template_id": "node", "domains": []string{id + ".example.com"}})
		w := send("POST", "/v1/projects", string(body), "projects.write", "create-"+environment)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
		p, err := a.Engine.Store.Project(id)
		if err != nil || p.AppID != "demo" || p.Environment != environment || p.ZeroDowntime != (environment == model.Production) {
			t.Fatal("incorrect target/mode", p, err)
		}
		if environment != model.Production && p.GreenPort != 0 {
			t.Fatal("non-production allocated a second instance")
		}
		for _, port := range []int{p.BluePort, p.GreenPort} {
			if port == 0 {
				continue
			}
			if ports[port] {
				t.Fatal("shared environment port", port)
			}
			ports[port] = true
		}
		if err := a.Engine.Initialize(); err != nil {
			t.Fatal(err)
		}
		var receipt map[string]string
		json.Unmarshal(w.Body.Bytes(), &receipt)
		j, _ := a.Engine.Store.Job(receipt["job_id"])
		if err := runtime.Compose(a.Engine.Config, p); err != nil {
			t.Fatal(err)
		}
		j.Status = "succeeded"
		p.State = "awaiting_release"
		if err := a.Engine.Store.Update(j, &p); err != nil {
			t.Fatal(err)
		}
		source := "services:\n  app:\n    image: ghcr.io/ziqx/demo@sha256:" + strings.Repeat("a", 64) + "\n    environment: {TOKEN: " + environment + "-secret}\n"
		deploy, _ := json.Marshal(map[string]string{"environment": environment, "compose_yaml": source})
		w = send("POST", "/v1/projects/"+id+"/deploy", string(deploy), "deploy.environment deploy.execute", "deploy-"+environment)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
		json.Unmarshal(w.Body.Bytes(), &receipt)
		j, _ = a.Engine.Store.Job(receipt["job_id"])
		if _, err := os.Stat(filepath.Join(a.Engine.Config.ProjectsRoot, id, "env", j.Input.Release.Environment+".env")); err != nil {
			t.Fatal("missing isolated environment artifact", err)
		}
	}
	body := `{"id":"duplicate","app_id":"demo","environment":"staging","template_id":"node","domains":["duplicate.example.com"]}`
	if w := send("POST", "/v1/projects", body, "projects.write", "duplicate"); w.Code != 409 || !strings.Contains(w.Body.String(), "APP_ENVIRONMENT_EXISTS") {
		t.Fatal("duplicate app/environment accepted", w.Code, w.Body.String())
	}
	// An app grouping never expands the key's explicit project authorization.
	a.Engine.Config.Keys[0].Projects = []string{"demo-development"}
	a.Auth, err = auth.New(a.Engine.Config)
	if err != nil {
		t.Fatal(err)
	}
	if w := send("GET", "/v1/projects/demo-production", "", "deploy.read", "deny-production-read"); w.Code != 403 {
		t.Fatal("app grouping granted production access", w.Code)
	}
	if w := send("POST", "/v1/projects/demo-production/deploy", `{"environment":"production","compose_yaml":"services: {}"}`, "deploy.execute", "deny-production-deploy"); w.Code != 403 {
		t.Fatal("development key could deploy production", w.Code)
	}
}

func TestEnvironmentRejectionsHaveNoSideEffects(t *testing.T) {
	for _, tc := range []struct {
		environment string
		zero        any
		secondary   int
		status      int
	}{
		{"", false, 0, 422}, {"dev", false, 0, 400}, {"staging", true, 0, 400}, {"development", true, 0, 400}, {"staging", false, 3002, 400},
	} {
		a, d, r, send := apiFixture(t)
		body, _ := json.Marshal(map[string]any{"id": "demo", "app_id": "demo", "environment": tc.environment, "template_id": "node", "domains": []string{"app.example.com"}, "zerodowntime": tc.zero, "secondary_port": tc.secondary})
		w := send("POST", "/v1/projects", string(body), "projects.write", "create")
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
		projects, _ := a.Engine.Store.Projects()
		if len(projects) != 0 {
			t.Fatal("invalid target stored")
		}
		if _, err := os.Stat(filepath.Join(a.Engine.Config.ProjectsRoot, "demo")); !os.IsNotExist(err) {
			t.Fatal("invalid target wrote project files")
		}
		if tc.secondary == 0 && d.calls+r.calls != 0 {
			t.Fatal("invalid environment reached adapters")
		}
	}
	for _, environment := range []string{"", "staging", "dev"} {
		a, d, r, send := apiFixture(t)
		seedComposeProject(t, a)
		body, _ := json.Marshal(map[string]string{"environment": environment, "compose_yaml": "services: {}"})
		w := send("POST", "/v1/projects/demo/deploy", string(body), "deploy.execute", "deploy")
		want := 409
		if environment == "" {
			want = 422
		}
		if environment == "dev" {
			want = 400
		}
		if w.Code != want || d.calls+r.calls != 0 {
			t.Fatal("wrong environment reached Compose", w.Code, w.Body.String())
		}
		if _, err := os.Stat(filepath.Join(a.Engine.Config.ProjectsRoot, "demo", "env")); !os.IsNotExist(err) {
			t.Fatal("wrong environment wrote secrets")
		}
	}
}
