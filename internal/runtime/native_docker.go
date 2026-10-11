package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func (d Docker) nativeContainers(ctx context.Context, p model.Project, slot string) ([]Container, error) {
	result, err := d.command(ctx, p, slot, "ps", "--all", "--quiet", "--orphans=false")
	if err != nil || result.Truncated {
		return nil, model.Fail("DOCKER_UNAVAILABLE")
	}
	ids := strings.Fields(string(result.Output))
	if len(ids) > 1000 {
		return nil, model.Fail("COMPOSE_LIMIT")
	}
	if len(ids) == 0 {
		return []Container{}, nil
	}
	for _, id := range ids {
		if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
			return nil, model.Uncertain("CONTAINER_OWNERSHIP_UNKNOWN")
		}
	}
	result, err = d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p.ID), append([]string{"inspect", "--type", "container"}, ids...))
	var containers []Container
	if err != nil || result.Truncated || json.Unmarshal(result.Output, &containers) != nil || len(containers) != len(ids) {
		return nil, model.Fail("DOCKER_UNAVAILABLE")
	}
	expected := map[string]bool{}
	for _, id := range ids {
		expected[id] = true
	}
	for _, c := range containers {
		if !expected[c.ID] || !d.ownsNative(p, slot, c) {
			return nil, model.Uncertain("CONTAINER_OWNERSHIP_UNKNOWN")
		}
		delete(expected, c.ID)
	}
	return containers, nil
}

func (d Docker) DetectedServicePorts(ctx context.Context, p model.Project) (map[string][]int, error) {
	containers, err := d.nativeContainers(ctx, p, p.Active)
	if err != nil {
		return nil, err
	}
	ports := map[string][]int{}
	for _, container := range containers {
		name := container.Config.Labels["com.docker.compose.service"]
		seen := map[int]bool{}
		for key := range container.Config.ExposedPorts {
			if !strings.HasSuffix(key, "/tcp") {
				continue
			}
			n, _ := strconv.Atoi(strings.TrimSuffix(key, "/tcp"))
			if n > 0 && n <= 65535 {
				seen[n] = true
			}
		}
		for key := range container.NetworkSettings.Ports {
			if !strings.HasSuffix(key, "/tcp") {
				continue
			}
			n, _ := strconv.Atoi(strings.TrimSuffix(key, "/tcp"))
			if n > 0 && n <= 65535 {
				seen[n] = true
			}
		}
		for n := range seen {
			ports[name] = append(ports[name], n)
		}
		sort.Ints(ports[name])
	}
	return ports, nil
}

func (d Docker) nativeHealthy(ctx context.Context, p model.Project, slot string, r model.Release) error {
	_, b, err := nativeRelease(d.Config, p, r)
	if err != nil {
		return err
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	services := object(doc["services"])
	containers, err := d.nativeContainers(ctx, p, slot)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, c := range containers {
		name := c.Config.Labels["com.docker.compose.service"]
		if slot == p.Active {
			if _, ok := p.ServiceInstances[name]; ok {
				continue
			}
		}
		if services[name] == nil {
			return model.Uncertain("CONTAINER_OWNERSHIP_UNKNOWN")
		}
		if !c.State.Running {
			if c.State.ExitCode != 0 || p.ZeroDowntime || name == p.RouteService {
				return model.Fail("CANDIDATE_UNHEALTHY")
			}
		} else if c.State.Health != nil && c.State.Health.Status != "healthy" {
			return model.Fail("CANDIDATE_UNHEALTHY")
		}
		if p.ZeroDowntime {
			for _, mount := range c.Mounts {
				if mount.Type != "tmpfs" {
					return model.Fail("COMPOSE_BLUE_GREEN_INCOMPATIBLE")
				}
			}
		}
		if p.ZeroDowntime && c.State.Health == nil {
			return model.Fail("CANDIDATE_UNHEALTHY")
		}
		if name == p.RouteService {
			ports := c.NetworkSettings.Ports[fmt.Sprintf("%d/tcp", p.RoutePort)]
			if len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].HostPort != strconv.Itoa(p.Port(slot)) {
				return model.Uncertain("CONTAINER_BINDING_DIVERGED")
			}
		}
		if pin := p.PublishedRoute; pin != nil && name == pin.Service {
			ipv4 := false
			for _, binding := range c.NetworkSettings.Ports[fmt.Sprintf("%d/tcp", pin.ContainerPort)] {
				if binding.HostPort != strconv.Itoa(pin.HostPort) {
					return model.Uncertain("CONTAINER_BINDING_DIVERGED")
				}
				if binding.HostIP == "127.0.0.1" || binding.HostIP == "0.0.0.0" {
					ipv4 = true
				}
			}
			if !ipv4 || !c.State.Running {
				return model.Uncertain("CONTAINER_BINDING_DIVERGED")
			}
		}
		for _, route := range p.ServiceRoutes {
			if route.Service != name {
				continue
			}
			found := false
			for _, binding := range c.NetworkSettings.Ports[fmt.Sprintf("%d/tcp", route.ContainerPort)] {
				if (binding.HostIP == "127.0.0.1" || binding.HostIP == "0.0.0.0") && binding.HostPort == strconv.Itoa(route.HostPort) {
					found = true
				}
			}
			if !found || !c.State.Running {
				return model.Uncertain("CONTAINER_BINDING_DIVERGED")
			}
		}
		counts[name]++
	}
	for name, raw := range services {
		if slot == p.Active {
			if _, ok := p.ServiceInstances[name]; ok {
				continue
			}
		}
		svc := object(raw)
		want := 1
		if scale, ok := svc["scale"].(float64); ok {
			want = int(scale)
		}
		if replicas, ok := object(svc["deploy"])["replicas"].(float64); ok {
			want = int(replicas)
		}
		if counts[name] < want {
			return model.Fail("CONTAINER_UNAVAILABLE")
		}
	}
	if slot == p.Active {
		for name, instance := range p.ServiceInstances {
			mode := "restart"
			if instance.Name != name && instance.Port > 0 {
				mode = "seamless"
			}
			if err := d.HealthyServiceUpdate(ctx, p, model.ServiceUpdate{Service: name, Mode: mode, Instance: instance}); err != nil {
				return err
			}
		}
	}
	if p.RouteService != "" && p.ReadinessPath != "" {
		client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		defer client.CloseIdleConnections()
		req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d%s", p.Port(slot), p.ReadinessPath), nil)
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
	}
	return nil
}

// Resolve a reviewed legacy route against positively identified running containers.
func (d Docker) PublishedUpstream(ctx context.Context, p model.Project, service string, port int) (string, error) {
	var containers []Container
	var err error
	if instance, ok := p.ServiceInstances[service]; ok {
		// A service replacement is the active domain backend. Its original
		// full-stack container may already be drained and stopped.
		c, inspectErr := d.inspectServiceInstance(ctx, p, instance)
		err = inspectErr
		containers = []Container{c}
	} else {
		containers, err = d.nativeContainers(ctx, p, p.Active)
	}
	if err != nil {
		return "", err
	}
	result := ""
	for _, c := range containers {
		name := service
		if instance, ok := p.ServiceInstances[service]; ok {
			name = instance.Name
		}
		if c.Config.Labels["com.docker.compose.service"] != name {
			continue
		}
		if !c.State.Running {
			return "", model.Fail("CONTAINER_UNAVAILABLE")
		}
		for _, binding := range c.NetworkSettings.Ports[fmt.Sprintf("%d/tcp", port)] {
			if binding.HostIP != "0.0.0.0" && binding.HostIP != "127.0.0.1" {
				continue
			}
			if result != "" {
				return "", model.Fail("BLUE_GREEN_ROUTE_TARGET_MISMATCH")
			}
			result = "127.0.0.1:" + binding.HostPort
		}
	}
	if result == "" {
		return "", model.Fail("BLUE_GREEN_ROUTE_TARGET_MISMATCH")
	}
	return result, nil
}

// A conversion stops the original stack after draining. Never include a database
// or durable mount that the candidate might still depend on in that stop.
func (d Docker) CheckBlueGreenSource(ctx context.Context, p model.Project) error {
	r, ok := p.Current()
	if !ok {
		return model.Fail("CONFIGURATION_UNAVAILABLE")
	}
	_, b, err := nativeRelease(d.Config, p, r)
	if err != nil {
		return err
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return model.Fail("COMPOSE_VALIDATION_FAILED")
	}
	if len(object(doc["volumes"])) > 0 {
		return model.Fail("BLUE_GREEN_SOURCE_HAS_STORAGE")
	}
	for _, raw := range object(doc["services"]) {
		if len(array(object(raw)["volumes"])) > 0 {
			return model.Fail("BLUE_GREEN_SOURCE_HAS_STORAGE")
		}
	}
	cs, err := d.nativeContainers(ctx, p, p.Active)
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		return model.Fail("CONTAINER_UNAVAILABLE")
	}
	for _, c := range cs {
		if !c.State.Running {
			return model.Fail("CONTAINER_UNAVAILABLE")
		}
		for _, m := range c.Mounts {
			if m.Type != "tmpfs" {
				return model.Fail("BLUE_GREEN_SOURCE_HAS_STORAGE")
			}
		}
	}
	return nil
}

// PublishedServicePorts reads host bindings only from verified containers.
// Exposed container ports do not imply a port is published on the host.
func (d Docker) PublishedServicePorts(ctx context.Context, p model.Project) (map[string][]int, error) {
	containers, err := d.nativeContainers(ctx, p, p.Active)
	if err != nil {
		return nil, err
	}
	ports := map[string][]int{}
	for _, c := range containers {
		name := c.Config.Labels["com.docker.compose.service"]
		if _, replaced := p.ServiceInstances[name]; replaced {
			continue
		}
		ports[name] = append(ports[name], publishedHostPorts(c)...)
	}
	for name, instance := range p.ServiceInstances {
		c, err := d.inspectServiceInstance(ctx, p, instance)
		if err != nil {
			return nil, err
		}
		ports[name] = publishedHostPorts(c)
	}
	for name, values := range ports {
		seen := map[int]bool{}
		ports[name] = []int{}
		for _, port := range values {
			if !seen[port] {
				ports[name] = append(ports[name], port)
				seen[port] = true
			}
		}
		sort.Ints(ports[name])
	}
	return ports, nil
}

func publishedHostPorts(c Container) []int {
	ports := []int{}
	if !c.State.Running {
		return ports
	}
	for _, bindings := range c.NetworkSettings.Ports {
		for _, binding := range bindings {
			port, err := strconv.Atoi(binding.HostPort)
			if err == nil && port > 0 && port <= 65535 {
				ports = append(ports, port)
			}
		}
	}
	return ports
}
