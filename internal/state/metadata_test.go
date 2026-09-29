package state

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func TestMetadataSurvivesMigrationAndRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := project("demo")
	p.Slots["blue"] = model.Release{ID: "release-1", Image: "approved-image", Environment: "env-1"}
	j := job("one", p.ID)
	j.Input.Project = &p
	if err = s.Accept(j, &p, "operation", "fingerprint", 10, 10); err != nil {
		t.Fatal(err)
	}
	// Recreate an old, unversioned database with only the original tables.
	if _, err = s.DB.Exec(`DROP TABLE project_targets; DROP TABLE dns_records; DROP TABLE project_slots; DROP TABLE compose_revisions; DROP TABLE project_domains; PRAGMA user_version=0;`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	domains, err := s.Domains(p.ID)
	if err != nil || len(domains) != 1 || !domains[0].Assigned {
		t.Fatal("migration lost assigned domains", domains, err)
	}
	services := map[string]model.ServiceSpec{"app": {Image: "${DOCKYARD_IMAGE}", Template: "web"}}
	if err = s.SaveCompose(p.ID, "", services); err != nil {
		t.Fatal(err)
	}
	items, err := s.Services(p.ID)
	if err != nil || len(items) != 1 || items[0].Image != "approved-image" || items[0].Environment != "env-1" {
		t.Fatal("migration lost slot identity", items, err)
	}
	if got, ok, err := s.Replay("operation", j.RequestID, "fingerprint"); err != nil || !ok || got.ID != j.ID {
		t.Fatal("migration lost request/job data", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if items, err = s.Services(p.ID); err != nil || len(items) != 1 {
		t.Fatal("metadata not durable", err)
	}
}

func TestMetadataCommitsAtomicallyAndKeepsRetiredDomains(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := project("demo")
	j := job("one", p.ID)
	j.Input.Project = &p
	if err = s.Accept(j, &p, "operation", "fp", 10, 10); err != nil {
		t.Fatal(err)
	}
	j.Status = "running"
	j.Input.Hostname = p.Domains[0]
	j.Input.DNSRecordID = "cloudflare-id"
	p.DNS = []string{"cloudflare-id"}
	if err = s.Update(j, &p); err != nil {
		t.Fatal(err)
	}
	p.Domains = []string{"new.example.com"}
	if err = s.Update(j, &p); err != nil {
		t.Fatal(err)
	}
	domains, err := s.Domains(p.ID)
	if err != nil || len(domains) != 2 || domains[0].Assigned || domains[0].DNSRecordID != "cloudflare-id" || !domains[1].Assigned {
		t.Fatal("retired domain/DNS tracking wrong", domains, err)
	}
	before, _ := s.Audit(0)
	if _, err = s.DB.Exec(`CREATE TRIGGER reject_metadata BEFORE INSERT ON project_domains WHEN NEW.hostname='blocked.example.com' BEGIN SELECT RAISE(ABORT,'test fault'); END;`); err != nil {
		t.Fatal(err)
	}
	p.Domains = []string{"blocked.example.com"}
	j.Status = "succeeded"
	if err = s.Update(j, &p); err == nil {
		t.Fatal("accepted partial transaction")
	}
	stored, _ := s.Project(p.ID)
	storedJob, _ := s.Job(j.ID)
	after, _ := s.Audit(0)
	if stored.Domains[0] != "new.example.com" || storedJob.Status != "running" || len(before) != len(after) {
		t.Fatal("metadata failure split project/job/audit transaction")
	}
	if owner, err := s.DomainOwner("app.example.com"); err != nil || owner != p.ID {
		t.Fatal("released retired reservation")
	}
}

func TestComposeMetadataImmutableAndProjectIsolated(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := project("demo")
	j := job("one", p.ID)
	if err = s.Accept(j, &p, "operation", "fp", 10, 10); err != nil {
		t.Fatal(err)
	}
	spec := map[string]model.ServiceSpec{"app": {Image: "image-1", Template: "web"}}
	if err = s.SaveCompose(p.ID, "revision-1", spec); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveCompose(p.ID, "revision-1", spec); err != nil {
		t.Fatal("identical metadata not idempotent", err)
	}
	spec["app"] = model.ServiceSpec{Image: "tampered", Template: "web"}
	if err = s.SaveCompose(p.ID, "revision-1", spec); err == nil {
		t.Fatal("overwrote immutable metadata")
	}
	if _, err = s.ComposeServices("other", "revision-1"); err != sql.ErrNoRows {
		t.Fatal("cross-project revision read", err)
	}
	var b []byte
	s.DB.QueryRow("SELECT metadata FROM compose_revisions WHERE project_id=?", p.ID).Scan(&b)
	if !json.Valid(b) {
		t.Fatal("invalid revision metadata")
	}
}
