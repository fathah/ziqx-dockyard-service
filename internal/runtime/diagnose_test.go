package runtime

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

type explainRunner struct{}

func (explainRunner) Run(_ context.Context, _, _ string, args []string) (process.Result, error) {
	switch args[0] {
	case "ps":
		return process.Result{Output: []byte("aaa\nbbb\n")}, nil
	case "inspect":
		return process.Result{Output: []byte(`[
{"Id":"aaa","Config":{"Labels":{"com.docker.compose.service":"api"}},"State":{"Status":"running","Running":true,"Health":{"Status":"unhealthy","Log":[{"ExitCode":1,"Output":"curl: (7) Failed to connect to localhost port 3016"}]}}},
{"Id":"bbb","Config":{"Labels":{"com.docker.compose.service":"database"}},"State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}}}]`)}, nil
	case "logs":
		return process.Result{Output: []byte("Error: password authentication failed for user \"api\"\n")}, nil
	}
	return process.Result{}, nil
}

func TestExplainStartLogsUnhealthyContainers(t *testing.T) {
	var buf bytes.Buffer
	ctx := process.WithJobLog(context.Background(), &buf)
	d := Docker{Config: config.Config{ServerID: "vps", ProjectsRoot: t.TempDir()}, Runner: explainRunner{}}
	d.explainStart(ctx, model.Project{ID: "demo", Mode: "compose"}, "blue")
	log := buf.String()
	for _, want := range []string{"── api: running, unhealthy", "Healthcheck (exit 1): curl: (7) Failed to connect", "password authentication failed"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q in:\n%s", want, log)
		}
	}
	if strings.Contains(log, "database") {
		t.Fatal("healthy containers should not be listed", log)
	}
}
