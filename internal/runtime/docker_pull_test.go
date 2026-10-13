package runtime

import (
	"context"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

type pullRunner struct{ calls [][]string }

func (r *pullRunner) Run(_ context.Context, _ string, _ string, args []string) (process.Result, error) {
	r.calls = append(r.calls, append([]string{}, args...))
	return process.Result{}, nil
}

func TestNativePullFetchesLatestImages(t *testing.T) {
	d, p, _ := runtimeServiceFixture(t)
	runner := &pullRunner{}
	d.Runner = runner
	if err := d.Pull(context.Background(), p, p.Slots["blue"]); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || len(runner.calls[0]) != 10 || runner.calls[0][0] != "compose" || runner.calls[0][9] != "pull" {
		t.Fatal("Compose project images were not pulled", runner.calls)
	}
}
