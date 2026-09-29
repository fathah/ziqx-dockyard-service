package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/engine"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type dockerStub struct {
	calls           int
	validationError error
}

func (f *dockerStub) Validate(context.Context, model.Project, model.Release) error {
	f.calls++
	return f.validationError
}
func (f *dockerStub) Pull(context.Context, model.Project, model.Release) error { f.calls++; return nil }
func (f *dockerStub) Start(context.Context, model.Project, string) error       { f.calls++; return nil }
func (f *dockerStub) Stop(context.Context, model.Project, string) error        { f.calls++; return nil }
func (f *dockerStub) Healthy(context.Context, model.Project, string, model.Release) error {
	f.calls++
	return nil
}
func (f *dockerStub) Logs(context.Context, model.Project, string, string, int, string) ([]byte, bool, error) {
	f.calls++
	return []byte("ok"), false, nil
}
func (f *dockerStub) PortFree(context.Context, int) (bool, error) { f.calls++; return true, nil }

type routesStub struct{ calls int }

func (f *routesStub) Ensure(context.Context, model.Project) error      { f.calls++; return nil }
func (f *routesStub) Set(context.Context, model.Project, string) error { f.calls++; return nil }
func (f *routesStub) DomainsAvailable(context.Context, []string, []string) error {
	f.calls++
	return nil
}

func apiFixture(t *testing.T) (*API, *dockerStub, *routesStub, func(string, string, string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "docker")
	os.Mkdir(root, 0700)
	secret := []byte(strings.Repeat("k", 32))
	keyFile := filepath.Join(dir, "hmac")
	os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(secret)), 0600)
	certificate := &x509.Certificate{Raw: []byte("client")}
	fp := sha256.Sum256(certificate.Raw)
	c := config.Config{ServerID: "vps-01", ProjectsRoot: root, PortMin: 3001, PortMax: 3010, MaxProjects: 10, MaxQueuedJobs: 10, AllowedDomains: []string{"example.com"}, Keys: []config.Key{{ID: "key-01", SecretFile: keyFile, Scopes: []string{"projects.write", "deploy.read", "deploy.logs", "deploy.execute", "deploy.environment", "deploy.stop", "sites.write"}, Projects: []string{"demo"}, CertificateSHA256: hex.EncodeToString(fp[:])}}, Templates: map[string]config.Template{"node": {ImageRepository: "ghcr.io/ziqx/demo", AllowedEnvironment: []string{"TOKEN"}, RequiredEnvironment: []string{"TOKEN"}, ContainerPort: 3000}}}
	s, e := state.Open(filepath.Join(dir, "state"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	verifier, e := auth.New(c)
	if e != nil {
		t.Fatal(e)
	}
	d := &dockerStub{}
	routes := &routesStub{}
	a := New(engine.New(c, s, d, routes, nil), verifier, []byte(strings.Repeat("f", 32)))
	send := func(method, path, body, scopes, op string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://agent"+path, strings.NewReader(body))
		r.RequestURI = r.URL.RequestURI()
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Deploy-Key-ID", "key-01")
		r.Header.Set("X-Deploy-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
		r.Header.Set("X-Deploy-Scopes", scopes)
		r.Header.Set("Idempotency-Key", op)
		r.Header.Set("X-Actor-ID", "alice")
		r.Header.Set("X-Request-ID", "req-"+op)
		r.Header.Set("X-Deploy-Signature", auth.Sign(secret, c.ServerID, r, []byte(body)))
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	return a, d, routes, send
}
func TestCreateRetryAndProjectPermissions(t *testing.T) {
	a, d, routes, send := apiFixture(t)
	body := `{"id":"demo","app_id":"demo","environment":"production","template_id":"node","domains":["app.example.com"],"zerodowntime":true}`
	w := send("POST", "/v1/projects", body, "projects.write", "create-01")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	calls := d.calls + routes.calls
	retry := send("POST", "/v1/projects", body, "projects.write", "create-01")
	if retry.Code != 202 || retry.Body.String() != w.Body.String() || d.calls+routes.calls != calls {
		t.Fatal("retry duplicated provisioning", retry.Code, retry.Body.String())
	}
	conflict := send("POST", "/v1/projects", strings.Replace(body, "app.example.com", "other.example.com", 1), "projects.write", "create-01")
	if conflict.Code != 409 {
		t.Fatal(conflict.Code)
	}
	foreign := send("GET", "/v1/projects/other/status", "", "deploy.read", "read-other")
	if foreign.Code != 403 {
		t.Fatal("cross-project read accepted", foreign.Code)
	}
	ps, _ := a.Engine.Store.Projects()
	if len(ps) != 1 || ps[0].BluePort == ps[0].GreenPort {
		t.Fatal("bad reservation")
	}
}
func TestRejectedInputsHaveNoSideEffects(t *testing.T) {
	cases := []struct{ body, scope string }{
		{`{"id":"demo","id":"evil","template_id":"node","domains":["app.example.com"],"zerodowntime":true}`, "projects.write"},
		{`{"id":"../evil","template_id":"node","domains":["app.example.com"],"zerodowntime":true}`, "projects.write"},
		{`{"id":"demo","app_id":"demo","environment":"production","template_id":"node","domains":["evil-example.com"],"zerodowntime":true}`, "projects.write"},
		{`{"id":"demo","app_id":"demo","environment":"production","template_id":"node","domains":["app.example.com"],"zerodowntime":"true"}`, "projects.write"},
		{`{"id":"demo","app_id":"demo","environment":"production","template_id":"node","domains":["app.example.com"],"zerodowntime":true,"compose":"bad"}`, "projects.write"},
		{`{"id":"demo","app_id":"demo","environment":"production","template_id":"node","domains":["app.example.com"],"zerodowntime":true}`, "deploy.read"},
	}
	for _, tc := range cases {
		a, d, routes, send := apiFixture(t)
		w := send("POST", "/v1/projects", tc.body, tc.scope, "op1")
		if w.Code < 400 || d.calls+routes.calls != 0 {
			t.Fatal("rejected input had side effects", w.Code, tc.body)
		}
		ps, _ := a.Engine.Store.Projects()
		if len(ps) != 0 {
			t.Fatal("stored rejected project")
		}
	}
}
func TestExistingDockerDirectoryNotAdopted(t *testing.T) {
	a, _, _, send := apiFixture(t)
	dir := filepath.Join(a.Engine.Config.ProjectsRoot, "demo")
	os.Mkdir(dir, 0700)
	os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("existing"), 0600)
	w := send("POST", "/v1/projects", `{"id":"demo","app_id":"demo","environment":"production","template_id":"node","domains":["app.example.com"],"zerodowntime":false}`, "projects.write", "op1")
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "compose.yml"))
	if string(b) != "existing" {
		t.Fatal("overwrote foreign application")
	}
}
func TestEnvironmentValuesAbsentFromDurableMetadata(t *testing.T) {
	a, _, _, send := apiFixture(t)
	c := a.Engine.Config
	p := model.Project{ID: "demo", AppID: "demo", Environment: model.Production, Template: "node", TemplateRevision: engine.TemplateHash(c.Templates["node"]), Domains: []string{"app.example.com"}, BluePort: 3001, State: "awaiting_release", Slots: map[string]model.Release{}, Releases: []model.Release{}}
	if e := runtime.Compose(c, p); e != nil {
		t.Fatal(e)
	}
	j := model.Job{ID: "seed", ProjectID: p.ID, Action: "project_create", Status: "succeeded", RequestID: "seed-request"}
	if e := a.Engine.Store.Accept(j, &p, "seed", "fp", 10, 10); e != nil {
		t.Fatal(e)
	}
	bodyBytes, _ := json.Marshal(map[string]any{"environment": "production", "compose_yaml": "services:\n  app:\n    image: ghcr.io/ziqx/demo@sha256:" + strings.Repeat("a", 64) + "\n", "variables": map[string]string{"TOKEN": "a-private-secret-$literal"}})
	body := string(bodyBytes)
	w := send("POST", "/v1/projects/demo/deploy", body, "deploy.environment deploy.execute", "deploy-01")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response map[string]string
	json.Unmarshal(w.Body.Bytes(), &response)
	accepted, _ := a.Engine.Store.Job(response["job_id"])
	meta, _ := json.Marshal(accepted.Input)
	events, _ := a.Engine.Store.Audit(0)
	if strings.Contains(string(meta), "a-private-secret") || strings.Contains(w.Body.String(), "a-private-secret") {
		t.Fatal("environment leaked in job metadata")
	}
	for _, event := range events {
		if strings.Contains(string(event), "a-private-secret") {
			t.Fatal("environment leaked in audit")
		}
	}
	read := send("GET", "/v1/projects/demo", "", "deploy.read", "read1")
	if strings.Contains(read.Body.String(), "a-private-secret") {
		t.Fatal("environment leaked in read API")
	}
}
