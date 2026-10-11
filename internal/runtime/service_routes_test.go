package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func TestServiceRouteBindingsPersistWithoutChangingResolvedInputs(t *testing.T) {
	d, p, runner := runtimeServiceFixture(t)
	original := p.Slots["blue"]
	p.Domains = append(p.Domains, "api.example.com")
	p.ServiceRoutes = []model.ServiceRoute{{Service: "postgres", ContainerPort: 5432, HostPort: 3010, Domains: []string{"api.example.com"}, Managed: true}}
	before := len(runner.calls)
	prepared, err := d.PrepareServiceRoutes(context.Background(), p, original)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Environment != original.Environment || prepared.Image != original.Image || prepared.Compose == original.Compose || len(runner.calls) != before {
		t.Fatal("resolved inputs or images reevaluated", prepared, runner.calls)
	}
	_, oldBytes, _ := nativeRelease(d.Config, p, original)
	_, newBytes, err := nativeRelease(d.Config, p, prepared)
	if err != nil {
		t.Fatal(err)
	}
	var oldDoc, newDoc map[string]any
	json.Unmarshal(oldBytes, &oldDoc)
	json.Unmarshal(newBytes, &newDoc)
	oldSvc := object(object(oldDoc["services"])["postgres"])
	newSvc := object(object(newDoc["services"])["postgres"])
	ports := array(newSvc["ports"])
	if len(ports) != 1 || object(ports[0])["host_ip"] != "127.0.0.1" || object(ports[0])["published"] != "3010" || object(ports[0])["target"] != float64(5432) {
		t.Fatal("private binding missing", ports)
	}
	delete(oldSvc, "ports")
	delete(newSvc, "ports")
	if !reflect.DeepEqual(oldSvc, newSvc) {
		t.Fatal("sibling data or resolved environment changed")
	}
	snippet := string(Snippet(p, "blue"))
	if strings.Count(snippet, "api.example.com {") != 1 || !strings.Contains(snippet, "reverse_proxy 127.0.0.1:3010") || !strings.Contains(snippet, "reverse_proxy 127.0.0.1:3001") {
		t.Fatal("routes replaced or duplicated", snippet)
	}
	again, err := RebindNativeRoute(d.Config, p, prepared)
	if err != nil || again.Compose != prepared.Compose {
		t.Fatal("repeated binding changed the release", again, err)
	}
	stopped := string(Snippet(p, ""))
	if strings.Count(stopped, "Application unavailable") != 2 || strings.Contains(stopped, "reverse_proxy") {
		t.Fatal("maintenance omitted service domain", stopped)
	}
}

func TestConnectingDomainPreservesIndividuallyUpdatedSiblingImage(t *testing.T) {
	d, p, runner := runtimeServiceFixture(t)
	plan, err := d.PlanServiceUpdate(context.Background(), p, "web", "restart", 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := d.PullServiceUpdate(context.Background(), p, &plan); err != nil || !changed {
		t.Fatal(changed, err)
	}
	p.ServiceInstances = map[string]model.ServiceInstance{"web": plan.Instance}
	p.Domains = append(p.Domains, "api.example.com")
	p.ServiceRoutes = []model.ServiceRoute{{Service: "postgres", ContainerPort: 5432, HostPort: 3010, Domains: []string{"api.example.com"}, Managed: true}}
	before := len(runner.calls)
	prepared, err := d.PrepareServiceRoutes(context.Background(), p, p.Slots["blue"])
	if err != nil {
		t.Fatal(err)
	}
	_, data, err := nativeRelease(d.Config, p, prepared)
	var doc map[string]any
	if err != nil || json.Unmarshal(data, &doc) != nil {
		t.Fatal(err)
	}
	web := object(object(doc["services"])["web"])
	if web["image"] != plan.Instance.Release.Image || object(web["depends_on"])["postgres"] == nil || len(runner.calls) != before {
		t.Fatal("domain connection rolled back the sibling image or changed dependencies", web, runner.calls)
	}
}
