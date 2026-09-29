package state

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func TestTargetMigrationPreservesLegacyModeJobsAndRetries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"single", "blue-green"} {
		p := project(id)
		p.ZeroDowntime = i == 1
		p.BluePort = 3001 + i*2
		p.GreenPort = 3002 + i*2
		p.Domains = []string{id + ".example.com"}
		p.Slots["blue"] = model.Release{ID: "old", Image: "approved", Environment: "env-old"}
		j := job(id, id)
		j.Input.Project = &p
		if err = s.Accept(j, &p, "op-"+id, "fp-"+id, 10, 10); err != nil {
			t.Fatal(err)
		}
		p.AppID = ""
		p.Environment = ""
		// Old v1 JSON has neither field in projects or queued job snapshots.
		b, _ := json.Marshal(p)
		input, _ := json.Marshal(j.Input)
		if _, err = s.DB.Exec("UPDATE projects SET data=? WHERE id=?", b, id); err != nil {
			t.Fatal(err)
		}
		if _, err = s.DB.Exec("UPDATE jobs SET input=? WHERE id=?", input, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.DB.Exec("DROP TABLE project_targets; PRAGMA user_version=1;"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, id := range []string{"single", "blue-green"} {
		p, err := s.Project(id)
		if err != nil || p.AppID != id || p.Environment != model.Production || p.ZeroDowntime != (i == 1) || p.Slots["blue"].Image != "approved" {
			t.Fatal("legacy target changed live mode", p, err)
		}
		j, replay, err := s.Replay("op-"+id, "req-"+id, "fp-"+id)
		if err != nil || !replay || j.Input.Project.Environment != model.Production || j.Input.Project.AppID != id {
			t.Fatal("migration lost retry or queued target", j, err)
		}
		if owner, err := s.TargetOwner(id, model.Production); err != nil || owner != id {
			t.Fatal("migration lost target index", owner, err)
		}
	}
}

func TestTargetUniquenessAndIdentityAreTransactional(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	wins := make(chan string, 2)
	for _, id := range []string{"first", "second"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			p := project(id)
			p.AppID = "app"
			p.Environment = model.Staging
			p.GreenPort = 0
			p.Domains = []string{id + ".example.com"}
			if s.Accept(job(id, id), &p, "op-"+id, "fp-"+id, 10, 10) == nil {
				wins <- id
			}
		}(id)
	}
	wg.Wait()
	close(wins)
	ids := []string{}
	for id := range wins {
		ids = append(ids, id)
	}
	ps, _ := s.Projects()
	if len(ids) != 1 || len(ps) != 1 {
		t.Fatal("app/environment uniqueness race", ids, ps)
	}
	p := ps[0]
	j, _ := s.Job(ids[0])
	before, _ := s.Audit(0)
	p.Environment = model.Development
	j.Status = "succeeded"
	if err := s.Update(j, &p); err == nil {
		t.Fatal("changed immutable environment")
	}
	p, _ = s.Project(p.ID)
	got, _ := s.Job(j.ID)
	after, _ := s.Audit(0)
	if p.Environment != model.Staging || got.Status != "queued" || len(before) != len(after) {
		t.Fatal("target failure split transaction")
	}
	p.ZeroDowntime = true
	p.GreenPort = 3002
	if err := s.Update(j, &p); err == nil {
		t.Fatal("stored a second staging slot")
	}
}
