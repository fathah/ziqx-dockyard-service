package runtime

import (
	"context"
	"encoding/json"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Exercise the real Compose resolver, rather than supplying already-resolved
// environment values from a stub. This reproduces a save followed by restart.
func TestRestartLoadsSavedEnvironmentWithRealCompose(t *testing.T) {
	if os.Getenv("DOCKYARD_COMPOSE_TEST") != "1" {
		t.Skip("real Compose parser opt-in")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	c := config.Config{ServerID: "vps-01", ProjectsRoot: t.TempDir(), DockerBinary: binary}
	p := model.Project{ID: "demo", Mode: "compose", Environment: model.Production, Slots: map[string]model.Release{}}
	if err = Compose(c, p); err != nil {
		t.Fatal(err)
	}
	d := Docker{Config: c, Runner: process.Exec{Timeout: 20 * time.Second, Limit: 2 << 20, DockerConfig: filepath.Join(home, ".docker")}}
	source := "services:\n  app:\n    image: nginx:${TAG}\n    command: [echo, deployed]\n  worker:\n    image: busybox:latest\n"
	old, err := d.PrepareNative(context.Background(), p, source, "TAG=alpine\nZIQX_CORE_INTERNAL_URL=http://host.docker.internal:2999\nREMOVED=old\n")
	if err != nil {
		t.Fatal(err)
	}
	p.Slots["blue"] = old
	latest := "TAG=other\nZIQX_CORE_INTERNAL_URL=https://ziqx.cc\nLITERAL='a$HOME${MISSING}$$'\n"
	if _, err = SaveDraft(c, p, "services: invalid unrelated draft", latest); err != nil {
		t.Fatal(err)
	}
	next, err := PrepareRestartEnvironment(context.Background(), c, d, p, old)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := nativeRelease(c, p, next)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(b, &doc)
	for _, name := range []string{"app", "worker"} {
		env := object(object(object(doc["services"])[name])["environment"])
		if env["ZIQX_CORE_INTERNAL_URL"] != "https://ziqx.cc" || env["REMOVED"] != nil || env["LITERAL"] != "a$$HOME$${MISSING}$$$$" {
			t.Fatalf("%s environment was not refreshed literally", name)
		}
	}
	if object(object(doc["services"])["app"])["image"] != "nginx:alpine" {
		t.Fatal("restart changed configured image")
	}
	actualSource, env, err := EditableConfiguration(c, p, next)
	if err != nil || actualSource != source || env != latest {
		t.Fatal("restart lost editable inputs", err)
	}
	// Saving an empty environment must remove previously supplied variables.
	if _, err = SaveDraft(c, p, source, ""); err != nil {
		t.Fatal(err)
	}
	empty, err := PrepareRestartEnvironment(context.Background(), c, d, p, next)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err = nativeRelease(c, p, empty)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &doc)
	if object(object(object(doc["services"])["worker"])["environment"])["ZIQX_CORE_INTERNAL_URL"] != nil {
		t.Fatal("empty saved environment ignored")
	}
}

type serviceEnvironmentComposeRunner struct {
	*serviceRunner
	compose process.Exec
}

func (r serviceEnvironmentComposeRunner) Run(ctx context.Context, binary, dir string, args []string) (process.Result, error) {
	if args[0] == "compose" {
		for _, arg := range args {
			if arg == "config" {
				return r.compose.Run(ctx, binary, dir, args)
			}
		}
	}
	return r.serviceRunner.Run(ctx, binary, dir, args)
}

func TestServiceEnvironmentRefreshWithRealComposeAndUnchangedImage(t *testing.T) {
	if os.Getenv("DOCKYARD_COMPOSE_TEST") != "1" {
		t.Skip("real Compose parser opt-in")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	for _, mode := range []string{"restart", "seamless"} {
		t.Run(mode, func(t *testing.T) {
			d, p, runner := runtimeServiceFixture(t)
			d.Config.DockerBinary = binary
			d.Runner = serviceEnvironmentComposeRunner{runner, process.Exec{Timeout: 20 * time.Second, Limit: 2 << 20, DockerConfig: filepath.Join(home, ".docker")}}
			source := "services:\n  web:\n    image: busybox:latest\n    healthcheck:\n      test: [CMD, 'true']\n    networks: [default]\nnetworks:\n  default:\n    name: original_default\n"
			old, err := d.PrepareNative(context.Background(), p, source, "ZIQX_CORE_INTERNAL_URL=http://host.docker.internal:2999\nREMOVED=old\n")
			if err != nil {
				t.Fatal(err)
			}
			old.ID = "rel-real"
			p.Slots["blue"] = old
			runner.image = runner.containers["web"].Image
			if _, err = SaveDraft(d.Config, p, source, "ZIQX_CORE_INTERNAL_URL=https://ziqx.cc\n"); err != nil {
				t.Fatal(err)
			}
			plan, err := d.PlanServiceUpdate(context.Background(), p, "web", mode, 3000, 3010, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = d.PrepareServiceEnvironment(context.Background(), p, &plan); err != nil {
				t.Fatal(err)
			}
			plan.PreparedImage = runner.image
			if changed, err := d.PullServiceUpdate(context.Background(), p, &plan); err != nil || !changed {
				t.Fatal("same-image env update skipped", changed, err)
			}
			_, b, err := nativeRelease(d.Config, p, plan.Instance.Release)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			json.Unmarshal(b, &doc)
			env := object(object(object(doc["services"])[plan.Instance.Name])["environment"])
			if env["ZIQX_CORE_INTERNAL_URL"] != "https://ziqx.cc" || env["REMOVED"] != nil {
				t.Fatal("replacement retained old environment")
			}
		})
	}
}

func TestAdoptedEnvironmentUsesOriginalRulesInsteadOfFrozenValues(t *testing.T) {
	if os.Getenv("DOCKYARD_COMPOSE_TEST") != "1" {
		t.Skip("real Compose parser opt-in")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	for _, mode := range []string{"restart", "seamless"} {
		t.Run(mode, func(t *testing.T) {
			d, p, runner := runtimeServiceFixture(t)
			d.Config.DockerBinary = binary
			d.Runner = serviceEnvironmentComposeRunner{runner, process.Exec{Timeout: 20 * time.Second, Limit: 2 << 20, DockerConfig: filepath.Join(home, ".docker")}}
			source := "services:\n  web:\n    image: busybox:latest\n    env_file: .env\n    environment:\n      ZIQX_CORE_INTERNAL_URL: ${ZIQX_CORE_INTERNAL_URL}\n      LITERAL: '$$HOME'\n    healthcheck:\n      test: [CMD, 'true']\n    networks: [default]\nnetworks:\n  default:\n    name: original_default\n"
			original := filepath.Join(d.Config.ProjectsRoot, "original-app")
			if err := os.Mkdir(original, 0700); err != nil {
				t.Fatal(err)
			}
			configFile := filepath.Join(original, "compose.yml")
			if err := os.WriteFile(configFile, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			oldEnv := "ZIQX_CORE_INTERNAL_URL=http://host.docker.internal:2999\nREMOVED=old\n"
			if err := os.WriteFile(filepath.Join(original, ".env"), []byte(oldEnv), 0600); err != nil {
				t.Fatal(err)
			}
			// Simulate an old adoption baseline, with inline values already resolved.
			frozen := strings.ReplaceAll(source, "${ZIQX_CORE_INTERNAL_URL}", "http://host.docker.internal:2999")
			frozen = strings.ReplaceAll(frozen, "    env_file: .env\n", "")
			old, err := d.PrepareNative(context.Background(), p, frozen, oldEnv)
			if err != nil {
				t.Fatal(err)
			}
			old.ID = "rel-adopted"
			p.Slots["blue"] = old
			p.Releases = []model.Release{old}
			p.Adoption = &model.Adoption{SourceName: "original-app", ComposeProject: "dy-vps-01-demo-blue", ConfigFiles: []string{configFile}}
			if err := ConsolidateAdoptedProjectDirectory(d.Config, p); err != nil {
				t.Fatal(err)
			}
			if _, err := SaveDraft(d.Config, p, frozen, "ZIQX_CORE_INTERNAL_URL=https://ziqx.cc\n"); err != nil {
				t.Fatal(err)
			}
			check := func(release model.Release, service string) {
				_, b, err := nativeRelease(d.Config, p, release)
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				json.Unmarshal(b, &doc)
				env := object(object(object(doc["services"])[service])["environment"])
				if env["ZIQX_CORE_INTERNAL_URL"] != "https://ziqx.cc" || env["REMOVED"] != nil || env["LITERAL"] != "$$HOME" {
					t.Fatal("adopted environment retained frozen values or interpolated a literal", env)
				}
			}
			// The configuration editor offers recovered rules, so deploying a
			// .env-only edit replaces the frozen value.
			editable, _, err := d.EnvironmentSource(context.Background(), p, old)
			if err != nil || strings.Contains(editable, "host.docker.internal") || !strings.Contains(editable, "${ZIQX_CORE_INTERNAL_URL}") || !strings.Contains(editable, "original_default") {
				t.Fatal("editor kept frozen environment or lost baseline settings", err, editable)
			}
			edited, err := d.PrepareNative(context.Background(), p, editable, "ZIQX_CORE_INTERNAL_URL=https://ziqx.cc\n")
			if err != nil {
				t.Fatal(err)
			}
			check(edited, "web")
			// The desktop editor reformats the baseline, so earlier configuration
			// deployments saved the same frozen values as different text.
			reformatted := strings.Replace(frozen, "services:\n", "# edited in the desktop\nservices:\n", 1)
			redeployed, err := d.PrepareNative(context.Background(), p, reformatted, oldEnv)
			if err != nil {
				t.Fatal(err)
			}
			later := p
			later.Releases = []model.Release{old, redeployed}
			editable, _, err = d.EnvironmentSource(context.Background(), later, redeployed)
			if err != nil || strings.Contains(editable, "host.docker.internal") || !strings.Contains(editable, "${ZIQX_CORE_INTERNAL_URL}") {
				t.Fatal("reformatted baseline kept frozen environment", err, editable)
			}
			if draft, err := d.RestoreAdoptedEnvironment(context.Background(), later, reformatted); err != nil || strings.Contains(draft, "host.docker.internal") {
				t.Fatal("draft kept frozen environment", err, draft)
			}
			full, err := PrepareRestartEnvironment(context.Background(), d.Config, d, p, old)
			if err != nil {
				t.Fatal(err)
			}
			check(full, "web")
			runner.image = runner.containers["web"].Image
			plan, err := d.PlanServiceUpdate(context.Background(), p, "web", mode, 3000, 3010, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := d.PrepareServiceEnvironment(context.Background(), p, &plan); err != nil {
				t.Fatal(err)
			}
			plan.PreparedImage = runner.image
			if changed, err := d.PullServiceUpdate(context.Background(), p, &plan); err != nil || !changed {
				t.Fatal("adopted same-image env update skipped", changed, err)
			}
			check(plan.Instance.Release, plan.Instance.Name)
			if b, err := os.ReadFile(configFile); err != nil || string(b) != source {
				t.Fatal("overwrote original Compose rules")
			}
			// A later full deployment can change the environment rules. Restarts
			// must then use that deployed source, rather than resurrecting old rules.
			newSource := strings.ReplaceAll(source, "${ZIQX_CORE_INTERNAL_URL}", "${NEW_INTERNAL_URL}")
			deployed, err := d.PrepareNative(context.Background(), p, newSource, "NEW_INTERNAL_URL=https://first.example.com\n")
			if err != nil {
				t.Fatal(err)
			}
			p.Slots["blue"] = deployed
			p.Releases = append(p.Releases, deployed)
			if _, err := SaveDraft(d.Config, p, newSource, "NEW_INTERNAL_URL=https://second.example.com\n"); err != nil {
				t.Fatal(err)
			}
			updated, err := PrepareRestartEnvironment(context.Background(), d.Config, d, p, deployed)
			if err != nil {
				t.Fatal(err)
			}
			_, b, err := nativeRelease(d.Config, p, updated)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			json.Unmarshal(b, &doc)
			if object(object(object(doc["services"])["web"])["environment"])["ZIQX_CORE_INTERNAL_URL"] != "https://second.example.com" {
				t.Fatal("restart ignored newly deployed environment rules")
			}
		})
	}
}

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
