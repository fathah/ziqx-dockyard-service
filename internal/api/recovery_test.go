package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

type recoveryDNS struct{ calls int }

func (d *recoveryDNS) Create(context.Context, string, string) (string, error) {
	d.calls++
	return "rec-01", nil
}

func TestReconcileJobFromDesktop(t *testing.T) {
	a, _, _, send := apiFixture(t)
	dns := &recoveryDNS{}
	a.Engine.DNS = dns
	p := model.Project{ID: "demo", AppID: "demo", Template: "node", Environment: model.Production, State: "running", Active: "blue", Slots: map[string]model.Release{}, Releases: []model.Release{}}
	job := model.Job{ID: "job-dns", ProjectID: p.ID, Action: "dns_create", Status: "queued", Created: time.Now(), Input: model.Input{Hostname: "app.example.com"}}
	if err := a.Engine.Store.Accept(job, &p, "dns", "dns", 10, 10); err != nil {
		t.Fatal(err)
	}
	job.Status, job.Error = "recovery_required", "JOB_INTERRUPTED"
	if err := a.Engine.Store.Update(job, nil); err != nil {
		t.Fatal(err)
	}
	if a.Engine.Ready() {
		t.Fatal("recovery lock not held")
	}
	if w := send("POST", "/v1/jobs/job-dns/reconcile", "{}", "deploy.read", "r1"); w.Code != 403 {
		t.Fatal("unscoped recovery accepted", w.Code, w.Body.String())
	}
	if w := send("POST", "/v1/jobs/job-missing/reconcile", "{}", "deploy.execute", "r2"); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := send("POST", "/v1/jobs/job-dns/reconcile", "{}", "deploy.execute", "r3")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "JOB_INTERRUPTED_RECONCILED") || dns.calls != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !a.Engine.Ready() {
		t.Fatal("recovery lock not released")
	}
	if w := send("POST", "/v1/jobs/job-dns/reconcile", "{}", "deploy.execute", "r4"); w.Code != 409 || !strings.Contains(w.Body.String(), "JOB_NOT_RECOVERABLE") {
		t.Fatal("reconciled twice", w.Code, w.Body.String())
	}
}

func TestRequestLookupResolvesUncertainWrite(t *testing.T) {
	a, _, _, send := apiFixture(t)
	p := model.Project{ID: "demo", AppID: "demo", Template: "node", Environment: model.Production, State: "running", Active: "blue", Slots: map[string]model.Release{}, Releases: []model.Release{}}
	job := model.Job{ID: "job-restart", ProjectID: p.ID, Action: "restart", Status: "queued", Created: time.Now()}
	if err := a.Engine.Store.Accept(job, &p, "op-restart", "fp", 10, 10); err != nil {
		t.Fatal(err)
	}
	if w := send("GET", "/v1/requests/op-restart", "", "deploy.read", "q1"); w.Code != 200 || !strings.Contains(w.Body.String(), "job-restart") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send("GET", "/v1/requests/op-never-sent", "", "deploy.read", "q2"); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send("GET", "/v1/requests/op-restart", "", "deploy.logs", "q3"); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestJobEventsTimeline(t *testing.T) {
	a, _, _, send := apiFixture(t)
	p := model.Project{ID: "demo", AppID: "demo", Template: "node", Environment: model.Production, State: "running", Active: "blue", Slots: map[string]model.Release{}, Releases: []model.Release{}}
	job := model.Job{ID: "job-deploy", ProjectID: p.ID, Action: "deploy", Status: "queued", Phase: "accepted", Created: time.Now()}
	if err := a.Engine.Store.Accept(job, &p, "op-deploy", "fp", 10, 10); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ status, phase, code string }{{"running", "pulling", ""}, {"running", "pulling", ""}, {"failed", "candidate_intent", "CONTAINER_START_FAILED"}} {
		job.Status, job.Phase, job.Error = step.status, step.phase, step.code
		if err := a.Engine.Store.Update(job, nil); err != nil {
			t.Fatal(err)
		}
	}
	w := send("GET", "/v1/jobs/job-deploy/events", "", "deploy.logs", "e1")
	var body struct {
		Events []struct{ Status, Phase, Error_code string } `json:"events"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Events) != 3 || body.Events[2].Phase != "candidate_intent" || body.Events[2].Error_code != "CONTAINER_START_FAILED" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send("GET", "/v1/jobs/job-deploy/events", "", "deploy.read", "e2"); w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestJobLogEndpoint(t *testing.T) {
	a, _, _, send := apiFixture(t)
	p := model.Project{ID: "demo", AppID: "demo", Template: "node", Environment: model.Production, State: "running", Active: "blue", Slots: map[string]model.Release{}, Releases: []model.Release{}}
	job := model.Job{ID: "job-log", ProjectID: p.ID, Action: "start", Status: "failed", Created: time.Now()}
	if err := a.Engine.Store.Accept(job, &p, "op-log", "fp", 10, 10); err != nil {
		t.Fatal(err)
	}
	if w := send("GET", "/v1/jobs/job-log/log", "", "deploy.logs", "l1"); w.Code != 200 || !strings.Contains(w.Body.String(), `"log":""`) {
		t.Fatal("missing log should be empty", w.Code, w.Body.String())
	}
	a.Engine.Config.StateDir = t.TempDir()
	os.MkdirAll(filepath.Dir(a.Engine.JobLogPath("job-log")), 0700)
	os.WriteFile(a.Engine.JobLogPath("job-log"), []byte("$ docker compose up\nError: port is already allocated\n"), 0600)
	if w := send("GET", "/v1/jobs/job-log/log", "", "deploy.logs", "l2"); w.Code != 200 || !strings.Contains(w.Body.String(), "port is already allocated") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send("GET", "/v1/jobs/job-log/log", "", "deploy.read", "l3"); w.Code != 403 {
		t.Fatal(w.Code)
	}
}
