package runtime

import (
	"errors"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogRedaction(t *testing.T) {
	b := Redact([]byte("\x1b[31msecret=longsecret\x1b[0m\npartial=longse"), []string{"longsecret", "secret"}, 1000, true)
	if strings.Contains(string(b), "longse") || strings.Contains(string(b), "\x1b") || strings.Contains(string(b), "secret") || !strings.Contains(string(b), "[REDACTED]") {
		t.Fatalf("unsafe output: %q", b)
	}
}
func TestPartialSecretAtCaptureBoundary(t *testing.T) {
	if b := Redact([]byte("superse"), []string{"supersecret"}, 100, true); len(b) != 0 {
		t.Fatalf("partial secret leaked: %q", b)
	}
}
func TestMarkerDoesNotGetReredacted(t *testing.T) {
	if got := string(Redact([]byte("password"), []string{"password", "R"}, 100, false)); got != "[REDACTED]" {
		t.Fatal(got)
	}
}

func TestANSIInsertedInsideSecret(t *testing.T) {
	got := string(Redact([]byte("su\x1b[31mpersecret\x1b[0m\n"), []string{"supersecret"}, 100, false))
	if got != "[REDACTED]\n" {
		t.Fatalf("displayed secret escaped redaction: %q", got)
	}
}
func TestComposeVersion(t *testing.T) {
	for _, v := range []string{"2.30.0", "v2.40.1\n", "3.0.0"} {
		if !ComposeSupported(v) {
			t.Fatal(v)
		}
	}
	for _, v := range []string{"2.29.9", "1.30.0", "bogus"} {
		if ComposeSupported(v) {
			t.Fatal(v)
		}
	}
}

func TestFailureKeepsRedactedDockerTail(t *testing.T) {
	c := config.Config{ProjectsRoot: t.TempDir()}
	d := Docker{Config: c}
	p := model.Project{ID: "demo"}
	os.MkdirAll(filepath.Join(projectDir(c, p.ID), "env"), 0700)
	os.WriteFile(filepath.Join(projectDir(c, p.ID), "env", "env-1.env"), []byte("DB_PASSWORD=hunter2hunter2\n"), 0600)
	stderr := strings.Repeat("noise\n", 40) + "Error: bind for 0.0.0.0:3000 failed: port is already allocated (password hunter2hunter2)"
	err := d.failure(p, "CONTAINER_START_FAILED", process.Result{Stderr: []byte(stderr)})
	var f *model.Fault
	if !errors.As(err, &f) || f.Code != "CONTAINER_START_FAILED" {
		t.Fatal(err)
	}
	if !strings.Contains(f.Detail, "port is already allocated") || strings.Contains(f.Detail, "hunter2") || strings.Count(f.Detail, "\n") > 29 {
		t.Fatal(f.Detail)
	}
	if err := d.failure(p, "CONTAINER_START_FAILED", process.Result{}); !errors.As(err, &f) || f.Detail != "" {
		t.Fatal("empty output should not add detail")
	}
}

func TestSecretLikeSkipsTrivialValues(t *testing.T) {
	for _, v := range []string{"5", "3016", "true", "'false'", "10.5", "abc", "production"} {
		if secretLike(v) {
			t.Errorf("%q should not be redacted", v)
		}
	}
	for _, v := range []string{"hunter2hunter2", "mah_office", "'s3cr3t-pass'", "18f7279960bd99aa"} {
		if !secretLike(v) {
			t.Errorf("%q should be redacted", v)
		}
	}
}
