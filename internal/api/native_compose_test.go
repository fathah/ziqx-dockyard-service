package api

import (
	"context"
	"encoding/json"
	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeComposeRequiresExplicitRootCapability(t *testing.T) {
	a, _, _, send := apiFixture(t)
	body := `{"id":"demo","app_id":"demo","environment":"production","domains":[]}`
	if w := send("POST", "/v1/projects", body, "projects.write", "no-admin"); w.Code != 403 || !strings.Contains(w.Body.String(), "COMPOSE_ACCESS_REQUIRED") {
		t.Fatal(w.Code, w.Body.String())
	}
	a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
	var err error
	a.Auth, err = auth.New(a.Engine.Config)
	if err != nil {
		t.Fatal(err)
	}
	if w := send("POST", "/v1/projects", body, "projects.write", "limited-admin"); w.Code != 403 {
		t.Fatal("project scoped key gained host access", w.Code)
	}
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	a.Engine.Config.Templates = nil
	a.Engine.Config.AllowedDomains = nil
	a.Auth, err = auth.New(a.Engine.Config)
	if err != nil {
		t.Fatal(err)
	}
	w := send("POST", "/v1/projects", body, "projects.write", "full-admin")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	p, err := a.Engine.Store.Project("demo")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != "compose" || p.ZeroDowntime || p.BluePort != 0 || p.Template != "" {
		t.Fatal("native project needs a template or route", p)
	}
	w = send("GET", "/v1/projects/demo/configuration", "", "deploy.environment", "empty-config")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"release_id":""`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// Merely possessing environment permission must not grant host-level secrets.
	a.Engine.Config.Keys[0].Scopes = []string{"deploy.environment"}
	a.Auth, err = auth.New(a.Engine.Config)
	if err != nil {
		t.Fatal(err)
	}
	w = send("GET", "/v1/projects/demo/configuration", "", "deploy.environment", "no-compose-grant")
	if w.Code != 403 || !strings.Contains(w.Body.String(), "COMPOSE_ACCESS_REQUIRED") {
		t.Fatal(w.Code, w.Body.String())
	}
}

type nativeRunner struct{}

func (nativeRunner) Run(context.Context, string, string, []string) (process.Result, error) {
	return process.Result{Output: []byte(`{"services":{"web":{"image":"nginx:alpine","environment":{"TOKEN":"private-test-token"}}}}`)}, nil
}

type nativePreparer struct {
	*dockerStub
	config config.Config
}

func (n nativePreparer) PrepareNative(ctx context.Context, p model.Project, source, env string) (model.Release, error) {
	return (runtime.Docker{Config: n.config, Runner: nativeRunner{}}).PrepareNative(ctx, p, source, env)
}
func TestNativeDeploymentQueuesRevisionsWithoutSecrets(t *testing.T) {
	a, d, _, send := apiFixture(t)
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
	a.Engine.Config.Templates = nil
	var err error
	a.Auth, err = auth.New(a.Engine.Config)
	if err != nil {
		t.Fatal(err)
	}
	a.Engine.Docker = nativePreparer{d, a.Engine.Config}
	w := send("POST", "/v1/projects", `{"id":"demo","app_id":"demo","environment":"production"}`, "projects.write", "new-compose")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response model.Job
	if json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatal("job response")
	}
	job, err := a.Engine.Store.Job(response.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Engine.Store.Project("demo")
	if err != nil {
		t.Fatal(err)
	}
	p.State = "awaiting_release"
	job.Status = "succeeded"
	if err := a.Engine.Store.Update(job, &p); err != nil {
		t.Fatal(err)
	}
	body := `{"environment":"production","compose_yaml":"services:\n  web:\n    image: nginx:alpine\n","env_file":"TOKEN=private-test-token\n","expected_release_id":""}`
	if w := send("POST", "/v1/projects/demo/deploy", body, "deploy.execute", "env-denied"); w.Code != 403 {
		t.Fatal("dotenv write requires scope", w.Code)
	}
	w = send("POST", "/v1/projects/demo/deploy", body, "deploy.environment deploy.execute", "native-deploy")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatal("deploy response")
	}
	job, err = a.Engine.Store.Job(response.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(job)
	if strings.Contains(string(b), "private-test-token") || job.Input.Release == nil || job.Input.Release.Compose == "" {
		t.Fatal("job leaked input or omitted revision")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Engine.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err = a.Engine.Store.Job(response.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == "succeeded" || job.Status == "failed" || job.Status == "recovery_required" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if job.Status != "succeeded" {
		t.Fatal("native lifecycle failed", job.Status, job.Error)
	}
	dotenv, err := os.ReadFile(filepath.Join(a.Engine.Config.ProjectsRoot, "demo", ".env"))
	if err != nil || string(dotenv) != "TOKEN=private-test-token\n" {
		t.Fatal("dotenv mirror missing", err)
	}
	project, err := a.Engine.Store.Project("demo")
	if err != nil || project.State != "running" || project.BluePort != 0 {
		t.Fatal("invalid native state", err, project)
	}

	// Configuration reads expose secrets only to explicitly enabled admins.
	for _, scopes := range []string{"deploy.read", "deploy.execute"} {
		w = send("GET", "/v1/projects/demo/configuration", "", scopes, "read-denied")
		if w.Code != 403 || strings.Contains(w.Body.String(), "private-test-token") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w = send("GET", "/v1/projects/demo/configuration", "", "deploy.environment", "read-config")
	var editable struct {
		Release string `json:"release_id"`
		Compose string `json:"compose_yaml"`
		Env     string `json:"env_file"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &editable) != nil || editable.Env != "TOKEN=private-test-token\n" || editable.Compose != "services:\n  web:\n    image: nginx:alpine\n" || editable.Release != job.Input.Release.ID || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	// Editing an older release must not silently overwrite a newer deployment.
	w = send("POST", "/v1/projects/demo/deploy", body, "deploy.environment deploy.execute", "stale-first-deploy")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "CONFIGURATION_CHANGED") {
		t.Fatal("a stale first-deployment editor overwrote a release", w.Code, w.Body.String())
	}
	stale, _ := json.Marshal(map[string]any{"environment": "production", "compose_yaml": editable.Compose, "env_file": "", "expected_release_id": "rel-old"})
	w = send("POST", "/v1/projects/demo/deploy", string(stale), "deploy.environment deploy.execute", "stale-edit")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "CONFIGURATION_CHANGED") {
		t.Fatal(w.Code, w.Body.String())
	}
	// An intentionally empty file clears saved values instead of reusing them.
	update, _ := json.Marshal(map[string]any{"environment": "production", "compose_yaml": editable.Compose, "env_file": "", "expected_release_id": editable.Release})
	w = send("POST", "/v1/projects/demo/deploy", string(update), "deploy.environment deploy.execute", "edit-deploy")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var edited model.Job
	json.Unmarshal(w.Body.Bytes(), &edited)
	edited, _ = a.Engine.Store.Job(edited.ID)
	if edited.Input.Release == nil {
		t.Fatal("missing edited release")
	}
	contents, err := os.ReadFile(filepath.Join(a.Engine.Config.ProjectsRoot, "demo", "env", edited.Input.Release.Environment+".env"))
	if err != nil || len(contents) != 0 {
		t.Fatal("empty environment was not applied", err)
	}
	// Normal project metadata never contains editor contents.
	w = send("GET", "/v1/projects", "", "deploy.read", "metadata")
	if strings.Contains(w.Body.String(), "private-test-token") {
		t.Fatal("secret in metadata")
	}

	services, err := a.Engine.Store.ComposeServices("demo", job.Input.Release.Compose)
	if err != nil || services["web"].Image != "nginx:alpine" {
		t.Fatal("native service inventory", err)
	}
}

func TestNativeDeployCanCorrectRouteService(t *testing.T) {
	a, d, _, send := apiFixture(t)
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
	a.Engine.Config.Templates = nil
	a.Auth, _ = auth.New(a.Engine.Config)
	a.Engine.Docker = nativePreparer{d, a.Engine.Config}
	w := send("POST", "/v1/projects", `{"id":"demo","app_id":"demo","environment":"production","domains":["app.example.com"],"route_service":"app","route_port":80}`, "projects.write sites.write", "create-routed")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created model.Job
	json.Unmarshal(w.Body.Bytes(), &created)
	job, _ := a.Engine.Store.Job(created.ID)
	p, _ := a.Engine.Store.Project("demo")
	p.State, job.Status = "awaiting_release", "succeeded"
	if err := a.Engine.Store.Update(job, &p); err != nil {
		t.Fatal(err)
	}
	compose := `"compose_yaml":"services:\n  api:\n    image: nginx:alpine\n","env_file":"","expected_release_id":""`
	// The test runner's resolved Compose always contains one service: web.
	if w := send("POST", "/v1/projects/demo/deploy", `{"environment":"production",`+compose+`}`, "deploy.environment deploy.execute", "route-missing"); w.Code != 409 || !strings.Contains(w.Body.String(), "COMPOSE_ROUTE_SERVICE_MISSING") {
		t.Fatal("expected the missing web service to be rejected", w.Code, w.Body.String())
	}
	if w := send("POST", "/v1/projects/demo/deploy", `{"environment":"production",`+compose+`,"route_service":"web"}`, "deploy.environment deploy.execute", "route-half"); w.Code != 400 {
		t.Fatal("service without port must be rejected", w.Code)
	}
	w = send("POST", "/v1/projects/demo/deploy", `{"environment":"production",`+compose+`,"route_service":"web","route_port":3000}`, "deploy.environment deploy.execute", "route-fixed")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var deployed model.Job
	json.Unmarshal(w.Body.Bytes(), &deployed)
	job, _ = a.Engine.Store.Job(deployed.ID)
	if job.Input.Project == nil || job.Input.Project.RouteService != "web" || job.Input.Project.RoutePort != 3000 {
		t.Fatal("corrected route not carried by the job", job.Input.Project)
	}
}

func TestDraftSaveReadAndDeployKeepsFiles(t *testing.T) {
	a, d, _, send := apiFixture(t)
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
	a.Engine.Config.Templates = nil
	a.Auth, _ = auth.New(a.Engine.Config)
	a.Engine.Docker = nativePreparer{d, a.Engine.Config}
	if w := send("POST", "/v1/projects", `{"id":"demo","app_id":"demo","environment":"production"}`, "projects.write", "create-draft"); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	draft := `{"compose_yaml":"services:\n  web: {image: nginx}\n","env_file":"PASSWORD=\n"}`
	if w := send("POST", "/v1/projects/demo/draft", draft, "deploy.read", "draft-denied"); w.Code != 403 {
		t.Fatal("draft needs the environment scope", w.Code)
	}
	if w := send("POST", "/v1/projects/demo/draft", draft, "deploy.environment", "draft-save"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := send("GET", "/v1/projects/demo/configuration", "", "deploy.environment", "draft-read")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"draft":true`) || !strings.Contains(w.Body.String(), "PASSWORD=") || !strings.Contains(w.Body.String(), "nginx") {
		t.Fatal(w.Code, w.Body.String())
	}
	// Omitting env_file keeps the saved .env.
	if w := send("POST", "/v1/projects/demo/draft", `{"compose_yaml":"services: {}\n"}`, "deploy.environment", "draft-compose-only"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send("GET", "/v1/projects/demo/configuration", "", "deploy.environment", "draft-read-2"); !strings.Contains(w.Body.String(), "PASSWORD=") || !strings.Contains(w.Body.String(), "services: {}") {
		t.Fatal(w.Body.String())
	}
}

func TestNativeDeployConnectsDomains(t *testing.T) {
	a, d, _, send := apiFixture(t)
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
	a.Engine.Config.Templates = nil
	a.Auth, _ = auth.New(a.Engine.Config)
	a.Engine.Docker = nativePreparer{d, a.Engine.Config}
	w := send("POST", "/v1/projects", `{"id":"demo","app_id":"demo","environment":"production"}`, "projects.write", "create-plain")
	var created model.Job
	json.Unmarshal(w.Body.Bytes(), &created)
	job, _ := a.Engine.Store.Job(created.ID)
	p, _ := a.Engine.Store.Project("demo")
	p.State, job.Status = "awaiting_release", "succeeded"
	if err := a.Engine.Store.Update(job, &p); err != nil {
		t.Fatal(err)
	}
	compose := `"environment":"production","compose_yaml":"services:\n  web:\n    image: nginx:alpine\n","env_file":"","expected_release_id":""`
	if w := send("POST", "/v1/projects/demo/deploy", `{`+compose+`,"domains":["app.example.com"]}`, "deploy.environment deploy.execute", "domain-no-service"); w.Code != 400 || !strings.Contains(w.Body.String(), "COMPOSE_ROUTE_INVALID") {
		t.Fatal("a domain needs a web service", w.Code, w.Body.String())
	}
	w = send("POST", "/v1/projects/demo/deploy", `{`+compose+`,"domains":["app.example.com"],"route_service":"web","route_port":80}`, "deploy.environment deploy.execute", "domain-connect")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var deployed model.Job
	json.Unmarshal(w.Body.Bytes(), &deployed)
	job, _ = a.Engine.Store.Job(deployed.ID)
	next := job.Input.Project
	if next == nil || len(next.Domains) != 1 || next.RouteService != "web" || next.RoutePort != 80 || next.BluePort < a.Engine.Config.PortMin {
		t.Fatal("domain connection not carried by the job", next)
	}
}
