package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func TestConfigurationUpdateAppliesEnvWithUnchangedImage(t *testing.T) {
	d, p, runner := runtimeServiceFixture(t)
	runner.image = runner.containers["web"].Image
	runner.doc = strings.ReplaceAll(runner.doc, "keep$secret", "new$secret")
	release, err := d.PrepareNative(context.Background(), p, "services: changed", "TOKEN=new$secret\n")
	if err != nil {
		t.Fatal(err)
	}
	plans, err := d.PlanConfigurationUpdates(context.Background(), p, release)
	if err != nil || len(plans) != 1 || plans[0].Service != "web" {
		t.Fatal(plans, err)
	}
	changed, err := d.PullServiceUpdate(context.Background(), p, &plans[0])
	if err != nil || !changed {
		t.Fatal("env-only update was skipped", changed, err)
	}
	plan := plans[0]
	if plan.Instance.Release.Environment != release.Environment {
		t.Fatal("used old environment")
	}
	_, b, err := nativeRelease(d.Config, p, plan.Instance.Release)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(b, &doc)
	svc := object(object(doc["services"])[plan.Instance.Name])
	if object(svc["environment"])["TOKEN"] != "new$$secret" || svc["image"] != runner.image || len(object(doc["services"])) != 1 {
		t.Fatal("wrong candidate", doc)
	}
	for _, args := range runner.calls {
		if strings.Contains(strings.Join(args, " "), " pull ") && args[len(args)-1] != "web" {
			t.Fatal("pulled sibling", args)
		}
	}
}

func TestRestartAfterConfigurationUpdatePreservesSiblingImageWithOlderEnvRevision(t *testing.T) {
	d, p, runner := runtimeServiceFixture(t)
	plan, err := d.PlanServiceUpdate(context.Background(), p, "web", "seamless", 3000, 3010, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.PullServiceUpdate(context.Background(), p, &plan); err != nil {
		t.Fatal(err)
	}
	p.ServiceInstances = map[string]model.ServiceInstance{"web": plan.Instance}
	// The new full .env revision differs, but this sibling's resolved env is
	// unchanged (the edited variable is only used by another app).
	release, err := d.PrepareNative(context.Background(), p, "services: changed", "TOKEN=keep$secret\nOTHER_APP=new\n")
	if err != nil {
		t.Fatal(err)
	}
	rebound, err := RebindNativeServices(d.Config, p, release)
	if err != nil {
		t.Fatal("restart rejected unchanged sibling's earlier revision", err)
	}
	_, b, err := nativeRelease(d.Config, p, rebound)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(b, &doc)
	if rebound.Environment != release.Environment || object(object(doc["services"])["web"])["image"] != runner.image {
		t.Fatal("lost full env or sibling image", rebound, doc)
	}
}

func TestConfigurationUpdateRejectsIncompatibleDraftWithoutMutation(t *testing.T) {
	for _, kind := range []string{"database", "network", "service removal", "healthcheck", "extra port", "build"} {
		t.Run(kind, func(t *testing.T) {
			d, p, runner := runtimeServiceFixture(t)
			var doc map[string]any
			json.Unmarshal([]byte(runner.doc), &doc)
			services := object(doc["services"])
			web := object(services["web"])
			switch kind {
			case "database":
				object(services["postgres"])["environment"] = map[string]any{"TOKEN": "changed"}
			case "network":
				object(object(doc["networks"])["default"])["name"] = "other_network"
			case "service removal":
				delete(services, "postgres")
			case "healthcheck":
				web["healthcheck"] = map[string]any{"disable": true}
			case "extra port":
				web["ports"] = append(array(web["ports"]), map[string]any{"target": 4000, "published": "4000"})
			case "build":
				web["build"] = map[string]any{"context": "/docker/demo"}
			}
			b, _ := json.Marshal(doc)
			runner.doc = string(b)
			release, err := d.PrepareNative(context.Background(), p, "services: changed", "TOKEN=keep$secret\n")
			if err != nil {
				t.Fatal(err)
			}
			runner.calls = nil
			if _, err := d.PlanConfigurationUpdates(context.Background(), p, release); err == nil {
				t.Fatal("accepted", kind)
			}
			for _, args := range runner.calls {
				if args[0] != "inspect" && args[9] != "ps" {
					t.Fatal("mutated live stack", args)
				}
			}
		})
	}
}

func TestConfigurationUpdatePlansMultipleRoutedApps(t *testing.T) {
	d, p, runner := runtimeServiceFixture(t)
	var doc map[string]any
	json.Unmarshal([]byte(runner.doc), &doc)
	object(doc["services"])["api"] = map[string]any{"image": "api:latest", "environment": map[string]any{"TOKEN": "old"}, "healthcheck": map[string]any{"test": []any{"CMD", "true"}}, "networks": map[string]any{"default": map[string]any{}}}
	p.Domains = append(p.Domains, "api.example.com")
	p.ServiceRoutes = []model.ServiceRoute{{Service: "api", ContainerPort: 8080, HostPort: 3002, Domains: []string{"api.example.com"}}}
	b, _ := json.Marshal(doc)
	runner.doc = string(b)
	r, err := d.PrepareNative(context.Background(), p, "services: both", "TOKEN=old\n")
	if err != nil {
		t.Fatal(err)
	}
	p.Slots["blue"] = r
	api := runner.containers["web"]
	api.ID = strings.Repeat("b", 64)
	api.Config.Labels = map[string]string{"com.docker.compose.project": "dy-vps-01-demo-blue", "com.docker.compose.service": "api", "io.ziqx.dockyard.server": "vps-01", "io.ziqx.dockyard.project": "demo"}
	runner.containers["api"] = api
	object(object(doc["services"])["api"])["environment"] = map[string]any{"TOKEN": "new"}
	object(object(doc["services"])["web"])["environment"] = map[string]any{"TOKEN": "new"}
	b, _ = json.Marshal(doc)
	runner.doc = string(b)
	next, err := d.PrepareNative(context.Background(), p, "services: changed", "TOKEN=new\n")
	if err != nil {
		t.Fatal(err)
	}
	plans, err := d.PlanConfigurationUpdates(context.Background(), p, next)
	if err != nil || len(plans) != 2 || plans[0].Service != "api" || plans[1].Service != "web" {
		t.Fatal(plans, err)
	}
}
