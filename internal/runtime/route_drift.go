package runtime

import (
	"context"
	"fmt"
	"maps"
	"strconv"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

// PortsFingerprint is a cheap summary of every running container's published
// ports. The route watcher inspects projects only when it changes.
func (d Docker) PortsFingerprint(ctx context.Context) (string, error) {
	result, err := d.Runner.Run(ctx, d.Config.DockerBinary, d.Config.ProjectsRoot, []string{"ps", "--no-trunc", "--format", "{{.ID}} {{.Ports}}"})
	if err != nil || result.Truncated {
		return "", model.Fail("DOCKER_UNAVAILABLE")
	}
	return string(result.Output), nil
}

// LiveRoutes re-reads the host ports Docker currently publishes for an active
// project's routed services. Automatic bindings get a new port whenever Docker
// restarts a container (host reboot, daemon restart, restart policy), so saved
// ports can go stale while the containers themselves stay healthy. Only running
// containers positively owned by the project are trusted; the input is unchanged.
func (d Docker) LiveRoutes(ctx context.Context, p model.Project) (model.Project, error) {
	next := p
	if !p.NativeCompose() || p.State != "running" || p.Active == "" {
		return next, nil
	}
	var primary []Container
	loaded := false
	containers := func(service string) ([]Container, error) {
		if instance, ok := p.ServiceInstances[service]; ok {
			cs, err := d.inspectServiceContainers(ctx, p, instance)
			if err != nil {
				return nil, err
			}
			if len(cs) != instanceReplicas(instance) {
				return nil, model.Fail("SERVICE_REPLICAS_CHANGED")
			}
			return cs, nil
		}
		if !loaded {
			var err error
			if primary, err = d.nativeContainers(ctx, p, p.Active); err != nil {
				return nil, err
			}
			loaded = true
		}
		found := []Container{}
		for _, c := range primary {
			if c.Config.Labels["com.docker.compose.service"] == service {
				found = append(found, c)
			}
		}
		return found, nil
	}
	ports := func(service string, target int) ([]int, error) {
		cs, err := containers(service)
		if err != nil {
			return nil, err
		}
		return replicaRoutePorts(cs, target, false)
	}
	port := func(service string, target int) (int, error) {
		values, err := ports(service, target)
		if err != nil {
			return 0, err
		}
		setRouteBindings(&next, p.Active, service, target, values)
		return values[0], nil
	}

	next.ServiceRoutes = append([]model.ServiceRoute(nil), p.ServiceRoutes...)
	next.ServiceInstances = maps.Clone(p.ServiceInstances)
	setPrimary := func(value int) {
		if p.Active == "green" {
			next.GreenPort = value
		} else {
			next.BluePort = value
		}
	}
	// An active seamless instance overrides its service's saved route ports.
	refreshInstance := func(service string) error {
		instance, ok := next.ServiceInstances[service]
		if !ok || instance.Port == 0 {
			return nil
		}
		values, err := ports(service, serviceInstancePort(p, service, instance))
		if err != nil {
			return err
		}
		instance.Port, instance.Ports = values[0], values
		next.ServiceInstances[service] = instance
		return nil
	}
	if p.RouteService != "" {
		if instance, ok := p.ServiceInstances[p.RouteService]; ok && instance.Port > 0 {
			if err := refreshInstance(p.RouteService); err != nil {
				return p, err
			}
		} else {
			value, err := port(p.RouteService, p.RoutePort)
			if err != nil {
				return p, err
			}
			setPrimary(value)
		}
	}
	if pin := p.PublishedRoute; pin != nil {
		value, err := port(pin.Service, pin.ContainerPort)
		if err != nil {
			return p, err
		}
		copy := *pin
		copy.HostPort = value
		next.PublishedRoute = &copy
		setPrimary(value)
	}
	for i, route := range next.ServiceRoutes {
		if instance, ok := next.ServiceInstances[route.Service]; ok && instance.Port > 0 {
			if err := refreshInstance(route.Service); err != nil {
				return p, err
			}
			continue
		}
		value, err := port(route.Service, route.ContainerPort)
		if err != nil {
			return p, err
		}
		next.ServiceRoutes[i].HostPort = value
	}
	// Internal backends also publish private candidate ports for readiness.
	// Keep their recorded bindings current after daemon/container restarts.
	for service, instance := range next.ServiceInstances {
		if instance.Port > 0 {
			if err := refreshInstance(service); err != nil {
				return p, err
			}
		}
	}
	return next, nil
}

// livePort accepts exactly one IPv4 host port that Caddy can reach on loopback.
// Docker may list the same port again for IPv6; any second port is ambiguous.
func livePort(c Container, target int) (int, error) {
	found := ""
	for _, binding := range c.NetworkSettings.Ports[fmt.Sprintf("%d/tcp", target)] {
		if binding.HostIP != "127.0.0.1" && binding.HostIP != "0.0.0.0" {
			continue
		}
		if found != "" && found != binding.HostPort {
			return 0, model.Fail("SERVICE_ROUTE_BINDING_UNAVAILABLE")
		}
		found = binding.HostPort
	}
	port, err := strconv.Atoi(found)
	if err != nil || port < 1 || port > 65535 {
		return 0, model.Fail("SERVICE_ROUTE_BINDING_UNAVAILABLE")
	}
	return port, nil
}
