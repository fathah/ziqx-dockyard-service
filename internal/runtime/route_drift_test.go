package runtime

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func TestLiveRoutesFollowPortsDockerReassignedAfterRestart(t *testing.T) {
	d, p, base := runtimeServiceFixture(t)
	p.Domains = append(p.Domains, "api.example.com")
	p.ServiceRoutes = []model.ServiceRoute{{Service: "postgres", ContainerPort: 5432, HostPort: 32876, Domains: []string{"api.example.com"}, Managed: true}}
	p.BluePort = 32835
	web := base.containers["web"]
	api := web
	api.ID = strings.Repeat("b", 64)
	api.Config.Labels = maps.Clone(web.Config.Labels)
	api.Config.Labels["com.docker.compose.service"] = "postgres"
	// After a reboot Docker restarts both containers from the bottom of its pool.
	setRouteTestPort(&web, 3000, 32772, "127.0.0.1")
	setRouteTestPort(&api, 5432, 32773, "127.0.0.1")
	d.Runner = &routeBindingRunner{base: base, containers: []Container{web, api}}
	before, _ := json.Marshal(p)
	next, err := d.LiveRoutes(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := json.Marshal(p); string(before) != string(after) {
		t.Fatal("observation changed the saved project")
	}
	if next.BluePort != 32772 || next.ServiceRoutes[0].HostPort != 32773 {
		t.Fatal("live ports not observed", next.BluePort, next.ServiceRoutes)
	}
	snippet := string(Snippet(next, next.Active))
	if !strings.Contains(snippet, "app.example.com {\n\treverse_proxy 127.0.0.1:32772") || !strings.Contains(snippet, "api.example.com {\n\treverse_proxy 127.0.0.1:32773") {
		t.Fatal("Caddy would keep proxying to the old ports", snippet)
	}
	// A second look at unchanged containers reports no drift.
	again, err := d.LiveRoutes(context.Background(), next)
	if err != nil || !reflect.DeepEqual(again, next) {
		t.Fatal("stable ports reported as drift", err, again)
	}
}

func TestLiveRoutesFollowSeamlessInstancePort(t *testing.T) {
	d, p, runner := runtimeServiceFixture(t)
	plan, err := d.PlanServiceUpdate(context.Background(), p, "web", "seamless", 3000, 3010, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.PullServiceUpdate(context.Background(), p, &plan); err != nil {
		t.Fatal(err)
	}
	candidate := runner.containers["web"]
	candidate.ID = strings.Repeat("c", 64)
	candidate.Config.Labels = map[string]string{"com.docker.compose.project": d.composeName(p, p.Active), "com.docker.compose.service": plan.Instance.Name, "io.ziqx.dockyard.server": d.Config.ServerID, "io.ziqx.dockyard.project": p.ID}
	candidate.NetworkSettings.Ports = map[string][]struct{ HostIP, HostPort string }{"3000/tcp": {{HostIP: "127.0.0.1", HostPort: "32790"}}}
	runner.containers[plan.Instance.Name] = candidate
	// The replaced primary has exited; it must not be consulted.
	primary := runner.containers["web"]
	primary.State.Running = false
	runner.containers["web"] = primary
	plan.Instance.Port = 3010
	p.ServiceInstances = map[string]model.ServiceInstance{"web": plan.Instance}
	next, err := d.LiveRoutes(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if next.ServiceInstances["web"].Port != 32790 || p.ServiceInstances["web"].Port != 3010 || next.BluePort != p.BluePort {
		t.Fatal("instance port not followed or input mutated", next.ServiceInstances, p.ServiceInstances)
	}
	if !strings.Contains(string(Snippet(next, next.Active)), "reverse_proxy 127.0.0.1:32790") {
		t.Fatal("Caddy would keep the stale instance port", string(Snippet(next, next.Active)))
	}
}

func TestLiveRoutesRejectUnverifiedContainers(t *testing.T) {
	for _, scenario := range []string{"stopped", "missing", "foreign", "public-ip", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			d, p, base := runtimeServiceFixture(t)
			c := base.containers["web"]
			setRouteTestPort(&c, 3000, 32772, "127.0.0.1")
			containers := []Container{c}
			switch scenario {
			case "stopped":
				containers[0].State.Running = false
			case "missing":
				containers = nil
			case "foreign":
				containers[0].Config.Labels["io.ziqx.dockyard.project"] = "other"
			case "public-ip":
				setRouteTestPort(&containers[0], 3000, 32772, "203.0.113.10")
			case "ambiguous":
				json.Unmarshal([]byte(`{"Ports":{"3000/tcp":[{"HostIP":"127.0.0.1","HostPort":"32772"},{"HostIP":"0.0.0.0","HostPort":"32773"}]}}`), &containers[0].NetworkSettings)
			}
			d.Runner = &routeBindingRunner{base: base, containers: containers}
			if _, err := d.LiveRoutes(context.Background(), p); err == nil {
				t.Fatal("trusted an unverified container port")
			}
		})
	}
}

func TestLivePortAcceptsSamePortOnBothFamilies(t *testing.T) {
	var c Container
	json.Unmarshal([]byte(`{"NetworkSettings":{"Ports":{"3000/tcp":[{"HostIP":"0.0.0.0","HostPort":"3009"},{"HostIP":"::","HostPort":"3009"}]}}}`), &c)
	if port, err := livePort(c, 3000); err != nil || port != 3009 {
		t.Fatal(port, err)
	}
}
