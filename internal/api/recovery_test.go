package api

import (
	"context"
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
