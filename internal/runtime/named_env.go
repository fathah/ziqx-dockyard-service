package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
)

const maxEnvBytes = 64 << 10
const maxNamedEnvFiles = 16

var namedEnvFilename = regexp.MustCompile(`^\.env\.[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// ValidEnvFilename accepts a named environment file in the project root.
// The main .env remains a separate input because Compose uses it to interpolate
// the Compose file; named files are selected by each service's env_file rules.
func ValidEnvFilename(name string) bool {
	return namedEnvFilename.MatchString(name) && !strings.Contains(name, "..")
}

// ValidateEnvFiles bounds the complete editable environment, including .env.
func ValidateEnvFiles(main string, files map[string]string) error {
	if len(main) > maxEnvBytes || strings.ContainsRune(main, 0) || len(files) > maxNamedEnvFiles {
		return model.Fail("ENV_FILES_INVALID")
	}
	total := len(main)
	for name, value := range files {
		if !ValidEnvFilename(name) || len(value) > maxEnvBytes || strings.ContainsRune(value, 0) {
			return model.Fail("ENV_FILES_INVALID")
		}
		total += len(value)
		if total > maxEnvBytes {
			return model.Fail("ENV_FILES_INVALID")
		}
	}
	return nil
}

func copyEnvFiles(files map[string]string) map[string]string {
	out := map[string]string{}
	for name, value := range files {
		out[name] = value
	}
	return out
}

func envFilesSHA(files map[string]string) string {
	// encoding/json orders map keys; normalize nil to an empty object so legacy
	// callers and an explicitly empty selector have the same file contents.
	b, _ := json.Marshal(copyEnvFiles(files))
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func namedEnvDir(c config.Config, p model.Project, revision string) string {
	return filepath.Join(projectDir(c, p), "env", revision+".files")
}

func writeNamedEnvFiles(c config.Config, p model.Project, revision string, files map[string]string) error {
	if len(files) == 0 {
		return nil
	}
	dir := namedEnvDir(c, p, revision)
	if err := secure.PrivateDir(dir); err != nil {
		return err
	}
	for name, value := range files {
		if err := secure.Atomic(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			return err
		}
	}
	return nil
}

// readEnvFile rejects links and special files, and bounds the read even if the
// file changes after its metadata is read. All callers provide validated paths.
func readEnvFile(path string, limit int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > limit {
		return nil, model.Fail("ENV_FILES_INVALID")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, model.Fail("ENV_FILES_INVALID")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(st, opened) {
		return nil, model.Fail("ENV_FILES_INVALID")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, model.Fail("ENV_FILES_INVALID")
	}
	return b, nil
}

func readNamedEnvFiles(c config.Config, p model.Project, revision string) (map[string]string, error) {
	dir := namedEnvDir(c, p, revision)
	st, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, model.Uncertain("ENVIRONMENT_IDENTITY_UNKNOWN")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > maxNamedEnvFiles {
		return nil, model.Uncertain("ENVIRONMENT_IDENTITY_UNKNOWN")
	}
	files := map[string]string{}
	for _, entry := range entries {
		if !ValidEnvFilename(entry.Name()) {
			return nil, model.Uncertain("ENVIRONMENT_IDENTITY_UNKNOWN")
		}
		b, err := readEnvFile(filepath.Join(dir, entry.Name()), maxEnvBytes)
		if err != nil {
			return nil, model.Uncertain("ENVIRONMENT_IDENTITY_UNKNOWN")
		}
		files[entry.Name()] = string(b)
	}
	if ValidateEnvFiles("", files) != nil {
		return nil, model.Uncertain("ENVIRONMENT_IDENTITY_UNKNOWN")
	}
	return files, nil
}

func verifyReleaseEnvFiles(c config.Config, p model.Project, r model.Release, doc map[string]any, main string) (map[string]string, error) {
	// Earlier immutable releases did not manage named files. Their resolved
	// container environment stays frozen and can still be rolled back safely.
	expected, exists := doc["x-dockyard-env-files-sha256"]
	if !exists {
		return map[string]string{}, nil
	}
	files, err := readNamedEnvFiles(c, p, r.Environment)
	if err != nil || ValidateEnvFiles(main, files) != nil || expected != envFilesSHA(files) {
		return nil, model.Uncertain("ENVIRONMENT_IDENTITY_UNKNOWN")
	}
	return files, nil
}

// ReadReleaseEnvFiles returns the selected release's verified, immutable named
// environment inputs. Callers must authorize the same secret access as .env.
func ReadReleaseEnvFiles(c config.Config, p model.Project, r model.Release) (map[string]string, error) {
	_, compiled, err := nativeRelease(c, p, r)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if json.Unmarshal(compiled, &doc) != nil {
		return nil, model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	return verifyReleaseEnvFiles(c, p, r, doc, "")
}

// DiscoverEnvFiles imports safe project-root .env.* files into the editor.
// Saved inputs win over mirrors, which can lag behind a draft or a deployment.
func DiscoverEnvFiles(c config.Config, p model.Project, managed map[string]string) (map[string]string, error) {
	files := copyEnvFiles(managed)
	if ValidateEnvFiles("", files) != nil {
		return nil, model.Fail("ENV_FILES_INVALID")
	}
	dir := ProjectWorkingDirectory(c, p)
	st, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return files, nil
	}
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, model.Fail("ENV_FILES_INVALID")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, model.Fail("ENV_FILES_INVALID")
	}
	for _, entry := range entries {
		name := entry.Name()
		if !ValidEnvFilename(name) {
			continue
		}
		if _, saved := files[name]; saved {
			continue
		}
		if len(files) >= maxNamedEnvFiles {
			return nil, model.Fail("ENV_FILES_INVALID")
		}
		b, err := readEnvFile(filepath.Join(dir, name), maxEnvBytes)
		if err != nil {
			return nil, err
		}
		files[name] = string(b)
	}
	if err := ValidateEnvFiles("", files); err != nil {
		return nil, err
	}
	return files, nil
}

// MirrorEnvFiles updates project-root copies only after an accepted release is
// activated. Preview and validation use the private snapshots exclusively.
func MirrorEnvFiles(c config.Config, p model.Project, r model.Release) error {
	files, err := ReadReleaseEnvFiles(c, p, r)
	if err != nil {
		return err
	}
	dir := ProjectWorkingDirectory(c, p)
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return model.Fail("PROJECT_FILES_FAILED")
	}
	for name, value := range files {
		path := filepath.Join(dir, name)
		if existing, err := os.Lstat(path); err == nil {
			if !existing.Mode().IsRegular() {
				return model.Fail("PROJECT_FILES_FAILED")
			}
		} else if !os.IsNotExist(err) {
			return model.Fail("PROJECT_FILES_FAILED")
		}
		if err := secure.Atomic(path, []byte(value), 0600); err != nil {
			return model.Fail("PROJECT_FILES_FAILED")
		}
	}
	return nil
}
