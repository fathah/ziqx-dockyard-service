package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

type publishedRunner struct{ container Container }

func (r *publishedRunner) Run(_ context.Context, _ string, _ string, args []string) (process.Result, error) {
	if args[0] == "inspect" {
		b, _ := json.Marshal([]Container{r.container})
		return process.Result{Output: b}, nil
	}
	if slices.Contains(args, "ps") {
		return process.Result{Output: []byte(r.container.ID + "\n")}, nil
	}
	return process.Result{Output: []byte(`{"services":{"web":{"image":"nginx","ports":[{"target":80,"published":"3031","host_ip":"0.0.0.0","protocol":"tcp"}]}}}`)}, nil
}

func TestPublishedRouteChecksExistingBindingWithoutChangingComposePorts(t *testing.T) {
	for _, test := range []struct {
		name, ip, port            string
		running, foreign, allowed bool
	}{
		{"public-existing", "0.0.0.0", "3031", true, false, true},
		{"private-existing", "127.0.0.1", "3031", true, false, true},
		{"different-port", "0.0.0.0", "3032", true, false, false},
		{"ipv6-only", "::", "3031", true, false, false},
		{"stopped", "0.0.0.0", "3031", false, false, false},
		{"foreign-identity", "0.0.0.0", "3031", true, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := config.Config{ServerID: "vps", ProjectsRoot: t.TempDir(), DockerBinary: "docker"}
			p := model.Project{ID: "demo", AppID: "demo", Mode: "compose", Environment: model.Production, State: "running", Active: "blue", Adoption: &model.Adoption{SourceName: "legacy", ComposeProject: "legacy"}, Slots: map[string]model.Release{}}
			if err := Compose(c, p); err != nil {
				t.Fatal(err)
			}
			runner := &publishedRunner{}
			runner.container.ID = strings.Repeat("a", 64)
			runner.container.State.Running = test.running
			runner.container.Config.Labels = map[string]string{"com.docker.compose.project": "legacy", "com.docker.compose.service": "web", "io.ziqx.dockyard.server": "vps", "io.ziqx.dockyard.project": "demo"}
			if test.foreign {
				runner.container.Config.Labels["io.ziqx.dockyard.project"] = "other"
			}
			json.Unmarshal([]byte(fmt.Sprintf(`{"Ports":{"80/tcp":[{"HostIP":%q,"HostPort":%q}]}}`, test.ip, test.port)), &runner.container.NetworkSettings)
			d := Docker{Config: c, Runner: runner}
			release, err := d.PrepareNative(context.Background(), p, "services: {}", "")
			if err != nil {
				t.Fatal(err)
			}
			p.Slots["blue"] = release
			if err = Binding(c, p, "blue", release); err != nil {
				t.Fatal(err)
			}
			p.BluePort = 3031
			p.Domains = []string{"app.example.com"}
			p.PublishedRoute = &model.PublishedRoute{Service: "web", ContainerPort: 80, HostPort: 3031}
			err = d.Healthy(context.Background(), p, "blue", release)
			if (err == nil) != test.allowed {
				t.Fatal("binding verification", err, test.allowed)
			}
			_, raw, err := nativeRelease(c, p, release)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			json.Unmarshal(raw, &doc)
			ports := array(object(object(doc["services"])["web"])["ports"])
			if len(ports) != 1 || object(ports[0])["published"] != "3031" || object(ports[0])["host_ip"] != "0.0.0.0" {
				t.Fatal("route setup changed published binding", ports)
			}
		})
	}
}

func TestPublishedRouteImportServesExistingInstance(t *testing.T) {
	p := model.Project{Active: "blue", BluePort: 3031, GreenPort: 0, Domains: []string{"tasks.example.com"}, PublishedRoute: &model.PublishedRoute{Service: "web", ContainerPort: 3000, HostPort: 3031}}
	if importSlot(p) != "blue" || !strings.Contains(string(Snippet(p, importSlot(p))), "127.0.0.1:3031") {
		t.Fatal("import targeted a different instance")
	}
}

func TestPublishedRouteLimitsSeamlessUpdatesToItsWebsiteService(t *testing.T) {
	d, p, _ := runtimeServiceFixture(t)
	p.RouteService = ""
	p.Adoption = &model.Adoption{SourceName: "legacy", ComposeProject: "legacy"}
	p.PublishedRoute = &model.PublishedRoute{Service: "web", ContainerPort: 3000, HostPort: 3031}
	p.BluePort = 3031
	options, err := ServiceUpdateOptions(d.Config, p)
	if err != nil || !options["web"].Seamless || options["postgres"].Seamless {
		t.Fatal("route service eligibility", options, err)
	}
	p.PublishedRoute.Service = "other"
	options, err = ServiceUpdateOptions(d.Config, p)
	if err != nil || options["web"].Seamless {
		t.Fatal("unrouted service could change traffic", options, err)
	}
}
