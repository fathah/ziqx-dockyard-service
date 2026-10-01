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
