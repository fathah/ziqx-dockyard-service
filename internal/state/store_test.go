package state

import (
	"encoding/json"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func project(id string) model.Project {
	return model.Project{ID: id, AppID: id, Environment: model.Production, BluePort: 3001, GreenPort: 3002, Domains: []string{"app.example.com"}, Slots: map[string]model.Release{}}
}
func job(id, p string) model.Job {
	return model.Job{ID: id, ProjectID: p, Action: "project_create", Status: "queued", Phase: "accepted", RequestID: "req-" + id, Actor: "alice", Created: time.Now()}
}
func TestDurableReplayAndAudit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	p := project("demo")
	j := job("job1", p.ID)
	j.Input.Project = &p
	if e = s.Accept(j, &p, "operation", "fingerprint", 10, 10); e != nil {
		t.Fatal(e)
	}
	j.Status = "running"
	j.Phase = "route_intent"
	if e = s.Update(j, nil); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	got, replay, e := s.Replay("operation", "req-job1", "fingerprint")
	if e != nil || !replay || got.ID != j.ID {
		t.Fatal("durable replay failed", e)
	}
	if _, _, e = s.Replay("operation", "req-job1", "changed"); e == nil {
		t.Fatal("conflicting retry accepted")
	}
	if _, _, e = s.Replay("different", "req-job1", "fingerprint"); e == nil {
		t.Fatal("request ID reuse accepted")
	}
	if e = s.InterruptRunning(); e != nil {
		t.Fatal(e)
	}
	blocked, _ := s.Blocked()
	if !blocked {
		t.Fatal("interruption did not block mutations")
	}
	events, e := s.Audit(0)
	if e != nil || len(events) != 3 {
		t.Fatalf("audit: %d %v", len(events), e)
	}
	for _, b := range events {
		if !json.Valid(b) {
			t.Fatal("invalid audit")
		}
	}
}
func TestReservationRaces(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "state"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var wg sync.WaitGroup
	wins := make(chan bool, 2)
	for _, id := range []string{"first", "second"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			p := project(id)
			j := job(id, id)
			j.Input.Project = &p
			wins <- s.Accept(j, &p, "op-"+id, "fp-"+id, 10, 10) == nil
		}(id)
	}
	wg.Wait()
	close(wins)
	n := 0
	for won := range wins {
		if won {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("got %d reservation winners", n)
	}
	ps, _ := s.Projects()
	if len(ps) != 1 {
		t.Fatal("failed reservation left a project")
	}
}
func TestProjectSerialization(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "state"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p := project("demo")
	j := job("one", p.ID)
	if e = s.Accept(j, &p, "op1", "fp1", 10, 10); e != nil {
		t.Fatal(e)
	}
	if s.Accept(job("two", p.ID), nil, "op2", "fp2", 10, 10) == nil {
		t.Fatal("accepted concurrent project job")
	}
}
