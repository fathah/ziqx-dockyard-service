package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
)

type ServiceOptions struct {
	DependsOn []string `json:"depends_on"`
	Updatable bool     `json:"updatable"`
	Seamless  bool     `json:"seamless"`
	Reason    string   `json:"update_reason"`
	Ports     []int    `json:"container_ports"`
}

func ValidateServiceInstance(c config.Config, p model.Project, instance model.ServiceInstance) error {
	if !config.ServiceName.MatchString(instance.Name) {
		return model.Uncertain("SERVICE_IDENTITY_UNKNOWN")
	}
	_, _, err := nativeRelease(c, p, instance.Release)
	return err
}

// This metadata contains no environment values or source file contents.
func ServiceUpdateOptions(c config.Config, p model.Project) (map[string]ServiceOptions, error) {
	r, ok := p.Current()
	if !ok {
		return map[string]ServiceOptions{}, nil
	}
	_, b, err := nativeRelease(c, p, r)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return nil, model.Fail("COMPOSE_CONFIG_DIVERGED")
	}
	out := map[string]ServiceOptions{}
	for name, raw := range object(doc["services"]) {
		svc := object(raw)
		dependencies := []string{}
		switch value := svc["depends_on"].(type) {
		case map[string]any:
			for dependency := range value {
				dependencies = append(dependencies, dependency)
			}
		case []any:
			for _, dependency := range value {
				if name, ok := dependency.(string); ok {
					dependencies = append(dependencies, name)
				}
			}
		}
		sort.Strings(dependencies)
		reason := serviceSeamlessReason(svc)
		ports := []int{}
		seen := map[int]bool{}
		for _, raw := range array(svc["ports"]) {
			v := object(raw)
			if v["protocol"] != nil && v["protocol"] != "tcp" {
				continue
			}
			n, _ := strconv.Atoi(fmt.Sprint(v["target"]))
			if n > 0 && n <= 65535 && !seen[n] {
				ports = append(ports, n)
				seen[n] = true
			}
		}
		for _, raw := range array(svc["expose"]) {
			n, _ := strconv.Atoi(fmt.Sprint(raw))
			if n > 0 && n <= 65535 && !seen[n] {
				ports = append(ports, n)
				seen[n] = true
			}
		}
		sort.Ints(ports)
		image, _ := svc["image"].(string)
		updatable := p.State == "running" && !p.ZeroDowntime && image != ""
		if image == "" {
			reason = "This service is built locally. Use Edit & deploy to rebuild it."
		} else if p.ZeroDowntime {
			reason = "This project uses the older whole-stack update strategy."
		} else if p.Environment != model.Production {
			reason = "This flavor uses a controlled restart."
		} else if p.PublishedRoute != nil && p.PublishedRoute.Service != name || p.PublishedRoute == nil && p.RouteService != name && (p.Adoption == nil || len(p.Domains) > 0 || len(p.ExternalDomains) == 0) {
			reason = "Only the service serving this project's domains can switch traffic seamlessly."
		}
		if n, ok := svc["scale"].(float64); ok && n != 1 {
			updatable = false
			reason = "Individual updates currently support one container per service."
		}
		if n, ok := object(svc["deploy"])["replicas"].(float64); ok && n != 1 {
			updatable = false
			reason = "Individual updates currently support one container per service."
		}
		if p.State != "running" {
			reason = "Start this project before updating individual services."
		}
		out[name] = ServiceOptions{DependsOn: dependencies, Updatable: updatable, Seamless: updatable && reason == "", Reason: reason, Ports: ports}
	}
	return out, nil
}

func serviceSeamlessReason(svc map[string]any) string {
	for _, raw := range array(svc["volumes"]) {
		if object(raw)["read_only"] != true {
			return "Persistent storage requires a controlled restart."
		}
	}
	if svc["network_mode"] != nil || svc["pid"] != nil || svc["ipc"] != nil || svc["privileged"] == true || len(array(svc["devices"])) > 0 || len(array(svc["links"])) > 0 || len(object(svc["develop"])) > 0 {
		return "This service shares runtime resources and requires a controlled restart."
	}
	if n, ok := svc["scale"].(float64); ok && n != 1 {
		return "Multiple replicas need a dedicated rollout strategy."
	}
	if n, ok := object(svc["deploy"])["replicas"].(float64); ok && n != 1 {
		return "Multiple replicas need a dedicated rollout strategy."
	}
	for _, raw := range object(svc["networks"]) {
		cfg := object(raw)
		if cfg["ipv4_address"] != nil || cfg["ipv6_address"] != nil {
			return "A fixed container IP requires a controlled restart."
		}
	}
	h := object(svc["healthcheck"])
	test := array(h["test"])
	if h["disable"] == true || len(test) == 0 || test[0] == "NONE" {
		return "Add a Compose healthcheck to enable seamless updates."
	}
	return ""
}

func (d Docker) serviceCommand(ctx context.Context, p model.Project, instance model.ServiceInstance, args ...string) (process.Result, error) {
	if !config.ServiceName.MatchString(instance.Name) {
		return process.Result{}, model.Fail("SERVICE_UPDATE_INVALID")
	}
	path, _, err := nativeRelease(d.Config, p, instance.Release)
	if err != nil {
		return process.Result{}, err
	}
	f, err := os.CreateTemp(projectDir(d.Config, p.ID), ".service-binding-*")
	if err != nil {
		return process.Result{}, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(bindingBytes(d.Config, p, p.Active, instance.Release))
	ce := f.Close()
	if err != nil || ce != nil {
		return process.Result{}, model.Fail("PROJECT_FILES_FAILED")
	}
	argv := d.args(p, p.Active, args...)
	argv[6], argv[8] = path, f.Name()
	return d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), argv)
}

func (d Docker) inspectServiceInstance(ctx context.Context, p model.Project, instance model.ServiceInstance) (Container, error) {
	var zero Container
	result, err := d.serviceCommand(ctx, p, instance, "ps", "--all", "--quiet", instance.Name)
	id := strings.TrimSpace(string(result.Output))
	if err != nil || result.Truncated || len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return zero, model.Fail("CONTAINER_UNAVAILABLE")
	}
	result, err = d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), []string{"inspect", "--type", "container", id})
	var cs []Container
	if err != nil || result.Truncated || json.Unmarshal(result.Output, &cs) != nil || len(cs) != 1 {
		return zero, model.Fail("DOCKER_UNAVAILABLE")
	}
	c := cs[0]
	if c.ID != id || c.Config.Labels["com.docker.compose.service"] != instance.Name || !d.ownsNative(p, p.Active, c) {
		return zero, model.Uncertain("CONTAINER_OWNERSHIP_UNKNOWN")
	}
	return c, nil
}

// Review checks the selected service, not the database or other siblings.
// Immutable source remains the complete user-supplied Compose file.
func (d Docker) PlanServiceUpdate(ctx context.Context, p model.Project, service, mode string, containerPort, hostPort int, readiness string) (model.ServiceUpdate, error) {
	var zero model.ServiceUpdate
	if !p.NativeCompose() || p.ZeroDowntime || p.State != "running" || !config.ServiceName.MatchString(service) {
		return zero, model.Fail("SERVICE_UPDATE_UNAVAILABLE")
	}
	r, ok := p.Current()
	if !ok {
		return zero, model.Fail("CONFIGURATION_UNAVAILABLE")
	}
	_, b, err := nativeRelease(d.Config, p, r)
	if err != nil {
		return zero, err
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return zero, model.Fail("COMPOSE_CONFIG_DIVERGED")
	}
	svc := object(object(doc["services"])[service])
	image, _ := svc["image"].(string)
	if image == "" {
		return zero, model.Fail("SERVICE_IMAGE_REQUIRED")
	}
	previous, ok := p.ServiceInstances[service]
	if !ok {
		previous = model.ServiceInstance{Name: service, Release: r}
		if service == p.RouteService {
			previous.Port = p.Port(p.Active)
		}
	}
	c, err := d.inspectServiceInstance(ctx, p, previous)
	if err != nil {
		return zero, err
	}
	if !c.State.Running {
		return zero, model.Fail("SERVICE_NOT_RUNNING")
	}
	if previous.Release.Image != "" && previous.Release.Image != c.Image {
		return zero, model.Uncertain("IMAGE_IDENTITY_UNKNOWN")
	}
	previous.Release.Image = c.Image
	previous.ContainerID = c.ID
	plan := model.ServiceUpdate{Service: service, Mode: mode, Previous: &previous, NetworkAliases: map[string][]string{}, NetworkIDs: map[string]string{}}
	if mode == "restart" {
		plan.Instance = previous
		plan.Instance.ContainerID = "" // Docker recreates the selected container.
		return plan, nil
	}
	if mode != "seamless" || p.Environment != model.Production || containerPort < 1 || containerPort > 65535 || hostPort < 1 || hostPort > 65535 {
		return zero, model.Fail("SERVICE_UPDATE_INVALID")
	}
	if serviceSeamlessReason(svc) != "" {
		return zero, model.Fail("SERVICE_SEAMLESS_INCOMPATIBLE")
	}
	for _, m := range c.Mounts {
		if m.Type != "tmpfs" && m.RW {
			return zero, model.Fail("SERVICE_HAS_STORAGE")
		}
	}
	for _, raw := range array(svc["ports"]) {
		v := object(raw)
		if fmt.Sprint(v["target"]) != strconv.Itoa(containerPort) || v["protocol"] != nil && v["protocol"] != "tcp" {
			return zero, model.Fail("SERVICE_EXTRA_PORTS")
		}
	}
	if c.State.Health == nil || c.State.Health.Status != "healthy" {
		return zero, model.Fail("SERVICE_HEALTHCHECK_REQUIRED")
	}
	plan.Instance = model.ServiceInstance{Name: "dy-update-" + strings.TrimPrefix(stateID(), "svc-")[:16], Release: r, Port: hostPort}
	// Candidate gets a unique DNS name until its readiness checks pass. Original
	// service aliases are attached only during promotion, while the old app is live.
	for network, connection := range c.NetworkSettings.Networks {
		if len(connection.NetworkID) != 64 || strings.Trim(connection.NetworkID, "0123456789abcdef") != "" {
			return zero, model.Uncertain("SERVICE_NETWORK_CHANGED")
		}
		plan.NetworkIDs[network] = connection.NetworkID
		plan.NetworkAliases[network] = []string{service}
		// Preserve reviewed aliases per network, excluding ephemeral container names.
		for _, alias := range connection.Aliases {
			if alias != service && alias != previous.Name && alias != strings.TrimPrefix(c.Name, "/") && alias != c.ID[:12] && config.ServiceName.MatchString(alias) {
				plan.NetworkAliases[network] = append(plan.NetworkAliases[network], alias)
			}
		}
		sort.Strings(plan.NetworkAliases[network])
	}
	if len(plan.NetworkAliases) == 0 {
		return zero, model.Fail("SERVICE_NETWORK_REQUIRED")
	}
	plan.Instance.NetworkAliases = plan.NetworkAliases
	return plan, nil
}

// Separate from planning so the review token never depends on a random name.
func stateID() string { return state.NewID("svc-") }

func checkServiceHTTP(ctx context.Context, port int, path string) error {
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if err != nil {
		return model.Fail("READINESS_FAILED")
	}
	res, err := client.Do(req)
	if err != nil {
		return model.Fail("READINESS_FAILED")
	}
	res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 400 {
		return model.Fail("READINESS_FAILED")
	}
	return nil
}

func (d Docker) PullServiceUpdate(ctx context.Context, p model.Project, plan *model.ServiceUpdate) (bool, error) {
	r, _ := p.Current()
	_, b, err := nativeRelease(d.Config, p, r)
	if err != nil {
		return false, err
	}
	var doc map[string]any
	json.Unmarshal(b, &doc)
	svc := object(object(doc["services"])[plan.Service])
	image, _ := svc["image"].(string)
	image = strings.ReplaceAll(image, "$$", "$")
	base := model.ServiceInstance{Name: plan.Service, Release: r}
	if _, err = d.serviceCommand(ctx, p, base, "pull", plan.Service); err != nil {
		return false, model.Fail("IMAGE_PULL_FAILED")
	}
	result, err := d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), []string{"image", "inspect", image})
	var images []struct {
		ID     string `json:"Id"`
		Config struct {
			Volumes map[string]any `json:"Volumes"`
		} `json:"Config"`
	}
	if err != nil || result.Truncated || json.Unmarshal(result.Output, &images) != nil || len(images) != 1 || len(images[0].ID) != 71 || !strings.HasPrefix(images[0].ID, "sha256:") || strings.Trim(images[0].ID[7:], "0123456789abcdef") != "" {
		return false, model.Fail("IMAGE_IDENTITY_UNKNOWN")
	}
	if images[0].ID == plan.Previous.Release.Image {
		return false, nil
	}
	if plan.Mode == "seamless" && len(images[0].Config.Volumes) > 0 {
		return false, model.Fail("SERVICE_IMAGE_HAS_STORAGE")
	}
	if plan.Mode == "restart" && plan.Previous.Name != plan.Service {
		_, b, err = nativeRelease(d.Config, p, plan.Previous.Release)
		if err != nil {
			return false, err
		}
		json.Unmarshal(b, &doc)
		svc = object(object(doc["services"])[plan.Previous.Name])
		for key, raw := range object(doc["networks"]) {
			name, _ := object(raw)["name"].(string)
			if aliases := plan.Previous.NetworkAliases[name]; len(aliases) > 0 {
				object(svc["networks"])[key] = map[string]any{"aliases": aliases}
			}
		}
	}
	svc["image"] = images[0].ID
	delete(svc, "build")
	delete(svc, "pull_policy")
	delete(svc, "depends_on")
	delete(svc, "profiles")
	if plan.Mode == "seamless" {
		delete(svc, "container_name")
		delete(svc, "hostname")
		delete(svc, "links")
		svc["ports"] = []any{map[string]any{"target": p.RoutePort, "published": strconv.Itoa(plan.Instance.Port), "host_ip": "127.0.0.1", "protocol": "tcp"}}
		nets := map[string]any{}
		defs := map[string]any{}
		names := []string{}
		for n := range plan.NetworkAliases {
			names = append(names, n)
		}
		sort.Strings(names)
		for i, n := range names {
			key := fmt.Sprintf("connection%d", i)
			nets[key] = map[string]any{}
			defs[key] = map[string]any{"external": true, "name": n}
		}
		svc["networks"] = nets
		doc["networks"] = defs
	}
	labels := object(svc["labels"])
	if labels == nil {
		labels = map[string]any{}
	}
	labels["io.ziqx.dockyard.server"] = d.Config.ServerID
	labels["io.ziqx.dockyard.project"] = p.ID
	svc["labels"] = labels
	// The restart keeps definitions for depends_on/config validation, but --no-deps
	// ensures Docker never starts or recreates any of these sibling services.
	services := object(doc["services"])
	services[plan.Instance.Name] = svc
	if plan.Mode == "seamless" {
		doc["services"] = map[string]any{plan.Instance.Name: svc}
	}
	b, err = json.MarshalIndent(doc, "", "  ")
	if err != nil || len(b) > 2<<20 {
		return false, model.Fail("COMPOSE_LIMIT")
	}
	sum := sha256.Sum256(b)
	revision := "cmp-" + hex.EncodeToString(sum[:])
	entries, err := os.ReadDir(projectDir(d.Config, p.ID) + "/compose")
	if err != nil || len(entries) >= 500 {
		return false, model.Fail("COMPOSE_LIMIT")
	}
	if err = secure.Atomic(composeRevisionPath(d.Config, p.ID, revision), b, 0600); err != nil {
		return false, err
	}
	plan.Instance.Release.Compose = revision
	plan.Instance.Release.Image = images[0].ID
	return true, nil
}

func (d Docker) StartServiceUpdate(ctx context.Context, p model.Project, plan model.ServiceUpdate) error {
	if _, err := d.serviceCommand(ctx, p, plan.Instance, "up", "--detach", "--no-deps", "--no-build", "--pull", "never", "--wait", "--wait-timeout", strconv.Itoa(d.Config.HealthSeconds), plan.Instance.Name); err != nil {
		return model.Fail("SERVICE_START_FAILED")
	}
	return nil
}
func (d Docker) HealthyServiceUpdate(ctx context.Context, p model.Project, plan model.ServiceUpdate) error {
	c, err := d.inspectServiceInstance(ctx, p, plan.Instance)
	if err != nil {
		return err
	}
	if !c.State.Running || c.Image != plan.Instance.Release.Image || c.State.Health != nil && c.State.Health.Status != "healthy" || plan.Mode == "seamless" && c.State.Health == nil {
		return model.Fail("SERVICE_UNHEALTHY")
	}
	if plan.Mode == "seamless" {
		for _, m := range c.Mounts {
			if m.Type != "tmpfs" && m.RW {
				return model.Fail("SERVICE_HAS_STORAGE")
			}
		}
		ports := c.NetworkSettings.Ports[fmt.Sprintf("%d/tcp", p.RoutePort)]
		if len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].HostPort != strconv.Itoa(plan.Instance.Port) {
			return model.Uncertain("CONTAINER_BINDING_DIVERGED")
		}
		if p.ReadinessPath != "" {
			return checkServiceHTTP(ctx, plan.Instance.Port, p.ReadinessPath)
		}
	}
	return nil
}
func (d Docker) PromoteServiceUpdate(ctx context.Context, p model.Project, plan model.ServiceUpdate) error {
	c, err := d.inspectServiceInstance(ctx, p, plan.Instance)
	if err != nil {
		return err
	}
	names := []string{}
	for name := range plan.NetworkAliases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if connection, ok := c.NetworkSettings.Networks[name]; !ok || connection.NetworkID != plan.NetworkIDs[name] {
			return model.Uncertain("SERVICE_NETWORK_CHANGED")
		}
		if _, err = d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), []string{"network", "disconnect", plan.NetworkIDs[name], c.ID}); err != nil {
			return model.Fail("SERVICE_NETWORK_PROMOTION_FAILED")
		}
		args := []string{"network", "connect"}
		for _, alias := range plan.NetworkAliases[name] {
			args = append(args, "--alias", alias)
		}
		args = append(args, plan.NetworkIDs[name], c.ID)
		if _, err = d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), args); err != nil {
			return model.Fail("SERVICE_NETWORK_PROMOTION_FAILED")
		}
	}
	return nil
}
func (d Docker) StopServiceInstance(ctx context.Context, p model.Project, instance model.ServiceInstance) error {
	c, err := d.inspectServiceInstance(ctx, p, instance)
	if err != nil {
		// A journaled replacement may not have been created before interruption.
		// An empty, successful query proves absence; never treat inspect failure as absence.
		if strings.HasPrefix(instance.Name, "dy-update-") {
			result, queryErr := d.serviceCommand(ctx, p, instance, "ps", "--all", "--quiet", instance.Name)
			if queryErr == nil && !result.Truncated && strings.TrimSpace(string(result.Output)) == "" {
				return nil
			}
		}
		return err
	}
	if !c.State.Running {
		return nil
	}
	if instance.ContainerID != "" && c.ID != instance.ContainerID || instance.Release.Image != "" && c.Image != instance.Release.Image {
		return model.Uncertain("SERVICE_IDENTITY_CHANGED")
	}
	if _, err = d.serviceCommand(ctx, p, instance, "stop", "--timeout", "30", instance.Name); err != nil {
		return model.Uncertain("SERVICE_STOP_FAILED")
	}
	c, err = d.inspectServiceInstance(ctx, p, instance)
	if err != nil || c.State.Running {
		return model.Uncertain("SERVICE_STOP_FAILED")
	}
	return nil
}

func (d Docker) VerifyServiceUpdate(ctx context.Context, p model.Project, plan model.ServiceUpdate) error {
	if plan.Previous == nil {
		return model.Fail("SERVICE_UPDATE_INVALID")
	}
	c, err := d.inspectServiceInstance(ctx, p, *plan.Previous)
	if err != nil {
		return err
	}
	if !c.State.Running || c.Image != plan.Previous.Release.Image || plan.Previous.ContainerID != "" && c.ID != plan.Previous.ContainerID {
		return model.Fail("SERVICE_REVIEW_CHANGED")
	}
	if plan.Mode == "seamless" {
		if c.State.Health == nil || c.State.Health.Status != "healthy" {
			return model.Fail("SERVICE_REVIEW_CHANGED")
		}
		if len(c.NetworkSettings.Networks) != len(plan.NetworkAliases) {
			return model.Fail("SERVICE_NETWORK_CHANGED")
		}
		for n := range plan.NetworkAliases {
			if connection, ok := c.NetworkSettings.Networks[n]; !ok || connection.NetworkID != plan.NetworkIDs[n] {
				return model.Fail("SERVICE_NETWORK_CHANGED")
			}
		}
	}
	return nil
}
