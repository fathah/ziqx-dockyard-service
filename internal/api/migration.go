package api

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/engine"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
)

type MigrationAssessment struct {
	ProjectID          string           `json:"project_id"`
	Status             string           `json:"status"`
	SourceSHA256       string           `json:"source_sha256,omitempty"`
	Checks             []MigrationCheck `json:"checks"`
	ExecutionAvailable bool             `json:"execution_available"`
	Strategy           string           `json:"strategy,omitempty"`
	ServiceCount       int              `json:"service_count,omitempty"`
}
type MigrationCheck struct {
	Code   string `json:"code"`
	Status string `json:"status"`
}
type adoptionDocker interface {
	PlanAdoption(context.Context, string) (runtime.AdoptionPlan, error)
	PrepareNative(context.Context, model.Project, string, string) (model.Release, error)
}

func observedProject(e *engine.Engine, id string) (*model.ExistingProject, error) {
	v, err := e.Store.Inventory()
	if err != nil {
		return nil, err
	}
	for _, p := range v.Projects {
		if p.ID == id && !p.Managed {
			return &p, nil
		}
	}
	return nil, nil
}
func migrationAssessment(ctx context.Context, e *engine.Engine, id string) (MigrationAssessment, bool, error) {
	result := MigrationAssessment{ProjectID: id, Status: "blocked", Checks: []MigrationCheck{}, Strategy: "adopt_in_place"}
	p, err := observedProject(e, id)
	if err != nil || p == nil {
		return result, false, err
	}
	d, ok := e.Docker.(adoptionDocker)
	if !ok {
		result.Checks = append(result.Checks, MigrationCheck{"MIGRATION_UNAVAILABLE", "blocked"})
		return result, true, nil
	}
	plan, err := d.PlanAdoption(ctx, p.Name)
	if err != nil {
		result.Checks = append(result.Checks, MigrationCheck{safeMigrationError(err), "blocked"})
		return result, true, nil
	}
	result.SourceSHA256 = plan.SHA256
	result.ServiceCount = plan.Services
	result.Status = "candidate"
	result.ExecutionAvailable = true
	result.Checks = append(result.Checks, MigrationCheck{"EXISTING_STACK_VERIFIED", "passed"}, MigrationCheck{"EXISTING_TRAFFIC_PRESERVED", "passed"})
	return result, true, nil
}
func safeMigrationError(err error) string {
	if f, ok := err.(*model.Fault); ok {
		return f.Code
	}
	return "MIGRATION_CHECK_FAILED"
}

// Adoption snapshots configuration and records existing Compose ownership.
// There are no Docker mutations or Caddy changes in this operation.
func (a *API) adopt(w http.ResponseWriter, r *http.Request, principal auth.Principal, body []byte, id string) {
	if !config.ID.MatchString(id) || !strings.HasPrefix(id, "existing-") {
		problem(w, 400, "REQUEST_INVALID", principal.RequestID)
		return
	}
	if !require(w, principal, "projects.write", id) || !require(w, principal, "deploy.environment", id) || !a.nativeAuthority(w, principal) {
		return
	}
	var input struct {
		SourceSHA256 string `json:"source_sha256"`
	}
	if secure.Decode(body, &input) != nil || len(input.SourceSHA256) != 64 || strings.Trim(input.SourceSHA256, "0123456789abcdef") != "" {
		problem(w, 400, "REQUEST_INVALID", principal.RequestID)
		return
	}
	e := a.Engine
	e.Admission.Lock()
	defer e.Admission.Unlock()
	fingerprint := auth.Fingerprint(a.FingerprintKey, e.Config.ServerID, r, body)
	job, replayed, err := e.Store.Replay(principal.Idempotency, principal.RequestID, fingerprint)
	if err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	if replayed {
		accepted(w, job)
		return
	}
	if !e.Ready() {
		problem(w, 503, "RECOVERY_REQUIRED", principal.RequestID)
		return
	}
	if _, err = e.Store.Project(id); err == nil {
		problem(w, 409, "PROJECT_EXISTS", principal.RequestID)
		return
	} else if err != sql.ErrNoRows {
		fail(w, err, principal.RequestID)
		return
	}
	source, err := observedProject(e, id)
	if err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	if source == nil {
		problem(w, 404, "PROJECT_NOT_FOUND", principal.RequestID)
		return
	}
	d, ok := e.Docker.(adoptionDocker)
	if !ok {
		problem(w, 409, "MIGRATION_UNAVAILABLE", principal.RequestID)
		return
	}
	plan, err := d.PlanAdoption(r.Context(), source.Name)
	if err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	if plan.SHA256 != input.SourceSHA256 {
		problem(w, 409, "MIGRATION_SOURCE_CHANGED", principal.RequestID)
		return
	}
	if owner, err := e.Store.TargetOwner(source.Name, model.Production); err != nil {
		fail(w, err, principal.RequestID)
		return
	} else if owner != "" {
		problem(w, 409, "APP_ENVIRONMENT_EXISTS", principal.RequestID)
		return
	}
	if err = e.Store.Capacity(e.Config.MaxQueuedJobs, e.Config.MaxProjects, true); err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	p := model.Project{ID: id, AppID: source.Name, Environment: model.Production, Mode: "compose", Adoption: &plan.Identity, Domains: []string{}, State: "provisioning", Active: "blue", Slots: map[string]model.Release{}, Releases: []model.Release{}}
	v, err := e.Store.Inventory()
	if err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	for _, site := range v.Sites {
		for _, pid := range site.ProjectIDs {
			if pid == id {
				p.ExternalDomains = append(p.ExternalDomains, site.HostMatcher)
			}
		}
	}
	dir := filepath.Join(e.Config.ProjectsRoot, id)
	if err = os.Mkdir(dir, 0700); err != nil {
		problem(w, 409, "PROJECT_DIRECTORY_EXISTS", principal.RequestID)
		return
	}
	acceptedProject := false
	defer func() {
		if !acceptedProject {
			os.RemoveAll(dir)
		}
	}() // Only our newly created private snapshot directory.
	release, err := d.PrepareNative(r.Context(), p, plan.Source, plan.Dotenv)
	if err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	release.ID = state.NewID("rel-")
	release.Created = time.Now().UTC()
	p.Slots["blue"] = release
	p.Releases = []model.Release{release}
	if err = runtime.Binding(e.Config, p, "blue", release); err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	if err = e.Docker.Validate(r.Context(), p, release); err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	// Recheck immediately before acceptance: changed files/containers need review again.
	current, err := d.PlanAdoption(r.Context(), source.Name)
	if err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	if current.SHA256 != plan.SHA256 {
		problem(w, 409, "MIGRATION_SOURCE_CHANGED", principal.RequestID)
		return
	}
	target := p
	target.State = plan.State
	job = model.Job{ID: state.NewID("job-"), ProjectID: id, Action: "project_adopt", Status: "queued", Phase: "accepted", Actor: principal.Actor, RequestID: principal.RequestID, Created: time.Now().UTC(), Input: model.Input{Project: &target}}
	if err = e.Store.Accept(job, &p, principal.Idempotency, fingerprint, e.Config.MaxQueuedJobs, e.Config.MaxProjects); err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	acceptedProject = true
	e.Notify()
	accepted(w, job)
}
