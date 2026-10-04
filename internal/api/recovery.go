package api

import (
	"net/http"
	"strings"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
)

// Recover an interrupted operation from the desktop app. This runs the same
// verified reconcile as `dockyard -reconcile-job`: containers are never
// started, stopped or relabeled; only observed state is recorded.
func (a *API) reconcileJob(w http.ResponseWriter, r *http.Request, principal auth.Principal, id string) {
	request := principal.RequestID
	if !strings.HasPrefix(id, "job-") || len(id) > 64 || strings.ContainsAny(id, "/\\.") {
		problem(w, 400, "REQUEST_INVALID", request)
		return
	}
	e := a.Engine
	job, err := e.Store.Job(id)
	if err != nil {
		problem(w, 404, "JOB_NOT_FOUND", request)
		return
	}
	if !require(w, principal, "deploy.execute", job.ProjectID) {
		return
	}
	if p, err := e.Store.Project(job.ProjectID); err == nil && p.NativeCompose() && !a.nativeAuthority(w, principal) {
		return
	}
	if job.Status != "recovery_required" {
		problem(w, 409, "JOB_NOT_RECOVERABLE", request)
		return
	}
	job, project, err := e.ReconcileOnline(r.Context(), id)
	if err != nil {
		fail(w, err, request)
		return
	}
	write(w, 200, map[string]any{"job_id": job.ID, "project_id": job.ProjectID, "status": job.Status, "error": job.Error, "warning": job.Warning, "project_state": project.State, "containers_preserved": true})
}
