package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
)

type dependencyRunner struct{ source string }

type runtimeInventoryStub struct {
	*dockerStub
	fail  bool
	reads int
}

func (s *runtimeInventoryStub) ServiceRuntime(_ context.Context, p model.Project) (map[string]model.ServiceRuntime, error) {
	s.reads++
	if s.fail || p.ID != "demo" || p.Active != "blue" {
		return nil, model.Fail("DOCKER_UNAVAILABLE")
	}
	return map[string]model.ServiceRuntime{"web": {Containers: []model.ServiceContainer{{ID: strings.Repeat("a", 64), Name: "live-web-container"}}, HostPorts: []int{32782}}}, nil
}

func (r dependencyRunner) Run(context.Context, string, string, []string) (process.Result, error) {
	return process.Result{Output: []byte(r.source)}, nil
}

func TestServiceInventoryReportsDependenciesWithoutSecretsOrDockerQueries(t *testing.T) {
	for _, dependencies := range []string{`{"postgres":{"condition":"service_healthy"},"cache":{"condition":"service_started"}}`, `["postgres","cache"]`} {
		a, docker, routes, send := apiFixture(t)
		p := model.Project{ID: "demo", AppID: "demo", Mode: "compose", Environment: model.Production, State: "running", Active: "blue", Slots: map[string]model.Release{}}
		if err := runtime.Compose(a.Engine.Config, p); err != nil {
			t.Fatal(err)
		}
		source := `{"services":{"web":{"image":"nginx","depends_on":` + dependencies + `,"environment":{"TOKEN":"private-dependency-test"}},"postgres":{"image":"postgres:17"},"cache":{"image":"redis:alpine"}}}`
		preparer := runtime.Docker{Config: a.Engine.Config, Runner: dependencyRunner{source}}
		release, err := preparer.PrepareNative(context.Background(), p, "services: supplied", "TOKEN=private-dependency-test\n")
		if err != nil {
			t.Fatal(err)
		}
		release.ID = "rel-original"
		p.Slots["blue"] = release
		p.Slots["green"] = release
		p.Releases = []model.Release{release}
		job := model.Job{ID: "seed", ProjectID: p.ID, Action: "project_create", Status: "succeeded"}
		if err := a.Engine.Store.Accept(job, &p, "seed", "seed", 10, 10); err != nil {
			t.Fatal(err)
		}
		if err := a.Engine.Store.SaveCompose(p.ID, release.Compose, map[string]model.ServiceSpec{"web": {Image: "nginx"}, "postgres": {Image: "postgres:17"}, "cache": {Image: "redis:alpine"}}); err != nil {
			t.Fatal(err)
		}
		w := send("GET", "/v1/projects/demo/services", "", "deploy.read", "service-dependencies")
		var body struct{ Services []model.ServiceInfo }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Services) != 6 || strings.Contains(w.Body.String(), "private-dependency-test") || strings.Contains(w.Body.String(), "environment\"") {
			t.Fatal("unsafe or incomplete service inventory", w.Code, w.Body.String())
		}
		for _, item := range body.Services {
			if item.Slot != p.Active {
				if item.DependsOn != nil {
					t.Fatal("inactive slot was assigned active Compose metadata", item)
				}
			} else if item.Name == "web" {
				if strings.Join(item.DependsOn, ",") != "cache,postgres" {
					t.Fatal("dependency order or names lost", item)
				}
			} else if item.DependsOn == nil || len(item.DependsOn) != 0 {
				t.Fatal("no dependencies must be represented by an empty array", item)
			}
		}
		if docker.calls != 0 || routes.calls != 0 {
			t.Fatal("dependency metadata queried live runtime adapters")
		}
		detector := &runtimeInventoryStub{dockerStub: docker}
		a.Engine.Docker = detector
		for _, failed := range []bool{false, true} {
			detector.fail = failed
			w = send("GET", "/v1/projects/demo/services", "", "deploy.read", "service-containers")
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
				t.Fatal("runtime lookup must preserve recorded inventory", w.Code, w.Body.String())
			}
			for _, item := range body.Services {
				if failed || item.Slot != p.Active {
					if item.Containers != nil || item.HostPorts != nil {
						t.Fatal("unavailable or inactive identity must stay unknown", item)
					}
				} else if item.Name == "web" {
					if len(item.Containers) != 1 || item.Containers[0].Name != "live-web-container" || item.Containers[0].ID != strings.Repeat("a", 64) || len(item.HostPorts) != 1 || item.HostPorts[0] != 32782 {
						t.Fatal("live identity or binding lost", item)
					}
				} else if item.Containers == nil || len(item.Containers) != 0 {
					t.Fatal("missing running container must be an empty array", item)
				}
			}
		}
		if detector.reads != 2 {
			t.Fatal("container metadata made extra runtime reads", detector.reads)
		}
	}
}
