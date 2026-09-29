package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
)

func composeFixture(t *testing.T) (config.Config, model.Project, string) {
	t.Helper()
	c := config.Config{ServerID: "vps-01", ProjectsRoot: t.TempDir(), HealthSeconds: 30, MaxStackMemoryMB: 1024, MaxStackCPUs: 4, Templates: map[string]config.Template{
		"web": {ImageRepository: "ghcr.io/org/web", ContainerPort: 3000, HealthCommand: []string{"healthcheck"}, AllowedEnvironment: []string{"TOKEN"}, RequiredEnvironment: []string{"TOKEN"}, User: "1000:1000", MemoryMB: 256, CPUs: 1},
		"db":  {ImageRepository: "ghcr.io/org/db", ContainerPort: 5432, HealthCommand: []string{"pg_isready"}, AllowedEnvironment: []string{"PASSWORD"}, RequiredEnvironment: []string{"PASSWORD"}, User: "999:999", MemoryMB: 256, CPUs: 1, AllowedVolumeTargets: []string{"/var/lib/postgresql/data"}},
	}}
	p := model.Project{ID: "demo", AppID: "demo", Environment: model.Production, Template: "web", BluePort: 3001, GreenPort: 3002, Slots: map[string]model.Release{}}
	if err := Compose(c, p); err != nil {
		t.Fatal(err)
	}
	source := "services:\n  app:\n    image: ghcr.io/org/web@sha256:" + strings.Repeat("a", 64) + "\n    environment:\n      TOKEN: app-secret-$HOME\n    depends_on: [db]\n    command: [serve, '$LITERAL']\n  db:\n    x-dockyard-template: db\n    image: ghcr.io/org/db@sha256:" + strings.Repeat("b", 64) + "\n    environment:\n      PASSWORD: companion-secret\n"
	return c, p, source
}

func preparedRelease(t *testing.T, c config.Config, p model.Project, source string) model.Release {
	t.Helper()
	plan, err := ParseCompose(c, p, source)
	if err != nil {
		t.Fatal(err)
	}
	env, err := Environment(c, p.ID, plan.Services["app"].Environment)
	if err != nil {
		t.Fatal(err)
	}
	compose, err := PrepareCompose(c, p, plan, env)
	if err != nil {
		t.Fatal(err)
	}
	return model.Release{Image: plan.Services["app"].Image, Environment: env, Compose: compose}
}

func TestComposeRejectsHostAccessAndAmbiguousYAML(t *testing.T) {
	c, p, _ := composeFixture(t)
	base := "services:\n  app:\n    image: ghcr.io/org/web@sha256:" + strings.Repeat("a", 64) + "\n    environment: {TOKEN: secret}\n"
	cases := map[string]string{
		"build":              base + "    build: /etc\n",
		"host env":           base + "    env_file: /etc/shadow\n",
		"privileged":         base + "    privileged: true\n",
		"socket mount":       base + "    volumes: ['/var/run/docker.sock:/var/run/docker.sock']\n",
		"host network":       base + "    network_mode: host\n",
		"external network":   base + "networks: {default: {external: true}}\n",
		"remote include":     base + "include: [https://evil.invalid/compose.yaml]\n",
		"extends":            base + "    extends: {file: /etc/secret, service: app}\n",
		"custom name":        base + "name: unrelated-project\n",
		"spoofed labels":     base + "    labels: {io.ziqx.dockyard.server: other}\n",
		"public port":        base + "    ports: ['3001:3000']\n",
		"duplicate image":    base + "    image: ghcr.io/evil/app:latest\n",
		"anchor":             strings.Replace(base, "app:", "app: &reuse", 1),
		"merge":              base + "    <<: {}\n",
		"second document":    base + "---\nservices: {}\n",
		"custom tag":         strings.Replace(base, "TOKEN: secret", "TOKEN: !secret value", 1),
		"non-string env":     strings.Replace(base, "TOKEN: secret", "TOKEN: 123", 1),
		"env control":        strings.Replace(base, "TOKEN: secret", `TOKEN: "a\nb"`, 1),
		"unapproved image":   strings.Replace(base, "ghcr.io/org/web@sha256:", "ghcr.io/evil/web@sha256:", 1),
		"floating image":     strings.Replace(base, "@sha256:"+strings.Repeat("a", 64), ":latest", 1),
		"self dependency":    base + "    depends_on: [app]\n",
		"missing dependency": base + "    depends_on: [unknown]\n",
		"host volume driver": base + "volumes: {data: {driver_opts: {device: /etc, type: none, o: bind}}}\n",
		"external volume":    base + "volumes: {data: {external: true}}\n",
		"null volume":        base + "volumes: {data: null}\n",
		"oversized":          strings.Repeat(" ", 65537),
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCompose(c, p, source); err == nil {
				t.Fatal("accepted unsafe or ambiguous Compose")
			}
		})
	}
}

func TestComposeMultiServicePersistencePolicy(t *testing.T) {
	c, p, source := composeFixture(t)
	source += "    volumes: ['data:/var/lib/postgresql/data']\nvolumes: {data: {}}\n"
	if _, err := ParseCompose(c, p, source); err != nil {
		t.Fatal("rejected approved persistent stack", err)
	}
	p.ZeroDowntime = true
	if _, err := ParseCompose(c, p, source); err == nil || err.Error() != "COMPOSE_PERSISTENT_BLUE_GREEN" {
		t.Fatal("allowed overlapping persistent writers", err)
	}
	p.ZeroDowntime = false
	for _, target := range []string{"/etc", "/var/run/docker.sock", "/var/lib/postgresql/data:shared"} {
		if _, err := ParseCompose(c, p, strings.Replace(source, "/var/lib/postgresql/data'", target+"'", 1)); err == nil {
			t.Fatal("accepted unapproved mount", target)
		}
	}
	// Both app and companion images are checked, not just the routed image.
	if _, err := ParseCompose(c, p, strings.Replace(source, "ghcr.io/org/db", "ghcr.io/evil/db", 1)); err == nil {
		t.Fatal("accepted unapproved companion")
	}
}

func TestComposeEnforcesAggregateResources(t *testing.T) {
	c, p, source := composeFixture(t)
	for _, limit := range []string{"memory", "cpu", "default ceiling"} {
		t.Run(limit, func(t *testing.T) {
			policy := c
			switch limit {
			case "memory":
				policy.MaxStackMemoryMB = 400
			case "cpu":
				policy.MaxStackCPUs = 1
			default:
				policy.MaxStackMemoryMB = 0
				policy.MaxStackCPUs = 0
			}
			if _, err := ParseCompose(policy, p, source); err == nil || err.Error() != "COMPOSE_RESOURCE_LIMIT" {
				t.Fatal("stack exceeded root resource budget", err)
			}
		})
	}
}

type composeRecorder struct {
	calls       [][]string
	p           model.Project
	c           config.Config
	foreignDB   bool
	unhealthyDB bool
}

func (f *composeRecorder) Run(_ context.Context, _ string, _ string, args []string) (process.Result, error) {
	f.calls = append(f.calls, append([]string{}, args...))
	if args[0] == "compose" {
		if len(args) > 9 && args[9] == "ps" {
			return process.Result{Output: []byte(strings.Repeat(map[string]string{"app": "a", "db": "b"}[args[len(args)-1]], 64))}, nil
		}
		if len(args) > 9 && args[9] == "logs" {
			return process.Result{Output: []byte("app-secret-$HOME companion-secret\n")}, nil
		}
		return process.Result{}, nil
	}
	if args[0] == "image" {
		return process.Result{Output: []byte("sha256:resolved")}, nil
	}
	if args[0] == "inspect" {
		name := "app"
		if strings.HasPrefix(args[len(args)-1], "b") {
			name = "db"
		}
		services, err := releaseServices(f.c, f.p, f.p.Slots["blue"])
		if err != nil {
			return process.Result{}, err
		}
		expected := services[name]
		container := Container{ID: args[len(args)-1], Image: "sha256:resolved"}
		container.Config.Image = expected.Image
		container.Config.Labels = expected.Labels
		container.Config.Labels["com.docker.compose.project"] = "dy-vps-01-demo-blue"
		container.Config.Labels["com.docker.compose.service"] = name
		container.Config.Healthcheck.Test = expected.Healthcheck.Test
		container.State.Running = true
		json.Unmarshal([]byte(`{"Status":"healthy"}`), &container.State.Health)
		if name == "db" && f.unhealthyDB {
			container.State.Health.Status = "unhealthy"
		}
		if name == "db" && f.foreignDB {
			container.Config.Labels["io.ziqx.dockyard.project"] = "foreign"
		}
		container.Config.Env = []string{"TOKEN=app-secret-$HOME", "PASSWORD=companion-secret"}
		if name == "app" {
			json.Unmarshal([]byte(`{"3000/tcp":[{"HostIP":"127.0.0.1","HostPort":"3001"}]}`), &container.NetworkSettings.Ports)
		}
		b, _ := json.Marshal([]Container{container})
		return process.Result{Output: b}, nil
	}
	return process.Result{}, nil
}

func TestComposeRevisionsKeepSlotsIndependent(t *testing.T) {
	c, p, source := composeFixture(t)
	blue := preparedRelease(t, c, p, source)
	green := preparedRelease(t, c, p, strings.Replace(source, strings.Repeat("a", 64), strings.Repeat("c", 64), 1))
	p.Slots["blue"], p.Slots["green"] = blue, green
	for _, slot := range []string{"blue", "green"} {
		if err := Binding(c, p, slot, p.Slots[slot]); err != nil {
			t.Fatal(err)
		}
	}
	f := &composeRecorder{c: c, p: p}
	d := Docker{Config: c, Runner: f}
	// An operator mirror change cannot select or mutate the live slot's file.
	os.WriteFile(filepath.Join(projectDir(c, p.ID), "compose.yml"), []byte("mirror"), 0600)
	if err := d.Start(context.Background(), p, "green"); err != nil {
		t.Fatal(err)
	}
	if f.calls[0][6] != composeRevisionPath(c, p.ID, green.Compose) || strings.Contains(strings.Join(f.calls[0], " "), "--no-deps") {
		t.Fatal("candidate uses wrong file or ignores dependencies")
	}
	_, b, err := ReleaseCompose(c, p, blue)
	if err != nil || strings.Contains(string(b), "companion-secret") || strings.Contains(string(b), "app-secret") {
		t.Fatal("manifest drift or inline secret storage", err)
	}
	var document map[string]any
	json.Unmarshal(b, &document)
	for _, raw := range document["services"].(map[string]any) {
		service := raw.(map[string]any)
		if service["read_only"] != true || service["user"] == "0:0" || service["mem_limit"] == nil || service["cap_drop"] == nil || service["security_opt"] == nil {
			t.Fatal("missing runtime confinement")
		}
	}
	calls := len(f.calls)
	os.WriteFile(composeRevisionPath(c, p.ID, green.Compose), []byte("tampered"), 0600)
	if err := d.Start(context.Background(), p, "green"); err == nil || len(f.calls) != calls {
		t.Fatal("executed tampered Compose")
	}
}

func TestComposeCompanionHealthOwnershipAndRedaction(t *testing.T) {
	c, p, source := composeFixture(t)
	r := preparedRelease(t, c, p, source)
	p.Slots["blue"] = r
	Binding(c, p, "blue", r)
	f := &composeRecorder{c: c, p: p, unhealthyDB: true}
	d := Docker{Config: c, Runner: f}
	if err := d.Healthy(context.Background(), p, "blue", r); err == nil || err.Error() != "CANDIDATE_UNHEALTHY" {
		t.Fatal("ignored companion health", err)
	}
	f.unhealthyDB, f.foreignDB = false, true
	f.calls = nil
	if err := d.Stop(context.Background(), p, "blue"); err == nil {
		t.Fatal("stopped foreign service")
	}
	for _, call := range f.calls {
		if len(call) > 9 && call[9] == "stop" {
			t.Fatal("stopped stack before ownership proof")
		}
	}
	f.foreignDB = false
	logs, _, err := d.Logs(context.Background(), p, "blue", "app", 100, "30m")
	if err != nil || strings.Contains(string(logs), "app-secret") || strings.Contains(string(logs), "companion-secret") {
		t.Fatal("companion secrets leaked", err, string(logs))
	}
	calls := len(f.calls)
	if _, _, err := d.Logs(context.Background(), p, "blue", "foreign", 100, "30m"); err == nil || len(f.calls) != calls {
		t.Fatal("read unknown service")
	}
}

type volumeRecorder struct {
	foreign, bindOptions bool
	started              bool
}

func (f *volumeRecorder) Run(_ context.Context, _ string, _ string, args []string) (process.Result, error) {
	if args[0] == "compose" {
		f.started = true
		return process.Result{}, nil
	}
	if args[1] == "ls" {
		return process.Result{Output: []byte("dy-vps-01-demo-blue_data\n")}, nil
	}
	project := "dy-vps-01-demo-blue"
	if f.foreign {
		project = "unrelated"
	}
	options := map[string]string{}
	if f.bindOptions {
		options = map[string]string{"type": "none", "device": "/etc", "o": "bind"}
	}
	b, _ := json.Marshal([]any{map[string]any{"Name": "dy-vps-01-demo-blue_data", "Driver": "local", "Scope": "local", "Labels": map[string]string{"com.docker.compose.project": project, "com.docker.compose.volume": "data"}, "Options": options}})
	return process.Result{Output: b}, nil
}

func TestComposeNeverAdoptsForeignOrHostBackedVolumes(t *testing.T) {
	c, p, source := composeFixture(t)
	source += "    volumes: ['data:/var/lib/postgresql/data']\nvolumes: {data: {}}\n"
	p.Slots["blue"] = preparedRelease(t, c, p, source)
	for _, tc := range []struct {
		name                 string
		foreign, bindOptions bool
	}{{"foreign", true, false}, {"host bind options", false, true}, {"owned", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &volumeRecorder{foreign: tc.foreign, bindOptions: tc.bindOptions}
			err := (Docker{Config: c, Runner: f}).Start(context.Background(), p, "blue")
			if tc.foreign || tc.bindOptions {
				if err == nil || f.started {
					t.Fatal("started against untrusted volume", err)
				}
			} else if err != nil || !f.started {
				t.Fatal("rejected owned local volume", err)
			}
		})
	}
}

func TestCachedServicesCannotBypassManifestIntegrity(t *testing.T) {
	c, p, source := composeFixture(t)
	r := preparedRelease(t, c, p, source)
	p.Slots["blue"] = r
	s, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	j := model.Job{ID: "job-1", ProjectID: p.ID, Status: "succeeded", RequestID: "request-1"}
	if err = s.Accept(j, &p, "key-1", "fp-1", 10, 10); err != nil {
		t.Fatal(err)
	}
	if err = IndexCompose(c, s, p, r); err != nil {
		t.Fatal(err)
	}
	f := &composeRecorder{c: c, p: p}
	d := Docker{Config: c, Runner: f, Store: s}
	if err = os.Remove(composeRevisionPath(c, p.ID, r.Compose)); err != nil {
		t.Fatal(err)
	}
	if services, err := d.services(p, r); err != nil || len(services) != 2 {
		t.Fatal("cached metadata needed a manifest", err)
	}
	if err := d.Start(context.Background(), p, "blue"); err == nil || len(f.calls) != 0 {
		t.Fatal("cached metadata allowed a missing manifest to reach Docker")
	}
	if err := d.Stop(context.Background(), p, "blue"); err == nil || len(f.calls) != 0 {
		t.Fatal("cached metadata allowed an unverified stop")
	}
}
