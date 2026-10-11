package runtime

import (
	"context"
	"encoding/json"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"reflect"
	"strings"
	"testing"
)

func TestServiceUpdateAppliesSavedEnvironmentWithSamePreparedImage(t *testing.T) {
	for _, mode := range []string{"restart", "seamless"} {
		t.Run(mode, func(t *testing.T) {
			d, p, runner := runtimeServiceFixture(t)
			runner.image = runner.containers["web"].Image
			var resolved map[string]any
			json.Unmarshal([]byte(runner.doc), &resolved)
			object(object(resolved["services"])["web"])["environment"] = map[string]any{"TOKEN": "new$secret", "MULTILINE": "first\nsecond"}
			// A changed sibling must stay on its existing manifest and environment.
			object(object(resolved["services"])["postgres"])["environment"] = map[string]any{"TOKEN": "sibling"}
			b, _ := json.Marshal(resolved)
			runner.doc = string(b)
			if _, err := SaveDraft(d.Config, p, "services: unrelated draft edits", "TOKEN='new$secret'\nMULTILINE='first\nsecond'\n"); err != nil {
				t.Fatal(err)
			}
			plan, err := d.PlanServiceUpdate(context.Background(), p, "web", mode, 3000, 3010, "")
			if err != nil {
				t.Fatal(err)
			}
			plan.PreparedImage = runner.image
			if err = d.PrepareServiceEnvironment(context.Background(), p, &plan); err != nil {
				t.Fatal(err)
			}
			if plan.SourceRelease == nil {
				t.Fatal("saved environment not selected")
			}
			again := plan
			if err = d.PrepareServiceEnvironment(context.Background(), p, &again); err != nil {
				t.Fatal(err)
			}
			if again.SourceRelease.Compose != plan.SourceRelease.Compose {
				t.Fatal("review hash would change between preview and execution")
			}
			changed, err := d.PullServiceUpdate(context.Background(), p, &plan)
			if err != nil || !changed {
				t.Fatal("same-image environment update skipped", changed, err)
			}
			_, data, err := nativeRelease(d.Config, p, plan.Instance.Release)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			json.Unmarshal(data, &doc)
			svc := object(object(doc["services"])[plan.Instance.Name])
			if !reflect.DeepEqual(svc["environment"], map[string]any{"TOKEN": "new$$secret", "MULTILINE": "first\nsecond"}) {
				t.Fatal("environment was not applied literally", svc["environment"])
			}
			if mode == "restart" && object(object(doc["services"])["postgres"])["environment"] != nil {
				t.Fatal("updated sibling environment")
			}
			for _, args := range runner.calls {
				if strings.Contains(strings.Join(args, " "), " pull ") {
					t.Fatal("repulled prepared image")
				}
			}
		})
	}
}

func TestServiceUpdateClearsRemovedEnvironmentVariables(t *testing.T) {
	d, p, runner := runtimeServiceFixture(t)
	runner.doc = strings.ReplaceAll(runner.doc, `"environment":{"TOKEN":"keep$secret"},`, "")
	if _, err := SaveDraft(d.Config, p, "services: supplied", ""); err != nil {
		t.Fatal(err)
	}
	plan, err := d.PlanServiceUpdate(context.Background(), p, "web", "restart", 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.PrepareServiceEnvironment(context.Background(), p, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.SourceRelease == nil {
		t.Fatal("empty saved .env ignored")
	}
	if changed, err := d.PullServiceUpdate(context.Background(), p, &plan); err != nil || !changed {
		t.Fatal(changed, err)
	}
	_, data, err := nativeRelease(d.Config, p, plan.Instance.Release)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(data, &doc)
	if object(object(doc["services"])[plan.Instance.Name])["environment"] != nil {
		t.Fatal("removed variable retained")
	}
}

func TestServiceEnvironmentCanRevertAnEarlierIndividualUpdate(t *testing.T) {
	for _, initialMode := range []string{"restart", "seamless"} {
		t.Run(initialMode, func(t *testing.T) {
			d, p, runner := runtimeServiceFixture(t)
			originalDoc := runner.doc
			runner.image = runner.containers["web"].Image
			runner.doc = strings.ReplaceAll(runner.doc, "keep$secret", "changed$secret")
			if _, err := SaveDraft(d.Config, p, "services: supplied", "TOKEN=changed$secret\n"); err != nil {
				t.Fatal(err)
			}
			first, err := d.PlanServiceUpdate(context.Background(), p, "web", initialMode, 3000, 3010, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = d.PrepareServiceEnvironment(context.Background(), p, &first); err != nil {
				t.Fatal(err)
			}
			first.PreparedImage = runner.image
			if changed, err := d.PullServiceUpdate(context.Background(), p, &first); err != nil || !changed {
				t.Fatal(changed, err)
			}
			p.ServiceInstances = map[string]model.ServiceInstance{"web": first.Instance}
			active := runner.containers["web"]
			active.Config.Labels["com.docker.compose.service"] = first.Instance.Name
			runner.containers[first.Instance.Name] = active
			runner.doc = originalDoc
			if _, err := SaveDraft(d.Config, p, "services: supplied", "TOKEN=keep$secret\n"); err != nil {
				t.Fatal(err)
			}
			second, err := d.PlanServiceUpdate(context.Background(), p, "web", "restart", 0, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = d.PrepareServiceEnvironment(context.Background(), p, &second); err != nil {
				t.Fatal(err)
			}
			if second.SourceRelease == nil {
				t.Fatal("compared against full release instead of active container environment")
			}
			if changed, err := d.PullServiceUpdate(context.Background(), p, &second); err != nil || !changed {
				t.Fatal(changed, err)
			}
			_, b, err := nativeRelease(d.Config, p, second.Instance.Release)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			json.Unmarshal(b, &doc)
			svc := object(object(doc["services"])[second.Instance.Name])
			if object(svc["environment"])["TOKEN"] != "keep$$secret" {
				t.Fatal("environment was not reverted")
			}
			if initialMode == "seamless" && len(array(object(object(svc["networks"])["connection0"])["aliases"])) == 0 {
				// JSON serializes the preserved aliases as an array of strings.
				t.Fatal("controlled restart lost promoted service DNS aliases")
			}
		})
	}
}
