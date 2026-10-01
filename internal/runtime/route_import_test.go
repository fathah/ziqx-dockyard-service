package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractReviewedPlainSites(t *testing.T) {
	source := "{\n admin unix//run/caddy.sock\n}\napp.example.com, www.example.com {\n reverse_proxy localhost:3000\n}\nother.example.com {\n reverse_proxy 127.0.0.1:4000\n}\nimport sites/*.caddy\n"
	out, err := extractSites(source, []string{"app.example.com", "www.example.com"}, "127.0.0.1:3000")
	if err != nil || strings.Contains(out, "app.example.com") || !strings.Contains(out, "other.example.com") || !strings.Contains(out, "import sites/*.caddy") {
		t.Fatal(out, err)
	}
}
func TestImportDoesNotDropCustomRoutes(t *testing.T) {
	for _, source := range []string{
		"app.example.com {\n basic_auth {\n user hash\n }\n reverse_proxy localhost:3000\n}",
		"app.example.com {\n reverse_proxy localhost:3000 localhost:3001\n}",
		"app.example.com,other.example.com { reverse_proxy localhost:3000 }",
		"outer {\napp.example.com { reverse_proxy localhost:3000 }\n}",
		"app.example.com { reverse_proxy localhost:4000 }",
		"app.example.com { reverse_proxy localhost:3000 }\napp.example.com { reverse_proxy localhost:3000 }",
	} {
		if _, err := extractSites(source, []string{"app.example.com"}, "127.0.0.1:3000"); err == nil {
			t.Fatal("accepted ambiguous/custom route", source)
		}
	}
}

type importRunner struct {
	root, site, mode string
	live             any
	unavailable      bool
}

func (f *importRunner) config() any {
	a, _ := os.ReadFile(f.root)
	b, _ := os.ReadFile(f.site)
	all := string(a) + "\n" + string(b)
	routes := []any{}
	for _, m := range plainSite.FindAllStringSubmatch(all, -1) {
		hosts := []any{}
		for _, h := range strings.Fields(strings.ReplaceAll(m[1], ",", " ")) {
			hosts = append(hosts, h)
		}
		routes = append(routes, map[string]any{"match": []any{map[string]any{"host": hosts}}, "handle": []any{map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": m[2]}}}}})
	}
	return map[string]any{"apps": map[string]any{"http": map[string]any{"servers": map[string]any{"srv0": map[string]any{"routes": routes}}}}}
}
func (f *importRunner) Run(_ context.Context, _, _ string, args []string) (process.Result, error) {
	switch args[0] {
	case "adapt":
		b, _ := json.Marshal(f.config())
		return process.Result{Output: b}, nil
	case "validate":
		if f.mode == "invalid" {
			return process.Result{}, errors.New("invalid")
		}
	case "reload":
		if f.mode == "reject" {
			return process.Result{}, errors.New("rejected")
		}
		if f.mode == "unknown" {
			f.unavailable = true
			return process.Result{}, errors.New("timeout")
		}
		f.live = f.config()
		if f.mode == "applied-timeout" {
			return process.Result{}, errors.New("timeout")
		}
	}
	return process.Result{}, nil
}
func TestRouteImportClassificationAndRollback(t *testing.T) {
	for _, mode := range []string{"success", "reject", "invalid", "unknown", "applied-timeout"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "Caddyfile")
			site := filepath.Join(dir, "demo.caddy")
			original := "app.example.com {\n reverse_proxy localhost:3000\n}\nother.example.com { reverse_proxy localhost:4000 }\nimport *.caddy\n"
			os.WriteFile(root, []byte(original), 0644)
			f := &importRunner{root: root, site: site, mode: mode}
			f.live = f.config()
			c := Caddy{Config: config.Config{Caddyfile: root, CaddySites: dir}, Runner: f, client: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
				if f.unavailable {
					return nil, errors.New("offline")
				}
				b, _ := json.Marshal(f.live)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
			})}}
			p := model.Project{ID: "demo", Domains: []string{"app.example.com"}, GreenPort: 3011, ZeroDowntime: true}
			edits, err := c.PlanRouteImport(context.Background(), p, "127.0.0.1:3000")
			if err != nil {
				t.Fatal(err)
			}
			err = c.ImportRoutes(context.Background(), edits, p)
			b, _ := os.ReadFile(root)
			switch mode {
			case "success", "applied-timeout":
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(b), "app.example.com") {
					t.Fatal("original route duplicated")
				}
				f.mode = "success"
				if err = c.RestoreImportedRoutes(context.Background(), edits, p); err != nil {
					t.Fatal(err)
				}
				b, _ = os.ReadFile(root)
				if string(b) != original {
					t.Fatal("rollback lost original")
				}
			case "reject", "invalid":
				if err == nil || string(b) != original {
					t.Fatal("known failure changed route", err)
				}
				if _, err = os.Stat(site); !os.IsNotExist(err) {
					t.Fatal("left duplicate site")
				}
			case "unknown":
				var fault *model.Fault
				if !errors.As(err, &fault) || !fault.Recovery {
					t.Fatal("unknown outcome not blocked", err)
				}
			}
		})
	}
}

func TestRouteImportRequiresWritableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory mode restrictions; use the read-only mount test")
	}
	for _, blocked := range []string{"main", "sites"} {
		t.Run(blocked, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "Caddyfile")
			sites := filepath.Join(dir, "sites")
			if err := os.Mkdir(sites, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(root, []byte("app.example.com { reverse_proxy localhost:3000 }\n"), 0644); err != nil {
				t.Fatal(err)
			}
			path := dir
			if blocked == "sites" {
				path = sites
			}
			if err := os.Chmod(path, 0555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(path, 0755) })
			c, p := checkRouteImportWritePreflight(t, root, sites)
			if err := os.Chmod(path, 0755); err != nil {
				t.Fatal(err)
			}
			if edits, err := c.PlanRouteImport(context.Background(), p, "127.0.0.1:3000"); err != nil || len(edits) != 1 {
				t.Fatal("review still blocked after directory access was restored", edits, err)
			}
		})
	}
}

// Opt-in Linux regression: mount a fixture containing Caddyfile read-only and
// set this path in a disposable container. Root must also fail the preflight,
// as it does in the service's ProtectSystem=strict mount namespace.
func TestRouteImportReadOnlyMount(t *testing.T) {
	dir := os.Getenv("DOCKYARD_TEST_READONLY_CADDY_DIR")
	if dir == "" {
		t.Skip("requires a disposable read-only Caddyfile fixture mount")
	}
	checkRouteImportWritePreflight(t, filepath.Join(dir, "Caddyfile"), t.TempDir())
}

func checkRouteImportWritePreflight(t *testing.T, root, sites string) (Caddy, model.Project) {
	t.Helper()
	before, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(sites, "demo.caddy")
	f := &importRunner{root: root, site: site, mode: "success"}
	f.live = f.config()
	c := Caddy{Config: config.Config{Caddyfile: root, CaddySites: sites}, Runner: f, client: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		b, _ := json.Marshal(f.live)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})}}
	p := model.Project{ID: "demo", Domains: []string{"app.example.com"}, GreenPort: 3011, ZeroDowntime: true}
	edits, err := c.PlanRouteImport(context.Background(), p, "127.0.0.1:3000")
	var fault *model.Fault
	if !errors.As(err, &fault) || fault.Code != "CADDY_CONFIG_WRITE_REQUIRED" || fault.Recovery || len(edits) != 0 {
		t.Fatal("unwritable route import was not rejected before deployment", edits, err)
	}
	after, err := os.ReadFile(root)
	if err != nil || string(after) != string(before) {
		t.Fatal("preflight changed the original route", err)
	}
	entries, err := os.ReadDir(sites)
	if err != nil || len(entries) != 0 {
		t.Fatal("preflight wrote a generated snippet", entries, err)
	}
	return c, p
}
