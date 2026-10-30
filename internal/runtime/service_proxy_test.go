package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

type proxyServiceRunner struct {
	*replicaServiceRunner
	servers       []proxyServer
	commands      []string
	controlFailed bool
}

func (r *proxyServiceRunner) Run(ctx context.Context, binary, dir string, args []string) (process.Result, error) {
	if args[0] != "exec" {
		return r.replicaServiceRunner.Run(ctx, binary, dir, args)
	}
	if len(args) != 8 || args[1] != r.groups["loadbalancer"][0].ID || args[5] != "dockyard-haproxy" || args[7] != "9999" {
		r.base.t.Fatal("unsafe proxy command", args)
	}
	command := args[6]
	r.commands = append(r.commands, command)
	if r.controlFailed {
		return process.Result{}, fmt.Errorf("runtime control unavailable")
	}
	if command == "show stat" {
		var b strings.Builder
		b.WriteString("# pxname,svname,scur,status,addr,\n")
		for _, server := range r.servers {
			fmt.Fprintf(&b, "api_replicas,%s,%d,%s,%s,\n", server.Name, server.Sessions, server.Status, server.Address)
		}
		b.WriteString("api_replicas,BACKEND,0,UP,,\n")
		return process.Result{Output: []byte(b.String())}, nil
	}
	parts := strings.Fields(command)
	if len(parts) != 5 || parts[0] != "set" || parts[1] != "server" || parts[3] != "state" {
		r.base.t.Fatal("unexpected proxy mutation", command)
	}
	for i, server := range r.servers {
		if "api_replicas/"+server.Name == parts[2] {
			r.servers[i].Status = map[string]string{"ready": "UP", "drain": "DRAIN"}[parts[4]]
			return process.Result{Output: []byte("\n")}, nil
		}
	}
	return process.Result{Output: []byte("No such server\n")}, nil
}

func proxyServiceFixture(t *testing.T) (Docker, model.Project, *proxyServiceRunner) {
	t.Helper()
	d, p, r := replicatedServiceFixture(t, false)
	p.Domains, p.RouteService, p.RoutePort, p.BluePort = nil, "", 0, 0
	var doc map[string]any
	json.Unmarshal([]byte(r.base.doc), &doc)
	svc := object(object(doc["services"])["web"])
	delete(svc, "ports")
	svc["labels"] = map[string]any{proxyLabel + "service": "loadbalancer", proxyLabel + "backend": "api_replicas", proxyLabel + "port": "3000"}
	object(doc["services"])["loadbalancer"] = map[string]any{"image": "haproxy:3.2-alpine", "healthcheck": map[string]any{"test": []any{"CMD", "true"}}, "networks": map[string]any{"default": map[string]any{}}}
	b, _ := json.Marshal(doc)
	r.base.doc = string(b)
	release, err := d.PrepareNative(context.Background(), p, "services: supplied", "")
	if err != nil {
		t.Fatal(err)
	}
	release.ID = "rel-proxy"
	p.Slots["blue"], p.Releases = release, []model.Release{release}
	for i := range r.groups["web"] {
		connection := r.groups["web"][i].NetworkSettings.Networks["original_default"]
		connection.IPAddress = fmt.Sprintf("10.0.0.%d", i+2)
		r.groups["web"][i].NetworkSettings.Networks["original_default"] = connection
		r.groups["web"][i].NetworkSettings.Ports = nil
	}
	proxy := cloneReplica(r.groups["web"][0])
	proxy.ID, proxy.Name = strings.Repeat("e", 64), "/loadbalancer-1"
	proxy.Config.Labels["com.docker.compose.service"] = "loadbalancer"
	connection := proxy.NetworkSettings.Networks["original_default"]
	connection.IPAddress = "10.0.0.10"
	proxy.NetworkSettings.Networks["original_default"] = connection
	r.groups["loadbalancer"] = []Container{proxy}
	runner := &proxyServiceRunner{replicaServiceRunner: r, servers: []proxyServer{{Name: "api1", Address: "10.0.0.2:3000", Status: "UP"}, {Name: "api2", Address: "10.0.0.3:3000", Status: "UP"}}}
	d.Runner = runner
	return d, p, runner
}

func prepareProxyCandidates(t *testing.T, d Docker, p *model.Project, r *proxyServiceRunner) model.ServiceUpdate {
	t.Helper()
	plan, err := d.PlanServiceUpdate(context.Background(), *p, "web", "seamless", 3000, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.PullServiceUpdate(context.Background(), *p, &plan); err != nil {
		t.Fatal(err)
	}
	addCandidateReplicas(r.replicaServiceRunner, plan, 2)
	for i := range r.groups[plan.Instance.Name] {
		connection := r.groups[plan.Instance.Name][i].NetworkSettings.Networks["original_default"]
		connection.IPAddress = fmt.Sprintf("10.0.0.%d", i+12)
		r.groups[plan.Instance.Name][i].NetworkSettings.Networks["original_default"] = connection
	}
	if err := d.ResolveServiceRouteBindings(context.Background(), p, &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestInternalHAProxyBackendUpdatesWithoutDomainOrHostPort(t *testing.T) {
	d, p, r := proxyServiceFixture(t)
	options, err := ServiceUpdateOptions(d.Config, p)
	if err != nil || !options["web"].Seamless || options["web"].LoadBalancer != "loadbalancer" || !slices.Contains(options["web"].Ports, 3000) {
		t.Fatal("internal replicated API was blocked", options, err)
	}
	plan := prepareProxyCandidates(t, d, &p, r)
	if plan.Proxy == nil || plan.Proxy.Instance.ContainerID != r.groups["loadbalancer"][0].ID {
		t.Fatal("review did not pin the load balancer", plan)
	}
	r.servers = append(r.servers, proxyServer{Name: "api3", Address: "10.0.0.12:3000", Status: "UP"}, proxyServer{Name: "api4", Address: "10.0.0.13:3000", Status: "UP"})
	if err := d.SwitchServiceProxy(context.Background(), p, plan, false); err != nil {
		t.Fatal(err)
	}
	if r.servers[0].Status != "DRAIN" || r.servers[1].Status != "DRAIN" || r.servers[2].Status != "UP" || r.servers[3].Status != "UP" {
		t.Fatal("proxy did not drain just the old replicas", r.servers)
	}
	r.servers[0].Sessions = 1
	if err := d.DrainedServiceProxy(context.Background(), p, plan, false); err == nil {
		t.Fatal("active request would have been interrupted")
	}
	r.servers[0].Sessions = 0
	if err := d.DrainedServiceProxy(context.Background(), p, plan, false); err != nil {
		t.Fatal(err)
	}
	if err := d.SwitchServiceProxy(context.Background(), p, plan, true); err != nil {
		t.Fatal("rollback could not restore the old backends", err)
	}
	if r.servers[0].Status != "UP" || r.servers[1].Status != "UP" || r.servers[2].Status != "DRAIN" || r.servers[3].Status != "DRAIN" {
		t.Fatal("rollback did not restore the old replicas", r.servers)
	}
	if err := d.DrainedServiceProxy(context.Background(), p, plan, true); err != nil {
		t.Fatal(err)
	}
	if p.BluePort != 0 || p.RouteService != "" || len(Snippet(p, p.Active)) != 0 {
		t.Fatal("internal update required a public route", p)
	}
}

func TestHAProxyWaitsForEveryReplacementBeforeDraining(t *testing.T) {
	d, p, r := proxyServiceFixture(t)
	plan := prepareProxyCandidates(t, d, &p, r)
	// One candidate has been discovered. The second is still missing/unhealthy.
	r.servers = append(r.servers, proxyServer{Name: "api3", Address: "10.0.0.12:3000", Status: "UP"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := d.SwitchServiceProxy(ctx, p, plan, false); err == nil {
		t.Fatal("partial proxy readiness was accepted")
	}
	for _, command := range r.commands {
		if strings.Contains(command, "state drain") {
			t.Fatal("old backends were drained before every replacement was ready", r.commands)
		}
	}
	for _, c := range r.groups["web"] {
		if !c.State.Running {
			t.Fatal("original replicas stopped before discovery completed")
		}
	}
}

func TestHAProxyPreviewRequiresPrivateControlAndReviewedIdentity(t *testing.T) {
	for _, scenario := range []string{"control-disabled", "unhealthy", "foreign", "wrong-backend", "unknown-address"} {
		t.Run(scenario, func(t *testing.T) {
			d, p, r := proxyServiceFixture(t)
			switch scenario {
			case "control-disabled":
				r.controlFailed = true
			case "unhealthy":
				r.groups["loadbalancer"][0].State.Health.Status = "unhealthy"
			case "foreign":
				r.groups["loadbalancer"][0].Config.Labels["io.ziqx.dockyard.project"] = "other"
			case "wrong-backend":
				r.servers = nil
			case "unknown-address":
				r.servers = append(r.servers, proxyServer{Name: "api5", Address: "10.0.0.90:3000", Status: "UP"})
			}
			if _, err := d.PlanServiceUpdate(context.Background(), p, "web", "seamless", 3000, 0, ""); err == nil {
				t.Fatal("an unverifiable load balancer was accepted")
			}
			for _, command := range r.commands {
				if command != "show stat" {
					t.Fatal("preview mutated the load balancer", command)
				}
			}
		})
	}
}

func TestProxyStatsRejectMalformedControlOutput(t *testing.T) {
	for _, raw := range []string{"", "Permission denied\n", "# pxname,svname,status\na,api1,UP\n", "# pxname,svname,scur,status,addr\napi_replicas,api1,no,UP,10.0.0.2:3000\n", "# pxname,svname,scur,status,addr\napi_replicas,api1,0,UP,10.0.0.2:3000\napi_replicas,api1,0,UP,10.0.0.3:3000\n"} {
		if _, err := parseProxyStats(raw, "api_replicas"); err == nil {
			t.Fatal("invalid control response accepted", strconv.Quote(raw))
		}
	}
}

func TestHAProxyDiscoveryNetworkIsPromotedAfterDependencyNetworks(t *testing.T) {
	d, p, r := proxyServiceFixture(t)
	plan := prepareProxyCandidates(t, d, &p, r)
	dependencyID := strings.Repeat("f", 64)
	plan.NetworkIDs["zzz_dependency"] = dependencyID
	plan.NetworkAliases["zzz_dependency"] = []string{"web"}
	for i := range r.groups[plan.Instance.Name] {
		connection := r.groups[plan.Instance.Name][i].NetworkSettings.Networks["original_default"]
		connection.NetworkID = dependencyID
		r.groups[plan.Instance.Name][i].NetworkSettings.Networks["zzz_dependency"] = connection
	}
	if err := d.PromoteServiceUpdate(context.Background(), p, plan); err != nil {
		t.Fatal(err)
	}
	connections := []string{}
	for _, call := range r.calls {
		if call[0] == "network" && call[1] == "connect" {
			connections = append(connections, call[len(call)-2])
		}
	}
	if !slices.Equal(connections, []string{dependencyID, plan.Proxy.NetworkID, dependencyID, plan.Proxy.NetworkID}) {
		t.Fatal("HAProxy could admit a replica before dependency networks were restored", connections)
	}
}
