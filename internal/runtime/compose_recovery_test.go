package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

type recoveryRunner struct {
	t          *testing.T
	containers []Container
	hashes     map[string]string
	reads      int
	change     bool
}

func (r *recoveryRunner) Run(_ context.Context, _ string, _ string, args []string) (process.Result, error) {
	for _, arg := range args {
		if arg == "up" || arg == "stop" || arg == "down" || arg == "restart" || arg == "pull" {
			r.t.Fatal("recovery mutated containers", args)
		}
	}
	if args[0] == "inspect" {
		containers := append([]Container{}, r.containers...)
		r.reads++
		if r.change && r.reads > 1 {
			containers[0].State.Running = !containers[0].State.Running
		}
		b, _ := json.Marshal(containers)
		return process.Result{Output: b}, nil
	}
	if args[0] == "compose" && args[9] == "config" {
		return process.Result{Output: []byte(r.hashes[filepath.Base(args[6])])}, nil
	}
	ids := []string{}
	for _, c := range r.containers {
		ids = append(ids, c.ID)
	}
	return process.Result{Output: []byte(strings.Join(ids, "\n"))}, nil
}

func recoveryFixture(t *testing.T) (Docker, model.Project, *recoveryRunner) {
	t.Helper()
	d, _, dir := adoptionFixture(t)
	plan, err := d.PlanAdoption(context.Background(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	p := model.Project{ID: "existing-abc", Mode: "compose", Adoption: &plan.Identity, Active: "blue", State: "stopped"}
	os.Mkdir(projectDir(d.Config, p.ID), 0700)
	old, err := d.PrepareNative(context.Background(), p, plan.Source, plan.Dotenv)
	if err != nil {
		t.Fatal(err)
	}
	old.ID = "old"
	newRelease, err := d.PrepareNative(context.Background(), p, plan.Source, "TOKEN=new\n")
	if err != nil {
		t.Fatal(err)
	}
	newRelease.ID = "candidate"
	p.Slots = map[string]model.Release{"blue": newRelease}
	p.Releases = []model.Release{old}
	if err := Binding(d.Config, p, "blue", newRelease); err != nil {
		t.Fatal(err)
	}
	c := Container{ID: plan.Identity.ContainerIDs[0]}
	c.State.Running = true
	c.Config.Labels = map[string]string{"com.docker.compose.project": "legacy_app", "com.docker.compose.service": "web", "com.docker.compose.project.working_dir": dir, "com.docker.compose.project.config_files": filepath.Join(dir, "compose.yml")}
	runner := &recoveryRunner{t: t, containers: []Container{c}, hashes: map[string]string{}}
	d.Runner = runner
	return d, p, runner
}

func TestComposeRecoveryOriginalAdoptedIDsSelectBaseline(t *testing.T) {
	d, p, runner := recoveryFixture(t)
	r, err := d.ObserveCompose(context.Background(), p)
	if err != nil || r == nil || r.ID != "old" {
		t.Fatal("assigned failed candidate to original containers", r, err)
	}
	if len(runner.hashes) != 0 || p.Slots["blue"].ID != "candidate" {
		t.Fatal("changed source project")
	}
}

func TestComposeRecoveryManagedHashesSelectActualRelease(t *testing.T) {
	for _, selected := range []string{"old", "candidate"} {
		t.Run(selected, func(t *testing.T) {
			d, p, runner := recoveryFixture(t)
			c := &runner.containers[0]
			c.ID = strings.Repeat("b", 64)
			c.Config.Labels["io.ziqx.dockyard.server"] = d.Config.ServerID
			c.Config.Labels["io.ziqx.dockyard.project"] = p.ID
			c.Config.Labels["com.docker.compose.config-hash"] = strings.Repeat("c", 64)
			for _, release := range []model.Release{p.Releases[0], p.Slots["blue"]} {
				hash := strings.Repeat("d", 64)
				if release.ID == selected {
					hash = strings.Repeat("c", 64)
				}
				runner.hashes[release.Compose+".yml"] = "web " + hash + "\n"
			}
			r, err := d.ObserveCompose(context.Background(), p)
			if err != nil || r == nil || r.ID != selected {
				t.Fatal(r, err)
			}
		})
	}
}

func TestComposeRecoveryRefusesUnverifiedLiveState(t *testing.T) {
	for _, variant := range []string{"foreign", "extra", "unhealthy", "changing", "hash-drift", "ambiguous", "missing-service", "overrides"} {
		t.Run(variant, func(t *testing.T) {
			d, p, runner := recoveryFixture(t)
			c := &runner.containers[0]
			switch variant {
			case "foreign":
				c.Config.Labels["io.ziqx.dockyard.server"] = "other-server"
			case "extra":
				extra := *c
				extra.ID = strings.Repeat("b", 64)
				runner.containers = append(runner.containers, extra)
			case "unhealthy":
				c.State.Health = &struct {
					Status string `json:"Status"`
				}{Status: "unhealthy"}
			case "changing":
				runner.change = true
			case "hash-drift", "ambiguous":
				c.Config.Labels["io.ziqx.dockyard.server"] = d.Config.ServerID
				c.Config.Labels["io.ziqx.dockyard.project"] = p.ID
				c.Config.Labels["com.docker.compose.config-hash"] = strings.Repeat("d", 64)
				if variant == "ambiguous" {
					c.Config.Labels["com.docker.compose.config-hash"] = strings.Repeat("c", 64)
				}
				for _, release := range []model.Release{p.Releases[0], p.Slots["blue"]} {
					runner.hashes[release.Compose+".yml"] = "web " + strings.Repeat("c", 64)
				}
			case "missing-service":
				c.Config.Labels["com.docker.compose.service"] = "unknown"
			case "overrides":
				p.ServiceInstances = map[string]model.ServiceInstance{"web": {}}
			}
			if _, err := d.ObserveCompose(context.Background(), p); err == nil {
				t.Fatal("unverified state cleared recovery")
			}
		})
	}
}

func TestComposeRecoveryObservesStoppedOrAbsentStack(t *testing.T) {
	for _, absent := range []bool{false, true} {
		d, p, runner := recoveryFixture(t)
		runner.containers[0].State.Running = false
		if absent {
			runner.containers = nil
		}
		r, err := d.ObserveCompose(context.Background(), p)
		if err != nil || r != nil {
			t.Fatal("stopped stack reported as running", r, err)
		}
	}
}

// A `docker compose up` in the adopted source folder recreates containers with
// new IDs. The source identity labels still prove ownership.
func TestComposeRecoveryRecreatedSourceContainersSelectBaseline(t *testing.T) {
	d, p, runner := recoveryFixture(t)
	runner.containers[0].ID = strings.Repeat("e", 64)
	r, err := d.ObserveCompose(context.Background(), p)
	if err != nil || r == nil || r.ID != "old" {
		t.Fatal("recreated source containers not recovered", r, err)
	}
}

func TestComposeRecoveryRejectsForeignSourceIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*Container){
		"config files": func(c *Container) {
			c.Config.Labels["com.docker.compose.project.config_files"] = "/elsewhere/compose.yml"
		},
		"working dir":  func(c *Container) { c.Config.Labels["com.docker.compose.project.working_dir"] = "/elsewhere" },
		"one-off":      func(c *Container) { c.Config.Labels["com.docker.compose.oneoff"] = "True" },
		"other server": func(c *Container) { c.Config.Labels["io.ziqx.dockyard.server"] = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			d, p, runner := recoveryFixture(t)
			runner.containers[0].ID = strings.Repeat("e", 64)
			mutate(&runner.containers[0])
			if _, err := d.ObserveCompose(context.Background(), p); faultCode(err) != "CONTAINER_OWNERSHIP_UNKNOWN" {
				t.Fatal(err)
			}
		})
	}
}

func faultCode(err error) string {
	var f *model.Fault
	if errors.As(err, &f) {
		return f.Code
	}
	return ""
}
