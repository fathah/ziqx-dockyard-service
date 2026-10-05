package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	apidocs "github.com/ziqx/ziqx-dockyard-service/docs"
	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/engine"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type API struct {
	Engine           *engine.Engine
	Auth             *auth.Verifier
	FingerprintKey   []byte
	slots            chan struct{}
	logSlots         chan struct{}
	rateMu           sync.Mutex
	tokens           float64
	at               time.Time
	authRejected     uint64
	lastRejectionLog time.Time
}

func New(e *engine.Engine, v *auth.Verifier, fp []byte) *API {
	return &API{Engine: e, Auth: v, FingerprintKey: fp, slots: make(chan struct{}, 32), logSlots: make(chan struct{}, 2), tokens: 100, at: time.Now()}
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, code, request string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": "The operation could not be completed.", "request_id": request}})
}

// problemDetail adds Docker's (redacted) explanation, when a fault carries one.
func problemDetail(w http.ResponseWriter, status int, code, detail, request string) {
	body := map[string]string{"code": code, "message": "The operation could not be completed.", "request_id": request}
	if detail != "" {
		body["detail"] = detail
	}
	write(w, status, map[string]any{"error": body})
}
func faultCode(err error) string {
	var f *model.Fault
	if errors.As(err, &f) {
		return f.Code
	}
	return "DEPENDENCY_UNAVAILABLE"
}
func fail(w http.ResponseWriter, err error, request string) {
	code := faultCode(err)
	status := 409
	switch code {
	case "QUEUE_FULL", "PROJECT_LIMIT":
		status = 429
	case "DEPENDENCY_UNAVAILABLE", "DOCKER_UNAVAILABLE", "CADDY_UNAVAILABLE", "CADDY_CONFIG_WRITE_REQUIRED", "RECOVERY_REQUIRED", "METADATA_UNAVAILABLE":
		status = 503
	case "HOSTNAME_INVALID", "HOSTNAME_NOT_ALLOWED", "PORT_INVALID", "ENVIRONMENT_INVALID", "IMAGE_INVALID", "REQUEST_INVALID", "COMPOSE_INVALID", "COMPOSE_PERSISTENT_BLUE_GREEN", "COMPOSE_RESOURCE_LIMIT":
		status = 400
	}
	detail := ""
	var f *model.Fault
	if errors.As(err, &f) {
		detail = f.Detail
	}
	problemDetail(w, status, code, detail, request)
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if apidocs.Serve(w, r) {
		return
	}
	if r.Method == "GET" && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
		if r.URL.Path == "/readyz" && !a.Engine.Ready() {
			problem(w, 503, "RECOVERY_REQUIRED", "")
			return
		}
		write(w, 200, map[string]string{"status": "ok"})
		return
	}
	a.rateMu.Lock()
	now := time.Now()
	a.tokens += now.Sub(a.at).Seconds() * 20
	if a.tokens > 100 {
		a.tokens = 100
	}
	a.at = now
	ok := a.tokens >= 1
	if ok {
		a.tokens--
	}
	a.rateMu.Unlock()
	if !ok {
		problem(w, 429, "RATE_LIMITED", "")
		return
	}
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	default:
		problem(w, 429, "RATE_LIMITED", "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		problem(w, 413, "BODY_TOO_LARGE", "")
		return
	}
	p, err := a.Auth.Verify(r, body)
	if err != nil {
		// Aggregate rejection counts without persisting untrusted actor/body data.
		a.rateMu.Lock()
		a.authRejected++
		if time.Since(a.lastRejectionLog) >= time.Minute {
			slog.Warn("authentication requests rejected", "count", a.authRejected)
			a.authRejected = 0
			a.lastRejectionLog = time.Now()
		}
		a.rateMu.Unlock()
		code := err.Error()
		status := 401
		if code == "RATE_LIMITED" {
			status = 429
		}
		if code == "SCOPE_REQUIRED" {
			status = 403
		}
		problem(w, status, code, "")
		return
	}
	if r.Method == "GET" {
		if len(body) > 0 {
			problem(w, 400, "REQUEST_INVALID", p.RequestID)
			return
		}
		a.read(w, r, p)
		return
	}
	if r.Method != "POST" && r.Method != "PUT" {
		problem(w, 405, "METHOD_NOT_ALLOWED", p.RequestID)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || r.URL.RawQuery != "" {
		problem(w, 400, "REQUEST_INVALID", p.RequestID)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	if r.Method == "POST" && len(parts) == 3 && parts[0] == "jobs" && parts[2] == "reconcile" {
		a.reconcileJob(w, r, p, parts[1])
		return
	}
	if r.Method == "POST" && len(parts) == 3 && parts[0] == "inventory" && parts[2] == "migrate" {
		a.adopt(w, r, p, body, parts[1])
		return
	}
	if r.Method == "POST" && len(parts) == 3 && parts[0] == "projects" && (parts[2] == "blue-green" || parts[2] == "blue-green-preview") {
		a.blueGreen(w, r, p, body, parts[1], parts[2] == "blue-green-preview")
		return
	}
	if r.Method == "POST" && len(parts) == 3 && parts[0] == "projects" && (parts[2] == "service-update" || parts[2] == "service-update-preview") {
		a.serviceUpdate(w, r, p, body, parts[1], parts[2] == "service-update-preview")
		return
	}
	if r.Method == "POST" && len(parts) == 3 && parts[0] == "projects" && (parts[2] == "route-setup" || parts[2] == "route-setup-preview") {
		a.routeSetup(w, r, p, body, parts[1], parts[2] == "route-setup-preview")
		return
	}
	a.mutate(w, r, p, body)
}
func require(w http.ResponseWriter, p auth.Principal, scope, id string) bool {
	if !p.Has(scope) || id != "" && !p.Allows(id) {
		problem(w, 403, "SCOPE_REQUIRED", p.RequestID)
		return false
	}
	return true
}
func query(r *http.Request, allowed ...string) (map[string]string, error) {
	out := map[string]string{}
	if r.URL.RawQuery == "" {
		return out, nil
	}
	for _, part := range strings.Split(r.URL.RawQuery, "&") {
		k, v, ok := strings.Cut(part, "=")
		found := false
		for _, a := range allowed {
			if k == a {
				found = true
			}
		}
		if !ok || !found || v == "" {
			return nil, model.Fail("REQUEST_INVALID")
		}
		if _, ok := out[k]; ok {
			return nil, model.Fail("REQUEST_INVALID")
		}
		out[k] = v
	}
	return out, nil
}
func (a *API) read(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	e := a.Engine
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	if len(parts) == 3 && parts[0] == "inventory" && parts[2] == "migration" {
		if !require(w, p, "deploy.read", "") {
			return
		}
		if !p.Allows("*") {
			problem(w, 403, "SCOPE_REQUIRED", p.RequestID)
			return
		}
		if _, err := query(r); err != nil || !config.ID.MatchString(parts[1]) || !strings.HasPrefix(parts[1], "existing-") {
			problem(w, 400, "REQUEST_INVALID", p.RequestID)
			return
		}
		assessment, found, err := migrationAssessment(r.Context(), e, parts[1])
		if err != nil {
			fail(w, err, p.RequestID)
			return
		}
		if !found {
			problem(w, 404, "PROJECT_NOT_FOUND", p.RequestID)
			return
		}
		write(w, 200, assessment)
		return
	}
	if r.URL.Path == "/v1/inventory" {
		if !require(w, p, "deploy.read", "") {
			return
		}
		if !p.Allows("*") {
			problem(w, 403, "SCOPE_REQUIRED", p.RequestID)
			return
		}
		if _, err := query(r); err != nil {
			fail(w, err, p.RequestID)
			return
		}
		inventory, err := e.Store.Inventory()
		if err != nil {
			fail(w, err, p.RequestID)
			return
		}
		write(w, 200, inventory)
		return
	}
	if r.URL.Path == "/v1/ports/next" {
		if !require(w, p, "projects.write", "") {
			return
		}
		if _, err := query(r); err != nil {
			fail(w, err, p.RequestID)
			return
		}
		e.Admission.Lock()
		port, err := e.AvailablePort(r.Context(), map[int]bool{})
		e.Admission.Unlock()
		if err != nil {
			fail(w, err, p.RequestID)
			return
		}
		write(w, 200, map[string]any{"port": port, "reserved": false})
		return
	}
	if len(parts) == 2 && parts[0] == "requests" {
		// Resolve an uncertain write: was this idempotency key ever admitted?
		// Signed requests expire after 60s, so "not found" later is definitive.
		if _, err := query(r); err != nil || parts[1] == "" || len(parts[1]) > 128 || strings.ContainsAny(parts[1], "\\.") {
			problem(w, 400, "REQUEST_INVALID", p.RequestID)
			return
		}
		if !require(w, p, "deploy.read", "") {
			return
		}
		j, found, err := e.Store.RequestJob(parts[1])
		if err != nil {
			fail(w, err, p.RequestID)
			return
		}
		if !found || !p.Allows(j.ProjectID) {
			problem(w, 404, "REQUEST_NOT_FOUND", p.RequestID)
			return
		}
		write(w, 200, map[string]string{"job_id": j.ID, "project_id": j.ProjectID, "action": j.Action, "status": j.Status})
		return
	}
	if len(parts) == 3 && parts[0] == "jobs" && parts[2] == "log" {
		if _, err := query(r); err != nil {
			fail(w, err, p.RequestID)
			return
		}
		j, err := e.Store.Job(parts[1])
		if err != nil {
			problem(w, 404, "JOB_NOT_FOUND", p.RequestID)
			return
		}
		if !require(w, p, "deploy.logs", j.ProjectID) {
			return
		}
		// Return the newest 512 KiB; the file itself is capped at 2 MiB.
		text, truncated := "", false
		if f, err := os.Open(e.JobLogPath(j.ID)); err == nil {
			defer f.Close()
			if st, err := f.Stat(); err == nil && st.Size() > 512<<10 {
				f.Seek(-512<<10, io.SeekEnd)
				truncated = true
			}
			b, _ := io.ReadAll(io.LimitReader(f, 512<<10))
			text = string(b)
		}
		write(w, 200, map[string]any{"job_id": j.ID, "log": text, "truncated": truncated})
		return
	}
	if len(parts) == 3 && parts[0] == "jobs" && parts[2] == "events" {
		if _, err := query(r); err != nil {
			fail(w, err, p.RequestID)
			return
		}
		j, err := e.Store.Job(parts[1])
		if err != nil {
			problem(w, 404, "JOB_NOT_FOUND", p.RequestID)
			return
		}
		// Docker output may reveal configuration, so it shares the logs scope.
		if !require(w, p, "deploy.logs", j.ProjectID) {
			return
		}
		events, err := e.Store.JobEvents(j.ID)
		if err != nil {
			fail(w, err, p.RequestID)
			return
		}
		write(w, 200, map[string]any{"job_id": j.ID, "events": events, "diagnostic": j.Diagnostic})
		return
	}
	if len(parts) == 2 && parts[0] == "jobs" {
		if _, err := query(r); err != nil {
			fail(w, err, p.RequestID)
			return
		}
		j, err := e.Store.Job(parts[1])
		if err != nil {
			problem(w, 404, "JOB_NOT_FOUND", p.RequestID)
			return
		}
		if !require(w, p, "deploy.read", j.ProjectID) {
			return
		}
		j.Diagnostic = "" // Only the logs-scoped events endpoint returns it.
		write(w, 200, j)
		return
	}
	if r.URL.Path == "/v1/audit" {
		if !require(w, p, "deploy.read", "") {
			return
		}
		if !p.Allows("*") {
			problem(w, 403, "SCOPE_REQUIRED", p.RequestID)
			return
		}
		q, err := query(r, "after")
		if err != nil {
			fail(w, err, p.RequestID)
			return
		}
		var n int64
		if q["after"] != "" {
			n, err = strconv.ParseInt(q["after"], 10, 64)
			if err != nil || n < 0 {
				problem(w, 400, "REQUEST_INVALID", p.RequestID)
				return
			}
		}
		items, err := e.Store.Audit(n)
		if err != nil {
			fail(w, err, p.RequestID)
			return
		}
		write(w, 200, map[string]any{"events": items})
		return
	}
	if r.URL.Path == "/v1/projects" {
		if !require(w, p, "deploy.read", "") {
			return
		}
		if _, err := query(r); err != nil {
			fail(w, err, p.RequestID)
			return
		}
		projects, err := e.Store.Projects()
		if err != nil {
			fail(w, err, p.RequestID)
			return
		}
		visible := []model.Project{}
		for _, project := range projects {
			if p.Allows(project.ID) {
				visible = append(visible, project)
			}
		}
		write(w, 200, map[string]any{"projects": visible})
		return
	}
	if len(parts) < 2 || len(parts) > 3 || parts[0] != "projects" || !config.ID.MatchString(parts[1]) {
		problem(w, 404, "NOT_FOUND", p.RequestID)
		return
	}
	id := parts[1]
	scope := "deploy.read"
	if len(parts) == 3 && parts[2] == "configuration" {
		scope = "deploy.environment"
	}
	if len(parts) == 3 && parts[2] == "logs" {
		select {
		case a.logSlots <- struct{}{}:
			defer func() { <-a.logSlots }()
		default:
			problem(w, 429, "RATE_LIMITED", p.RequestID)
			return
		}
		scope = "deploy.logs"
	}
	if !require(w, p, scope, id) {
		return
	}
	project, err := e.Store.Project(id)
	if err != nil {
		problem(w, 404, "PROJECT_NOT_FOUND", p.RequestID)
		return
	}
	if len(parts) == 3 && parts[2] == "configuration" {
		if _, err := query(r); err != nil {
			fail(w, err, p.RequestID)
			return
		}
		if !a.nativeAuthority(w, p) {
			return
		}
		if !project.NativeCompose() {
			problem(w, 409, "CONFIGURATION_UNAVAILABLE", p.RequestID)
			return
		}
		current, exists := project.Current()
		if !exists {
			write(w, 200, map[string]string{"release_id": "", "compose_yaml": "", "env_file": ""})
			return
		}
		source, dotenv, err := runtime.EditableConfiguration(e.Config, project, current)
		if err != nil {
			fail(w, err, p.RequestID)
			return
		}
		write(w, 200, map[string]string{"release_id": current.ID, "compose_yaml": source, "env_file": dotenv})
		return
	}
	if len(parts) == 3 && parts[2] == "logs" {
		q, err := query(r, "slot", "service", "tail", "since")
		if err != nil {
			fail(w, err, p.RequestID)
			return
		}
		slot := q["slot"]
		if slot == "" || slot == "active" {
			slot = project.Active
		}
		if slot == "inactive" {
			slot = "blue"
			if project.Active == "blue" {
				slot = "green"
			}
		}
		if slot != "blue" && slot != "green" || slot == "green" && !project.ZeroDowntime {
			problem(w, 400, "REQUEST_INVALID", p.RequestID)
			return
		}
		tail := 200
		if q["tail"] != "" {
			tail, err = strconv.Atoi(q["tail"])
			if err != nil || tail < 1 || tail > 2000 {
				problem(w, 400, "REQUEST_INVALID", p.RequestID)
				return
			}
		}
		since := q["since"]
		if since == "" {
			since = "30m"
		}
		duration, err := time.ParseDuration(since)
		if err != nil || duration <= 0 || duration > 24*time.Hour {
			problem(w, 400, "REQUEST_INVALID", p.RequestID)
			return
		}
		service := q["service"]
		if service == "" {
			service = "app"
			if project.NativeCompose() {
				service = project.RouteService
				if service == "" {
					items, err := e.Store.Services(id)
					if err == nil {
						for _, item := range items {
							if item.Slot == slot {
								service = item.Name
								break
							}
						}
					}
				}
			}
		}
		if (!project.NativeCompose() && !config.ID.MatchString(service)) || (project.NativeCompose() && !config.ServiceName.MatchString(service)) {
			problem(w, 400, "REQUEST_INVALID", p.RequestID)
			return
		}
		b, truncated, err := e.Docker.Logs(r.Context(), project, slot, service, tail, since)
		if err != nil {
			fail(w, err, p.RequestID)
			return
		}
		write(w, 200, map[string]any{"slot": slot, "service": service, "logs": string(b), "truncated": truncated})
		return
	}
	if _, err := query(r); err != nil {
		fail(w, err, p.RequestID)
		return
	}
	if len(parts) == 2 {
		write(w, 200, project)
		return
	}
	switch parts[2] {
	case "status":
		busy, _ := e.Store.Busy(id)
		route := "ok"
		health := "not_running"
		if err := e.Routes.Ensure(r.Context(), project); err != nil {
			route = faultCode(err)
		}
		if project.State == "running" {
			release, ok := project.Current()
			if !ok {
				health = "STATE_DIVERGED"
			} else if err := e.Docker.Healthy(r.Context(), project, project.Active, release); err != nil {
				health = faultCode(err)
			} else {
				health = "healthy"
			}
		}
		write(w, 200, map[string]any{"project": project, "busy": busy, "route": route, "health": health, "downtime_expected": !project.ZeroDowntime, "public_tls_state": "unverified"})
	case "releases":
		write(w, 200, map[string]any{"releases": project.Releases})
	case "services":
		services, err := e.Store.Services(id)
		if err != nil {
			fail(w, err, p.RequestID)
			return
		}
		if project.NativeCompose() {
			options, err := runtime.ServiceUpdateOptions(e.Config, project)
			if err != nil {
				fail(w, err, p.RequestID)
				return
			}
			for i := range services {
				item := &services[i]
				option := options[item.Name]
				if item.Slot == project.Active {
					item.DependsOn = option.DependsOn
					item.Updatable = option.Updatable
					item.Seamless = option.Seamless
					item.UpdateReason = option.Reason
					item.ContainerPorts = option.Ports
					if instance, ok := project.ServiceInstances[item.Name]; ok {
						item.Image = instance.Release.Image
						item.Compose = instance.Release.Compose
						item.Environment = instance.Release.Environment
					}
				}
			}
		}
		write(w, 200, map[string]any{"services": services})
	case "domains":
		domains, err := e.Store.Domains(id)
		if err != nil {
			fail(w, err, p.RequestID)
			return
		}
		write(w, 200, map[string]any{"domains": domains})
	default:
		problem(w, 404, "NOT_FOUND", p.RequestID)
	}
}

type createRequest struct {
	ID            string   `json:"id"`
	AppID         string   `json:"app_id"`
	Environment   string   `json:"environment"`
	Template      string   `json:"template_id"`
	Domains       []string `json:"domains"`
	Zero          *bool    `json:"zerodowntime"`
	Port          int      `json:"port,omitempty"`
	Secondary     int      `json:"secondary_port,omitempty"`
	RouteService  string   `json:"route_service,omitempty"`
	RoutePort     int      `json:"route_port,omitempty"`
	ReadinessPath string   `json:"readiness_path,omitempty"`
}
type deployRequest struct {
	ExpectedRelease *string           `json:"expected_release_id,omitempty"`
	Environment     string            `json:"environment"`
	Compose         string            `json:"compose_yaml"`
	Variables       map[string]string `json:"variables,omitempty"`
	EnvFile         *string           `json:"env_file,omitempty"`
	// Compose projects with domains may re-point their web service per release.
	RouteService *string `json:"route_service,omitempty"`
	RoutePort    *int    `json:"route_port,omitempty"`
}

// Full Compose can mount the host or run privileged services. It is only
// available to an explicitly enabled, server-wide administrator credential.
func (a *API) nativeAuthority(w http.ResponseWriter, p auth.Principal) bool {
	for _, key := range a.Engine.Config.Keys {
		if key.ID == p.KeyID && p.Allows("*") {
			for _, scope := range key.Scopes {
				if scope == "compose.admin" {
					return true
				}
			}
		}
	}
	problem(w, 403, "COMPOSE_ACCESS_REQUIRED", p.RequestID)
	return false
}

func (a *API) nativeDomains(domains []string, id string) error {
	if len(domains) > 10 {
		return model.Fail("HOSTNAME_INVALID")
	}
	seen := map[string]bool{}
	for _, host := range domains {
		if !config.Hostname(host) || seen[host] {
			return model.Fail("HOSTNAME_INVALID")
		}
		seen[host] = true
		for _, reserved := range a.Engine.Config.ReservedDomains {
			if host == reserved || strings.HasSuffix(host, "."+reserved) {
				return model.Fail("HOSTNAME_NOT_ALLOWED")
			}
		}
		owner, err := a.Engine.Store.DomainOwner(host)
		if err != nil {
			return err
		}
		if owner != "" && owner != id {
			return model.Fail("DOMAIN_IN_USE")
		}
	}
	return nil
}

type routesRequest struct {
	Domains   []string `json:"domains"`
	Port      int      `json:"port,omitempty"`
	Secondary int      `json:"secondary_port,omitempty"`
}

func (a *API) domains(domains []string, id string) error {
	if len(domains) < 1 || len(domains) > 10 {
		return model.Fail("HOSTNAME_INVALID")
	}
	seen := map[string]bool{}
	for _, host := range domains {
		if !a.Engine.Config.DomainAllowed(host) || seen[host] {
			return model.Fail("HOSTNAME_NOT_ALLOWED")
		}
		seen[host] = true
		owner, err := a.Engine.Store.DomainOwner(host)
		if err != nil {
			return err
		}
		if owner != "" && owner != id {
			return model.Fail("DOMAIN_IN_USE")
		}
	}
	return nil
}
func (a *API) port(ctx context.Context, n int, id string, exclude map[int]bool) (int, error) {
	e := a.Engine
	if n == 0 {
		return e.AvailablePort(ctx, exclude)
	}
	if !e.Config.PortAllowed(n) || exclude[n] {
		return 0, model.Fail("PORT_INVALID")
	}
	r, err := e.Store.Reserved(n, id)
	if err != nil {
		return 0, err
	}
	if r {
		return 0, model.Fail("PORT_IN_USE")
	}
	free, err := e.Docker.PortFree(ctx, n)
	if err != nil {
		return 0, err
	}
	if !free {
		return 0, model.Fail("PORT_IN_USE")
	}
	return n, nil
}
func (a *API) mutate(w http.ResponseWriter, r *http.Request, principal auth.Principal, body []byte) {
	e := a.Engine
	request := principal.RequestID
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	id := ""
	action := ""
	scope := ""
	var create createRequest
	if r.Method == "POST" && r.URL.Path == "/v1/projects" {
		if secure.Decode(body, &create) != nil || !config.ID.MatchString(create.ID) {
			problem(w, 400, "REQUEST_INVALID", request)
			return
		}
		var fields map[string]json.RawMessage
		json.Unmarshal(body, &fields)
		if raw, exists := fields["zerodowntime"]; exists && string(raw) == "null" {
			problem(w, 400, "REQUEST_INVALID", request)
			return
		}
		id = create.ID
		action = "project_create"
		scope = "projects.write"
	} else if len(parts) == 3 && parts[0] == "projects" && config.ID.MatchString(parts[1]) {
		id = parts[1]
		action = parts[2]
		switch action {
		case "deploy", "restart":
			scope = "deploy.execute"
		case "rollback":
			scope = "deploy.rollback"
		case "start":
			scope = "deploy.lifecycle"
		case "stop":
			scope = "deploy.stop"
		case "routes":
			action = "routes_update"
			scope = "sites.write"
		case "dns":
			action = "dns_create"
			scope = "dns.write"
		default:
			problem(w, 404, "NOT_FOUND", request)
			return
		}
		if action == "routes_update" && r.Method != "PUT" || action != "routes_update" && r.Method != "POST" {
			problem(w, 405, "METHOD_NOT_ALLOWED", request)
			return
		}
	} else {
		problem(w, 404, "NOT_FOUND", request)
		return
	}
	if !require(w, principal, scope, id) {
		return
	}
	if action == "project_create" && create.Template == "" {
		if !a.nativeAuthority(w, principal) {
			return
		}
	} else if existing, err := e.Store.Project(id); err == nil && existing.NativeCompose() {
		if !a.nativeAuthority(w, principal) {
			return
		}
	}
	e.Admission.Lock()
	defer e.Admission.Unlock()
	fingerprint := auth.Fingerprint(a.FingerprintKey, e.Config.ServerID, r, body)
	j, replayed, err := e.Store.Replay(principal.Idempotency, request, fingerprint)
	if err != nil {
		fail(w, err, request)
		return
	}
	if replayed {
		accepted(w, j)
		return
	}
	if !e.Ready() {
		problem(w, 503, "RECOVERY_REQUIRED", request)
		return
	}
	busy, err := e.Store.Busy(id)
	if err != nil {
		fail(w, err, request)
		return
	}
	if busy {
		problem(w, 409, "PROJECT_BUSY", request)
		return
	}
	j = model.Job{ID: state.NewID("job-"), ProjectID: id, Action: action, Status: "queued", Phase: "accepted", Actor: principal.Actor, RequestID: request, Created: time.Now().UTC()}
	if err = e.Store.Capacity(e.Config.MaxQueuedJobs, e.Config.MaxProjects, action == "project_create"); err != nil {
		fail(w, err, request)
		return
	}
	var newProject *model.Project
	if action == "project_create" {
		if create.Environment == "" {
			problem(w, 422, "DEPLOYMENT_ENVIRONMENT_REQUIRED", request)
			return
		}
		if !model.ValidEnvironment(create.Environment) || !config.ID.MatchString(create.AppID) {
			problem(w, 400, "REQUEST_INVALID", request)
			return
		}
		native := create.Template == ""
		zero := create.Environment == model.Production && !native
		if create.Zero != nil {
			zero = *create.Zero
		}
		if create.Environment != model.Production && zero {
			problem(w, 400, "BLUE_GREEN_PRODUCTION_ONLY", request)
			return
		}
		if !zero && create.Secondary != 0 {
			problem(w, 400, "PORT_INVALID", request)
			return
		}
		if owner, err := e.Store.TargetOwner(create.AppID, create.Environment); err != nil {
			fail(w, err, request)
			return
		} else if owner != "" {
			problem(w, 409, "APP_ENVIRONMENT_EXISTS", request)
			return
		}
		if _, err = e.Store.Project(id); err == nil {
			problem(w, 409, "PROJECT_EXISTS", request)
			return
		} else if err != sql.ErrNoRows {
			fail(w, err, request)
			return
		}
		if create.Domains == nil {
			create.Domains = []string{}
		}
		t, ok := e.Config.Templates[create.Template]
		if !ok && !native {
			problem(w, 400, "TEMPLATE_INVALID", request)
			return
		}
		if native {
			err = a.nativeDomains(create.Domains, id)
		} else {
			err = a.domains(create.Domains, id)
		}
		if err != nil {
			fail(w, err, request)
			return
		}
		if err = e.Routes.DomainsAvailable(r.Context(), create.Domains, nil); err != nil {
			fail(w, err, request)
			return
		}
		if native && (len(create.Domains) > 0 && (!config.ServiceName.MatchString(create.RouteService) || create.RoutePort < 1 || create.RoutePort > 65535) || len(create.Domains) == 0 && (zero || create.RouteService != "" || create.RoutePort != 0 || create.Port != 0 || create.Secondary != 0) || create.ReadinessPath != "" && (!strings.HasPrefix(create.ReadinessPath, "/") || strings.ContainsAny(create.ReadinessPath, "?#\r\n"))) {
			problem(w, 400, "COMPOSE_ROUTE_INVALID", request)
			return
		}
		blue := 0
		if !native || len(create.Domains) > 0 {
			blue, err = a.port(r.Context(), create.Port, id, map[int]bool{})
			if err != nil {
				fail(w, err, request)
				return
			}
		}
		green := 0
		if zero {
			green, err = a.port(r.Context(), create.Secondary, id, map[int]bool{blue: true})
			if err != nil {
				fail(w, err, request)
				return
			}
		} else if create.Secondary != 0 {
			problem(w, 400, "PORT_INVALID", request)
			return
		}
		p := model.Project{ID: id, AppID: create.AppID, Environment: create.Environment, Template: create.Template, TemplateRevision: engine.TemplateHash(t), Domains: create.Domains, ZeroDowntime: zero, BluePort: blue, GreenPort: green, State: "provisioning", Slots: map[string]model.Release{}, Releases: []model.Release{}}
		if native {
			p.Mode = "compose"
			p.TemplateRevision = ""
			p.RouteService = create.RouteService
			p.RoutePort = create.RoutePort
			p.ReadinessPath = create.ReadinessPath
		}
		// Refuse to adopt or overwrite an existing /docker directory.
		if err = os.Mkdir(filepath.Join(e.Config.ProjectsRoot, id), 0700); err != nil {
			problem(w, 409, "PROJECT_DIRECTORY_EXISTS", request)
			return
		}
		newProject = &p
		j.Input.Project = &p
	} else {
		p, err := e.Store.Project(id)
		if err != nil {
			problem(w, 404, "PROJECT_NOT_FOUND", request)
			return
		}
		if p.State == "provisioning" {
			problem(w, 409, "PROJECT_UNINITIALIZED", request)
			return
		}
		if err = p.ValidateTarget(); err != nil {
			fail(w, err, request)
			return
		}
		if len(p.Releases) >= 500 && (action == "deploy" || action == "rollback" || action == "restart" || action == "start") {
			problem(w, 409, "HISTORY_LIMIT", request)
			return
		}
		if !p.NativeCompose() && engine.TemplateHash(e.Config.Templates[p.Template]) != p.TemplateRevision {
			problem(w, 409, "TEMPLATE_CHANGED", request)
			return
		}
		switch action {
		case "deploy":
			var input deployRequest
			if secure.Decode(body, &input) != nil {
				problem(w, 400, "REQUEST_INVALID", request)
				return
			}
			if input.ExpectedRelease != nil {
				current, _ := p.Current()
				if *input.ExpectedRelease != current.ID {
					problem(w, 409, "CONFIGURATION_CHANGED", request)
					return
				}
			}
			if input.Environment == "" {
				problem(w, 422, "DEPLOYMENT_ENVIRONMENT_REQUIRED", request)
				return
			}
			if !model.ValidEnvironment(input.Environment) {
				problem(w, 400, "REQUEST_INVALID", request)
				return
			}
			if input.Environment != p.Environment {
				problem(w, 409, "DEPLOYMENT_ENVIRONMENT_MISMATCH", request)
				return
			}
			if p.NativeCompose() {
				if input.Variables != nil {
					problem(w, 400, "USE_ENV_FILE", request)
					return
				}
				if !require(w, principal, "deploy.environment", id) {
					return
				}
				dotenv := ""
				if input.EnvFile != nil {
					dotenv = *input.EnvFile
				} else if current, ok := p.Current(); ok {
					b, readErr := os.ReadFile(filepath.Join(e.Config.ProjectsRoot, id, "env", current.Environment+".env"))
					if readErr != nil {
						fail(w, model.Fail("ENVIRONMENT_UNAVAILABLE"), request)
						return
					}
					dotenv = string(b)
				}
				// A deploy may correct which service receives the domains' traffic.
				target := p
				if input.RouteService != nil || input.RoutePort != nil {
					if input.RouteService == nil || input.RoutePort == nil || len(p.Domains) == 0 || p.ZeroDowntime || p.PublishedRoute != nil || !config.ServiceName.MatchString(*input.RouteService) || *input.RoutePort < 1 || *input.RoutePort > 65535 {
						problem(w, 400, "COMPOSE_ROUTE_INVALID", request)
						return
					}
					target.RouteService, target.RoutePort = *input.RouteService, *input.RoutePort
					j.Input.Project = &target
				}
				preparer, ok := e.Docker.(interface {
					PrepareNative(context.Context, model.Project, string, string) (model.Release, error)
				})
				if !ok {
					fail(w, model.Fail("COMPOSE_UNAVAILABLE"), request)
					return
				}
				release, prepareErr := preparer.PrepareNative(r.Context(), target, input.Compose, dotenv)
				if prepareErr != nil {
					fail(w, prepareErr, request)
					return
				}
				release.ID = state.NewID("rel-")
				release.Created = time.Now().UTC()
				j.Input.Release = &release
				if err = e.Docker.Validate(r.Context(), target, release); err != nil {
					fail(w, err, request)
					return
				}
				if err = runtime.IndexCompose(e.Config, e.Store, target, release); err != nil {
					fail(w, err, request)
					return
				}
				break
			}
			if input.EnvFile != nil {
				problem(w, 400, "LEGACY_PROJECT_REQUIRES_VARIABLES", request)
				return
			}
			plan, parseErr := runtime.ParseCompose(e.Config, p, input.Compose)
			if parseErr != nil {
				fail(w, parseErr, request)
				return
			}
			if p.State == "stopped" {
				problem(w, 409, "PROJECT_STOPPED", request)
				return
			}
			app := plan.Services["app"]
			values := app.Environment
			if input.Variables != nil {
				if values != nil {
					problem(w, 400, "COMPOSE_INVALID", request)
					return
				}
				values = input.Variables
			}
			writesEnvironment := input.Variables != nil
			for _, service := range plan.Services {
				writesEnvironment = writesEnvironment || service.Environment != nil
			}
			if writesEnvironment && !require(w, principal, "deploy.environment", id) {
				return
			}
			if values == nil {
				current, ok := p.Current()
				if !ok {
					problem(w, 422, "ENVIRONMENT_REQUIRED", request)
					return
				}
				values, err = runtime.ReadEnvironment(e.Config, id, current.Environment)
				if err != nil {
					fail(w, err, request)
					return
				}
			}
			if err = runtime.ValidateVariables(e.Config.Templates[p.Template], values); err != nil {
				fail(w, err, request)
				return
			}
			env, envErr := runtime.Environment(e.Config, id, values)
			if envErr != nil {
				fail(w, envErr, request)
				return
			}
			compose, composeErr := runtime.PrepareCompose(e.Config, p, plan, env)
			if composeErr != nil {
				fail(w, composeErr, request)
				return
			}
			j.Input.Release = &model.Release{ID: state.NewID("rel-"), Image: app.Image, Environment: env, Compose: compose, Created: time.Now().UTC()}
			if err = e.Docker.Validate(r.Context(), p, *j.Input.Release); err != nil {
				fail(w, err, request)
				return
			}
			if err = runtime.IndexCompose(e.Config, e.Store, p, *j.Input.Release); err != nil {
				fail(w, err, request)
				return
			}
		case "rollback", "restart", "start":
			var input struct {
				ReleaseID string `json:"release_id,omitempty"`
			}
			if secure.Decode(body, &input) != nil || action != "rollback" && input.ReleaseID != "" {
				problem(w, 400, "REQUEST_INVALID", request)
				return
			}
			release, ok := p.Current()
			if !ok {
				problem(w, 409, "PROJECT_UNINITIALIZED", request)
				return
			}
			if action == "start" {
				if p.State != "stopped" {
					problem(w, 409, "PROJECT_NOT_STOPPED", request)
					return
				}
			} else if p.State != "running" {
				problem(w, 409, "PROJECT_NOT_RUNNING", request)
				return
			}
			if action == "rollback" {
				if input.ReleaseID == "" {
					if len(p.Releases) < 2 {
						problem(w, 409, "RELEASE_NOT_FOUND", request)
						return
					}
					release = p.Releases[len(p.Releases)-2]
				} else {
					found := false
					for _, r := range p.Releases {
						if r.ID == input.ReleaseID {
							release = r
							found = true
						}
					}
					if !found {
						problem(w, 404, "RELEASE_NOT_FOUND", request)
						return
					}
				}
			}
			if action == "rollback" && p.NativeCompose() && p.ServiceMode {
				release, err = runtime.RebindNativeRoute(e.Config, p, release)
				if err != nil {
					fail(w, err, request)
					return
				}
				if err = runtime.IndexCompose(e.Config, e.Store, p, release); err != nil {
					fail(w, err, request)
					return
				}
			}
			release.ID = state.NewID("rel-")
			release.Created = time.Now().UTC()
			j.Input.Release = &release
		case "stop":
			var input struct {
				Confirmation string `json:"confirmation"`
			}
			if secure.Decode(body, &input) != nil || input.Confirmation != id {
				problem(w, 400, "CONFIRMATION_REQUIRED", request)
				return
			}
			if p.State != "running" {
				problem(w, 409, "PROJECT_NOT_RUNNING", request)
				return
			}
		case "routes_update":
			if p.Adoption != nil && len(p.Domains) == 0 {
				problem(w, 409, "ADOPTED_ROUTES_PRESERVED", request)
				return
			}
			var input routesRequest
			if secure.Decode(body, &input) != nil {
				problem(w, 400, "REQUEST_INVALID", request)
				return
			}
			if p.NativeCompose() {
				if instance := p.ServiceInstances[p.RouteService]; instance.Port > 0 && input.Port != 0 && input.Port != p.BluePort {
					problem(w, 409, "SERVICE_PORT_MANAGED", request)
					return
				}
				if p.PublishedRoute != nil && (len(input.Domains) == 0 || input.Port != 0 && input.Port != p.BluePort || input.Secondary != 0) {
					problem(w, 409, "PUBLISHED_ROUTE_FIXED", request)
					return
				}
				if p.RouteService == "" && p.PublishedRoute == nil && (len(input.Domains) > 0 || input.Port != 0 || input.Secondary != 0) {
					problem(w, 400, "COMPOSE_ROUTE_NOT_CONFIGURED", request)
					return
				}
				err = a.nativeDomains(input.Domains, id)
				if p.ZeroDowntime && len(input.Domains) == 0 {
					err = model.Fail("COMPOSE_BLUE_GREEN_ROUTE_REQUIRED")
				}
			} else {
				err = a.domains(input.Domains, id)
			}
			if err != nil {
				fail(w, err, request)
				return
			}
			if input.Domains == nil {
				input.Domains = []string{}
			}
			p.Domains = input.Domains
			if input.Port != 0 && input.Port != p.BluePort || input.Secondary != 0 && input.Secondary != p.GreenPort {
				if p.State == "running" {
					problem(w, 409, "PORT_MIGRATION_REQUIRES_STOP", request)
					return
				}
				if input.Port != 0 && input.Port != p.BluePort {
					p.BluePort, err = a.port(r.Context(), input.Port, id, map[int]bool{p.GreenPort: true})
					if err != nil {
						fail(w, err, request)
						return
					}
				}
				if input.Secondary != 0 && input.Secondary != p.GreenPort {
					if !p.ZeroDowntime {
						problem(w, 400, "PORT_INVALID", request)
						return
					}
					p.GreenPort, err = a.port(r.Context(), input.Secondary, id, map[int]bool{p.BluePort: true})
					if err != nil {
						fail(w, err, request)
						return
					}
				}
			}
			j.Input.Project = &p
		case "dns_create":
			var input struct {
				Hostname string `json:"hostname"`
			}
			if secure.Decode(body, &input) != nil {
				problem(w, 400, "REQUEST_INVALID", request)
				return
			}
			found := false
			for _, h := range p.Domains {
				if h == input.Hostname {
					found = true
				}
			}
			if !found {
				problem(w, 400, "HOSTNAME_NOT_ASSIGNED", request)
				return
			}
			if e.Config.Cloudflare == nil {
				problem(w, 409, "DNS_NOT_CONFIGURED", request)
				return
			}
			j.Input.Hostname = input.Hostname
		}
	}
	if err = e.Store.Accept(j, newProject, principal.Idempotency, fingerprint, e.Config.MaxQueuedJobs, e.Config.MaxProjects); err != nil {
		// The newly created directory is empty, so removal cannot erase an application.
		if newProject != nil {
			os.Remove(filepath.Join(e.Config.ProjectsRoot, id))
		}
		fail(w, err, request)
		return
	}
	e.Notify()
	accepted(w, j)
}
func accepted(w http.ResponseWriter, j model.Job) {
	write(w, 202, map[string]string{"job_id": j.ID, "project_id": j.ProjectID, "action": j.Action, "status": "queued"})
}
