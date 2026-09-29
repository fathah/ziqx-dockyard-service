package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func TestInventoryReadsUseSQLiteWithoutProjectFiles(t *testing.T) {
	a, d, routes, send := apiFixture(t)
	seedComposeProject(t, a)
	source := "services:\n  app:\n    image: ghcr.io/ziqx/demo@sha256:" + strings.Repeat("a", 64) + "\n    environment: {TOKEN: private-secret}\n"
	b, _ := json.Marshal(map[string]string{"environment": "production", "compose_yaml": source})
	w := send("POST", "/v1/projects/demo/deploy", string(b), "deploy.environment deploy.execute", "deploy-1")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var receipt map[string]string
	json.Unmarshal(w.Body.Bytes(), &receipt)
	j, _ := a.Engine.Store.Job(receipt["job_id"])
	p, _ := a.Engine.Store.Project("demo")
	p.Slots["blue"] = *j.Input.Release
	j.Status = "running"
	if err := a.Engine.Store.Update(j, &p); err != nil {
		t.Fatal(err)
	}
	root := a.Engine.Config.ProjectsRoot
	if err := os.Rename(filepath.Join(root, "demo"), filepath.Join(root, "offline")); err != nil {
		t.Fatal(err)
	}
	calls := d.calls + routes.calls
	for _, path := range []string{"/v1/projects", "/v1/projects/demo", "/v1/projects/demo/releases", "/v1/projects/demo/services", "/v1/projects/demo/domains"} {
		w := send("GET", path, "", "deploy.read", "read-"+strings.ReplaceAll(strings.TrimPrefix(path, "/v1/"), "/", "-"))
		if w.Code != 200 || !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), "private-secret") {
			t.Fatal("inventory needed disk or exposed secrets", path, w.Code, w.Body.String())
		}
		if path == "/v1/projects/demo/services" {
			var body struct{ Services []model.ServiceInfo }
			json.Unmarshal(w.Body.Bytes(), &body)
			if len(body.Services) != 1 || body.Services[0].Name != "app" || body.Services[0].Compose != j.Input.Release.Compose {
				t.Fatal("lost service metadata")
			}
		}
	}
	if d.calls+routes.calls != calls {
		t.Fatal("inventory queried runtime adapters")
	}
	for _, path := range []string{"/v1/projects/other/services", "/v1/projects/other/domains"} {
		if w := send("GET", path, "", "deploy.read", "deny-"+strings.ReplaceAll(path, "/", "-")); w.Code != 403 {
			t.Fatal("cross-project metadata read", w.Code)
		}
	}
}
