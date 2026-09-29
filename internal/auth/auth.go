package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,95}$`)
var signature = regexp.MustCompile(`^[0-9a-f]{64}$`)
var headers = []string{"X-Deploy-Key-ID", "X-Deploy-Timestamp", "X-Deploy-Scopes", "Idempotency-Key", "X-Actor-ID", "X-Request-ID", "X-Deploy-Signature"}

type Principal struct {
	KeyID, Actor, RequestID, Idempotency, Scopes string
	Projects                                     []string
}

func (p Principal) Has(scope string) bool {
	for _, s := range strings.Fields(p.Scopes) {
		if s == scope {
			return true
		}
	}
	return false
}
func (p Principal) Allows(id string) bool {
	for _, s := range p.Projects {
		if s == "*" || s == id {
			return true
		}
	}
	return false
}

type material struct {
	policy config.Key
	secret []byte
	tokens float64
	at     time.Time
}
type Verifier struct {
	server string
	keys   map[string]*material
	mu     sync.Mutex
	Now    func() time.Time
}

func ReadSecret(path string) ([]byte, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	decoded, e := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if e != nil || len(decoded) < 32 {
		return nil, errors.New("secret must be base64 with at least 32 bytes")
	}
	return decoded, nil
}
func New(c config.Config) (*Verifier, error) {
	v := &Verifier{server: c.ServerID, keys: map[string]*material{}, Now: time.Now}
	for _, k := range c.Keys {
		b, e := ReadSecret(k.SecretFile)
		if e != nil {
			return nil, e
		}
		v.keys[k.ID] = &material{policy: k, secret: b, tokens: 60, at: time.Now()}
	}
	return v, nil
}

func canonical(scopes string) bool {
	x := strings.Fields(scopes)
	if len(x) == 0 || len(x) > 10 || strings.Join(x, " ") != scopes || !sort.StringsAreSorted(x) {
		return false
	}
	for i, s := range x {
		if !config.KnownScope(s) || i > 0 && x[i-1] == s {
			return false
		}
	}
	return true
}
func TargetOK(r *http.Request) bool {
	if r.URL.RawPath != "" || strings.Contains(r.RequestURI, "%") || strings.Contains(r.URL.Path, "//") || strings.Contains(r.URL.Path, "..") || strings.ContainsAny(r.RequestURI, "\r\n\x00\\") || strings.HasSuffix(r.URL.Path, "/") {
		return false
	}
	return r.RequestURI == r.URL.RequestURI() && strings.HasPrefix(r.URL.Path, "/v1/")
}

func SigningInput(server string, r *http.Request, body []byte) string {
	h := sha256.Sum256(body)
	return strings.Join([]string{"deploy-agent-v1", server, r.Header.Get(headers[0]), r.Header.Get(headers[1]), r.Header.Get(headers[3]), r.Header.Get(headers[4]), r.Header.Get(headers[5]), r.Header.Get(headers[2]), r.Method, r.URL.RequestURI(), hex.EncodeToString(h[:])}, "\n")
}
func Sign(secret []byte, server string, r *http.Request, body []byte) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(SigningInput(server, r, body)))
	return hex.EncodeToString(h.Sum(nil))
}

func (v *Verifier) Verify(r *http.Request, body []byte) (Principal, error) {
	var p Principal
	denied := errors.New("AUTHENTICATION_FAILED")
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 || !TargetOK(r) {
		return p, denied
	}
	for _, name := range headers {
		vals := r.Header.Values(name)
		if len(vals) != 1 || len(vals[0]) > 1024 || strings.ContainsAny(vals[0], "\r\n\x00") {
			return p, denied
		}
	}
	for _, name := range []string{headers[0], headers[3], headers[4], headers[5]} {
		if !identifier.MatchString(r.Header.Get(name)) {
			return p, denied
		}
	}
	stamp := r.Header.Get(headers[1])
	n, e := strconv.ParseInt(stamp, 10, 64)
	if e != nil || strconv.FormatInt(n, 10) != stamp {
		return p, denied
	}
	now := v.Now().Unix()
	if n < now-60 || n > now+60 || !canonical(r.Header.Get(headers[2])) {
		return p, denied
	}
	k, ok := v.keys[r.Header.Get(headers[0])]
	if !ok {
		return p, denied
	}
	fingerprint := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
	if !hmac.Equal([]byte(hex.EncodeToString(fingerprint[:])), []byte(k.policy.CertificateSHA256)) {
		return p, denied
	}
	sig := r.Header.Get(headers[6])
	if !signature.MatchString(sig) || !hmac.Equal([]byte(Sign(k.secret, v.server, r, body)), []byte(sig)) {
		return p, denied
	}
	for _, scope := range strings.Fields(r.Header.Get(headers[2])) {
		allowed := false
		for _, s := range k.policy.Scopes {
			if s == scope {
				allowed = true
			}
		}
		if !allowed {
			return p, errors.New("SCOPE_REQUIRED")
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	t := v.Now()
	elapsed := t.Sub(k.at).Seconds()
	if elapsed > 0 {
		k.tokens += elapsed * 5
		if k.tokens > 60 {
			k.tokens = 60
		}
		k.at = t
	}
	if k.tokens < 1 {
		return p, errors.New("RATE_LIMITED")
	}
	k.tokens--
	return Principal{KeyID: k.policy.ID, Actor: r.Header.Get(headers[4]), RequestID: r.Header.Get(headers[5]), Idempotency: r.Header.Get(headers[3]), Scopes: r.Header.Get(headers[2]), Projects: k.policy.Projects}, nil
}

func Fingerprint(key []byte, server string, r *http.Request, body []byte) string {
	// Keyed stable identity protects low-entropy environment values in SQLite.
	h := hmac.New(sha256.New, key)
	h.Write([]byte(strings.Join([]string{"deploy-agent-v1", server, r.Header.Get(headers[4]), r.Header.Get(headers[5]), r.Header.Get(headers[2]), r.Method, r.URL.RequestURI()}, "\n")))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}
