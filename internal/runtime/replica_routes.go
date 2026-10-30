package runtime

import (
	"fmt"
	"maps"
	"sort"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func routeBindingKey(slot, service string, target int) string {
	return fmt.Sprintf("%s/%s/%d", slot, service, target)
}

func setRouteBindings(p *model.Project, slot, service string, target int, ports []int) {
	p.RouteBindings = maps.Clone(p.RouteBindings)
	if p.RouteBindings == nil {
		p.RouteBindings = map[string][]int{}
	}
	p.RouteBindings[routeBindingKey(slot, service, target)] = append([]int(nil), ports...)
}

func serviceRoutePorts(p model.Project, slot, service string, target, fallback int) []int {
	if slot == p.Active {
		if instance, ok := p.ServiceInstances[service]; ok && instance.Port > 0 && (instance.ContainerPort == 0 || instance.ContainerPort == target) {
			if len(instance.Ports) > 0 {
				return instance.Ports
			}
			return []int{instance.Port}
		}
	}
	if ports := p.RouteBindings[routeBindingKey(slot, service, target)]; len(ports) > 0 {
		return ports
	}
	return []int{fallback}
}

func routeHostPorts(p model.Project, domain, slot string) []int {
	for _, route := range p.ServiceRoutes {
		for _, host := range route.Domains {
			if host == domain {
				return serviceRoutePorts(p, slot, route.Service, route.ContainerPort, route.HostPort)
			}
		}
	}
	if pin := p.PublishedRoute; pin != nil {
		return serviceRoutePorts(p, slot, pin.Service, pin.ContainerPort, pin.HostPort)
	}
	return serviceRoutePorts(p, slot, p.RouteService, p.RoutePort, p.Port(slot))
}

// Every replica must have its own verified, reachable IPv4 binding. Sorting
// keeps route snapshots stable when Docker changes the order of ps/inspect.
func replicaRoutePorts(containers []Container, target int, private bool) ([]int, error) {
	if len(containers) == 0 {
		return nil, model.Fail("CONTAINER_UNAVAILABLE")
	}
	ports := make([]int, 0, len(containers))
	seen := map[int]bool{}
	for _, c := range containers {
		if !c.State.Running {
			return nil, model.Fail("CONTAINER_UNAVAILABLE")
		}
		port, err := livePort(c, target)
		if private {
			port, err = privateRoutePort(c, target)
		}
		if err != nil {
			return nil, err
		}
		if seen[port] {
			return nil, model.Fail("SERVICE_ROUTE_BINDING_UNAVAILABLE")
		}
		seen[port] = true
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports, nil
}
