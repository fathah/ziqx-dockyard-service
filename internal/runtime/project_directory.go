package runtime

import (
	"os"
	"path/filepath"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
)

// Adopted projects keep their application folder and Compose identity. Private
// revisions live inside that folder, rather than in a second inventory-ID folder.
func ProjectDirectory(c config.Config, p model.Project) string {
	if p.StorageFolder != "" {
		return filepath.Join(c.ProjectsRoot, p.StorageFolder, ".dockyard")
	}
	if p.Adoption != nil {
		return filepath.Join(c.ProjectsRoot, p.Adoption.SourceName, ".dockyard")
	}
	return filepath.Join(c.ProjectsRoot, p.ID)
}

func ProjectWorkingDirectory(c config.Config, p model.Project) string {
	if p.StorageFolder != "" {
		return filepath.Join(c.ProjectsRoot, p.StorageFolder)
	}
	if p.Adoption != nil {
		return filepath.Join(c.ProjectsRoot, p.Adoption.SourceName)
	}
	return filepath.Join(c.ProjectsRoot, p.ID)
}

func EnvironmentPath(c config.Config, p model.Project, revision string) string {
	return envPath(c, p, revision)
}

func ProjectMirrorPath(c config.Config, p model.Project, file string) string {
	if p.Adoption != nil || p.StorageFolder != "" {
		if file == "compose.yml" {
			// Keep the original editable source (and its interpolation rules).
			return filepath.Join(ProjectDirectory(c, p), file)
		}
		return filepath.Join(ProjectWorkingDirectory(c, p), file)
	}
	return filepath.Join(c.ProjectsRoot, p.ID, file)
}

func adoptedSourceDirectory(c config.Config, p model.Project) error {
	if p.Adoption == nil || !config.ID.MatchString(p.Adoption.SourceName) || !config.ID.MatchString(p.ID) {
		return model.Fail("MIGRATION_SOURCE_UNSAFE")
	}
	st, err := os.Lstat(filepath.Join(c.ProjectsRoot, p.Adoption.SourceName))
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return model.Fail("MIGRATION_SOURCE_UNSAFE")
	}
	return nil
}

// CreateAdoptedProjectDirectory only creates metadata; source files are untouched.
// A conflicting directory is never reused or removed.
func CreateAdoptedProjectDirectory(c config.Config, p model.Project) (string, error) {
	if err := adoptedSourceDirectory(c, p); err != nil {
		return "", err
	}
	dir := ProjectDirectory(c, p)
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", model.Fail("PROJECT_DIRECTORY_EXISTS")
	}
	return dir, nil
}

// ConsolidateAdoptedProjectDirectory upgrades the old two-folder layout on
// startup. Rename preserves every revision/draft and is atomic on this filesystem.
// Project IDs and old job snapshots remain valid because paths use adoption identity.
func ConsolidateAdoptedProjectDirectory(c config.Config, p model.Project) error {
	if p.Adoption == nil {
		return nil
	}
	if err := adoptedSourceDirectory(c, p); err != nil {
		return err
	}
	legacy := filepath.Join(c.ProjectsRoot, p.ID)
	dir := ProjectDirectory(c, p)
	old, oldErr := os.Lstat(legacy)
	_, newErr := os.Lstat(dir)
	if oldErr == nil {
		if !old.IsDir() || old.Mode()&os.ModeSymlink != 0 || !os.IsNotExist(newErr) {
			return model.Fail("PROJECT_DIRECTORY_EXISTS")
		}
		// Verify pinned content before moving a directory under the source folder.
		baseline := p
		baseline.Adoption = nil
		for _, r := range p.Slots {
			if _, _, err := nativeRelease(c, baseline, r); err != nil {
				return err
			}
		}
		if err := os.Rename(legacy, dir); err != nil {
			return err
		}
		for _, parent := range []string{filepath.Dir(dir), c.ProjectsRoot} {
			f, err := os.Open(parent)
			if err != nil {
				return err
			}
			err = f.Sync()
			f.Close()
			if err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(oldErr) {
		return oldErr
	}
	if err := secure.PrivateDir(dir); err != nil {
		return err
	}
	// Bindings contain absolute revision paths; regenerate them after relocation,
	// including on a retry after interruption between rename and binding writes.
	for slot, r := range p.Slots {
		if err := Binding(c, p, slot, r); err != nil {
			return err
		}
	}
	return nil
}

// RestoreLegacyProjectDirectory allows a failed startup/upgrade to restore the
// previous layout, so reverting to an older server binary remains possible.
func RestoreLegacyProjectDirectory(c config.Config, p model.Project) error {
	legacy := filepath.Join(c.ProjectsRoot, p.ID)
	if _, err := os.Lstat(legacy); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(ProjectDirectory(c, p), legacy); err != nil {
		return err
	}
	baseline := p
	baseline.Adoption = nil
	for slot, r := range p.Slots {
		if err := Binding(c, baseline, slot, r); err != nil {
			return err
		}
	}
	return nil
}
