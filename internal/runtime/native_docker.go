package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
		counts[name]++
	}
	for name, raw := range services {
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
