package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

// Resolve only containers positively identified by their Compose and Dockyard
// ownership. Run after Docker creates a candidate, before health checks and
// before publishing its Caddy routes. Saved ports describe the live assignment,
// never an instruction for Docker to reuse a potentially occupied host port.
func (d Docker) ResolveRouteBindings(ctx context.Context, p *model.Project, slot string) error {
	if p.RouteService == "" && p.PublishedRoute == nil && len(p.ServiceRoutes) == 0 {
		return nil
	}
	containers, err := d.nativeContainers(ctx, *p, slot)
	if err != nil {
		return err
	}
	next := *p
	resolve := func(service string, port int) (int, error) {
		found := []Container{}
		for _, c := range containers {
			if c.Config.Labels["com.docker.compose.service"] == service {
				found = append(found, c)
			}
		}
		ports, err := replicaRoutePorts(found, port, true)
		if err != nil {
			return 0, err
		}
		setRouteBindings(&next, slot, service, port, ports)
		return ports[0], nil
	}
	next.ServiceRoutes = append([]model.ServiceRoute(nil), p.ServiceRoutes...)
	setPrimary := func(port int) {
		if slot == "green" {
			next.GreenPort = port
		} else {
			next.BluePort = port
		}
	}
	if p.RouteService != "" {
		port, err := resolve(p.RouteService, p.RoutePort)
		if err != nil {
			return err
		}
		setPrimary(port)
	}
	if pin := p.PublishedRoute; pin != nil {
		port, err := resolve(pin.Service, pin.ContainerPort)
		if err != nil {
			return err
		}
		copy := *pin
		copy.HostPort = port
		next.PublishedRoute = &copy
		setPrimary(port)
	}
	for i, route := range next.ServiceRoutes {
		port, err := resolve(route.Service, route.ContainerPort)
		if err != nil {
			return err
		}
		next.ServiceRoutes[i].HostPort = port
		next.ServiceRoutes[i].Managed = true
	}
	*p = next
	return nil
}

func privateRoutePort(c Container, containerPort int) (int, error) {
	bindings := c.NetworkSettings.Ports[fmt.Sprintf("%d/tcp", containerPort)]
	if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" {
		return 0, model.Fail("SERVICE_ROUTE_BINDING_UNAVAILABLE")
	}
	port, err := strconv.Atoi(bindings[0].HostPort)
	if err != nil || port < 1 || port > 65535 {
		return 0, model.Fail("SERVICE_ROUTE_BINDING_UNAVAILABLE")
	}
	return port, nil
}

func (d Docker) ResolveServiceRouteBindings(ctx context.Context, p *model.Project, plan *model.ServiceUpdate) error {
	if plan.Mode == "seamless" {
		containers, err := d.inspectServiceContainers(ctx, *p, plan.Instance)
		if err != nil {
			return err
		}
		if len(containers) != instanceReplicas(plan.Instance) || len(plan.Instance.ContainerIDs) > 0 && !instanceIdentity(plan.Instance, containers) {
			return model.Uncertain("SERVICE_IDENTITY_CHANGED")
		}
		ports, err := replicaRoutePorts(containers, serviceInstancePort(*p, plan.Service, plan.Instance), true)
		if err != nil {
			return err
		}
		plan.Instance.Port, plan.Instance.Ports = ports[0], ports
		plan.Instance.ContainerIDs = nil
		for _, c := range containers {
			plan.Instance.ContainerIDs = append(plan.Instance.ContainerIDs, c.ID)
		}
	} else {
		// A controlled service restart can also receive a fresh ephemeral port.
		// Only refresh bindings compiled as automatic in this release. Legacy
		// externally managed mappings are preserved until a full deployment.
		_, b, err := nativeRelease(d.Config, *p, plan.Instance.Release)
		if err != nil {
			return err
		}
		if err := resolveRestartRouteBindings(d, ctx, p, plan, b); err != nil {
			return err
		}
	}
	return nil
}

func resolveRestartRouteBindings(d Docker, ctx context.Context, p *model.Project, plan *model.ServiceUpdate, b []byte) error {
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return model.Uncertain("COMPOSE_CONFIG_DIVERGED")
	}
	automatic := map[int]bool{}
	for _, raw := range array(object(object(doc["services"])[plan.Instance.Name])["ports"]) {
		binding := object(raw)
		if binding["published"] == nil && binding["host_ip"] == "127.0.0.1" && (binding["protocol"] == nil || binding["protocol"] == "tcp") {
			port, _ := strconv.Atoi(fmt.Sprint(binding["target"]))
			automatic[port] = true
		}
	}
	if len(automatic) == 0 {
		return nil
	}
	containers, err := d.inspectServiceContainers(ctx, *p, plan.Instance)
	if err != nil {
		return err
	}
	if len(containers) != instanceReplicas(plan.Instance) {
		return model.Fail("SERVICE_REPLICAS_CHANGED")
	}
	ports := map[int][]int{}
	for target := range automatic {
		values, err := replicaRoutePorts(containers, target, true)
		if err != nil {
			return err
		}
		ports[target] = values
	}
	next := *p
	next.ServiceRoutes = append([]model.ServiceRoute(nil), p.ServiceRoutes...)
	for target, values := range ports {
		setRouteBindings(&next, p.Active, plan.Service, target, values)
	}
	for i, route := range next.ServiceRoutes {
		if route.Service == plan.Service && len(ports[route.ContainerPort]) > 0 {
			next.ServiceRoutes[i].HostPort = ports[route.ContainerPort][0]
		}
	}
	primary := 0
	if p.RouteService == plan.Service {
		if values := ports[p.RoutePort]; len(values) > 0 {
			primary = values[0]
		}
	}
	if pin := p.PublishedRoute; pin != nil && pin.Service == plan.Service && len(ports[pin.ContainerPort]) > 0 {
		copy := *pin
		copy.HostPort = ports[pin.ContainerPort][0]
		next.PublishedRoute = &copy
		primary = copy.HostPort
	}
	if primary > 0 {
		if p.Active == "green" {
			next.GreenPort = primary
		} else {
			next.BluePort = primary
		}
	}
	plan.Instance.Port = 0
	plan.Instance.Ports = nil
	if len(ports) == 1 {
		for _, values := range ports {
			plan.Instance.Port, plan.Instance.Ports = values[0], values
		}
	}
	*p = next
	return nil
}
