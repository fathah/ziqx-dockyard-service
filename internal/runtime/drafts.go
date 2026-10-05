package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
)

// A draft is the operator's last saved Compose file and .env, kept verbatim
// (not validated) so edits survive a failed or not-yet-run deployment.
func draftDir(c config.Config, p model.Project) string {
	return filepath.Join(projectDir(c, p.ID), "draft")
}

func draftSHA(compose, env string) string {
	h := sha256.Sum256([]byte(compose + "\x00" + env))
	return hex.EncodeToString(h[:])
}

// SaveDraft stores the files privately and returns their combined hash.
func SaveDraft(c config.Config, p model.Project, compose, env string) (string, error) {
	dir := draftDir(c, p)
	if err := secure.PrivateDir(dir); err != nil {
		return "", model.Fail("PROJECT_FILES_FAILED")
	}
	if err := secure.Atomic(filepath.Join(dir, "compose.yml"), []byte(compose), 0600); err != nil {
		return "", model.Fail("PROJECT_FILES_FAILED")
	}
	if err := secure.Atomic(filepath.Join(dir, ".env"), []byte(env), 0600); err != nil {
		return "", model.Fail("PROJECT_FILES_FAILED")
	}
	return draftSHA(compose, env), nil
}

// ReadDraft returns the saved draft, if there is one.
func ReadDraft(c config.Config, p model.Project) (compose, env string, ok bool) {
	dir := draftDir(c, p)
	cb, err := os.ReadFile(filepath.Join(dir, "compose.yml"))
	if err != nil {
		return "", "", false
	}
	eb, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		return "", "", false
	}
	return string(cb), string(eb), true
}

// ClearDraft removes the draft once its exact content is deployed; a newer
// draft saved while the deployment ran is kept.
func ClearDraft(c config.Config, p model.Project, sha string) {
	if sha == "" {
		return
	}
	if compose, env, ok := ReadDraft(c, p); ok && draftSHA(compose, env) == sha {
		os.RemoveAll(draftDir(c, p))
	}
}
