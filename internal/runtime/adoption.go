package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

// AdoptionPlan stays server-side; Source and Dotenv must never be returned in API responses.
type AdoptionPlan struct {
	Identity                      model.Adoption
	Source, Dotenv, SHA256, State string
	Services                      int
}

func (d Docker) ownsNative(p model.Project, slot string, c Container) bool {
	labels := c.Config.Labels
	if labels["com.docker.compose.project"] != d.composeName(p, slot) {
		return false
	}
	if labels["io.ziqx.dockyard.server"] == d.Config.ServerID && labels["io.ziqx.dockyard.project"] == p.ID {
		return true
	}
	if p.Adoption == nil || labels["io.ziqx.dockyard.server"] != "" || labels["io.ziqx.dockyard.project"] != "" {
		return false
	}
	if labels["com.docker.compose.project.working_dir"] != d.workingDir(p) || labels["com.docker.compose.project.config_files"] != strings.Join(p.Adoption.ConfigFiles, ",") {
		return false
	}
	for _, id := range p.Adoption.ContainerIDs {
		if c.ID == id {
			return true
		}
	}
	return false
}

// Existing administrator-selected files may be owned by a deploy user. Reject
// links and escapes, snapshot their content, and bind approval to the resolved
// config and exact container identities. Never execute source shell commands.
func adoptionRead(root, path string, limit int64) ([]byte, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, model.Fail("MIGRATION_SOURCE_UNSAFE")
	}
	for current := path; ; current = filepath.Dir(current) {
		st, err := os.Lstat(current)
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return nil, model.Fail("MIGRATION_SOURCE_UNSAFE")
		}
		if current == path {
			if !st.Mode().IsRegular() || st.Size() > limit {
				return nil, model.Fail("MIGRATION_SOURCE_UNSAFE")
			}
		} else if !st.IsDir() {
			return nil, model.Fail("MIGRATION_SOURCE_UNSAFE")
		}
		if current == root {
			break
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, model.Fail("MIGRATION_SOURCE_UNREADABLE")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, model.Fail("MIGRATION_SOURCE_UNREADABLE")
	}
	return b, nil
}

func (d Docker) PlanAdoption(ctx context.Context, name string) (AdoptionPlan, error) {
	plan := AdoptionPlan{}
	if !config.ID.MatchString(name) {
		return plan, model.Fail("PROJECT_ID_SUPPORTED")
	}
	dir := filepath.Join(d.Config.ProjectsRoot, name)
	result, err := d.Runner.Run(ctx, d.Config.DockerBinary, d.Config.ProjectsRoot, []string{"ps", "--all", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project.working_dir=" + dir})
	if err != nil || result.Truncated {
		return plan, model.Fail("DOCKER_UNAVAILABLE")
	}
	ids := strings.Fields(string(result.Output))
	if len(ids) == 0 || len(ids) > 1000 {
		return plan, model.Fail("MIGRATION_CONTAINERS_NOT_FOUND")
	}
	for _, id := range ids {
		if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
			return plan, model.Fail("CONTAINER_OWNERSHIP_UNKNOWN")
		}
	}
	sort.Strings(ids)
	result, err = d.Runner.Run(ctx, d.Config.DockerBinary, d.Config.ProjectsRoot, append([]string{"inspect", "--type", "container"}, ids...))
	var containers []Container
	if err != nil || result.Truncated || json.Unmarshal(result.Output, &containers) != nil || len(containers) != len(ids) {
		return plan, model.Fail("DOCKER_UNAVAILABLE")
	}
	labels := containers[0].Config.Labels
	project := labels["com.docker.compose.project"]
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`).MatchString(project) {
		return plan, model.Fail("MIGRATION_COMPOSE_IDENTITY_UNKNOWN")
	}
	files := strings.Split(labels["com.docker.compose.project.config_files"], ",")
	if len(files) == 0 || len(files) > 16 {
		return plan, model.Fail("MIGRATION_COMPOSE_IDENTITY_UNKNOWN")
	}
	plan.Identity = model.Adoption{SourceName: name, ComposeProject: project, ConfigFiles: files, ContainerIDs: ids}
	plan.State = "running"
	expected := map[string]bool{}
	for _, id := range ids {
		expected[id] = true
	}
	digest := sha256.New()
	for _, file := range files {
		if !filepath.IsAbs(file) || filepath.Clean(file) != file {
			return plan, model.Fail("MIGRATION_SOURCE_UNSAFE")
		}
		b, err := adoptionRead(dir, file, 64<<10)
		if err != nil {
			return plan, err
		}
		digest.Write(b)
		digest.Write([]byte{0})
	}
	dotenvPath := filepath.Join(dir, ".env")
	if _, err := os.Lstat(dotenvPath); !os.IsNotExist(err) {
		b, err := adoptionRead(dir, dotenvPath, 64<<10)
		if err != nil {
			return plan, err
		}
		plan.Dotenv = string(b)
	}
	for _, c := range containers {
		l := c.Config.Labels
		if !expected[c.ID] || l["com.docker.compose.project"] != project || l["com.docker.compose.project.working_dir"] != dir || l["com.docker.compose.project.config_files"] != strings.Join(files, ",") || l["io.ziqx.dockyard.server"] != "" || l["io.ziqx.dockyard.project"] != "" || strings.EqualFold(l["com.docker.compose.oneoff"], "true") {
			return plan, model.Fail("MIGRATION_COMPOSE_IDENTITY_UNKNOWN")
		}
		delete(expected, c.ID)
		if !c.State.Running {
			plan.State = "stopped"
		}
	}
	// Ensure the Compose project name isn't shared with another folder.
	result, err = d.Runner.Run(ctx, d.Config.DockerBinary, dir, []string{"ps", "--all", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project=" + project})
	all := strings.Fields(string(result.Output))
	sort.Strings(all)
	if err != nil || result.Truncated || strings.Join(all, ",") != strings.Join(ids, ",") {
		return plan, model.Fail("MIGRATION_COMPOSE_IDENTITY_UNKNOWN")
	}
	args := []string{"compose", "--project-name", project, "--project-directory", dir}
	for _, file := range files {
		args = append(args, "--file", file)
	}
	args = append(args, "config", "--format", "json")
	result, err = d.Runner.Run(ctx, d.Config.DockerBinary, dir, args)
	if err != nil || result.Truncated || len(result.Output) > 64<<10 {
		return plan, model.Fail("COMPOSE_VALIDATION_FAILED")
	}
	var doc map[string]any
	if json.Unmarshal(result.Output, &doc) != nil || len(object(doc["services"])) == 0 {
		return plan, model.Fail("COMPOSE_VALIDATION_FAILED")
	}
	services := object(doc["services"])
	seen := map[string]bool{}
	for _, c := range containers {
		service := c.Config.Labels["com.docker.compose.service"]
		if services[service] == nil {
			return plan, model.Fail("MIGRATION_SERVICE_MISMATCH")
		}
		seen[service] = true
	}
	if len(seen) != len(services) {
		return plan, model.Fail("MIGRATION_SERVICE_MISMATCH")
	}
	// Resolved paths, resource names and environment are frozen for the baseline.
	plan.Source = string(result.Output)
	plan.Services = len(services)
	identity, _ := json.Marshal(plan.Identity)
	digest.Write(identity)
	digest.Write([]byte(plan.Source))
	digest.Write([]byte(plan.Dotenv))
	digest.Write([]byte(plan.State))
	plan.SHA256 = hex.EncodeToString(digest.Sum(nil))
	return plan, nil
}
