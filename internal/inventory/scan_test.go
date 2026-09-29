package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
)

type fixtureRunner struct {
	calls                 [][]string
	dockerFail, caddyFail bool
}

func (r *fixtureRunner) Run(_ context.Context, binary, dir string, args []string) (process.Result, error) {
	r.calls = append(r.calls, append([]string{binary}, args...))
	if binary == "/usr/bin/docker" && len(args) > 0 && args[0] == "ps" {
		if r.dockerFail {
			return process.Result{}, errors.New("offline")
		}
		return process.Result{Output: []byte(`"0.0.0.0:3300->80/tcp, [::]:3300->80/tcp"` + "\n")}, nil
	}
	if binary == "/usr/bin/caddy" && args[0] == "adapt" {
		if r.caddyFail {
			return process.Result{}, errors.New("offline")
		}
		return process.Result{Output: []byte(`{"apps":{"http":{"servers":{"srv0":{"routes":[{"match":[{"host":["shop.example.com"]}],"handle":[{"handler":"subroute","routes":[{"handle":[{"handler":"reverse_proxy","upstreams":[{"dial":"127.0.0.1:3200"}]}]}]}]},{"match":[{"host":["static.example.com"]}],"handle":[{"handler":"static_response","body":"caddy-secret-body"}]}]}}}}}`)}, nil
	}
	return process.Result{}, errors.New("unexpected privileged command")
}

func scannerFixture(t *testing.T) (Scanner, *fixtureRunner, string) {
	t.Helper()
	root := t.TempDir()
	docker := filepath.Join(root, "docker")
	os.Mkdir(docker, 0700)
	project := filepath.Join(docker, "shop")
	os.Mkdir(project, 0700)
	source := "services:\n  app:\n    image: ghcr.io/org/shop:v1\n    ports: ['127.0.0.1:3200:3000']\n    environment: {TOKEN: compose-secret-value}\n    command: [run, command-secret-value]\n  db:\n    image: postgres:16\n"
	os.WriteFile(filepath.Join(project, "compose.yaml"), []byte(source), 0600)
	os.WriteFile(filepath.Join(project, ".env"), []byte("TOKEN=dotenv-secret-value"), 0600)
	caddy := filepath.Join(root, "Caddyfile")
	os.WriteFile(caddy, []byte("shop.example.com { reverse_proxy 127.0.0.1:3200 }"), 0600)
	s, err := state.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := &fixtureRunner{}
	return Scanner{Config: config.Config{ProjectsRoot: docker, Caddyfile: caddy, DockerBinary: "/usr/bin/docker", CaddyBinary: "/usr/bin/caddy"}, Store: s, Runner: r, Check: func(string, bool) error { return nil }}, r, project
}

func TestExistingSyncIsSafeDurableAndReservesPorts(t *testing.T) {
	s, r, project := scannerFixture(t)
	before, _ := os.ReadFile(filepath.Join(project, "compose.yaml"))
	v, err := s.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Projects) != 1 || v.Projects[0].Environment != model.Production || v.Projects[0].Managed || len(v.Projects[0].Services) != 2 || len(v.Sites) != 2 {
		t.Fatal("existing metadata not synced", v)
	}
	if v.Sites[0].HostMatcher != "shop.example.com" || len(v.Sites[0].ProjectIDs) != 1 || v.Sites[0].ProjectIDs[0] != v.Projects[0].ID {
		t.Fatal("local route association wrong", v.Sites)
	}
	b, _ := json.Marshal(v)
	for _, secret := range []string{"compose-secret-value", "command-secret-value", "dotenv-secret-value", "caddy-secret-body"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("secret persisted", secret)
		}
	}
	after, _ := os.ReadFile(filepath.Join(project, "compose.yaml"))
	if string(before) != string(after) {
		t.Fatal("changed existing Compose")
	}
	if len(r.calls) != 2 || r.calls[0][1] != "ps" || r.calls[1][1] != "adapt" {
		t.Fatal("sync changed live resources", r.calls)
	}
	for _, port := range []int{3200, 3300} {
		if reserved, err := s.Store.Reserved(port, "new"); err != nil || !reserved {
			t.Fatal("observed port not reserved", port, err)
		}
	}
	if managed, _ := s.Store.Projects(); len(managed) != 0 {
		t.Fatal("inventory silently adopted deployments")
	}
	dir := filepath.Dir(s.Config.Caddyfile)
	s.Store.Close()
	db, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stored, err := db.Inventory()
	if err != nil || len(stored.Projects) != 1 || len(stored.Sites) != 2 {
		t.Fatal("inventory not durable", err)
	}
}

func TestFailedSourcesAndMissingProjectsKeepLastKnownInventory(t *testing.T) {
	s, r, project := scannerFixture(t)
	old, err := s.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r.dockerFail = true
	r.caddyFail = true
	os.WriteFile(filepath.Join(project, "compose.yaml"), []byte("invalid: ["), 0600)
	v, err := s.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sites) != 2 || len(v.Projects[0].Services) != 2 || len(v.Projects[0].Warnings) == 0 || !v.CaddyObservedAt.Equal(*old.CaddyObservedAt) || !v.DockerObservedAt.Equal(*old.DockerObservedAt) {
		t.Fatal("failed scan destroyed last success", v)
	}
	os.RemoveAll(project)
	v, err = s.Sync(context.Background())
	if err != nil || len(v.Projects) != 1 || v.Projects[0].Present {
		t.Fatal("disappeared project lost history", v, err)
	}
	if reserved, _ := s.Store.Reserved(3200, ""); !reserved {
		t.Fatal("released an uncertain legacy port")
	}
}

func TestComposeMetadataDoesNotFollowAliasesOrUnsafeFiles(t *testing.T) {
	for _, source := range []string{"services: {app: {image: postgres:16, image: alpine}}", "services: {app: &app {image: postgres:16}, db: *app}", "services: {}\n---\nservices: {}"} {
		if _, ok := parseCompose([]byte(source)); ok {
			t.Fatal("accepted ambiguous source")
		}
	}
	s, _, project := scannerFixture(t)
	path := filepath.Join(project, "compose.yaml")
	os.Remove(path)
	os.Symlink(filepath.Join(project, ".env"), path)
	v, err := s.Sync(context.Background())
	if err != nil || len(v.Projects[0].Warnings) == 0 || len(v.Projects[0].Services) != 0 {
		t.Fatal("followed Compose symlink", v, err)
	}
}

func TestInventorySupportsOneHundredExistingProjects(t *testing.T) {
	s, _, project := scannerFixture(t)
	os.RemoveAll(project)
	for i := 0; i < 100; i++ {
		dir := filepath.Join(s.Config.ProjectsRoot, fmt.Sprintf("service-%03d", i))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("services:\n  app:\n    image: ghcr.io/org/app:v1\n    ports: ['%d:3000']\n", 5200+i)
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	v, err := s.Sync(context.Background())
	if err != nil || len(v.Projects) != 100 {
		t.Fatal("100-project inventory failed", len(v.Projects), err)
	}
	ids := map[string]bool{}
	for _, p := range v.Projects {
		if ids[p.ID] || p.Environment != model.Production {
			t.Fatal("target identity collision")
		}
		ids[p.ID] = true
	}
	if reserved, _ := s.Store.Reserved(5299, ""); !reserved {
		t.Fatal("last project's port omitted")
	}
}
