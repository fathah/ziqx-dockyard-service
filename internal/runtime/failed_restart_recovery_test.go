package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func TestObserveFailedRestartVerifiesIdentityWithoutRequiringHealth(t *testing.T) {
	for _, scenario := range []string{"stopped", "crash-loop", "wrong-image", "wrong-owner", "wrong-config", "changed-id"} {
		t.Run(scenario, func(t *testing.T) {
			d, p, runner := runtimeServiceFixture(t)
			c := runner.containers["web"]
			c.State.Running = false
			c.State.Health.Status = "unhealthy"
			hash := strings.Repeat("c", 64)
			c.Config.Labels["com.docker.compose.config-hash"] = hash
			release := p.Slots[p.Active]
			release.Image = c.Image
			plan := model.ServiceUpdate{Service: "web", Mode: "restart", Instance: model.ServiceInstance{Name: "web", Release: release}}
			switch scenario {
			case "wrong-image":
				c.Image = "sha256:" + strings.Repeat("9", 64)
			case "wrong-owner":
				c.Config.Labels["io.ziqx.dockyard.project"] = "foreign"
			case "wrong-config":
				c.Config.Labels["com.docker.compose.config-hash"] = strings.Repeat("d", 64)
			}
			d.Runner = &recoveryRunner{t: t, containers: []Container{c}, hashes: map[string]string{release.Compose + ".yml": "web " + hash}, change: scenario == "crash-loop", changeID: scenario == "changed-id"}
			observed, err := d.ObserveFailedServiceRestart(context.Background(), p, plan)
			if scenario == "stopped" || scenario == "crash-loop" {
				if err != nil || len(observed.ContainerIDs) != 1 || observed.ContainerIDs[0] != c.ID {
					t.Fatal("verified failed restart cannot be recorded", observed, err)
				}
			} else if err == nil {
				t.Fatal("unverified restart was adopted", observed)
			}
		})
	}
}
