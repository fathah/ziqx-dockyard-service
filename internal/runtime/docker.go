package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type Docker struct {
	Store  *state.Store
	Config config.Config
	Runner process.Runner
}
type Container struct {
	ID     string `json:"Id"`
	Image  string `json:"Image"`
	Config struct {
		Image       string            `json:"Image"`
		Env         []string          `json:"Env"`
		Labels      map[string]string `json:"Labels"`
		Healthcheck struct {
			Test []string `json:"Test"`
		} `json:"Healthcheck"`
	} `json:"Config"`
	State struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
		Health   *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	NetworkSettings struct {
		Ports map[string][]struct{ HostIP, HostPort string } `json:"Ports"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Type, Name, Destination string
		RW                      bool
	} `json:"Mounts"`
}

func (d Docker) composeName(p model.Project, slot string) string {
	if p.Adoption != nil {
		return p.Adoption.ComposeProject
	}
	return "dy-" + d.Config.ServerID + "-" + p.ID + "-" + slot
}
func (d Docker) workingDir(p model.Project) string {
	if p.Adoption != nil {
		return filepath.Join(d.Config.ProjectsRoot, p.Adoption.SourceName)
	}
	return projectDir(d.Config, p.ID)
}
func (d Docker) args(p model.Project, slot string, args ...string) []string {
	base := []string{"compose", "--project-name", d.composeName(p, slot), "--project-directory", d.workingDir(p), "--file", filepath.Join(projectDir(d.Config, p.ID), "compose.yml"), "--env-file", bindingPath(d.Config, p.ID, slot)}
	return append(base, args...)
}
func (d Docker) command(ctx context.Context, p model.Project, slot string, args ...string) (process.Result, error) {
	path, _, err := ReleaseCompose(d.Config, p, p.Slots[slot])
	if err != nil {
		return process.Result{}, err
	}
	argv := d.args(p, slot, args...)
	argv[6] = path
	return d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), argv)
}
func (d Docker) releaseCommand(ctx context.Context, p model.Project, r model.Release, args ...string) error {
	path, _, err := ReleaseCompose(d.Config, p, r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(projectDir(d.Config, p.ID), ".compose-binding-*")
	if err != nil {
		return model.Fail("PROJECT_FILES_FAILED")
	}
	defer os.Remove(f.Name())
	_, err = f.Write(bindingBytes(d.Config, p, "blue", r))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return model.Fail("PROJECT_FILES_FAILED")
	}
	argv := d.args(p, "blue", args...)
	if p.Adoption == nil {
		argv[2] = "dy-" + d.Config.ServerID + "-" + p.ID + "-validation"
	}
	argv[6], argv[8] = path, f.Name()
	_, err = d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), argv)
	return err
}
func (d Docker) Validate(ctx context.Context, p model.Project, r model.Release) error {
	if err := d.releaseCommand(ctx, p, r, "config", "--quiet"); err != nil {
		var fault *model.Fault
		if errors.As(err, &fault) {
			return err
		}
		return model.Fail("COMPOSE_INVALID")
	}
	return nil
}
func (d Docker) Pull(ctx context.Context, p model.Project, r model.Release) error {
	if p.NativeCompose() {
		return nil
	} // Compose up honors image/build/pull_policy.
	if err := d.releaseCommand(ctx, p, r, "pull"); err != nil {
		var fault *model.Fault
		if errors.As(err, &fault) {
			return err
		}
		return model.Fail("IMAGE_PULL_FAILED")
	}
	return nil
}
func (d Docker) Start(ctx context.Context, p model.Project, slot string) error {
	if p.NativeCompose() {
		_, err := d.command(ctx, p, slot, "up", "--detach", "--force-recreate", "--wait", "--wait-timeout", strconv.Itoa(d.Config.HealthSeconds))
		if err != nil {
			return model.Fail("CONTAINER_START_FAILED")
		}
		return nil
	}
	if err := d.checkVolumes(ctx, p, slot, false); err != nil {
		return err
	}
	_, e := d.command(ctx, p, slot, "up", "-d", "--no-build", "--pull", "never", "--force-recreate", "--wait", "--wait-timeout", strconv.Itoa(d.Config.HealthSeconds))
	if e != nil {
		var fault *model.Fault
		if errors.As(e, &fault) {
			return e
		}
		return model.Fail("CONTAINER_START_FAILED")
	}
	return nil
}
func (d Docker) Inspect(ctx context.Context, p model.Project, slot string) (Container, error) {
	return d.inspectService(ctx, p, slot, "app")
}
func (d Docker) inspectService(ctx context.Context, p model.Project, slot, service string) (Container, error) {
	var c Container
	res, e := d.command(ctx, p, slot, "ps", "--all", "--quiet", service)
	if e != nil {
		return c, e
	}
	if res.Truncated {
		return c, model.Fail("DOCKER_UNAVAILABLE")
	}
	id := strings.TrimSpace(string(res.Output))
	if len(id) != 64 || strings.ContainsAny(id, " \n\r") {
		return c, model.Fail("CONTAINER_UNAVAILABLE")
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return c, model.Fail("CONTAINER_UNAVAILABLE")
		}
	}
	res, e = d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), []string{"inspect", "--type", "container", id})
	var containers []Container
	if e != nil || res.Truncated || json.Unmarshal(res.Output, &containers) != nil || len(containers) != 1 {
		return c, model.Fail("DOCKER_UNAVAILABLE")
	}
	c = containers[0]
	labels := c.Config.Labels
	if c.ID != id || labels["io.ziqx.dockyard.server"] != d.Config.ServerID || labels["io.ziqx.dockyard.project"] != p.ID || labels["com.docker.compose.project"] != "dy-"+d.Config.ServerID+"-"+p.ID+"-"+slot || labels["com.docker.compose.service"] != service {
		return Container{}, model.Uncertain("CONTAINER_OWNERSHIP_UNKNOWN")
	}
	return c, nil
}
func (d Docker) Healthy(ctx context.Context, p model.Project, slot string, r model.Release) error {
	if p.NativeCompose() {
		return d.nativeHealthy(ctx, p, slot, r)
	}
	services, err := d.services(p, r)
	if err != nil {
		return err
	}
	if err := d.checkVolumes(ctx, p, slot, true); err != nil {
		return err
	}
	for name, expected := range services {
		c, err := d.inspectService(ctx, p, slot, name)
		if err != nil {
			return err
		}
		image := expected.Image
		if r.Compose == "" {
			image = r.Image
		}
		if c.Config.Image != image || !c.State.Running || c.State.Health == nil || c.State.Health.Status != "healthy" {
			return model.Fail("CANDIDATE_UNHEALTHY")
		}
		if !reflect.DeepEqual(c.Config.Healthcheck.Test, expected.Healthcheck.Test) {
			return model.Uncertain("HEALTHCHECK_DIVERGED")
		}
		for key, value := range expected.Labels {
			if c.Config.Labels[key] != value {
				return model.Uncertain("CONTAINER_OWNERSHIP_UNKNOWN")
			}
		}
		envFile := envPath(d.Config, p.ID, r.Environment)
		if name != "app" {
			envFile = filepath.Join(projectDir(d.Config, p.ID), "env", r.Environment, name+".env")
		}
		variables, err := os.ReadFile(envFile)
		if err != nil {
			return model.Uncertain("ENVIRONMENT_IDENTITY_UNKNOWN")
		}
		actual := map[string]string{}
		for _, line := range c.Config.Env {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				actual[key] = value
			}
		}
		for _, line := range strings.Split(string(variables), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				got, present := actual[key]
				if !present || got != value {
					return model.Uncertain("ENVIRONMENT_IDENTITY_UNKNOWN")
				}
			}
		}
		if name == "app" {
			ports := c.NetworkSettings.Ports[fmt.Sprintf("%d/tcp", d.Config.Templates[p.Template].ContainerPort)]
			if len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].HostPort != strconv.Itoa(p.Port(slot)) {
				return model.Uncertain("CONTAINER_BINDING_DIVERGED")
			}
		}
		for port, bindings := range c.NetworkSettings.Ports {
			if len(bindings) > 0 && (name != "app" || port != fmt.Sprintf("%d/tcp", d.Config.Templates[p.Template].ContainerPort)) {
				return model.Uncertain("CONTAINER_BINDING_DIVERGED")
			}
		}
		mounts := map[string]string{}
		readOnly := map[string]bool{}
		for _, volume := range expected.Volumes {
			parts := strings.Split(volume, ":")
			mounts[parts[1]] = "dy-" + d.Config.ServerID + "-" + p.ID + "-" + slot + "_" + parts[0]
			readOnly[parts[1]] = len(parts) == 3 && parts[2] == "ro"
		}
		for _, mount := range c.Mounts {
			if mount.Type == "tmpfs" && mount.Destination == "/tmp" {
				continue
			}
			if mount.Type != "volume" || mounts[mount.Destination] != mount.Name || mount.RW == readOnly[mount.Destination] {
				return model.Uncertain("CONTAINER_MOUNT_DIVERGED")
			}
			delete(mounts, mount.Destination)
		}
		if len(mounts) != 0 {
			return model.Uncertain("CONTAINER_MOUNT_DIVERGED")
		}
		res, err := d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), []string{"image", "inspect", "--format", "{{.Id}}", image})
		if err != nil || res.Truncated || strings.TrimSpace(string(res.Output)) != c.Image {
			return model.Uncertain("IMAGE_IDENTITY_UNKNOWN")
		}
	}
	httpClient := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer httpClient.CloseIdleConnections()
	req, e := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d%s", p.Port(slot), d.Config.Templates[p.Template].ReadinessPath), nil)
	if e != nil {
		return model.Fail("READINESS_FAILED")
	}
	resp, e := httpClient.Do(req)
	if e != nil {
		return model.Fail("READINESS_FAILED")
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return model.Fail("READINESS_FAILED")
	}
	return nil
}
func (d Docker) Stop(ctx context.Context, p model.Project, slot string) error {
	if p.NativeCompose() {
		if _, err := d.nativeContainers(ctx, p, slot); err != nil {
			return err
		}
		if _, err := d.command(ctx, p, slot, "stop", "--timeout", "30"); err != nil {
			return model.Fail("CONTAINER_STOP_FAILED")
		}
		containers, err := d.nativeContainers(ctx, p, slot)
		if err != nil {
			return err
		}
		for _, c := range containers {
			if c.State.Running {
				return model.Uncertain("CONTAINER_STOP_FAILED")
			}
		}
		return nil
	}
	services, err := d.services(p, p.Slots[slot])
	if err != nil {
		return err
	}
	// Positively identify every service before stopping the Compose stack.
	for name := range services {
		if _, err := d.inspectService(ctx, p, slot, name); err != nil {
			return err
		}
	}
	if _, err := d.command(ctx, p, slot, "stop", "--timeout", "30"); err != nil {
		return model.Fail("CONTAINER_STOP_FAILED")
	}
	for name := range services {
		container, err := d.inspectService(ctx, p, slot, name)
		if err != nil || container.State.Running {
			return model.Uncertain("CONTAINER_STOP_FAILED")
		}
	}
	return nil
}
func (d Docker) Logs(ctx context.Context, p model.Project, slot, service string, tail int, since string) ([]byte, bool, error) {
	services, err := d.services(p, p.Slots[slot])
	if err != nil {
		return nil, false, err
	}
	if _, exists := services[service]; !exists {
		return nil, false, model.Fail("REQUEST_INVALID")
	}
	if p.NativeCompose() {
		if _, err := d.nativeContainers(ctx, p, slot); err != nil {
			return nil, false, err
		}
	} else if _, e := d.inspectService(ctx, p, slot, service); e != nil {
		return nil, false, e
	}
	res, e := d.command(ctx, p, slot, "logs", "--no-color", "--timestamps", "--tail", strconv.Itoa(tail), "--since", since, service)
	if e != nil {
		return nil, false, model.Fail("LOGS_UNAVAILABLE")
	}
	// Drop partial final lines independently before concatenating streams.
	if res.Truncated {
		res.Output = completeLines(res.Output)
		res.Stderr = completeLines(res.Stderr)
	}
	data := append(res.Output, res.Stderr...)
	// Redact all retained revisions, including inactive and rolled-back releases.
	envRoot := filepath.Join(projectDir(d.Config, p.ID), "env")
	var files []string
	err = filepath.WalkDir(envRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
			return model.Fail("LOGS_UNAVAILABLE")
		}
		if entry.IsDir() {
			if path != envRoot && filepath.Dir(path) != envRoot {
				return model.Fail("LOG_REDACTION_LIMIT")
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".env") || strings.HasSuffix(entry.Name(), ".secrets.json") {
			files = append(files, path)
			if len(files) > 4000 {
				return model.Fail("LOG_REDACTION_LIMIT")
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	var secrets []string
	unique := map[string]bool{}
	secretBytes := 0
	for _, file := range files {
		b, e := os.ReadFile(file)
		if e != nil {
			return nil, false, model.Fail("LOGS_UNAVAILABLE")
		}
		if strings.HasSuffix(file, ".secrets.json") {
			var values []string
			if json.Unmarshal(b, &values) != nil {
				return nil, false, model.Fail("LOGS_UNAVAILABLE")
			}
			for _, v := range values {
				if v != "" && !unique[v] {
					unique[v] = true
					secrets = append(secrets, v)
					secretBytes += len(v)
				}
			}
			if len(unique) > 5000 || secretBytes > 1<<20 {
				return nil, false, model.Fail("LOG_REDACTION_LIMIT")
			}
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			_, v, ok := strings.Cut(line, "=")
			if ok && v != "" && !unique[v] {
				unique[v] = true
				secretBytes += len(v)
				if len(unique) > 5000 || secretBytes > 1<<20 {
					return nil, false, model.Fail("LOG_REDACTION_LIMIT")
				}
				secrets = append(secrets, v)
			}
		}
	}
	return Redact(data, secrets, 2<<20, res.Truncated), res.Truncated || len(data) > 2<<20, nil
}

func (d Docker) PortFree(ctx context.Context, port int) (bool, error) {
	// Docker may reserve a published port without a userspace listening socket.
	res, e := d.Runner.Run(ctx, d.Config.DockerBinary, d.Config.ProjectsRoot, []string{"ps", "--format", "{{json .Ports}}"})
	if e != nil || res.Truncated {
		return false, model.Fail("DOCKER_UNAVAILABLE")
	}
	if strings.Contains(string(res.Output), ":"+strconv.Itoa(port)+"->") {
		return false, nil
	}
	l, e := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if e != nil {
		return false, nil
	}
	l.Close()
	l, e = net.Listen("tcp6", net.JoinHostPort("::", strconv.Itoa(port)))
	if e == nil {
		l.Close()
	} else if strings.Contains(e.Error(), "address already in use") {
		return false, nil
	}
	return true, nil
}

func (d Docker) services(p model.Project, r model.Release) (map[string]runtimeService, error) {
	if d.Store == nil {
		return releaseServices(d.Config, p, r)
	}
	services, err := d.Store.ComposeServices(p.ID, r.Compose)
	if err == sql.ErrNoRows {
		if err = IndexCompose(d.Config, d.Store, p, r); err != nil {
			return nil, err
		}
		return d.Store.ComposeServices(p.ID, r.Compose)
	}
	return services, err
}
