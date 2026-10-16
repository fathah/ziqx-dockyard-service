package api

import (
	"net/http"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
)

type draftRequest struct {
	Compose  string            `json:"compose_yaml"`
	EnvFile  *string           `json:"env_file,omitempty"`
	EnvFiles map[string]string `json:"env_files,omitempty"`
}

// Save the Compose file and .env as-is, without validating or deploying.
// Omitting env_file keeps the draft's .env, or else the deployed one.
func (a *API) saveDraft(w http.ResponseWriter, principal auth.Principal, body []byte, id string) {
	request := principal.RequestID
	if !config.ID.MatchString(id) {
		problem(w, 400, "REQUEST_INVALID", request)
		return
	}
	if !require(w, principal, "deploy.environment", id) || !a.nativeAuthority(w, principal) {
		return
	}
	var input draftRequest
	if secure.Decode(body, &input) != nil || len(input.Compose) > 64<<10 || input.EnvFile != nil && len(*input.EnvFile) > 64<<10 {
		problem(w, 400, "REQUEST_INVALID", request)
		return
	}
	e := a.Engine
	// Serialize admission-time file reads with lifecycle/deploy preparation.
	// The worker releases Admission before running accepted jobs, so operators
	// can still save newer drafts while a deployment is in progress.
	e.Admission.Lock()
	defer e.Admission.Unlock()
	p, err := e.Store.Project(id)
	if err != nil {
		problem(w, 404, "PROJECT_NOT_FOUND", request)
		return
	}
	if !p.NativeCompose() {
		problem(w, 409, "CONFIGURATION_UNAVAILABLE", request)
		return
	}
	env := ""
	files := input.EnvFiles
	if input.EnvFile == nil || files == nil {
		_, savedMain, savedFiles, _, readErr := savedConfiguration(e.Config, p)
		if readErr != nil {
			fail(w, readErr, request)
			return
		}
		env = savedMain
		if files == nil {
			files = savedFiles
		}
	}
	if input.EnvFile != nil {
		env = *input.EnvFile
	}
	if err = runtime.ValidateEnvFiles(env, files); err != nil {
		fail(w, err, request)
		return
	}
	sha, err := runtime.SaveDraft(e.Config, p, input.Compose, env, files)
	if err != nil {
		fail(w, err, request)
		return
	}
	write(w, 200, map[string]any{"project_id": id, "draft_sha": sha, "saved": true})
}
