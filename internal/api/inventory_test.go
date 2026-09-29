package api

import (
	"encoding/json"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func TestInventoryRequiresHostWideAccessAndReadsOnlySQLite(t *testing.T) {
	a, d, r, send := apiFixture(t)
	if w := send("GET", "/v1/inventory", "", "deploy.read", "denied"); w.Code != 403 {
		t.Fatal("project-only key read host inventory", w.Code)
	}
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	var err error
	a.Auth, err = auth.New(a.Engine.Config)
	if err != nil {
		t.Fatal(err)
	}
	v := model.Inventory{Projects: []model.ExistingProject{{ID: "existing-one", Name: "legacy", Environment: model.Production, Present: true, ComposeFiles: []string{"compose.yml"}, Services: []model.ExistingService{}, Warnings: []string{}}}, Sites: []model.ExistingSite{}, Warnings: []string{}, ReservedPorts: []int{3200}}
	if err := a.Engine.Store.SyncInventory(v, true, true, false); err != nil {
		t.Fatal(err)
	}
	w := send("GET", "/v1/inventory", "", "deploy.read", "read")
	var got model.Inventory
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Projects) != 1 || got.Projects[0].Managed || d.calls+r.calls != 0 {
		t.Fatal("inventory read touched adapters or lost data", w.Code, w.Body.String())
	}
	if w := send("GET", "/v1/inventory?scan=1", "", "deploy.read", "bad-query"); w.Code != 400 {
		t.Fatal("GET invoked a scan")
	}
}
