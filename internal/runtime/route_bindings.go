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
	resolve := func(service string, port int) (int, error) {
		var found *Container
		for i := range containers {
			if containers[i].Config.Labels["com.docker.compose.service"] == service {
				if found != nil {
					return 0, model.Fail("SERVICE_ROUTE_REPLICAS_UNSUPPORTED")
				}
				found = &containers[i]
			}
		}
		if found == nil || !found.State.Running {
			return 0, model.Fail("CONTAINER_UNAVAILABLE")
		}
		return privateRoutePort(*found, port)
	}
	next := *p
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
		c, err := d.inspectServiceInstance(ctx, *p, plan.Instance)
		if err != nil {
			return err
		}
		port, err := privateRoutePort(c, serviceInstancePort(*p, plan.Service, plan.Instance))
		if err != nil {
			return err
		}
		plan.Instance.Port = port
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
	c, err := d.inspectServiceInstance(ctx, *p, plan.Instance)
	if err != nil {
		return err
	}
	ports := map[int]int{}
	for target := range automatic {
		port, err := privateRoutePort(c, target)
		if err != nil {
			return err
		}
		ports[target] = port
	}
	next := *p
	next.ServiceRoutes = append([]model.ServiceRoute(nil), p.ServiceRoutes...)
	for i, route := range next.ServiceRoutes {
		if route.Service == plan.Service && ports[route.ContainerPort] > 0 {
			next.ServiceRoutes[i].HostPort = ports[route.ContainerPort]
		}
	}
	primary := 0
	if p.RouteService == plan.Service {
		primary = ports[p.RoutePort]
	}
	if pin := p.PublishedRoute; pin != nil && pin.Service == plan.Service && ports[pin.ContainerPort] > 0 {
		copy := *pin
		copy.HostPort = ports[pin.ContainerPort]
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
	if len(ports) == 1 {
		for _, port := range ports {
			plan.Instance.Port = port
		}
	}
	*p = next
	return nil
}
