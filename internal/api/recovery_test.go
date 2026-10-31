package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
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
	if !a.Engine.Ready() || a.Engine.ProjectAdmission(p.ID) == nil {
		t.Fatal("recovery should lock only its project")
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
	if !a.Engine.Ready() || a.Engine.ProjectAdmission(p.ID) != nil {
		t.Fatal("recovery lock not released")
	}
	if w := send("POST", "/v1/jobs/job-dns/reconcile", "{}", "deploy.execute", "r4"); w.Code != 409 || !strings.Contains(w.Body.String(), "JOB_NOT_RECOVERABLE") {
		t.Fatal("reconciled twice", w.Code, w.Body.String())
	}
}

func TestRecoveryBlocksOnlyItsProjectAcrossAPIAdmissions(t *testing.T) {
	a, _, _, send := apiFixture(t)
	policy := a.Engine.Config
	policy.Keys[0].Projects = []string{"*"}
	var err error
	a.Auth, err = auth.New(policy)
	if err != nil {
		t.Fatal(err)
	}
	p := model.Project{ID: "broken", AppID: "broken", Environment: model.Production, Template: "node", State: "running", Active: "blue", Slots: map[string]model.Release{}, Releases: []model.Release{}}
	job := model.Job{ID: "job-broken", ProjectID: p.ID, Action: "service_update", Status: "recovery_required", Phase: "service_candidate_intent", Error: "SERVICE_UPDATE_RECOVERY_REQUIRED", Created: time.Now()}
	if err := a.Engine.Store.Accept(job, &p, "broken", "broken", 10, 10); err != nil {
		t.Fatal(err)
	}
	if w := send("POST", "/v1/projects/broken/stop", "{}", "deploy.stop", "same-project"); w.Code != 503 || !strings.Contains(w.Body.String(), "RECOVERY_REQUIRED") {
		t.Fatal("affected project was not held for recovery", w.Code, w.Body.String())
	}
	if w := send("GET", "/readyz", "", "deploy.read", "ready"); w.Code != 200 {
		t.Fatal("unrelated work must remain available", w.Code, w.Body.String())
	}
	body := `{"id":"demo","app_id":"demo","environment":"production","template_id":"node","domains":["other.example.com"],"zerodowntime":false}`
	w := send("POST", "/v1/projects", body, "projects.write", "create-unrelated")
	if w.Code != 202 {
		t.Fatal("unrelated project creation was blocked", w.Code, w.Body.String())
	}
	var queued model.Job
	if err := json.Unmarshal(w.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	if next, err := a.Engine.Store.Next(); err != nil || next.ID != queued.ID || next.ProjectID != "demo" {
		t.Fatal("unrelated job is not runnable", next, err)
	}
	if got, err := a.Engine.Store.Job(job.ID); err != nil || got.Status != "recovery_required" || got.Error != job.Error {
		t.Fatal("recovery evidence changed", got, err)
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

func TestFaultDetailReachesErrorResponse(t *testing.T) {
	w := httptest.NewRecorder()
	fail(w, model.FailDetail("COMPOSE_VALIDATION_FAILED", "required variable POSTGRES_PASSWORD is missing a value"), "req-1")
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"detail":"required variable POSTGRES_PASSWORD is missing a value"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	fail(w, model.Fail("PROJECT_BUSY"), "req-2")
	if strings.Contains(w.Body.String(), "detail") {
		t.Fatal("empty detail should be omitted", w.Body.String())
	}
}

func TestLogsOfNeverActivatedProjectUseFirstCandidate(t *testing.T) {
	a, _, _, send := apiFixture(t)
	release := model.Release{ID: "rel-1", Image: "ghcr.io/ziqx/demo@sha256:" + strings.Repeat("a", 64)}
	p := model.Project{ID: "demo", AppID: "demo", Template: "node", Environment: model.Production, State: "awaiting_release", Slots: map[string]model.Release{"blue": release}, Releases: []model.Release{}}
	job := model.Job{ID: "job-first", ProjectID: p.ID, Action: "deploy", Status: "failed", Created: time.Now()}
	if err := a.Engine.Store.Accept(job, &p, "op-first", "fp", 10, 10); err != nil {
		t.Fatal(err)
	}
	if w := send("GET", "/v1/projects/demo/logs?service=app&slot=active&tail=300&since=1h", "", "deploy.logs", "logs-first"); w.Code == 400 {
		t.Fatal("logs of a never-activated project were rejected", w.Body.String())
	}
}
