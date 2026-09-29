package auth

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// Same deterministic fixture as the Rust desktop client, covering exact body bytes,
// header order, no trailing newline and lowercase hexadecimal HMAC.
func TestDesktopSigningInterop(t *testing.T) {
	r, err := http.NewRequest("POST", "https://127.0.0.1:9123/v1/projects/demo/restart", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"X-Deploy-Key-ID": "desktop-01", "X-Deploy-Timestamp": "1700000000", "Idempotency-Key": "op-01",
		"X-Actor-ID": "owner", "X-Request-ID": "req-01", "X-Deploy-Scopes": "deploy.execute",
	} {
		r.Header.Set(k, v)
	}
	got := Sign(bytes.Repeat([]byte{42}, 32), "vps-01", r, []byte("{}"))
	const want = "5681f2392d91fe8b3eb330325236b1eb277cc348dc845509a02eaf33d82e12a0"
	if got != want {
		t.Fatalf("desktop protocol mismatch: %s", got)
	}
}
