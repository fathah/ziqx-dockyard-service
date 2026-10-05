package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

// explainStart appends, to the job's deployment log, why containers of a
// failed `up` are not running or healthy: state, healthcheck output and the
// last log lines. It only reads; nothing is started or stopped.
func (d Docker) explainStart(ctx context.Context, p model.Project, slot string) {
	w := process.JobLog(ctx)
	if w == nil {
		return
	}
	res, err := d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), []string{"ps", "--all", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project=" + d.composeName(p, slot)})
	if err != nil {
		return
	}
	ids := strings.Fields(string(res.Output))
	if len(ids) == 0 || len(ids) > 50 {
		return
	}
	res, err = d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), append([]string{"inspect", "--type", "container"}, ids...))
	if err != nil {
		return
	}
	var containers []struct {
		ID     string `json:"Id"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		State struct {
			Status   string `json:"Status"`
			Running  bool   `json:"Running"`
			ExitCode int    `json:"ExitCode"`
			Error    string `json:"Error"`
			Health   *struct {
				Status string `json:"Status"`
				Log    []struct {
					ExitCode int    `json:"ExitCode"`
					Output   string `json:"Output"`
				} `json:"Log"`
			} `json:"Health"`
		} `json:"State"`
	}
	if json.Unmarshal(res.Output, &containers) != nil {
		return
	}
	for _, c := range containers {
		healthy := c.State.Running && (c.State.Health == nil || c.State.Health.Status == "healthy")
		if healthy {
			continue
		}
		service := c.Config.Labels["com.docker.compose.service"]
		state := c.State.Status
		if c.State.Health != nil {
			state += ", " + c.State.Health.Status
		}
		if !c.State.Running {
			state += fmt.Sprintf(", exit code %d", c.State.ExitCode)
		}
		fmt.Fprintf(w, "\n── %s: %s\n", service, state)
		if c.State.Error != "" {
			fmt.Fprintf(w, "Docker: %s\n", c.State.Error)
		}
		if h := c.State.Health; h != nil && len(h.Log) > 0 {
			last := h.Log[len(h.Log)-1]
			fmt.Fprintf(w, "Healthcheck (exit %d): %s\n", last.ExitCode, strings.TrimSpace(last.Output))
		}
		logs, _ := d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), []string{"logs", "--tail", "40", c.ID})
		if out := strings.TrimSpace(string(logs.Output) + string(logs.Stderr)); out != "" {
			fmt.Fprintf(w, "Last log lines:\n%s\n", out)
		}
	}
}
