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
	"strings"
	"testing"
	"time"
)

// Opt in to exercising the installed Compose parser. No Docker daemon is used.
func TestComposeLiteralEnvironmentRoundTrip(t *testing.T) {
	if os.Getenv("DOCKYARD_COMPOSE_TEST") != "1" {
		t.Skip("set DOCKYARD_COMPOSE_TEST=1 to test the real Compose parser")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("Docker CLI required")
	}
	root := filepath.Join(t.TempDir(), "docker")
	os.Mkdir(root, 0700)
	c := config.Config{ServerID: "vps-01", ProjectsRoot: root, DockerBinary: binary, Templates: map[string]config.Template{"node": {ContainerPort: 3000, HealthCommand: []string{"node", "/app/health.js"}, User: "1000:1000", MemoryMB: 256, CPUs: 1}}}
	p := model.Project{ID: "demo", Template: "node", BluePort: 3001}
	if err = Compose(c, p); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"DOLLAR": "a$HOME${MISSING}$$", "QUOTES": "'single' and \"double\"", "HASH": "abc # literal", "EQUALS": "x=y=z", "EMPTY": "", "SPACES": "  spaced  ", "SLASH": "back\\slash"}
	env, err := Environment(c, p.ID, values)
	if err != nil {
		t.Fatal(err)
	}
	r := model.Release{Image: "ghcr.io/ziqx/demo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Environment: env}
	if err = Binding(c, p, "blue", r); err != nil {
		t.Fatal(err)
	}
	dockerConfig := os.Getenv("DOCKER_CONFIG")
	if dockerConfig == "" {
		homeDir, _ := os.UserHomeDir()
		dockerConfig = filepath.Join(homeDir, ".docker")
	}
	d := Docker{Config: c, Runner: process.Exec{Timeout: 20 * time.Second, Limit: 2 << 20, DockerConfig: dockerConfig}}
	result, err := d.command(context.Background(), p, "blue", "config", "--format", "json")
	if err != nil {
		t.Fatal("Compose render failed")
	}
	var rendered struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	// Compose's config command doubles every $ when serializing a reusable
	// configuration (cmd/compose/config.go runConfig). Undo that presentation
	// escape once before comparing the resolved raw environment values.
	output := strings.ReplaceAll(string(result.Output), "$$", "$")
	if err = json.Unmarshal([]byte(output), &rendered); err != nil {
		t.Fatal("Compose returned invalid JSON")
	}
	for k, value := range values {
		if rendered.Services["app"].Environment[k] != value {
			t.Fatalf("literal environment roundtrip failed for %s: got %q expected %q", k, rendered.Services["app"].Environment[k], value)
		}
	}
}

func TestSubmittedMultiServiceComposeRoundTrip(t *testing.T) {
	if os.Getenv("DOCKYARD_COMPOSE_TEST") != "1" {
		t.Skip("set DOCKYARD_COMPOSE_TEST=1 to test the real Compose parser")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	c, p, source := composeFixture(t)
	c.DockerBinary = binary
	source += "    volumes: ['data:/var/lib/postgresql/data']\nvolumes: {data: {}}\n"
	homeDir, _ := os.UserHomeDir()
	d := Docker{Config: c, Runner: process.Exec{Timeout: 20 * time.Second, Limit: 2 << 20, DockerConfig: filepath.Join(homeDir, ".docker")}}
	volumeName := ""
	for _, digest := range []string{strings.Repeat("a", 64), strings.Repeat("c", 64)} {
		r := preparedRelease(t, c, p, strings.Replace(source, strings.Repeat("a", 64), digest, 1))
		if err := d.Validate(context.Background(), p, r); err != nil {
			t.Fatal("normalized Compose rejected", err)
		}
		p.Slots["blue"] = r
		Binding(c, p, "blue", r)
		result, err := d.command(context.Background(), p, "blue", "config", "--format", "json")
		if err != nil {
			t.Fatal(err)
		}
		var rendered struct {
			Services map[string]struct {
				Environment map[string]string
				Command     []string
				Ports       []struct {
					HostIP    string `json:"host_ip"`
					Published string
					Target    int
				}
				ReadOnly bool `json:"read_only"`
			}
			Volumes map[string]struct{ Name string }
		}
		if err := json.Unmarshal([]byte(strings.ReplaceAll(string(result.Output), "$$", "$")), &rendered); err != nil {
			t.Fatal(err)
		}
		app, db := rendered.Services["app"], rendered.Services["db"]
		if app.Environment["TOKEN"] != "app-secret-$HOME" || db.Environment["PASSWORD"] != "companion-secret" || len(app.Command) != 2 || app.Command[1] != "$LITERAL" {
			t.Fatal("Compose changed literal environment/command values")
		}
		if len(app.Ports) != 1 || app.Ports[0].HostIP != "127.0.0.1" || app.Ports[0].Published != "3001" || len(db.Ports) != 0 || !app.ReadOnly || !db.ReadOnly {
			t.Fatal("Compose changed enforced runtime policy")
		}
		name := rendered.Volumes["data"].Name
		if name != "dy-vps-01-demo-blue_data" || volumeName != "" && volumeName != name {
			t.Fatal("persistent volume identity changed across releases", name)
		}
		volumeName = name
	}
}
