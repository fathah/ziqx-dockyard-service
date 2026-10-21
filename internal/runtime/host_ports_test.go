package runtime

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

func TestPublishedServicePortsUsesLiveBindings(t *testing.T) {
	d, p, base := runtimeServiceFixture(t)
	web := base.containers["web"]
	// Container 3000 is exposed, but host bindings are 45001 and 45002.
	json.Unmarshal([]byte(`{"Ports":{"3000/tcp":[{"HostIP":"127.0.0.1","HostPort":"45002"},{"HostIP":"::","HostPort":"45002"}],"8080/udp":[{"HostIP":"0.0.0.0","HostPort":"45001"}],"9000/tcp":null}}`), &web.NetworkSettings)
	worker := web
	worker.ID = strings.Repeat("b", 64)
	worker.Config.Labels = maps.Clone(web.Config.Labels)
	worker.Config.Labels["com.docker.compose.service"] = "worker"
	worker.NetworkSettings.Ports = nil
	d.Runner = &routeBindingRunner{base: base, containers: []Container{web, worker}}
	ports, err := d.PublishedServicePorts(context.Background(), p)
	if err != nil || !reflect.DeepEqual(ports["web"], []int{45001, 45002}) || len(ports["worker"]) != 0 {
		t.Fatal("host bindings differ from Docker", ports, err)
	}
	web.State.Running = false
	if len(publishedHostPorts(web)) != 0 {
		t.Fatal("stopped container reported running bindings")
	}
}

func TestPublishedServicePortsRejectsForeignContainers(t *testing.T) {
	d, p, base := runtimeServiceFixture(t)
	web := base.containers["web"]
	web.Config.Labels["io.ziqx.dockyard.project"] = "foreign"
	d.Runner = &routeBindingRunner{base: base, containers: []Container{web}}
	if _, err := d.PublishedServicePorts(context.Background(), p); err == nil {
		t.Fatal("reported bindings for an unverified container")
	}
}

func TestServiceRuntimeReportsNamesReplicasAndExcludesStoppedContainers(t *testing.T) {
	d, p, base := runtimeServiceFixture(t)
	web := base.containers["web"]
	replica := web
	replica.ID, replica.Name = strings.Repeat("b", 64), "/web-replica-2"
	stopped := web
	stopped.ID, stopped.Name = strings.Repeat("c", 64), "/old-web"
	stopped.State.Running = false
	d.Runner = &routeBindingRunner{base: base, containers: []Container{replica, stopped, web}}
	services, err := d.ServiceRuntime(context.Background(), p)
	want := []model.ServiceContainer{{ID: web.ID, Name: "web-fixed"}, {ID: replica.ID, Name: "web-replica-2"}}
	if err != nil || !reflect.DeepEqual(services["web"].Containers, want) {
		t.Fatal("wrong live container identities", services, err)
	}
}

// Preserve per-ID inspect replies for the replacement while serving the base Compose set.
type serviceIdentityRunner struct{ *serviceRunner }

func (r serviceIdentityRunner) Run(ctx context.Context, binary, dir string, args []string) (process.Result, error) {
	if args[0] == "compose" && args[len(args)-1] == "--orphans=false" {
		return process.Result{Output: []byte(r.containers["web"].ID)}, nil
	}
	return r.serviceRunner.Run(ctx, binary, dir, args)
}

func TestServiceRuntimeUsesReplacementIdentityUnderLogicalServiceName(t *testing.T) {
	d, p, base := runtimeServiceFixture(t)
	plan, err := d.PlanServiceUpdate(context.Background(), p, "web", "seamless", 3000, 3010, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate := base.containers["web"]
	candidate.ID, candidate.Name = strings.Repeat("b", 64), "/dy-vps-01-demo-blue-new-web-1"
	candidate.Config.Labels = maps.Clone(candidate.Config.Labels)
	candidate.Config.Labels["com.docker.compose.service"] = plan.Instance.Name
	base.containers[plan.Instance.Name] = candidate
	p.ServiceInstances = map[string]model.ServiceInstance{"web": plan.Instance}
	d.Runner = serviceIdentityRunner{base}
	services, err := d.ServiceRuntime(context.Background(), p)
	want := []model.ServiceContainer{{ID: candidate.ID, Name: strings.TrimPrefix(candidate.Name, "/")}}
	if err != nil || !reflect.DeepEqual(services["web"].Containers, want) || len(services) != 1 {
		t.Fatal("reported previous instance or physical service as a sibling", services, err)
	}
}
