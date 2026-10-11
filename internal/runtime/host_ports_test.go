package runtime

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"
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
