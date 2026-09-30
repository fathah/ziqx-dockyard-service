package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func TestMigrationPreflightIsScopedAndReadOnly(t *testing.T) {
	a, docker, routes, send := apiFixture(t)
	path := "/v1/inventory/existing-one/migration"
	if w := send("GET", path, "", "deploy.read", "migration-denied"); w.Code != 403 {
		t.Fatal("project-limited key read migration", w.Code)
	}
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	var err error
	a.Auth, err = auth.New(a.Engine.Config)
	if err != nil {
		t.Fatal(err)
	}
	v := model.Inventory{Projects: []model.ExistingProject{{ID: "existing-one", Name: "legacy", Environment: model.Production, Present: true, ComposeFiles: []string{"compose.yml"}, Services: []model.ExistingService{{Name: "app", Image: "private-image", PublishedPorts: []int{3000}}}, Warnings: []string{}}}, Sites: []model.ExistingSite{}, Warnings: []string{}, ReservedPorts: []int{3000}}
	if err := a.Engine.Store.SyncInventory(v, true, true, true); err != nil {
		t.Fatal(err)
	}
	w := send("GET", path, "", "deploy.read", "migration-read")
	var got MigrationAssessment
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Status != "blocked" || got.ExecutionAvailable || len(got.Checks) == 0 {
		t.Fatal("invalid migration assessment", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-image") || docker.calls+routes.calls != 0 {
		t.Fatal("assessment disclosed source data or touched adapters")
	}
	if w := send("GET", "/v1/inventory/existing-missing/migration", "", "deploy.read", "migration-missing"); w.Code != 404 {
		t.Fatal("unknown project accepted", w.Code)
	}
	if w := send("GET", path+"?scan=1", "", "deploy.read", "migration-query"); w.Code != 400 {
		t.Fatal("preflight accepted an unknown query", w.Code)
	}
	v.Projects[0].ComposeFiles = []string{"../../etc/dockyard/config.json"}
	if err := a.Engine.Store.SyncInventory(v, true, true, true); err != nil {
		t.Fatal(err)
	}
	w = send("GET", path, "", "deploy.read", "migration-traversal")
	got = MigrationAssessment{}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Status != "blocked" || got.SourceSHA256 != "" {
		t.Fatal("assessment followed an untrusted inventory filename", w.Code, w.Body.String())
	}
}
