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
	body := `{"environment":"production","compose_yaml":"services:\n  web:\n    image: nginx:alpine\n","env_file":"TOKEN=private-test-token\n"}`
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

	services, err := a.Engine.Store.ComposeServices("demo", job.Input.Release.Compose)
	if err != nil || services["web"].Image != "nginx:alpine" {
		t.Fatal("native service inventory", err)
	}
}
