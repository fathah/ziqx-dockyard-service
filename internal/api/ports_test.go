package api

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

type portAvailabilityDocker struct {
	*dockerStub
	busy    map[int]bool
	checked []int
	failAt  int
}

func (d *portAvailabilityDocker) PortFree(_ context.Context, port int) (bool, error) {
	d.checked = append(d.checked, port)
	if port == d.failAt {
		return false, model.Fail("DOCKER_UNAVAILABLE")
	}
	return !d.busy[port], nil
}

func TestNextPortsSkipsReservationsAndListenersWithoutReserving(t *testing.T) {
	a, docker, _, send := apiFixture(t)
	a.Engine.Config.ReservedPorts = []int{3003}
	p := model.Project{ID: "demo", AppID: "demo", Environment: model.Production, BluePort: 3001}
	j := model.Job{ID: "reserved", ProjectID: p.ID, Status: "succeeded", Action: "project_create", Input: model.Input{Project: &p}}
	if err := a.Engine.Store.Accept(j, &p, "reserved", "reserved", 10, 10); err != nil {
		t.Fatal(err)
	}
	if err := a.Engine.Store.SyncInventory(model.Inventory{ReservedPorts: []int{3002}}, false, false, true); err != nil {
		t.Fatal(err)
	}
	d := &portAvailabilityDocker{dockerStub: docker, busy: map[int]bool{3004: true, 3006: true}}
	a.Engine.Docker = d
	for _, request := range []string{"suggestions", "suggestions-again"} {
		d.checked = nil
		w := send("GET", "/v1/ports/next?count=3", "", "projects.write", request)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result struct {
			Port     int   `json:"port"`
			Ports    []int `json:"ports"`
			Reserved bool  `json:"reserved"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Port != 3005 || !reflect.DeepEqual(result.Ports, []int{3005, 3007, 3008}) || result.Reserved {
			t.Fatal("incorrect port suggestions", w.Body.String())
		}
		if !reflect.DeepEqual(d.checked, []int{3004, 3005, 3006, 3007, 3008}) {
			t.Fatal("reserved ports checked or repeated host probes", d.checked)
		}
		for _, port := range result.Ports {
			reserved, err := a.Engine.Store.Reserved(port, "")
			if err != nil || reserved {
				t.Fatal("advisory read reserved a port", port, err)
			}
		}
	}
}

func TestNextPortPreservesSinglePortResponse(t *testing.T) {
	_, _, _, send := apiFixture(t)
	w := send("GET", "/v1/ports/next", "", "projects.write", "legacy")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"port":3001,"reserved":false}` {
		t.Fatal("single-port contract changed", w.Code, w.Body.String())
	}
	for _, count := range []string{"1", "2", "3"} {
		w = send("GET", "/v1/ports/next?count="+count, "", "projects.write", "count-"+count)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"ports":[`) {
			t.Fatal("explicit count omitted port list", w.Code, w.Body.String())
		}
	}
}

func TestNextPortsValidatesCountBeforeCheckingHost(t *testing.T) {
	a, docker, _, send := apiFixture(t)
	d := &portAvailabilityDocker{dockerStub: docker}
	a.Engine.Docker = d
	for i, query := range []string{"count=0", "count=4", "count=-1", "count=03", "count=+3", "count=three", "count=", "count=3&count=2", "other=3", "count=1.5"} {
		w := send("GET", "/v1/ports/next?"+query, "", "projects.write", "invalid-"+string(rune('a'+i)))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "REQUEST_INVALID") {
			t.Fatal("invalid count accepted", query, w.Code, w.Body.String())
		}
	}
	w := send("GET", "/v1/ports/next?count=3", "", "deploy.read", "denied")
	if w.Code != 403 || len(d.checked) != 0 {
		t.Fatal("invalid or unauthorized request checked host ports", w.Code, d.checked)
	}
}

func TestNextPortsReportsSmallOrUnavailablePool(t *testing.T) {
	a, docker, _, send := apiFixture(t)
	a.Engine.Config.PortMax = 3002
	d := &portAvailabilityDocker{dockerStub: docker, busy: map[int]bool{3001: true}}
	a.Engine.Docker = d
	w := send("GET", "/v1/ports/next?count=3", "", "projects.write", "partial")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"port":3002,"ports":[3002],"reserved":false}` {
		t.Fatal("available subset not reported", w.Code, w.Body.String())
	}
	d.busy[3002] = true
	w = send("GET", "/v1/ports/next?count=3", "", "projects.write", "empty")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "PORT_POOL_EXHAUSTED") {
		t.Fatal("empty pool not reported", w.Code, w.Body.String())
	}
	d.busy = nil
	d.failAt = 3002
	w = send("GET", "/v1/ports/next?count=3", "", "projects.write", "docker-failed")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "DOCKER_UNAVAILABLE") {
		t.Fatal("failed host check returned misleading suggestions", w.Code, w.Body.String())
	}
}
