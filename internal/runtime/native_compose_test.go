package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

func TestNativeComposeDoesNotRequireTemplate(t *testing.T) {
	doc := map[string]any{"name": "user-name", "services": map[string]any{"web_api": map[string]any{"image": "nginx:alpine", "privileged": true, "build": map[string]any{"context": "/docker/context"}, "volumes": []any{map[string]any{"type": "bind", "source": "/var/run/docker.sock", "target": "/var/run/docker.sock"}}, "ports": []any{map[string]any{"target": float64(80), "published": "8080"}}}}}
	p := model.Project{ID: "demo", Mode: "compose"}
	if err := compileNative(config.Config{ServerID: "server"}, p, doc); err != nil {
		t.Fatal(err)
	}
	svc := object(object(doc["services"])["web_api"])
	if doc["name"] != nil || svc["image"] != "nginx:alpine" || svc["privileged"] != true || len(array(svc["volumes"])) != 1 || len(array(svc["ports"])) != 1 || object(svc["labels"])["io.ziqx.dockyard.project"] != "demo" {
		t.Fatal("Compose configuration lost", doc)
	}
}

func TestNativeBlueGreenRejectsSharedState(t *testing.T) {
	for _, field := range []string{"volumes", "container_name", "network_mode", "ports", "healthcheck"} {
		t.Run(field, func(t *testing.T) {
			svc := map[string]any{"image": "nginx:alpine", "healthcheck": map[string]any{"test": []any{"CMD", "true"}}}
			switch field {
			case "volumes":
				svc[field] = []any{map[string]any{"source": "data", "target": "/data"}}
			case "ports":
				svc[field] = []any{map[string]any{"target": float64(90), "published": "8080"}}
			case "healthcheck":
				svc[field] = map[string]any{"disable": true}
			default:
				svc[field] = "shared"
			}
			doc := map[string]any{"services": map[string]any{"web": svc}}
			p := model.Project{ID: "demo", Mode: "compose", ZeroDowntime: true, RouteService: "web", RoutePort: 80, Domains: []string{"example.com"}}
			if compileNative(config.Config{}, p, doc) == nil {
				t.Fatal("accepted unsafe blue-green", field)
			}
		})
	}
}

// Uses Docker's real Compose loader only; no daemon or live containers.
func TestNativeComposeRoundTrip(t *testing.T) {
	if os.Getenv("DOCKYARD_COMPOSE_TEST") != "1" {
		t.Skip("set DOCKYARD_COMPOSE_TEST=1 for the installed Compose parser")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	c := config.Config{ServerID: "vps-01", ProjectsRoot: t.TempDir(), DockerBinary: binary}
	p := model.Project{ID: "native", Mode: "compose", Environment: model.Production, Slots: map[string]model.Release{}}
	if err := Compose(c, p); err != nil {
		t.Fatal(err)
	}
	d := Docker{Config: c, Runner: process.Exec{Timeout: 20 * time.Second, Limit: 2 << 20, DockerConfig: filepath.Join(home, ".docker")}}
	source := `name: ignored
services:
  web_api:
    image: nginx:${TAG}
    env_file: ${ENV_FILE:-.env}
    environment:
      INLINE: '$$HOME literal'
    ports: ['8080:80']
    volumes: [data:/data]
    command: [echo, '$$LITERAL']
  database:
    image: postgres:17
    environment:
      POSTGRES_PASSWORD: ${PASSWORD}
    volumes: [data:/shared]
  worker:
    image: busybox:latest
    profiles: [background]
    command: [sleep, infinity]
  reports:
    image: busybox:latest
    profiles: [reports]
    command: [sleep, infinity]
volumes:
  data: {}
`
	dotenv := "TAG=alpine\nPASSWORD='a$HOME${NOT_SET}$$'\nCOMPOSE_PROFILES=background\n"
	r, err := d.PrepareNative(context.Background(), p, source, dotenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := Binding(c, p, "blue", r); err != nil {
		t.Fatal(err)
	}
	p.Slots["blue"] = r
	rendered, err := d.command(context.Background(), p, "blue", "config", "--format", "json")
	if err != nil {
		t.Fatalf("normalized config failed: %s", rendered.Stderr)
	}
	var doc map[string]any
	if err = json.Unmarshal([]byte(strings.ReplaceAll(string(rendered.Output), "$$", "$")), &doc); err != nil {
		t.Fatal(err)
	}
	web := object(object(doc["services"])["web_api"])
	env := object(web["environment"])
	if web["image"] != "nginx:alpine" || env["PASSWORD"] != "a$HOME${NOT_SET}$$" || env["INLINE"] != "$HOME literal" || array(web["command"])[1] != "$LITERAL" {
		t.Fatalf("literal values were re-interpolated: %#v command=%#v", env, web["command"])
	}
	if len(array(web["volumes"])) != 1 || len(array(web["ports"])) != 1 || len(object(doc["services"])) != 4 {
		t.Fatal("stack features were lost")
	}
	for _, name := range []string{"worker", "reports"} {
		svc := object(object(doc["services"])[name])
		if svc == nil || svc["profiles"] != nil {
			t.Fatalf("profile service %s is missing or still gated", name)
		}
	}
	b, err := ComposeMirror(c, p, r)
	if err != nil || string(b) != source {
		t.Fatal("source not preserved", err)
	}
	if err := os.WriteFile(envPath(c, p.ID, r.Environment), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReleaseCompose(c, p, r); err == nil {
		t.Fatal("accepted changed dotenv")
	}
}

func TestNativeBlueGreenSlotRoundTrip(t *testing.T) {
	if os.Getenv("DOCKYARD_COMPOSE_TEST") != "1" {
		t.Skip("real Compose parser opt-in")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	c := config.Config{ServerID: "vps-01", ProjectsRoot: t.TempDir(), DockerBinary: binary}
	p := model.Project{ID: "bg", Mode: "compose", Environment: model.Production, ZeroDowntime: true, RouteService: "frontend", RoutePort: 80, Domains: []string{"example.com"}, BluePort: 4001, GreenPort: 4002, Slots: map[string]model.Release{}}
	if err := Compose(c, p); err != nil {
		t.Fatal(err)
	}
	d := Docker{Config: c, Runner: process.Exec{Timeout: 20 * time.Second, Limit: 2 << 20, DockerConfig: filepath.Join(home, ".docker")}}
	r, err := d.PrepareNative(context.Background(), p, "services:\n  frontend:\n    image: nginx:alpine\n    ports: ['80:80']\n    healthcheck:\n      test: ['CMD', 'true']\n", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range []string{"blue", "green"} {
		p.Slots[slot] = r
		if err := Binding(c, p, slot, r); err != nil {
			t.Fatal(err)
		}
		result, err := d.command(context.Background(), p, slot, "config", "--format", "json")
		if err != nil {
			t.Fatal("slot config failed", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(result.Output, &doc); err != nil {
			t.Fatal(err)
		}
		name := object(object(doc["networks"])["default"])["name"]
		if name != "dy-vps-01-bg-"+slot+"_default" {
			t.Fatal("slots share network", name)
		}
		ports := array(object(object(doc["services"])["frontend"])["ports"])
		if len(ports) != 1 || object(ports[0])["host_ip"] != "127.0.0.1" || object(ports[0])["published"] != strconv.Itoa(p.Port(slot)) {
			t.Fatal("slot route incorrect", ports)
		}
	}
}
