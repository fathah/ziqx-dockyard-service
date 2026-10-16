package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
)

// A draft is the operator's last saved Compose file and environment files, kept
// verbatim so edits survive a failed or not-yet-run deployment. Save and clear
// share a lock so completing a deployment cannot remove a newer saved bundle.
var draftFilesMu sync.RWMutex

type draftBundle struct {
	Version int               `json:"version"`
	Compose string            `json:"compose_yaml"`
	EnvFile string            `json:"env_file"`
	Files   map[string]string `json:"env_files"`
}

func draftDir(c config.Config, p model.Project) string {
	return filepath.Join(projectDir(c, p), "draft")
}

func draftSHA(compose, env string, extra ...map[string]string) string {
	content := compose + "\x00" + env
	if len(extra) > 0 && len(extra[0]) > 0 {
		b, _ := json.Marshal(extra[0])
		content += "\x00" + string(b)
	}
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// SaveDraft stores the files privately and returns their combined hash.
func SaveDraft(c config.Config, p model.Project, compose, env string, extra ...map[string]string) (string, error) {
	if len(compose) > 64<<10 {
		return "", model.Fail("COMPOSE_INPUT_INVALID")
	}
	files := map[string]string{}
	if len(extra) > 0 {
		files = copyEnvFiles(extra[0])
	}
	if err := ValidateEnvFiles(env, files); err != nil {
		return "", err
	}
	b, err := json.Marshal(draftBundle{Version: 1, Compose: compose, EnvFile: env, Files: files})
	if err != nil {
		return "", model.Fail("PROJECT_FILES_FAILED")
	}
	draftFilesMu.Lock()
	defer draftFilesMu.Unlock()
	dir := draftDir(c, p)
	if err := secure.PrivateDir(dir); err != nil {
		return "", model.Fail("PROJECT_FILES_FAILED")
	}
	if err := secure.Atomic(filepath.Join(dir, "draft.json"), b, 0600); err != nil {
		return "", model.Fail("PROJECT_FILES_FAILED")
	}
	return draftSHA(compose, env, files), nil
}

// ReadDraftEnvFiles reads named files saved with a draft. Old drafts contain
// only .env and return an empty map. File contents require secret authorization.
func ReadDraftEnvFiles(c config.Config, p model.Project) (map[string]string, error) {
	_, _, files, _, err := ReadDraftConfiguration(c, p)
	return files, err
}

// ReadDraft returns the saved draft, if there is one.
func ReadDraft(c config.Config, p model.Project) (compose, env string, ok bool) {
	compose, env, _, ok, _ = ReadDraftConfiguration(c, p)
	return compose, env, ok
}

// ReadDraftConfiguration reads all saved inputs from one atomic bundle. Use this
// when Compose, .env and named files are consumed together: separate accessor
// calls can otherwise select different saves while an operator is editing.
// Legacy drafts with individual files remain readable until the next save.
func ReadDraftConfiguration(c config.Config, p model.Project) (compose, env string, files map[string]string, ok bool, err error) {
	draftFilesMu.RLock()
	defer draftFilesMu.RUnlock()
	return readDraftConfiguration(c, p)
}

func readDraftConfiguration(c config.Config, p model.Project) (compose, env string, files map[string]string, ok bool, err error) {
	dir := draftDir(c, p)
	path := filepath.Join(dir, "draft.json")
	if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
		b, readErr := readEnvFile(path, 1<<20)
		var bundle draftBundle
		if readErr != nil || secure.Decode(b, &bundle) != nil || bundle.Version != 1 || len(bundle.Compose) > 64<<10 {
			return "", "", nil, false, model.Fail("PROJECT_FILES_FAILED")
		}
		if err := ValidateEnvFiles(bundle.EnvFile, bundle.Files); err != nil {
			return "", "", nil, false, err
		}
		return bundle.Compose, bundle.EnvFile, copyEnvFiles(bundle.Files), true, nil
	}
	// The old layout is a compatibility fallback only; never merge legacy files
	// with a bundle, even when a bundle is malformed or unreadable.
	for _, name := range []string{"compose.yml", ".env"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); os.IsNotExist(err) {
			return "", "", map[string]string{}, false, nil
		}
	}
	cb, composeErr := readEnvFile(filepath.Join(dir, "compose.yml"), 64<<10)
	eb, envErr := readEnvFile(filepath.Join(dir, ".env"), maxEnvBytes)
	if composeErr != nil || envErr != nil {
		return "", "", nil, false, model.Fail("PROJECT_FILES_FAILED")
	}
	files = map[string]string{}
	path = filepath.Join(dir, ".env-files.json")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		b, readErr := readEnvFile(path, maxEnvBytes*6+4096)
		if readErr != nil || secure.Decode(b, &files) != nil {
			return "", "", nil, false, model.Fail("ENV_FILES_INVALID")
		}
	}
	if err := ValidateEnvFiles(string(eb), files); err != nil {
		return "", "", nil, false, err
	}
	return string(cb), string(eb), copyEnvFiles(files), true, nil
}

// ClearDraft removes the draft once its exact content is deployed; a newer
// draft saved while the deployment ran is kept.
func ClearDraft(c config.Config, p model.Project, sha string) {
	if sha == "" {
		return
	}
	draftFilesMu.Lock()
	defer draftFilesMu.Unlock()
	if compose, env, files, ok, err := readDraftConfiguration(c, p); err == nil && ok && draftSHA(compose, env, files) == sha {
		os.RemoveAll(draftDir(c, p))
	}
}
