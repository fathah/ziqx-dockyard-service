package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
)

type localDockerRunner struct{}

func (localDockerRunner) Run(ctx context.Context, binary, dir string, args []string) (process.Result, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	err := cmd.Run()
	return process.Result{Output: out.Bytes(), Stderr: diagnostic.Bytes()}, err
}

// Uses an already-cached image, publishes no ports and has no network access.
func TestHAProxyRuntimeControlRoundTrip(t *testing.T) {
	if os.Getenv("DOCKYARD_HAPROXY_TEST") != "1" {
		t.Skip("set DOCKYARD_HAPROXY_TEST=1 with cached haproxy:3.2.23-alpine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		b, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		return string(b), err
	}
	path := filepath.Join(t.TempDir(), "haproxy.cfg")
	config := `global
  stats socket ipv4@127.0.0.1:9999 level admin
defaults
  mode http
  timeout connect 1s
  timeout client 1s
  timeout server 1s
frontend app
  bind 127.0.0.1:8080
  default_backend api_replicas
frontend healthy_one
  bind 127.0.0.1:8081
  http-request return status 200
frontend healthy_two
  bind 127.0.0.1:8082
  http-request return status 200
backend api_replicas
  option httpchk GET /
  server api1 127.0.0.1:8081 check inter 100ms rise 1 fall 1
  server api2 127.0.0.1:8082 check inter 100ms rise 1 fall 1
`
	if err := os.WriteFile(path, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	name := state.NewID("dockyard-proxy-test-")
	defer exec.Command("docker", "rm", "--force", name).Run()
	if output, err := run("run", "--detach", "--rm", "--pull", "never", "--network", "none", "--name", name, "--mount", "type=bind,src="+path+",dst=/usr/local/etc/haproxy/haproxy.cfg,readonly", "haproxy:3.2.23-alpine"); err != nil {
		t.Fatal(err, output)
	}
	query := func(command string) (string, error) {
		return run("exec", name, "sh", "-c", `printf '%s\n' "$1" | nc -w 2 127.0.0.1 "$2"`, "dockyard-haproxy", command, "9999")
	}
	control := func(command string) string {
		output, err := query(command)
		if err != nil {
			t.Fatal(err, output)
		}
		return output
	}
	addresses := map[string]bool{"127.0.0.1:8081": true, "127.0.0.1:8082": true}
	for {
		output, queryErr := query("show stat")
		servers, err := parseProxyStats(output, "api_replicas")
		if queryErr == nil && err == nil && proxyGroupReady(servers, addresses) {
			break
		}
		select {
		case <-ctx.Done():
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Fatal("real HAProxy never reported both servers UP", err, queryErr, string(logs))
		case <-time.After(100 * time.Millisecond):
		}
	}
	if response := control("set server api_replicas/api1 state drain"); strings.TrimSpace(response) != "" {
		t.Fatal("HAProxy rejected drain", response)
	}
	servers, err := parseProxyStats(control("show stat"), "api_replicas")
	if err != nil || len(servers) != 2 || servers[0].Status != "DRAIN" || servers[1].Status != "UP" || servers[0].Sessions != 0 {
		t.Fatal("real HAProxy stats differ from rollout assumptions", servers, err)
	}
	if response := control("set server api_replicas/api1 state ready"); strings.TrimSpace(response) != "" {
		t.Fatal("HAProxy rejected rollback", response)
	}
	servers, err = parseProxyStats(control("show stat"), "api_replicas")
	if err != nil || !proxyGroupReady(servers, addresses) {
		t.Fatal("real HAProxy did not restore both servers", servers, err)
	}
}

// Exercises Compose replicas, isolated candidate aliases, Docker DNS discovery,
// actual per-replica health, and HAProxy retirement together without host ports.
func TestHAProxyReplicatedBackendRolloutRoundTrip(t *testing.T) {
	if os.Getenv("DOCKYARD_HAPROXY_TEST") != "1" {
		t.Skip("set DOCKYARD_HAPROXY_TEST=1 with cached haproxy:3.2.23-alpine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	c := config.Config{ServerID: "replica-test", ProjectsRoot: t.TempDir(), DockerBinary: binary, HealthSeconds: 20}
	p := model.Project{ID: state.NewID("proxy-test-"), AppID: "proxy-test", Mode: "compose", Environment: model.Production, State: "running", Active: "blue", Slots: map[string]model.Release{}}
	if err := Compose(c, p); err != nil {
		t.Fatal(err)
	}
	d := Docker{Config: c, Runner: localDockerRunner{}}
	write := func(name, content string) string {
		path := filepath.Join(projectDir(c, p), name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	appConfig := write("app.cfg", "defaults\n  mode http\n  timeout connect 1s\n  timeout client 1s\n  timeout server 1s\nfrontend app\n  bind :8080\n  http-request return status 200\n")
	proxyConfig := write("proxy.cfg", `global
  stats socket ipv4@127.0.0.1:9999 level admin
defaults
  mode http
  timeout connect 1s
  timeout client 1s
  timeout server 1s
resolvers docker
  nameserver docker 127.0.0.11:53
  hold valid 1s
  hold obsolete 1s
  timeout resolve 1s
  timeout retry 1s
frontend app
  bind :8080
  default_backend api_replicas
backend api_replicas
  option httpchk GET /health
  server-template api 1-16 api:8080 check inter 200ms rise 1 fall 1 resolvers docker resolve-prefer ipv4 init-addr none
`)
	source := fmt.Sprintf(`services:
  api:
    image: haproxy:3.2.23-alpine
    configs: [{source: app, target: /usr/local/etc/haproxy/haproxy.cfg}]
    deploy: {replicas: 2}
    labels:
      io.ziqx.dockyard.haproxy.service: loadbalancer
      io.ziqx.dockyard.haproxy.backend: api_replicas
      io.ziqx.dockyard.haproxy.port: "8080"
    healthcheck:
      test: [CMD, wget, -q, -O, /dev/null, http://127.0.0.1:8080/health]
      interval: 1s
      timeout: 1s
      retries: 10
  loadbalancer:
    image: haproxy:3.2.23-alpine
    configs: [{source: proxy, target: /usr/local/etc/haproxy/haproxy.cfg}]
    healthcheck:
      test: [CMD, wget, -q, -O, /dev/null, http://127.0.0.1:8080/health]
      interval: 1s
      timeout: 1s
      retries: 10
configs:
  app: {file: %q}
  proxy: {file: %q}
`, appConfig, proxyConfig)
	release, err := d.PrepareNative(ctx, p, source, "")
	if err != nil {
		t.Fatal("prepare", err)
	}
	release.ID = "integration"
	p.Slots[p.Active], p.Releases = release, []model.Release{release}
	manifest, _, _ := nativeRelease(c, p, release)
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		output, err := exec.CommandContext(cleanup, binary, "compose", "--project-name", d.composeName(p, p.Active), "--file", manifest, "down", "--remove-orphans", "--volumes").CombinedOutput()
		if err != nil {
			t.Errorf("could not remove isolated rollout test: %v %s", err, output)
		}
	}()
	if err := Binding(c, p, p.Active, release); err != nil {
		t.Fatal(err)
	}
	if err := d.Start(ctx, p, p.Active); err != nil {
		logs, _ := d.command(ctx, p, p.Active, "logs", "--no-color", "--tail", "10")
		t.Fatal("start", err, string(logs.Output), string(logs.Stderr))
	}
	plan, err := d.PlanServiceUpdate(ctx, p, "api", "seamless", 8080, 0, "")
	if err != nil {
		t.Fatal("plan", err)
	}
	// Send real requests through the existing proxy throughout replacement and
	// retirement. State assertions alone cannot prove the public path stays up.
	trafficCtx, stopTraffic := context.WithCancel(ctx)
	trafficDone := make(chan struct{})
	var trafficErr error
	requests := 0
	go func() {
		defer close(trafficDone)
		for {
			output, err := exec.CommandContext(trafficCtx, binary, "exec", plan.Proxy.Instance.ContainerID,
				"wget", "-q", "-T", "2", "-O", "/dev/null", "http://127.0.0.1:8080/health").CombinedOutput()
			if trafficCtx.Err() != nil {
				return
			}
			if err != nil {
				trafficErr = fmt.Errorf("request %d failed: %w: %s", requests+1, err, output)
				return
			}
			requests++
			select {
			case <-trafficCtx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	defer func() {
		stopTraffic()
		<-trafficDone
	}()
	// Exercise replacement even though both groups use the cached test image.
	plan.SourceRelease = &release
	image, err := exec.CommandContext(ctx, binary, "image", "inspect", "haproxy:3.2.23-alpine", "--format", "{{.Id}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	plan.PreparedImage = strings.TrimSpace(string(image))
	if changed, err := d.PullServiceUpdate(ctx, p, &plan); err != nil || !changed {
		t.Fatal("prepare candidate", changed, err)
	}
	if err := d.StartServiceUpdate(ctx, p, plan); err != nil {
		t.Fatal("start candidates", err)
	}
	if err := d.ResolveServiceRouteBindings(ctx, &p, &plan); err != nil {
		t.Fatal("resolve", err)
	}
	if err := d.HealthyServiceUpdate(ctx, p, plan); err != nil {
		t.Fatal("readiness", err)
	}
	if err := d.PromoteServiceUpdate(ctx, p, plan); err != nil {
		t.Fatal("aliases", err)
	}
	if err := d.ResolveServiceRouteBindings(ctx, &p, &plan); err != nil {
		t.Fatal("resolve promoted", err)
	}
	if err := d.SwitchServiceProxy(ctx, p, plan, false); err != nil {
		t.Fatal("proxy switch", err)
	}
	if err := d.DrainedServiceProxy(ctx, p, plan, false); err != nil {
		t.Fatal("drain", err)
	}
	if err := d.StopServiceInstance(ctx, p, *plan.Previous); err != nil {
		t.Fatal("retire", err)
	}
	p.ServiceInstances = map[string]model.ServiceInstance{"api": plan.Instance}
	services, err := d.ServiceRuntime(ctx, p)
	if err != nil || len(services["api"].Containers) != 2 || len(services["loadbalancer"].Containers) != 1 || services["loadbalancer"].Containers[0].ID != plan.Proxy.Instance.ContainerID {
		b, _ := json.Marshal(services)
		t.Fatal("replicas or load balancer were replaced incorrectly", string(b), err)
	}
	if _, err := d.PlanServiceUpdate(ctx, p, "api", "seamless", 8080, 0, ""); err != nil {
		t.Fatal("second update review", err)
	}
	stopTraffic()
	<-trafficDone
	if trafficErr != nil || requests == 0 {
		t.Fatal("proxy traffic was interrupted during replica replacement", requests, trafficErr)
	}
	t.Logf("served %d successful requests through the unchanged load balancer during replacement", requests)
}
