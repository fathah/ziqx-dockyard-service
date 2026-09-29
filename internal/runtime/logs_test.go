package runtime

import (
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
