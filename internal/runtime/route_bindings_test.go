package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

type routeBindingRunner struct {
	base       process.Runner
	containers []Container
}

func (r *routeBindingRunner) Run(ctx context.Context, binary, dir string, args []string) (process.Result, error) {
	if args[0] == "inspect" {
		data, _ := json.Marshal(r.containers)
		return process.Result{Output: data}, nil
	}
	if slices.Contains(args, "--orphans=false") {
		ids := []string{}
		for _, c := range r.containers {
			ids = append(ids, c.ID)
		}
		return process.Result{Output: []byte(strings.Join(ids, "\n"))}, nil
	}
	return r.base.Run(ctx, binary, dir, args)
}

func setRouteTestPort(c *Container, target, published int, ip string) {
	data := fmt.Sprintf(`{"Ports":{"%d/tcp":[{"HostIP":%q,"HostPort":"%d"}]}}`, target, ip, published)
	json.Unmarshal([]byte(data), &c.NetworkSettings)
}

func TestDockerAssignedBindingsDriveAllDomainRoutes(t *testing.T) {
	d, p, base := runtimeServiceFixture(t)
	p.Domains = append(p.Domains, "api.example.com")
	p.ServiceRoutes = []model.ServiceRoute{{Service: "postgres", ContainerPort: 5432, HostPort: 0, Domains: []string{"api.example.com"}, Managed: true}}
	web := base.containers["web"]
	setRouteTestPort(&web, 3000, 45001, "127.0.0.1")
	api := web
	api.ID = strings.Repeat("b", 64)
	api.Config.Labels = maps.Clone(web.Config.Labels)
	api.Config.Labels["com.docker.compose.service"] = "postgres"
	setRouteTestPort(&api, 5432, 45002, "127.0.0.1")
	runner := &routeBindingRunner{base: base, containers: []Container{web, api}}
	d.Runner = runner
	if err := d.ResolveRouteBindings(context.Background(), &p, "blue"); err != nil {
		t.Fatal(err)
	}
	if p.BluePort != 45001 || p.ServiceRoutes[0].HostPort != 45002 {
		t.Fatal("saved ports did not follow Docker", p)
	}
	if err := d.Healthy(context.Background(), p, "blue", p.Slots["blue"]); err != nil {
		t.Fatal("healthy containers rejected after automatic allocation", err)
	}
	snippet := string(Snippet(p, "blue"))
	if !strings.Contains(snippet, "reverse_proxy 127.0.0.1:45001") || !strings.Contains(snippet, "reverse_proxy 127.0.0.1:45002") || strings.Contains(snippet, ":3001") {
		t.Fatal("Caddy retained an old host binding", snippet)
	}
	// Recreating a candidate can assign new ports. Resolution must not pin the
	// next deployment to the previous assignment or overwrite source inputs.
	setRouteTestPort(&runner.containers[0], 3000, 46001, "127.0.0.1")
	setRouteTestPort(&runner.containers[1], 5432, 46002, "127.0.0.1")
	if err := d.ResolveRouteBindings(context.Background(), &p, "blue"); err != nil || p.BluePort != 46001 || p.ServiceRoutes[0].HostPort != 46002 {
		t.Fatal("new assignment was stuck on previous ports", err, p)
	}
}

func TestBindingDiscoveryRejectsUnverifiedOrAmbiguousTargetsAtomically(t *testing.T) {
	for _, scenario := range []string{"public", "missing", "foreign", "replicas", "stopped", "zero-port"} {
		t.Run(scenario, func(t *testing.T) {
			d, p, base := runtimeServiceFixture(t)
			c := base.containers["web"]
			setRouteTestPort(&c, 3000, 45001, "127.0.0.1")
			containers := []Container{c}
			switch scenario {
			case "public":
				setRouteTestPort(&containers[0], 3000, 45001, "0.0.0.0")
			case "missing":
				containers = nil
			case "foreign":
				containers[0].Config.Labels["io.ziqx.dockyard.project"] = "other"
			case "replicas":
				other := c
				other.ID = strings.Repeat("b", 64)
				containers = append(containers, other)
			case "stopped":
				containers[0].State.Running = false
			case "zero-port":
				setRouteTestPort(&containers[0], 3000, 0, "127.0.0.1")
			}
			d.Runner = &routeBindingRunner{base: base, containers: containers}
			before, _ := json.Marshal(p)
			if err := d.ResolveRouteBindings(context.Background(), &p, "blue"); err == nil {
				t.Fatal("accepted an unverified binding")
			}
			after, _ := json.Marshal(p)
			if string(before) != string(after) {
				t.Fatal("failed discovery changed saved routing")
			}
		})
	}
}

func TestServiceRestartRefreshesPortWithoutChangingSiblingDomain(t *testing.T) {
	d, p, runner := runtimeServiceFixture(t)
	p.Domains = append(p.Domains, "api.example.com")
	p.ServiceRoutes = []model.ServiceRoute{{Service: "postgres", ContainerPort: 5432, HostPort: 45002, Domains: []string{"api.example.com"}, Managed: true}}
	sibling := p.ServiceRoutes[0]
	plan, err := d.PlanServiceUpdate(context.Background(), p, "web", "restart", 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.PullServiceUpdate(context.Background(), p, &plan); err != nil {
		t.Fatal(err)
	}
	c := runner.containers["web"]
	setRouteTestPort(&c, 3000, 46001, "127.0.0.1")
	runner.containers["web"] = c
	if err := d.ResolveServiceRouteBindings(context.Background(), &p, &plan); err != nil {
		t.Fatal(err)
	}
	if p.BluePort != 46001 || plan.Instance.Port != 46001 || !reflect.DeepEqual(p.ServiceRoutes[0], sibling) {
		t.Fatal("service restart did not preserve sibling routing", p, plan)
	}
}

func TestSeamlessCandidateUsesDockersAssignedPort(t *testing.T) {
	d, p, runner := runtimeServiceFixture(t)
	plan, err := d.PlanServiceUpdate(context.Background(), p, "web", "seamless", 3000, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.PullServiceUpdate(context.Background(), p, &plan); err != nil {
		t.Fatal(err)
	}
	_, b, err := nativeRelease(d.Config, p, plan.Instance.Release)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(b, &doc)
	bindings := array(object(object(doc["services"])[plan.Instance.Name])["ports"])
	if len(bindings) != 1 || object(bindings[0])["published"] != nil || object(bindings[0])["host_ip"] != "127.0.0.1" {
		t.Fatal("candidate still pins a host port", bindings)
	}
	c := runner.containers["web"]
	c.Config.Labels = maps.Clone(c.Config.Labels)
	c.Config.Labels["com.docker.compose.service"] = plan.Instance.Name
	c.ID = strings.Repeat("c", 64)
	c.Image = plan.Instance.Release.Image
	setRouteTestPort(&c, 3000, 47001, "127.0.0.1")
	runner.containers[plan.Instance.Name] = c
	if err := d.ResolveServiceRouteBindings(context.Background(), &p, &plan); err != nil || plan.Instance.Port != 47001 {
		t.Fatal("Docker-assigned candidate port was not discovered", err, plan)
	}
	if err := d.HealthyServiceUpdate(context.Background(), p, plan); err != nil {
		t.Fatal("discovered candidate port rejected", err)
	}
	if p.BluePort != 3001 {
		t.Fatal("discovery switched primary traffic before verification")
	}
	p.ServiceInstances = map[string]model.ServiceInstance{"web": plan.Instance}
	original := runner.containers["web"]
	original.State.Running = false
	runner.containers["web"] = original
	upstream, err := d.PublishedUpstream(context.Background(), p, "web", 3000)
	if err != nil || upstream != "127.0.0.1:47001" {
		t.Fatal("a new domain resolved the drained full-stack container", upstream, err)
	}
}

func TestUnassignedRouteCannotBePublishedToCaddy(t *testing.T) {
	p := model.Project{Mode: "compose", Domains: []string{"app.example.com"}, ServiceRoutes: []model.ServiceRoute{{Service: "web", ContainerPort: 3000, Domains: []string{"app.example.com"}, Managed: true}}}
	if routeBindingsReady(p, "blue") == nil || routeBindingsReady(p, "") != nil {
		t.Fatal("pending host port was published or maintenance was blocked")
	}
}

func TestComposeExposedTCPPortIsAvailableForAutofill(t *testing.T) {
	d, p, runner := runtimeServiceFixture(t)
	var doc map[string]any
	json.Unmarshal([]byte(runner.doc), &doc)
	object(object(doc["services"])["postgres"])["expose"] = []any{"5432/tcp", "5353/udp"}
	data, _ := json.Marshal(doc)
	runner.doc = string(data)
	release, err := d.PrepareNative(context.Background(), p, "services: supplied", "TOKEN=keep$secret\n")
	if err != nil {
		t.Fatal(err)
	}
	p.Slots["blue"] = release
	options, err := ServiceUpdateOptions(d.Config, p)
	if err != nil || !reflect.DeepEqual(options["postgres"].Ports, []int{5432}) {
		t.Fatal("Compose expose did not provide the TCP container port", options, err)
	}
}
