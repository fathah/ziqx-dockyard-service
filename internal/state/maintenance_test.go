package state

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMaintenanceLockPausesAdmissionAndAutomaticallyReleases(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertAvailable := func(want bool) {
		t.Helper()
		if available, err := s.Available(); err != nil || available != want {
			t.Fatal(available, err)
		}
	}
	assertAvailable(true)
	f, err := os.OpenFile(filepath.Join(dir, "maintenance.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	assertAvailable(true)
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	assertAvailable(false)
	f.Close()
	assertAvailable(true)
	if err := os.Chmod(filepath.Join(dir, "maintenance.lock"), 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Available(); err == nil {
		t.Fatal("unsafe lock allowed admission")
	}
}

func TestRecoveryLockIsProjectScopedAndSurvivesReopening(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := project("broken")
	p.BluePort, p.GreenPort, p.Domains = 0, 0, nil
	j := job("broken-job", p.ID)
	j.Status = "recovery_required"
	if err := s.Accept(j, &p, "broken", "broken", 10, 10); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for id, want := range map[string]bool{"broken": true, "unrelated": false} {
		if blocked, err := s.Blocked(id); err != nil || blocked != want {
			t.Fatal(id, blocked, err)
		}
	}
	if err := s.Accept(job("same-project", p.ID), nil, "same-project", "same-project", 10, 10); err == nil {
		t.Fatal("accepted work on the recovering project")
	}
	other := project("unrelated")
	if err := s.Accept(job("unrelated-job", other.ID), &other, "unrelated", "unrelated", 10, 10); err != nil {
		t.Fatal(err)
	}
	if next, err := s.Next(); err != nil || next.ID != "unrelated-job" {
		t.Fatal(next, err)
	}
}
