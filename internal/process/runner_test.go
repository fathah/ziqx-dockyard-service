package process

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBoundedOutputAndCleanEnvironment(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "script")
	os.Setenv("DOCKYARD_TEST_SECRET", "supersecret")
	defer os.Unsetenv("DOCKYARD_TEST_SECRET")
	os.WriteFile(script, []byte("#!/bin/sh\nif [ -n \"$DOCKYARD_TEST_SECRET\" ]; then exit 9; fi\ni=0; while [ $i -lt 2000 ]; do printf x; i=$((i+1)); done\n"), 0700)
	r, e := (Exec{Timeout: 5 * time.Second, Limit: 100, DockerConfig: dir}).Run(context.Background(), script, dir, nil)
	if e != nil || len(r.Output) != 100 || !r.Truncated {
		t.Fatalf("output bound failed %d %v", len(r.Output), e)
	}
}
func TestArgumentIsLiteral(t *testing.T) {
	dir := t.TempDir()
	value := "$(touch " + filepath.Join(dir, "owned") + ")"
	r, e := (Exec{Timeout: time.Second, Limit: 1000, DockerConfig: dir}).Run(context.Background(), "/bin/echo", dir, []string{value})
	if e != nil || strings.TrimSpace(string(r.Output)) != value {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dir, "owned")); !os.IsNotExist(e) {
		t.Fatal("argument executed")
	}
}
func TestTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "script")
	marker := filepath.Join(dir, "survived")
	os.WriteFile(script, []byte("#!/bin/sh\n(sleep 1; touch \""+marker+"\") &\nwait\n"), 0700)
	start := time.Now()
	_, e := (Exec{Timeout: 100 * time.Millisecond, Limit: 100, DockerConfig: dir}).Run(context.Background(), script, dir, nil)
	if e == nil || time.Since(start) > time.Second {
		t.Fatal("timeout failed", e)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("grandchild survived")
	}
}
