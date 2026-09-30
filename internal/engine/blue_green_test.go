package engine

import (
	"context"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"strings"
	"testing"
)

type conversionDocker struct {
	*fakeDocker
	checks          int
	failAfterSwitch bool
}

func (d *conversionDocker) CheckBlueGreenSource(context.Context, model.Project) error { return nil }
func (d *conversionDocker) Healthy(ctx context.Context, p model.Project, s string, r model.Release) error {
	d.checks++
	if d.failAfterSwitch && d.checks == 2 {
		return model.Fail("CANDIDATE_UNHEALTHY")
	}
	return d.fakeDocker.Healthy(ctx, p, s, r)
}
func TestBlueGreenConversionOrdersTrafficAndPreservesOriginalOnFailure(t *testing.T) {
	for _, mode := range []string{"success", "readiness-fails", "route-rejected", "route-unknown", "post-switch-unhealthy"} {
		t.Run(mode, func(t *testing.T) {
			e, j, d, r, events := engineFixture(t, "blue")
			e.Config.DrainSeconds = 0
			old, _ := e.Store.Project(j.ProjectID)
			old.ZeroDowntime = false
			old.GreenPort = 0
			// The engine test uses an existing legacy release fixture so Binding verifies
			// a real immutable environment without involving Docker or Caddy processes.
			next := old
			next.ZeroDowntime = true
			next.Active = "green"
			next.BluePort = 3010
			next.GreenPort = 3011
			next.Slots = map[string]model.Release{"green": *j.Input.Release}
			next.Releases = []model.Release{*j.Input.Release}
			j.Action = "blue_green"
			j.Input.Previous = &old
			j.Input.Project = &next
			if err := e.Store.Update(j, &old); err != nil {
				t.Fatal(err)
			}
			cd := &conversionDocker{fakeDocker: d}
			e.Docker = cd
			switch mode {
			case "readiness-fails":
				d.healthErr = model.Fail("CANDIDATE_UNHEALTHY")
			case "route-rejected":
				r.err = model.Fail("CADDY_RELOAD_REJECTED")
			case "route-unknown":
				r.err = model.Uncertain("ROUTE_OUTCOME_UNKNOWN")
			case "post-switch-unhealthy":
				cd.failAfterSwitch = true
			}
			e.execute(context.Background(), j)
			done, _ := e.Store.Job(j.ID)
			p, _ := e.Store.Project(j.ProjectID)
			sequence := strings.Join(*events, ",")
			if mode == "success" {
				if done.Status != "succeeded" || !p.ZeroDowntime || p.Active != "green" || !strings.Contains(sequence, "health:green,route:green,health:green,stop:blue") {
					t.Fatal(done, p, sequence)
				}
			} else {
				if strings.Contains(sequence, "stop:blue") {
					t.Fatal("stopped original on failure", sequence)
				}
				if mode == "route-unknown" {
					if done.Status != "recovery_required" || strings.Contains(sequence, "stop:green") {
						t.Fatal(done, sequence)
					}
				} else {
					if done.Status != "failed" || p.ZeroDowntime || !strings.Contains(sequence, "stop:green") {
						t.Fatal(done, p, sequence)
					}
				}
			}
		})
	}
}
