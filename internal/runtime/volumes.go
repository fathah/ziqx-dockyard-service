package runtime

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

// Named volumes are managed by Compose's project namespace. Never adopt a
// pre-existing foreign volume or a local driver with host bind options.
func (d Docker) checkVolumes(ctx context.Context, p model.Project, slot string, required bool) error {
	_, b, err := ReleaseCompose(d.Config, p, p.Slots[slot])
	if err != nil {
		return err
	}
	var document struct {
		Volumes map[string]json.RawMessage `json:"volumes"`
	}
	if json.Unmarshal(b, &document) != nil {
		return model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	project := "dy-" + d.Config.ServerID + "-" + p.ID + "-" + slot
	for key := range document.Volumes {
		name := project + "_" + key
		result, err := d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), []string{"volume", "ls", "--filter", "name=" + name, "--format", "{{.Name}}"})
		if err != nil || result.Truncated {
			return model.Fail("DOCKER_UNAVAILABLE")
		}
		exists := false
		for _, item := range strings.Fields(string(result.Output)) {
			exists = exists || item == name
		}
		if !exists {
			if required {
				return model.Uncertain("VOLUME_OWNERSHIP_UNKNOWN")
			}
			continue
		}
		result, err = d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), []string{"volume", "inspect", name})
		var volumes []struct {
			Name, Driver, Scope string
			Labels              map[string]string
			Options             map[string]string
		}
		if err != nil || result.Truncated || json.Unmarshal(result.Output, &volumes) != nil || len(volumes) != 1 {
			return model.Uncertain("VOLUME_OWNERSHIP_UNKNOWN")
		}
		volume := volumes[0]
		if volume.Name != name || volume.Driver != "local" || volume.Scope != "local" || len(volume.Options) != 0 || volume.Labels["com.docker.compose.project"] != project || volume.Labels["com.docker.compose.volume"] != key {
			return model.Uncertain("VOLUME_OWNERSHIP_UNKNOWN")
		}
	}
	return nil
}
