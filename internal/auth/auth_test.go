package auth

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"net/http"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Verifier, *http.Request, []byte) {
	t.Helper()
	secret := []byte(strings.Repeat("k", 32))
	certificate := &x509.Certificate{Raw: []byte("client certificate")}
	fp := sha256.Sum256(certificate.Raw)
	v := &Verifier{server: "vps-01", Now: func() time.Time { return time.Unix(1800000000, 0) }, keys: map[string]*material{"control-01": {secret: secret, tokens: 60, at: time.Unix(1800000000, 0), policy: config.Key{ID: "control-01", CertificateSHA256: hex.EncodeToString(fp[:]), Scopes: []string{"deploy.read", "deploy.execute"}, Projects: []string{"demo"}}}}}
	body := []byte(`{"image":"digest"}`)
	r, _ := http.NewRequest("POST", "https://agent/v1/projects/demo/deploy", strings.NewReader(string(body)))
	r.RequestURI = r.URL.RequestURI()
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
	for k, value := range map[string]string{"X-Deploy-Key-ID": "control-01", "X-Deploy-Timestamp": "1800000000", "X-Deploy-Scopes": "deploy.execute", "Idempotency-Key": "operation-01", "X-Actor-ID": "alice", "X-Request-ID": "request-01"} {
		r.Header.Set(k, value)
	}
	r.Header.Set("X-Deploy-Signature", Sign(secret, v.server, r, body))
	return v, r, body
}
func TestSignatureBoundaries(t *testing.T) {
	tests := map[string]func(*http.Request, *[]byte){
		"body tampering":        func(r *http.Request, b *[]byte) { *b = []byte(`{"image":"evil"}`) },
		"query tampering":       func(r *http.Request, b *[]byte) { r.URL.RawQuery = "slot=green"; r.RequestURI = r.URL.RequestURI() },
		"actor tampering":       func(r *http.Request, b *[]byte) { r.Header.Set("X-Actor-ID", "bob") },
		"duplicate header":      func(r *http.Request, b *[]byte) { r.Header.Add("X-Actor-ID", "alice") },
		"expired":               func(r *http.Request, b *[]byte) { r.Header.Set("X-Deploy-Timestamp", "1799999900") },
		"unverified TLS":        func(r *http.Request, b *[]byte) { r.TLS.VerifiedChains = nil },
		"different certificate": func(r *http.Request, b *[]byte) { r.TLS.PeerCertificates = []*x509.Certificate{{Raw: []byte("other")}} },
		"encoded separator":     func(r *http.Request, b *[]byte) { r.RequestURI = "/v1/projects/demo%2fdeploy" },
		"ambiguous scopes":      func(r *http.Request, b *[]byte) { r.Header.Set("X-Deploy-Scopes", "deploy.execute  deploy.read") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			v, r, b := fixture(t)
			mutate(r, &b)
			if _, e := v.Verify(r, b); e == nil {
				t.Fatal("accepted tampered request")
			}
		})
	}
	v, r, b := fixture(t)
	p, e := v.Verify(r, b)
	if e != nil || p.Actor != "alice" || !p.Allows("demo") || p.Allows("other") {
		t.Fatalf("valid request rejected: %v", e)
	}
}
func TestKeyCannotAssertUnpermittedScope(t *testing.T) {
	v, r, b := fixture(t)
	r.Header.Set("X-Deploy-Scopes", "deploy.stop")
	r.Header.Set("X-Deploy-Signature", Sign(v.keys["control-01"].secret, v.server, r, b))
	if _, e := v.Verify(r, b); e == nil || e.Error() != "SCOPE_REQUIRED" {
		t.Fatal("key exceeded policy")
	}
}
func TestFingerprintSurvivesTransportRotation(t *testing.T) {
	v, r, b := fixture(t)
	fp := Fingerprint([]byte("independent fingerprint secret"), v.server, r, b)
	r.Header.Set("X-Deploy-Timestamp", "1800000010")
	r.Header.Set("X-Deploy-Key-ID", "rotated-key")
	r.Header.Set("X-Deploy-Signature", "changed")
	if Fingerprint([]byte("independent fingerprint secret"), v.server, r, b) != fp {
		t.Fatal("retry identity changed")
	}
	if Fingerprint([]byte("independent fingerprint secret"), "other-vps", r, b) == fp {
		t.Fatal("server omitted from identity")
	}
}
func TestSigningVector(t *testing.T) {
	v, r, b := fixture(t)
	got := SigningInput(v.server, r, b)
	if !strings.HasPrefix(got, "deploy-agent-v1\nvps-01\ncontrol-01\n1800000000\noperation-01\nalice\nrequest-01\ndeploy.execute\nPOST\n/v1/projects/demo/deploy\n") || strings.HasSuffix(got, "\n") {
		t.Fatalf("wrong signing contract: %q", got)
	}
}
