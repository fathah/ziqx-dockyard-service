package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

func TestRegistryImageIDMatchesPlatformConfigNotManifestDigest(t *testing.T) {
	current := "sha256:" + strings.Repeat("1", 64)
	next := "sha256:" + strings.Repeat("2", 64)
	manifest := func(os, arch, variant, id, format string) map[string]any {
		return map[string]any{"Descriptor": map[string]any{"digest": "sha256:" + strings.Repeat("f", 64), "platform": map[string]string{"os": os, "architecture": arch, "variant": variant}}, format: map[string]any{"config": map[string]string{"digest": id}}}
	}
	tests := []struct {
		name                    string
		value                   any
		os, arch, variant, want string
	}{
		{"multi arch", []any{manifest("linux", "arm64", "v8", current, "OCIManifest"), manifest("unknown", "unknown", "", next, "OCIManifest"), manifest("linux", "amd64", "", next, "SchemaV2Manifest")}, "linux", "amd64", "", next},
		{"oci single", manifest("linux", "arm64", "v8", current, "OCIManifest"), "linux", "arm64", "", current},
		{"arm variant", []any{manifest("linux", "arm", "v6", current, "OCIManifest"), manifest("linux", "arm", "v7", next, "OCIManifest")}, "linux", "arm", "v7", next},
		{"wrong platform", manifest("linux", "arm64", "", next, "OCIManifest"), "linux", "amd64", "", ""},
		{"ambiguous", []any{manifest("linux", "amd64", "", current, "OCIManifest"), manifest("linux", "amd64", "", next, "OCIManifest")}, "linux", "amd64", "", ""},
		{"invalid config", manifest("linux", "amd64", "", "bad", "OCIManifest"), "linux", "amd64", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(tt.value)
			got, err := registryImageID(raw, tt.os, tt.arch, tt.variant)
			if got != tt.want || (err != nil) != (tt.want == "") {
				t.Fatal(got, err)
			}
		})
	}
}

func TestPreparedServiceImageNeverRepullsMutableTag(t *testing.T) {
	d, p, runner := runtimeServiceFixture(t)
	plan, err := d.PlanServiceUpdate(context.Background(), p, "web", "seamless", 3000, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	image, err := d.PrepareServiceImage(context.Background(), p, "web")
	if err != nil {
		t.Fatal(err)
	}
	plan.PreparedImage = image
	runner.calls = nil
	changed, err := d.PullServiceUpdate(context.Background(), p, &plan)
	if err != nil || !changed || plan.Instance.Release.Image != image {
		t.Fatal(changed, err, plan)
	}
	for _, args := range runner.calls {
		if args[0] == "compose" && args[9] == "pull" {
			t.Fatal("prepared image pulled twice", args)
		}
		if args[0] == "image" && args[len(args)-1] != image {
			t.Fatal("inspected mutable tag instead of prepared digest", args)
		}
	}
	// Image removal/replacement must fail without falling back to a new tag.
	runner.image = "sha256:" + strings.Repeat("3", 64)
	if _, err = d.PullServiceUpdate(context.Background(), p, &plan); err == nil {
		t.Fatal("lost prepared image accepted")
	}
}

type imageCheckRunner struct {
	*serviceRunner
	manifest []byte
	fail     bool
}

func (r *imageCheckRunner) Run(ctx context.Context, binary, dir string, args []string) (process.Result, error) {
	if args[0] == "manifest" {
		r.calls = append(r.calls, args)
		if r.fail {
			return process.Result{}, context.DeadlineExceeded
		}
		return process.Result{Output: r.manifest}, nil
	}
	if args[0] == "image" {
		r.calls = append(r.calls, args)
		b, _ := json.Marshal([]any{map[string]any{"Id": args[len(args)-1], "Os": "linux", "Architecture": "amd64"}})
		return process.Result{Output: b}, nil
	}
	return r.serviceRunner.Run(ctx, binary, dir, args)
}
func TestImageCheckReadsRegistryWithoutPullingOrChangingContainers(t *testing.T) {
	d, p, base := runtimeServiceFixture(t)
	base.doc = strings.ReplaceAll(base.doc, "example/app:latest", "ghcr.io/example/app:latest")
	r, err := d.PrepareNative(context.Background(), p, "services: supplied", "TOKEN=keep$secret\n")
	if err != nil {
		t.Fatal(err)
	}
	r.ID = "rel-ghcr"
	p.Slots["blue"] = r
	runner := &imageCheckRunner{serviceRunner: base, manifest: []byte(`{"Descriptor":{"platform":{"os":"linux","architecture":"amd64"}},"OCIManifest":{"config":{"digest":"sha256:` + strings.Repeat("2", 64) + `"}}}`)}
	d.Runner = runner
	base.calls = nil
	statuses, err := d.CheckServiceImages(context.Background(), p)
	if err != nil || len(statuses) != 1 || statuses[0].Status != "available" {
		t.Fatal(statuses, err)
	}
	for _, args := range runner.calls {
		if args[0] == "compose" && args[9] != "ps" {
			t.Fatal("check changed Docker state", args)
		}
	}
	runner.manifest = []byte(strings.ReplaceAll(string(runner.manifest), strings.Repeat("2", 64), strings.Repeat("1", 64)))
	statuses, err = d.CheckServiceImages(context.Background(), p)
	if err != nil || statuses[0].Status != "current" {
		t.Fatal(statuses, err)
	}
	runner.fail = true
	statuses, err = d.CheckServiceImages(context.Background(), p)
	if err != nil || statuses[0].Status != "unknown" || statuses[0].Error != "REGISTRY_CHECK_FAILED" {
		t.Fatal("registry failure reported current", statuses, err)
	}
}
