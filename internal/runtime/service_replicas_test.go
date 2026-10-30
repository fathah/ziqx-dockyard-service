package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

type replicaServiceRunner struct {
	base   *serviceRunner
	groups map[string][]Container
	calls  [][]string
}

func (r *replicaServiceRunner) Run(ctx context.Context, binary, dir string, args []string) (process.Result, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if args[0] == "inspect" {
		cs := []Container{}
		for _, id := range args[3:] {
			for _, group := range r.groups {
				for _, c := range group {
					if c.ID == id {
						cs = append(cs, c)
					}
				}
			}
		}
		slices.Reverse(cs) // Docker's output order is not an identity.
		b, _ := json.Marshal(cs)
		return process.Result{Output: b}, nil
	}
	if args[0] == "compose" && len(args) > 9 {
		switch args[9] {
		case "ps":
			ids := []string{}
			for name, cs := range r.groups {
				if args[len(args)-1] != name && !(slices.Contains(args, "--orphans=false") && !strings.HasPrefix(name, "dy-update-")) {
					continue
				}
				for _, c := range cs {
					ids = append(ids, c.ID)
				}
			}
			return process.Result{Output: []byte(strings.Join(ids, "\n"))}, nil
		case "stop":
			name := args[len(args)-1]
			for i := range r.groups[name] {
				r.groups[name][i].State.Running = false
			}
			return process.Result{}, nil
		}
	}
	return r.base.Run(ctx, binary, dir, args)
}

func cloneReplica(c Container) Container {
	b, _ := json.Marshal(c)
	var copy Container
	json.Unmarshal(b, &copy)
	return copy
}

func replicatedServiceFixture(t *testing.T, scale bool) (Docker, model.Project, *replicaServiceRunner) {
	t.Helper()
	d, p, base := runtimeServiceFixture(t)
	var doc map[string]any
	json.Unmarshal([]byte(base.doc), &doc)
	svc := object(object(doc["services"])["web"])
	delete(svc, "container_name")
	if scale {
		svc["scale"] = 2
	} else {
		svc["deploy"] = map[string]any{"replicas": 2, "resources": map[string]any{"limits": map[string]any{"memory": "256M"}}}
	}
	b, _ := json.Marshal(doc)
	base.doc = string(b)
	release, err := d.PrepareNative(context.Background(), p, "services: supplied", "")
	if err != nil {
		t.Fatal(err)
	}
	release.ID = "rel-scaled"
	p.Slots["blue"], p.Releases = release, []model.Release{release}
	first := cloneReplica(base.containers["web"])
	first.Name = "/web-1"
	setRouteTestPort(&first, 3000, 45001, "127.0.0.1")
	second := cloneReplica(first)
	second.ID, second.Name = strings.Repeat("b", 64), "/web-2"
	setRouteTestPort(&second, 3000, 45002, "127.0.0.1")
	runner := &replicaServiceRunner{base: base, groups: map[string][]Container{"web": {first, second}}}
	d.Runner = runner
	return d, p, runner
}

func addCandidateReplicas(r *replicaServiceRunner, plan model.ServiceUpdate, n int) {
	cs := []Container{}
	for i := 0; i < n; i++ {
		c := cloneReplica(r.groups["web"][0])
		c.ID, c.Name = strings.Repeat(fmt.Sprintf("%x", i+12), 64), fmt.Sprintf("/%s-%d", plan.Instance.Name, i+1)
		c.Config.Labels["com.docker.compose.service"] = plan.Instance.Name
		c.Image = plan.Instance.Release.Image
		setRouteTestPort(&c, 3000, 47001+i, "127.0.0.1")
		cs = append(cs, c)
	}
	r.groups[plan.Instance.Name] = cs
}

func TestReplicatedServiceSeamlessUpdatePreservesScaleAndAllBackends(t *testing.T) {
	for _, scale := range []bool{false, true} {
		t.Run(fmt.Sprintf("scale=%v", scale), func(t *testing.T) {
			d, p, r := replicatedServiceFixture(t, scale)
			ctx := context.Background()
			if err := d.ResolveRouteBindings(ctx, &p, p.Active); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(p)
			options, err := ServiceUpdateOptions(d.Config, p)
			if err != nil || !options["web"].Updatable || !options["web"].Seamless {
				t.Fatal("replicated stateless service remained blocked", options, err)
			}
			plan, err := d.PlanServiceUpdate(ctx, p, "web", "seamless", 3000, 0, "")
			if err != nil || plan.Instance.Replicas != 2 || len(plan.Previous.ContainerIDs) != 2 {
				t.Fatal("review did not pin both replicas", plan, err)
			}
			if changed, err := d.PullServiceUpdate(ctx, p, &plan); err != nil || !changed {
				t.Fatal(changed, err)
			}
			_, b, _ := nativeRelease(d.Config, p, plan.Instance.Release)
			var doc map[string]any
			json.Unmarshal(b, &doc)
			svc := object(object(doc["services"])[plan.Instance.Name])
			if serviceReplicas(svc) != 2 || svc["container_name"] != nil || svc["depends_on"] != nil || len(object(doc["services"])) != 1 {
				t.Fatal("candidate lost scale or would recreate dependencies", doc)
			}
			if err := d.StartServiceUpdate(ctx, p, plan); err != nil {
				t.Fatal(err)
			}
			addCandidateReplicas(r, plan, 2)
			if err := d.ResolveServiceRouteBindings(ctx, &p, &plan); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(plan.Instance.Ports, []int{47001, 47002}) || len(plan.Instance.ContainerIDs) != 2 {
				t.Fatal("not all bindings were journaled", plan.Instance)
			}
			if err := d.HealthyServiceUpdate(ctx, p, plan); err != nil {
				t.Fatal(err)
			}
			if err := d.PromoteServiceUpdate(ctx, p, plan); err != nil {
				t.Fatal(err)
			}
			connections := 0
			for _, call := range r.calls {
				if call[0] == "network" && call[1] == "connect" {
					connections++
				}
			}
			if connections != 2 {
				t.Fatal("not every replica received service aliases", r.calls)
			}
			if after, _ := json.Marshal(p); string(after) != string(before) {
				t.Fatal("preparing replacements changed the running project")
			}
			next := p
			next.ServiceInstances = map[string]model.ServiceInstance{"web": plan.Instance}
			if !strings.Contains(string(Snippet(next, next.Active)), "reverse_proxy 127.0.0.1:47001 127.0.0.1:47002") || !strings.Contains(string(Snippet(p, p.Active)), "reverse_proxy 127.0.0.1:45001 127.0.0.1:45002") {
				t.Fatal("cutover or rollback would lose replicas", string(Snippet(next, next.Active)))
			}
			if err := d.StopServiceInstance(ctx, p, *plan.Previous); err != nil {
				t.Fatal(err)
			}
			for _, c := range r.groups["web"] {
				if c.State.Running {
					t.Fatal("old replica was not retired")
				}
			}
			services, err := d.ServiceRuntime(ctx, next)
			if err != nil || len(services["web"].Containers) != 2 || !slices.Equal(services["web"].HostPorts, []int{47001, 47002}) {
				t.Fatal("inventory lost active replica identity", services, err)
			}
			if _, err := d.PlanServiceUpdate(ctx, next, "web", "seamless", 3000, 0, ""); err != nil {
				t.Fatal("second update did not follow the active replicas", err)
			}
		})
	}
}

func TestReplicatedServiceRequiresEveryReviewedContainer(t *testing.T) {
	for _, scenario := range []string{"missing", "unhealthy", "replaced", "foreign", "image", "network"} {
		t.Run(scenario, func(t *testing.T) {
			d, p, r := replicatedServiceFixture(t, false)
			plan, err := d.PlanServiceUpdate(context.Background(), p, "web", "seamless", 3000, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "missing":
				r.groups["web"] = r.groups["web"][:1]
			case "unhealthy":
				r.groups["web"][1].State.Health.Status = "unhealthy"
			case "replaced":
				r.groups["web"][1].ID = strings.Repeat("e", 64)
			case "foreign":
				r.groups["web"][1].Config.Labels["io.ziqx.dockyard.project"] = "other"
			case "image":
				r.groups["web"][1].Image = "sha256:" + strings.Repeat("f", 64)
			case "network":
				connection := r.groups["web"][1].NetworkSettings.Networks["original_default"]
				connection.NetworkID = strings.Repeat("f", 64)
				r.groups["web"][1].NetworkSettings.Networks["original_default"] = connection
			}
			if err := d.VerifyServiceUpdate(context.Background(), p, plan); err == nil {
				t.Fatal("a changed replica passed the reviewed preconditions")
			}
			for _, call := range r.calls {
				if call[0] == "network" || slices.Contains(call, "stop") || slices.Contains(call, "up") {
					t.Fatal("review validation mutated containers", call)
				}
			}
		})
	}
}

func TestReplicatedCandidateRejectsPartialOrUnhealthyGroupAndCleansUp(t *testing.T) {
	for _, scenario := range []string{"missing", "unhealthy", "wrong-image", "public-binding"} {
		t.Run(scenario, func(t *testing.T) {
			d, p, r := replicatedServiceFixture(t, false)
			ctx := context.Background()
			plan, err := d.PlanServiceUpdate(ctx, p, "web", "seamless", 3000, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.PullServiceUpdate(ctx, p, &plan); err != nil {
				t.Fatal(err)
			}
			addCandidateReplicas(r, plan, 2)
			switch scenario {
			case "missing":
				r.groups[plan.Instance.Name] = r.groups[plan.Instance.Name][:1]
			case "unhealthy":
				r.groups[plan.Instance.Name][1].State.Health.Status = "unhealthy"
			case "wrong-image":
				r.groups[plan.Instance.Name][1].Image = "sha256:" + strings.Repeat("f", 64)
			case "public-binding":
				setRouteTestPort(&r.groups[plan.Instance.Name][1], 3000, 47002, "0.0.0.0")
			}
			before := plan.Instance
			err = d.ResolveServiceRouteBindings(ctx, &p, &plan)
			if err == nil {
				err = d.HealthyServiceUpdate(ctx, p, plan)
			}
			if err == nil {
				t.Fatal("incomplete/unhealthy candidate passed readiness")
			}
			// Cleanup of a partially created candidate uses the immutable image
			// and unique service name before a full identity set is available.
			if scenario != "wrong-image" {
				if err := d.StopServiceInstance(ctx, p, before); err != nil {
					t.Fatal("could not clean up partial candidate", err)
				}
			}
			for _, c := range r.groups["web"] {
				if !c.State.Running {
					t.Fatal("failed candidate stopped the running replica")
				}
			}
		})
	}
}

func TestLiveReplicaRoutesRefreshEveryPortWithoutMutatingSnapshot(t *testing.T) {
	d, p, r := replicatedServiceFixture(t, false)
	if err := d.ResolveRouteBindings(context.Background(), &p, p.Active); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(p)
	setRouteTestPort(&r.groups["web"][0], 3000, 48001, "127.0.0.1")
	setRouteTestPort(&r.groups["web"][1], 3000, 48002, "127.0.0.1")
	next, err := d.LiveRoutes(context.Background(), p)
	if err != nil || !strings.Contains(string(Snippet(next, next.Active)), "127.0.0.1:48001 127.0.0.1:48002") {
		t.Fatal("replica routes were not refreshed", next.RouteBindings, err)
	}
	if after, _ := json.Marshal(p); string(after) != string(before) {
		t.Fatal("live discovery changed rollback snapshot")
	}
	again, err := d.LiveRoutes(context.Background(), next)
	if err != nil || !reflect.DeepEqual(next, again) {
		t.Fatal("stable replica bindings reported drift", again.RouteBindings, err)
	}
}
