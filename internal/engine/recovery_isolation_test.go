package engine

import (
	"context"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

type isolationDNS struct{}

func (isolationDNS) Create(context.Context, string, string) (string, error) { return "record", nil }

func TestWorkerContinuesUnrelatedJobsDuringRecovery(t *testing.T) {
	e, recovery, _, _, _ := engineFixture(t, "blue")
	recovery.Status, recovery.Error = "recovery_required", "SERVICE_UPDATE_RECOVERY_REQUIRED"
	if err := e.Store.Update(recovery, nil); err != nil {
		t.Fatal(err)
	}
	p := model.Project{ID: "other", AppID: "other", Environment: model.Production, Template: "node", State: "running", Slots: map[string]model.Release{}}
	j := model.Job{ID: "job-other", ProjectID: p.ID, Action: "dns_create", Status: "queued", Created: time.Now(), Input: model.Input{Hostname: "other.example.com"}}
	if err := e.Store.Accept(j, &p, "other", "other", 10, 10); err != nil {
		t.Fatal(err)
	}
	e.DNS = isolationDNS{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		got, err := e.Store.Job(j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "succeeded" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("recovery stopped the unrelated worker", got)
		case <-tick.C:
		}
	}
	if err := e.ProjectAdmission(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.ProjectAdmission(recovery.ProjectID); err == nil {
		t.Fatal("lost project recovery lock")
	}
	if got, err := e.Store.Job(recovery.ID); err != nil || got.Status != "recovery_required" || got.Error != recovery.Error {
		t.Fatal(got, err)
	}
	e.failed.Store(true)
	if e.Ready() || e.ProjectAdmission(p.ID) == nil {
		t.Fatal("durable state failure must still stop all work")
	}
}

func TestOnlineRecoveryDoesNotOverlapWorkerSideEffects(t *testing.T) {
	e, j, _, _, _ := engineFixture(t, "blue")
	j.Status = "recovery_required"
	if err := e.Store.Update(j, nil); err != nil {
		t.Fatal(err)
	}
	e.execution.Lock()
	_, _, err := e.ReconcileOnline(context.Background(), j.ID)
	e.execution.Unlock()
	if fault, ok := err.(*model.Fault); !ok || fault.Code != "PROJECT_BUSY" {
		t.Fatal("recovery overlapped active execution", err)
	}
	if got, _ := e.Store.Job(j.ID); got.Status != "recovery_required" {
		t.Fatal("busy recovery changed the job", got)
	}
}
