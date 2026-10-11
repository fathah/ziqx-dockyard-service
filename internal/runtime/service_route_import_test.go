package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

func TestServiceSiteMovePreservesCustomRulesAndSharedHosts(t *testing.T) {
	source := "# operator config\n{\n admin unix//run/caddy/admin.sock\n}\n" +
		"(shared) {\n header X-Shared yes\n}\n" +
		"api.example.com, www.example.com {\n # quoted braces are data\n header X-Data `{" + `"key":"}"` + "}`\n basic_auth {\n user private_hash\n }\n handle /api/* {\n reverse_proxy localhost:8080 {\n header_up X-Forwarded-Host {host}\n }\n }\n}\n" +
		"other.example.com {\n reverse_proxy 127.0.0.1:4000\n}\nimport /etc/caddy/dockyard/*.caddy\n"
	after, templates, existing, err := planServiceSiteMove(source, []string{"api.example.com"}, nil, false)
	if err != nil || len(templates) != 1 || len(existing) != 1 {
		t.Fatal(after, templates, existing, err)
	}
	if strings.Contains(after, "api.example.com") || !strings.Contains(after, "www.example.com {") || !strings.Contains(after, "private_hash") || !strings.Contains(after, "other.example.com {\n reverse_proxy 127.0.0.1:4000\n}") || !strings.Contains(after, "import /etc/caddy/dockyard/*.caddy") {
		t.Fatal("lost an unrelated rule", after)
	}
	if existing[0].PreviousUpstream != "localhost:8080" || existing[0].RemainingDomains[0] != "www.example.com" || !existing[0].RulesPreserved {
		t.Fatal(existing)
	}
	p := model.Project{Domains: []string{"api.example.com"}, CaddyRoutes: templates, BluePort: 3031}
	rendered := string(Snippet(p, "blue"))
	if !strings.Contains(rendered, "private_hash") || !strings.Contains(rendered, "reverse_proxy 127.0.0.1:3031 {") || !strings.Contains(rendered, "{host}") || strings.Contains(rendered, model.CaddyUpstream) {
		t.Fatal("site rules were lost", rendered)
	}
	if strings.Contains(string(Snippet(p, "")), "private_hash") {
		t.Fatal("maintenance template leaked custom rules")
	}
	encoded, _ := json.Marshal(existing)
	if strings.Contains(string(encoded), "private_hash") {
		t.Fatal("review exposed site credentials")
	}
}

func TestServiceSiteMoveRequiresExplicitCustomRuleReplacement(t *testing.T) {
	for _, body := range []string{"redir https://other.example.com{uri}", "reverse_proxy localhost:3000 localhost:3001", "reverse_proxy {\n to localhost:3000\n}", "respond \"custom\""} {
		source := "api.example.com, www.example.com {\n" + body + "\n}\n"
		if _, _, _, err := planServiceSiteMove(source, []string{"api.example.com"}, nil, false); err == nil {
			t.Fatal("silently replaced custom route", source)
		}
		after, templates, existing, err := planServiceSiteMove(source, []string{"api.example.com"}, nil, true)
		if err != nil || len(templates) != 0 || len(existing) != 1 || existing[0].RulesPreserved || !strings.Contains(after, body) || strings.Contains(after, "api.example.com") {
			t.Fatal(after, existing, err)
		}
	}
}

func TestServiceSiteMoveQuotedDataHeredocAndAmbiguousSites(t *testing.T) {
	source := "api.example.com {\n header X-Brace \"{\"\n respond /test <<DATA\n }\n api.example.com {\n DATA\n reverse_proxy localhost:3000\n}\n"
	_, templates, _, err := planServiceSiteMove(source, []string{"api.example.com"}, nil, false)
	if err != nil || len(templates) != 1 || !strings.Contains(templates[0].Body, "api.example.com {") {
		t.Fatal(templates, err)
	}
	for _, source := range []string{"api.example.com {\n reverse_proxy localhost:3000\n}\napi.example.com {\n reverse_proxy localhost:4000\n}\n", "api.example.com {\n reverse_proxy localhost:3000\n"} {
		if _, _, _, err := planServiceSiteMove(source, []string{"api.example.com"}, nil, false); err == nil {
			t.Fatal("accepted ambiguous/unclosed config", source)
		}
	}
}

func TestPrimaryServiceSiteImportUsesActiveBinding(t *testing.T) {
	p := model.Project{Mode: "compose", Active: "blue", State: "running", BluePort: 3031, RouteService: "web", RoutePort: 3000}
	if importSlot(p) != "blue" {
		t.Fatal("primary import chose an inactive slot")
	}
	p.State = "stopped"
	if importSlot(p) != "" {
		t.Fatal("primary import bypassed maintenance")
	}
}

func TestServiceSiteMoveSupportsSingleSiteWithoutBraces(t *testing.T) {
	source := "import /etc/caddy/dockyard/*.caddy\napi.example.com, www.example.com\nheader X-Test yes\nreverse_proxy localhost:8080\n"
	after, templates, existing, err := planServiceSiteMove(source, []string{"api.example.com"}, nil, false)
	if err != nil || len(templates) != 1 || len(existing) != 1 || strings.Contains(after, "api.example.com") || !strings.Contains(after, "www.example.com {\nheader X-Test yes") || !strings.Contains(after, "import /etc/caddy/dockyard/*.caddy") {
		t.Fatal(after, templates, existing, err)
	}
}

// A real Caddy adapter catches changes hidden by a simplified test parser.
// Set DOCKYARD_TEST_CADDY_BINARY to an installed binary in a disposable runtime.
type serviceImportCaddyRunner struct {
	binary, root string
	live         any
	mode         string
}

func (r *serviceImportCaddyRunner) Run(ctx context.Context, _ string, cwd string, args []string) (process.Result, error) {
	if args[0] == "reload" {
		if r.mode == "reject" {
			return process.Result{}, errors.New("rejected")
		}
		if err := r.observe(ctx); err != nil {
			return process.Result{}, err
		}
		return process.Result{}, nil
	}
	if args[0] == "validate" && r.mode == "invalid" {
		return process.Result{}, errors.New("invalid")
	}
	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Dir = cwd
	data, err := cmd.Output()
	if failure, ok := err.(*exec.ExitError); ok {
		err = fmt.Errorf("%v: %s", err, failure.Stderr)
	}
	return process.Result{Output: data}, err
}
func (r *serviceImportCaddyRunner) observe(ctx context.Context) error {
	result, err := r.Run(ctx, r.binary, filepath.Dir(r.root), []string{"adapt", "--config", r.root, "--adapter", "caddyfile"})
	if err != nil {
		return err
	}
	return json.Unmarshal(result.Output, &r.live)
}

func TestServiceSiteMoveWithRealCaddyPreservesOtherSites(t *testing.T) {
	binary := os.Getenv("DOCKYARD_TEST_CADDY_BINARY")
	if binary == "" {
		t.Skip("requires a disposable real Caddy adapter")
	}
	for _, mode := range []string{"success", "reject", "invalid", "replaced-rules", "maintenance", "changed-source", "imported-file-rules"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "Caddyfile")
			sites := filepath.Join(dir, "sites")
			os.Mkdir(sites, 0755)
			body := " header X-Test \"value } inside\"\n encode gzip\n reverse_proxy localhost:8080 {\n header_up X-Forwarded-Host {host}\n }\n"
			if mode == "replaced-rules" {
				body = " redir https://www.example.com{uri}\n"
			}
			if mode == "imported-file-rules" {
				os.WriteFile(filepath.Join(dir, "headers.inc"), []byte("header X-Imported yes\n"), 0644)
				body = " import headers.inc\n reverse_proxy localhost:8080\n"
			}
			original := "{\n admin off\n}\napi.example.com, www.example.com {\n" + body + "}\nother.example.com {\n reverse_proxy localhost:9000\n}\nimport " + sites + "/*.caddy\n"
			os.WriteFile(root, []byte(original), 0644)
			before := model.Project{ID: "demo", Domains: []string{"app.example.com"}, BluePort: 3031, State: "running", Active: "blue"}
			generated := string(Snippet(before, "blue"))
			path := filepath.Join(sites, "demo.caddy")
			os.WriteFile(path, []byte(generated), 0644)
			runner := &serviceImportCaddyRunner{binary: binary, root: root}
			if err := runner.observe(context.Background()); err != nil {
				t.Fatal(err)
			}
			c := Caddy{Config: config.Config{Caddyfile: root, CaddySites: sites, CaddyBinary: binary}, Runner: runner, client: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
				data, _ := json.Marshal(runner.live)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
			})}}
			plan, err := c.PlanServiceRouteImport(context.Background(), before, []string{"api.example.com"}, mode == "replaced-rules")
			if err != nil || len(plan.Edits) != 1 || plan.Edits[0].ManagedBefore == nil {
				t.Fatal(plan, err)
			}
			next := before
			next.Domains = append(append([]string{}, before.Domains...), "api.example.com")
			next.CaddyRoutes = plan.Templates
			next.ServiceRoutes = []model.ServiceRoute{{Service: "api", ContainerPort: 8080, HostPort: 3041, Domains: []string{"api.example.com"}}}
			if mode == "maintenance" {
				next.State = "stopped"
			}
			if mode == "changed-source" {
				os.WriteFile(root, []byte(original+"# operator changed config\n"), 0644)
			}
			runner.mode = mode
			err = c.ImportRoutes(context.Background(), plan.Edits, next)
			rootAfter, _ := os.ReadFile(root)
			siteAfter, _ := os.ReadFile(path)
			if mode == "success" || mode == "replaced-rules" || mode == "maintenance" || mode == "imported-file-rules" {
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(rootAfter), "api.example.com") || !strings.Contains(string(rootAfter), "www.example.com {") || !strings.Contains(string(rootAfter), "localhost:9000") {
					t.Fatal("unrelated sites were lost", string(rootAfter))
				}
				if mode != "maintenance" && !strings.Contains(string(siteAfter), "127.0.0.1:3041") {
					t.Fatal("wrong service upstream", string(siteAfter))
				}
				if mode == "success" && !strings.Contains(string(siteAfter), "header_up X-Forwarded-Host {host}") {
					t.Fatal("lost custom rules", string(siteAfter))
				}
				if mode == "maintenance" && !strings.Contains(string(siteAfter), "Application unavailable") {
					t.Fatal("maintenance not installed", string(siteAfter))
				}
				if mode == "imported-file-rules" && !strings.Contains(string(siteAfter), dir+"/headers.inc") {
					t.Fatal("site import changed its base directory", string(siteAfter))
				}
			} else {
				if err == nil || string(siteAfter) != generated {
					t.Fatal("stale/failed move changed managed routes", err, string(siteAfter))
				}
				if mode != "changed-source" && string(rootAfter) != original {
					t.Fatal("failed move changed main Caddyfile", string(rootAfter))
				}
			}
		})
	}
}
