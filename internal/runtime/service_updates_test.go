package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

type serviceRunner struct {
	t            *testing.T
	doc          string
	calls        [][]string
	containers   map[string]Container
	image        string
	imageStorage bool
}

func (r *serviceRunner) Run(_ context.Context, _ string, _ string, args []string) (process.Result, error) {
	r.calls = append(r.calls, append([]string{}, args...))
	output := func(v any) (process.Result, error) { b, _ := json.Marshal(v); return process.Result{Output: b}, nil }
	if args[0] == "inspect" {
		for _, c := range r.containers {
			if c.ID == args[len(args)-1] {
				return output([]Container{c})
			}
		}
		return output([]Container{})
	}
	if args[0] == "image" {
		volumes := map[string]any{}
		if r.imageStorage {
			volumes["/data"] = map[string]any{}
		}
		return output([]any{map[string]any{"Id": r.image, "Config": map[string]any{"Volumes": volumes}}})
	}
	if args[0] == "network" {
		return process.Result{}, nil
	}
	if args[0] != "compose" || len(args) < 10 {
		r.t.Fatal("unexpected command", args)
	}
	switch args[9] {
	case "--profile", "config":
		return process.Result{Output: []byte(r.doc)}, nil
	case "pull":
		if len(args) != 11 {
			r.t.Fatal("pull includes dependencies", args)
		}
	case "ps":
		name := args[len(args)-1]
		if c, ok := r.containers[name]; ok {
			return process.Result{Output: []byte(c.ID)}, nil
		}
		return process.Result{}, nil
	case "up":
		if !strings.Contains(strings.Join(args, " "), "--no-deps") {
			r.t.Fatal("update may recreate dependencies", args)
		}
	case "stop":
		name := args[len(args)-1]
		c := r.containers[name]
		c.State.Running = false
		r.containers[name] = c
	default:
		r.t.Fatal("unexpected mutation", args)
	}
	return process.Result{}, nil
}

func runtimeServiceFixture(t *testing.T) (Docker, model.Project, *serviceRunner) {
	t.Helper()
	c := config.Config{ServerID: "vps-01", ProjectsRoot: t.TempDir(), DockerBinary: "docker", HealthSeconds: 30}
	p := model.Project{ID: "demo", AppID: "demo", Mode: "compose", Environment: model.Production, State: "running", Active: "blue", RouteService: "web", RoutePort: 3000, BluePort: 3001, Domains: []string{"app.example.com"}, Slots: map[string]model.Release{}}
	if err := Compose(c, p); err != nil {
		t.Fatal(err)
	}
	runner := &serviceRunner{t: t, image: "sha256:" + strings.Repeat("2", 64), containers: map[string]Container{}, doc: `{"services":{"web":{"image":"example/app:latest","container_name":"web-fixed","environment":{"TOKEN":"keep$secret"},"depends_on":{"postgres":{"condition":"service_healthy"}},"healthcheck":{"test":["CMD","true"]},"ports":[{"target":3000,"published":"8080","protocol":"tcp"}],"networks":{"default":{"aliases":["api"]}}},"postgres":{"image":"postgres:17","container_name":"database-fixed","volumes":[{"type":"volume","source":"database","target":"/var/lib/postgresql/data"}],"networks":{"default":{}}}},"volumes":{"database":{"name":"original_database"}},"networks":{"default":{"name":"original_default"}}}`}
	d := Docker{Config: c, Runner: runner}
	release, err := d.PrepareNative(context.Background(), p, "services: supplied", "TOKEN=keep$secret\n")
	if err != nil {
		t.Fatal(err)
	}
	release.ID = "rel-original"
	p.Slots["blue"] = release
	p.Releases = []model.Release{release}
	if err = Binding(c, p, "blue", release); err != nil {
		t.Fatal(err)
	}
	var container Container
	raw := `{"Id":"` + strings.Repeat("a", 64) + `","Name":"/web-fixed","Image":"sha256:` + strings.Repeat("1", 64) + `","Config":{"Labels":{"com.docker.compose.project":"dy-vps-01-demo-blue","com.docker.compose.service":"web","io.ziqx.dockyard.server":"vps-01","io.ziqx.dockyard.project":"demo"}},"State":{"Running":true,"Health":{"Status":"healthy"}},"NetworkSettings":{"Networks":{"original_default":{"NetworkID":"` + strings.Repeat("d", 64) + `","Aliases":["web-fixed","web","api"]}},"Ports":{"3000/tcp":[{"HostIP":"127.0.0.1","HostPort":"3001"}]}}}`
	if json.Unmarshal([]byte(raw), &container) != nil {
		t.Fatal("bad fixture")
	}
	runner.containers["web"] = container
	runner.calls = nil
	return d, p, runner
}

func TestServiceCandidateKeepsDatabaseAndUsesExistingNetwork(t *testing.T) {
	d, p, runner := runtimeServiceFixture(t)
	options, err := ServiceUpdateOptions(d.Config, p)
	if err != nil || !options["web"].Seamless || options["postgres"].Seamless || !options["postgres"].Updatable {
		t.Fatal(options, err)
	}
	if strings.Join(options["web"].DependsOn, ",") != "postgres" || options["postgres"].DependsOn == nil || len(options["postgres"].DependsOn) != 0 {
		t.Fatal("dependency metadata must distinguish a dependency from none", options)
	}
	plan, err := d.PlanServiceUpdate(context.Background(), p, "web", "seamless", 3000, 3010, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.NetworkAliases["original_default"], ",") != "api,web" {
		t.Fatal("lost logical aliases or promoted fixed container name", plan)
	}
	if changed, err := d.PullServiceUpdate(context.Background(), p, &plan); err != nil || !changed {
		t.Fatal(changed, err)
	}
	_, b, err := nativeRelease(d.Config, p, plan.Instance.Release)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(b, &doc)
	svc := object(object(doc["services"])[plan.Instance.Name])
	if len(object(doc["services"])) != 1 || svc["image"] != runner.image || svc["container_name"] != nil || svc["depends_on"] != nil || object(svc["environment"])["TOKEN"] != "keep$$secret" {
		t.Fatal("unsafe candidate manifest", doc)
	}
	net := object(object(doc["networks"])["connection0"])
	if net["external"] != true || net["name"] != "original_default" || len(array(object(object(svc["networks"])["connection0"])["aliases"])) != 0 {
		t.Fatal("candidate would take traffic early or change network", doc)
	}
	if err = d.StartServiceUpdate(context.Background(), p, plan); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.calls {
		if len(call) > 9 && (call[9] == "up" || call[9] == "pull" || call[9] == "stop") && call[len(call)-1] == "postgres" {
			t.Fatal("touched database", call)
		}
	}
	if original, _, err := nativeRelease(d.Config, p, p.Slots["blue"]); err != nil || original == composeRevisionPath(d.Config, p.ID, plan.Instance.Release.Compose) {
		t.Fatal("overwrote full source", original, err)
	}
}

func TestServiceUpdateRejectsStorageAndChangedNetworksBeforeMutation(t *testing.T) {
	for _, scenario := range []string{"writable-mount", "image-volume", "network-replaced", "container-replaced", "same-image", "wrong-owner", "missing-health"} {
		t.Run(scenario, func(t *testing.T) {
			d, p, r := runtimeServiceFixture(t)
			c := r.containers["web"]
			switch scenario {
			case "writable-mount":
				json.Unmarshal([]byte(`[{"Type":"volume","RW":true}]`), &c.Mounts)
			case "wrong-owner":
				c.Config.Labels["io.ziqx.dockyard.project"] = "foreign"
			case "missing-health":
				c.State.Health = nil
			}
			r.containers["web"] = c
			plan, err := d.PlanServiceUpdate(context.Background(), p, "web", "seamless", 3000, 3010, "")
			if scenario == "writable-mount" || scenario == "wrong-owner" || scenario == "missing-health" {
				if err == nil {
					t.Fatal("unsafe review accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "container-replaced" {
				c.ID = strings.Repeat("f", 64)
				r.containers["web"] = c
				if d.VerifyServiceUpdate(context.Background(), p, plan) == nil || d.StopServiceInstance(context.Background(), p, *plan.Previous) == nil {
					t.Fatal("stopped a container outside the review")
				}
				return
			}
			if scenario == "network-replaced" {
				connection := c.NetworkSettings.Networks["original_default"]
				connection.NetworkID = strings.Repeat("e", 64)
				c.NetworkSettings.Networks["original_default"] = connection
				r.containers["web"] = c
				if d.VerifyServiceUpdate(context.Background(), p, plan) == nil {
					t.Fatal("same-name network substitution accepted")
				}
				return
			}
			if scenario == "image-volume" {
				r.imageStorage = true
			} else {
				r.image = c.Image
			}
			changed, err := d.PullServiceUpdate(context.Background(), p, &plan)
			if changed || (scenario == "image-volume" && err == nil) || (scenario == "same-image" && err != nil) {
				t.Fatal(changed, err)
			}
			for _, call := range r.calls {
				if len(call) > 9 && call[9] == "up" {
					t.Fatal("started rejected image")
				}
			}
		})
	}
}

func TestRestartOfServiceVersionPreservesAliasAndWholeProjectLifecycleSkipsOriginalApp(t *testing.T) {
	d, p, r := runtimeServiceFixture(t)
	plan, err := d.PlanServiceUpdate(context.Background(), p, "web", "seamless", 3000, 3010, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.PullServiceUpdate(context.Background(), p, &plan); err != nil {
		t.Fatal(err)
	}
	c := r.containers["web"]
	c.ID = strings.Repeat("c", 64)
	c.Config.Labels = map[string]string{"com.docker.compose.project": d.composeName(p, p.Active), "com.docker.compose.service": plan.Instance.Name, "io.ziqx.dockyard.server": d.Config.ServerID, "io.ziqx.dockyard.project": p.ID}
	c.Image = r.image
	r.containers[plan.Instance.Name] = c
	p.ServiceInstances = map[string]model.ServiceInstance{"web": plan.Instance}
	restart, err := d.PlanServiceUpdate(context.Background(), p, "web", "restart", 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	r.image = "sha256:" + strings.Repeat("3", 64)
	if _, err = d.PullServiceUpdate(context.Background(), p, &restart); err != nil {
		t.Fatal(err)
	}
	_, b, _ := nativeRelease(d.Config, p, restart.Instance.Release)
	var doc map[string]any
	json.Unmarshal(b, &doc)
	svc := object(object(doc["services"])[restart.Instance.Name])
	aliases := object(object(svc["networks"])["connection0"])["aliases"]
	if len(array(aliases)) != 2 {
		t.Fatal("restart lost service DNS aliases", string(b))
	}
	r.calls = nil
	if err = d.Start(context.Background(), p, p.Active); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 || r.calls[0][len(r.calls[0])-1] != "postgres" || r.calls[1][len(r.calls[1])-1] != plan.Instance.Name {
		t.Fatal("whole lifecycle resurrected stale app", r.calls)
	}
}

func TestServiceCandidateUsesRealComposeLoader(t *testing.T) {
	if os.Getenv("DOCKYARD_COMPOSE_TEST") != "1" {
		t.Skip("set DOCKYARD_COMPOSE_TEST=1; no Docker daemon or containers needed")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	d, p, _ := runtimeServiceFixture(t)
	plan, err := d.PlanServiceUpdate(context.Background(), p, "web", "seamless", 3000, 3010, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.PullServiceUpdate(context.Background(), p, &plan); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	d.Config.DockerBinary = binary
	d.Runner = process.Exec{Timeout: 20 * time.Second, Limit: 2 << 20, DockerConfig: filepath.Join(home, ".docker")}
	result, err := d.serviceCommand(context.Background(), p, plan.Instance, "config", "--format", "json")
	if err != nil {
		t.Fatalf("Compose rejected generated candidate: %s", result.Stderr)
	}
	var doc map[string]any
	json.Unmarshal([]byte(strings.ReplaceAll(string(result.Output), "$$", "$")), &doc)
	svc := object(object(doc["services"])[plan.Instance.Name])
	if len(object(doc["services"])) != 1 || svc["image"] != plan.Instance.Release.Image || object(svc["environment"])["TOKEN"] != "keep$secret" {
		t.Fatal("round trip changed candidate", string(result.Output))
	}
}

func TestFullRollbackRebindsAdoptedRouteWithoutReevaluatingSecrets(t *testing.T) {
	d, p, _ := runtimeServiceFixture(t)
	r := p.Slots[p.Active]
	p.ServiceMode = true
	p.BluePort = 3010
	rebound, err := RebindNativeRoute(d.Config, p, r)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := nativeRelease(d.Config, p, rebound)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(b, &doc)
	web := object(object(doc["services"])["web"])
	if len(object(doc["services"])) != 2 || object(web["environment"])["TOKEN"] != "keep$$secret" || object(array(web["ports"])[0])["published"] != "${DOCKYARD_PORT}" || rebound.Environment != r.Environment {
		t.Fatal("rollback changed full source or secrets", string(b))
	}
	if source, err := ComposeMirror(d.Config, p, rebound); err != nil || string(source) != "services: supplied" {
		t.Fatal("lost editable source", err)
	}
}
