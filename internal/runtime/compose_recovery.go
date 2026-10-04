package runtime

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

// ObserveCompose recovers a single stack without a managed Caddy route. It
// never starts/stops containers or reads the mutable operator Compose mirror.
// Original adopted IDs identify the adoption snapshot; later managed stacks
// must match Docker Compose's per-service configuration hashes.
func (d Docker) ObserveCompose(ctx context.Context, p model.Project) (*model.Release, error) {
	if !p.NativeCompose() || len(p.Domains) != 0 || p.ZeroDowntime || len(p.ServiceInstances) != 0 || p.Active == "green" {
		return nil, model.Uncertain("COMPOSE_RECOVERY_UNSUPPORTED")
	}
	if _, green := p.Slots["green"]; green {
		return nil, model.Uncertain("COMPOSE_RECOVERY_UNSUPPORTED")
	}
	before, err := d.recoveryContainers(ctx, p)
	if err != nil {
		return nil, err
	}
	running := false
	// Unlabeled containers are the adopted source stack (ownsNative verified its
	// project, folder and config files); nativeHealthy verifies service counts.
	legacy := p.Adoption != nil && len(before) > 0
	for _, c := range before {
		running = running || c.State.Running
		legacy = legacy && c.Config.Labels["io.ziqx.dockyard.server"] == "" && c.Config.Labels["io.ziqx.dockyard.project"] == ""
	}
	var observed *model.Release
	if running {
		if legacy && len(p.Releases) > 0 {
			// ownsNative already checked the adopted source identity labels.
			r := p.Releases[0]
			_, b, err := nativeRelease(d.Config, p, r)
			if err != nil {
				return nil, err
			}
			// Without exact IDs, require exactly the baseline's containers per
			// service so stray or duplicate source containers are never accepted.
			if !exactServiceCounts(b, before) {
				return nil, model.Uncertain("CONTAINER_OWNERSHIP_UNKNOWN")
			}
			observed = &r
		} else {
			candidates := []model.Release{}
			if r, ok := p.Slots["blue"]; ok {
				candidates = append(candidates, r)
			}
			for i := len(p.Releases) - 1; i >= 0; i-- {
				candidates = append(candidates, p.Releases[i])
			}
			seen := map[string]bool{}
			for _, r := range candidates {
				key := r.Compose + ":" + r.Environment
				if seen[key] {
					continue
				}
				seen[key] = true
				hashes, err := d.recoveryHashes(ctx, p, r)
				if err != nil {
					return nil, err
				}
				matches := true
				for _, c := range before {
					service := c.Config.Labels["com.docker.compose.service"]
					matches = matches && hashes[service] != "" && hashes[service] == c.Config.Labels["com.docker.compose.config-hash"]
				}
				if matches {
					if observed != nil {
						return nil, model.Uncertain("COMPOSE_LIVE_RELEASE_AMBIGUOUS")
					}
					observed = &r
				}
			}
		}
		if observed == nil {
			return nil, model.Uncertain("COMPOSE_LIVE_RELEASE_UNKNOWN")
		}
		// Health/count verification uses the positively identified release, not
		// the candidate left in Slots by an interrupted deployment.
		verified := p
		verified.Active = "blue"
		verified.Slots = map[string]model.Release{"blue": *observed}
		if err := d.nativeHealthy(ctx, verified, "blue", *observed); err != nil {
			return nil, err
		}
	}
	after, err := d.recoveryContainers(ctx, p)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(before, after) {
		return nil, model.Uncertain("COMPOSE_RECOVERY_STATE_CHANGED")
	}
	return observed, nil // nil means all positively identified containers are stopped/absent.
}

func (d Docker) recoveryContainers(ctx context.Context, p model.Project) ([]Container, error) {
	// Include orphans: Compose ps for a candidate can hide services from the old release.
	result, err := d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), []string{"ps", "--all", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project=" + d.composeName(p, "blue")})
	if err != nil || result.Truncated {
		return nil, model.Fail("DOCKER_UNAVAILABLE")
	}
	ids := strings.Fields(string(result.Output))
	if len(ids) > 1000 {
		return nil, model.Fail("COMPOSE_LIMIT")
	}
	expected := map[string]bool{}
	for _, id := range ids {
		if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" || expected[id] {
			return nil, model.Uncertain("CONTAINER_OWNERSHIP_UNKNOWN")
		}
		expected[id] = true
	}
	if len(ids) == 0 {
		return []Container{}, nil
	}
	result, err = d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), append([]string{"inspect", "--type", "container"}, ids...))
	var containers []Container
	if err != nil || result.Truncated || json.Unmarshal(result.Output, &containers) != nil || len(containers) != len(ids) {
		return nil, model.Fail("DOCKER_UNAVAILABLE")
	}
	for _, c := range containers {
		if !expected[c.ID] || !d.ownsNative(p, "blue", c) || c.Config.Labels["com.docker.compose.oneoff"] == "True" {
			return nil, model.Uncertain("CONTAINER_OWNERSHIP_UNKNOWN")
		}
		delete(expected, c.ID)
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].ID < containers[j].ID })
	return containers, nil
}

func (d Docker) recoveryHashes(ctx context.Context, p model.Project, r model.Release) (map[string]string, error) {
	path, _, err := nativeRelease(d.Config, p, r)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(projectDir(d.Config, p.ID), ".recovery-binding-*")
	if err != nil {
		return nil, model.Fail("PROJECT_FILES_FAILED")
	}
	defer os.Remove(f.Name())
	_, err = f.Write(bindingBytes(d.Config, p, "blue", r))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return nil, model.Fail("PROJECT_FILES_FAILED")
	}
	args := d.args(p, "blue", "config", "--hash", "*")
	args[6], args[8] = path, f.Name()
	result, err := d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), args)
	if err != nil || result.Truncated {
		return nil, model.Uncertain("COMPOSE_RECOVERY_HASH_UNAVAILABLE")
	}
	hashes := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(result.Output)), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 || len(parts[1]) != 64 || strings.Trim(parts[1], "0123456789abcdef") != "" || hashes[parts[0]] != "" {
			return nil, model.Uncertain("COMPOSE_RECOVERY_HASH_UNAVAILABLE")
		}
		hashes[parts[0]] = parts[1]
	}
	return hashes, nil
}

func exactServiceCounts(release []byte, containers []Container) bool {
	var doc map[string]any
	if json.Unmarshal(release, &doc) != nil {
		return false
	}
	want := map[string]int{}
	for name, raw := range object(doc["services"]) {
		svc := object(raw)
		want[name] = 1
		if scale, ok := svc["scale"].(float64); ok {
			want[name] = int(scale)
		}
		if replicas, ok := object(svc["deploy"])["replicas"].(float64); ok {
			want[name] = int(replicas)
		}
	}
	for _, c := range containers {
		want[c.Config.Labels["com.docker.compose.service"]]--
	}
	for _, n := range want {
		if n != 0 {
			return false
		}
	}
	return true
}
