package runtime

import (
	"context"
	"encoding/csv"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

const proxyLabel = "io.ziqx.dockyard.haproxy."

func serviceProxySpec(svc map[string]any) (*model.ServiceProxy, error) {
	labels := object(svc["labels"])
	service, _ := labels[proxyLabel+"service"].(string)
	backend, _ := labels[proxyLabel+"backend"].(string)
	port, _ := labels[proxyLabel+"port"].(string)
	admin, _ := labels[proxyLabel+"admin-port"].(string)
	if service == "" && backend == "" && port == "" && admin == "" {
		return nil, nil
	}
	if admin == "" {
		admin = "9999"
	}
	appPort, err := strconv.Atoi(port)
	adminPort, adminErr := strconv.Atoi(admin)
	if !config.ServiceName.MatchString(service) || !config.ServiceName.MatchString(backend) || err != nil || adminErr != nil || appPort < 1 || appPort > 65535 || adminPort < 1 || adminPort > 65535 {
		return nil, model.Fail("SERVICE_PROXY_INVALID")
	}
	return &model.ServiceProxy{Service: service, Backend: backend, AppPort: appPort, AdminPort: adminPort}, nil
}

func (d Docker) planServiceProxy(ctx context.Context, p model.Project, plan *model.ServiceUpdate, proxy *model.ServiceProxy, apps []Container) error {
	if proxy.Service == plan.Service {
		return model.Fail("SERVICE_PROXY_INVALID")
	}
	instance, ok := p.ServiceInstances[proxy.Service]
	if !ok {
		r, _ := p.Current()
		instance = model.ServiceInstance{Name: proxy.Service, Release: r}
	}
	cs, err := d.inspectServiceContainers(ctx, p, instance)
	if err != nil || len(cs) != 1 || !cs[0].State.Running || cs[0].State.Health == nil || cs[0].State.Health.Status != "healthy" {
		return model.Fail("SERVICE_PROXY_UNAVAILABLE")
	}
	instance.Replicas = 1
	instance.ContainerID, instance.ContainerIDs, instance.Release.Image = cs[0].ID, []string{cs[0].ID}, cs[0].Image
	proxy.Instance = instance
	names := []string{}
	for name, connection := range cs[0].NetworkSettings.Networks {
		if plan.NetworkIDs[name] == connection.NetworkID {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) != 1 {
		return model.Fail("SERVICE_PROXY_NETWORK_AMBIGUOUS")
	}
	proxy.Network = names[0]
	proxy.NetworkID = plan.NetworkIDs[proxy.Network]
	proxy.Addresses = map[string]string{}
	for _, c := range apps {
		proxy.Addresses[c.ID] = c.NetworkSettings.Networks[proxy.Network].IPAddress
	}
	addresses, err := proxyAddresses(apps, proxy, plan.NetworkIDs[proxy.Network])
	if err != nil {
		return err
	}
	stats, err := d.proxyStats(ctx, p, proxy)
	if err != nil {
		return err
	}
	if !proxyGroupReady(stats, addresses) {
		return model.Fail("SERVICE_PROXY_NOT_READY")
	}
	for _, server := range stats {
		if server.Status == "UP" && !addresses[server.Address] {
			return model.Fail("SERVICE_PROXY_BACKEND_CHANGED")
		}
	}
	return nil
}

type proxyServer struct {
	Name, Address, Status string
	Sessions              int
}

func (d Docker) proxyCommand(ctx context.Context, p model.Project, proxy *model.ServiceProxy, command string) (string, error) {
	if proxy == nil || !config.ServiceName.MatchString(proxy.Backend) || proxy.AdminPort < 1 || proxy.AdminPort > 65535 {
		return "", model.Fail("SERVICE_PROXY_INVALID")
	}
	cs, err := d.inspectServiceContainers(ctx, p, proxy.Instance)
	if err != nil || !instanceIdentity(proxy.Instance, cs) || !cs[0].State.Running {
		return "", model.Uncertain("SERVICE_PROXY_CHANGED")
	}
	if cs[0].NetworkSettings.Networks[proxy.Network].NetworkID != proxy.NetworkID {
		return "", model.Uncertain("SERVICE_PROXY_CHANGED")
	}
	// Command and port are positional arguments to a fixed shell program. No
	// source, environment value or server name is interpolated into shell code.
	args := []string{"exec", cs[0].ID, "sh", "-c", `printf '%s\n' "$1" | nc -w 2 127.0.0.1 "$2"`, "dockyard-haproxy", command, strconv.Itoa(proxy.AdminPort)}
	result, err := d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p), args)
	if err != nil || result.Truncated || len(result.Output) > 2<<20 {
		return "", model.Fail("SERVICE_PROXY_CONTROL_REQUIRED")
	}
	return string(result.Output), nil
}

func parseProxyStats(raw, backend string) ([]proxyServer, error) {
	r := csv.NewReader(strings.NewReader(raw))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil || len(rows) < 2 {
		return nil, model.Fail("SERVICE_PROXY_CONTROL_REQUIRED")
	}
	columns := map[string]int{}
	for i, name := range rows[0] {
		columns[strings.TrimSpace(strings.TrimPrefix(name, "#"))] = i
	}
	for _, key := range []string{"pxname", "svname", "status", "addr", "scur"} {
		if _, ok := columns[key]; !ok {
			return nil, model.Fail("SERVICE_PROXY_CONTROL_REQUIRED")
		}
	}
	servers := []proxyServer{}
	seen := map[string]bool{}
	for _, row := range rows[1:] {
		if len(row) != len(rows[0]) {
			return nil, model.Fail("SERVICE_PROXY_CONTROL_REQUIRED")
		}
		value := func(key string) string { return row[columns[key]] }
		if value("pxname") != backend || value("svname") == "BACKEND" || value("svname") == "FRONTEND" {
			continue
		}
		name := value("svname")
		n, err := strconv.Atoi(value("scur"))
		if !config.ServiceName.MatchString(name) || seen[name] || err != nil || n < 0 {
			return nil, model.Fail("SERVICE_PROXY_CONTROL_REQUIRED")
		}
		seen[name] = true
		servers = append(servers, proxyServer{Name: name, Address: value("addr"), Status: value("status"), Sessions: n})
	}
	if len(servers) == 0 {
		return nil, model.Fail("SERVICE_PROXY_BACKEND_REQUIRED")
	}
	return servers, nil
}

func (d Docker) proxyStats(ctx context.Context, p model.Project, proxy *model.ServiceProxy) ([]proxyServer, error) {
	raw, err := d.proxyCommand(ctx, p, proxy, "show stat")
	if err != nil {
		return nil, err
	}
	return parseProxyStats(raw, proxy.Backend)
}

func proxyAddresses(cs []Container, proxy *model.ServiceProxy, networkID string) (map[string]bool, error) {
	addresses := map[string]bool{}
	for _, c := range cs {
		connection := c.NetworkSettings.Networks[proxy.Network]
		ip := net.ParseIP(connection.IPAddress)
		if networkID == "" || connection.NetworkID != networkID || ip == nil || ip.To4() == nil {
			return nil, model.Uncertain("SERVICE_NETWORK_CHANGED")
		}
		address := net.JoinHostPort(ip.String(), strconv.Itoa(proxy.AppPort))
		if addresses[address] {
			return nil, model.Uncertain("SERVICE_IDENTITY_CHANGED")
		}
		addresses[address] = true
	}
	return addresses, nil
}

func proxyGroupReady(servers []proxyServer, addresses map[string]bool) bool {
	found := map[string]bool{}
	for _, server := range servers {
		if addresses[server.Address] && server.Status == "UP" {
			found[server.Address] = true
		}
	}
	return len(addresses) > 0 && len(found) == len(addresses)
}

func (d Docker) proxyInstanceAddresses(ctx context.Context, p model.Project, plan model.ServiceUpdate, instance model.ServiceInstance) (map[string]bool, error) {
	cs, err := d.inspectServiceContainers(ctx, p, instance)
	if err != nil {
		return nil, err
	}
	if !instanceIdentity(instance, cs) {
		return nil, model.Uncertain("SERVICE_IDENTITY_CHANGED")
	}
	for _, c := range cs {
		if reviewed, ok := plan.Proxy.Addresses[c.ID]; ok && reviewed != c.NetworkSettings.Networks[plan.Proxy.Network].IPAddress {
			return nil, model.Uncertain("SERVICE_NETWORK_CHANGED")
		}
	}
	return proxyAddresses(cs, plan.Proxy, plan.NetworkIDs[plan.Proxy.Network])
}

func (d Docker) proxyRetiringAddresses(ctx context.Context, p model.Project, plan model.ServiceUpdate, instance model.ServiceInstance) (map[string]bool, error) {
	cs, err := d.inspectServiceContainers(ctx, p, instance)
	if err != nil {
		if strings.HasPrefix(instance.Name, "dy-update-") {
			result, queryErr := d.serviceCommand(ctx, p, instance, "ps", "--all", "--quiet", instance.Name)
			if queryErr == nil && !result.Truncated && strings.TrimSpace(string(result.Output)) == "" {
				return map[string]bool{}, nil
			}
		}
		return nil, err
	}
	if !instanceIdentity(instance, cs) {
		return nil, model.Uncertain("SERVICE_IDENTITY_CHANGED")
	}
	running := []Container{}
	for _, c := range cs {
		if c.State.Running {
			running = append(running, c)
		}
	}
	if len(running) == 0 {
		return map[string]bool{}, nil
	}
	for _, c := range running {
		if reviewed, ok := plan.Proxy.Addresses[c.ID]; ok && reviewed != c.NetworkSettings.Networks[plan.Proxy.Network].IPAddress {
			return nil, model.Uncertain("SERVICE_NETWORK_CHANGED")
		}
	}
	return proxyAddresses(running, plan.Proxy, plan.NetworkIDs[plan.Proxy.Network])
}

func (d Docker) setProxyState(ctx context.Context, p model.Project, proxy *model.ServiceProxy, server, state string) error {
	if !config.ServiceName.MatchString(server) || state != "ready" && state != "drain" {
		return model.Fail("SERVICE_PROXY_INVALID")
	}
	response, err := d.proxyCommand(ctx, p, proxy, fmt.Sprintf("set server %s/%s state %s", proxy.Backend, server, state))
	if err != nil {
		return err
	}
	if strings.TrimSpace(response) != "" {
		return model.Uncertain("SERVICE_PROXY_SWITCH_REQUIRED")
	}
	return nil
}

// HAProxy must discover and health-check EVERY new address before any old
// address is drained. A stale DNS answer or exhausted server-template leaves
// the original group running and requires recovery instead of guessing.
func (d Docker) SwitchServiceProxy(ctx context.Context, p model.Project, plan model.ServiceUpdate, restore bool) error {
	if plan.Proxy == nil || plan.Previous == nil {
		return model.Fail("SERVICE_UPDATE_INVALID")
	}
	keep, retire := plan.Instance, *plan.Previous
	if restore {
		keep, retire = retire, keep
	}
	ready, err := d.proxyInstanceAddresses(ctx, p, plan, keep)
	if err != nil {
		return err
	}
	old, err := d.proxyRetiringAddresses(ctx, p, plan, retire)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(d.Config.HealthSeconds)*time.Second)
	defer cancel()
	for {
		servers, err := d.proxyStats(ctx, p, plan.Proxy)
		if err != nil {
			return err
		}
		for _, server := range servers {
			if server.Status == "UP" && !ready[server.Address] && !old[server.Address] {
				return model.Uncertain("SERVICE_PROXY_BACKEND_CHANGED")
			}
			if ready[server.Address] && strings.HasPrefix(server.Status, "DRAIN") {
				if err := d.setProxyState(ctx, p, plan.Proxy, server.Name, "ready"); err != nil {
					return err
				}
			}
		}
		if proxyGroupReady(servers, ready) {
			for _, server := range servers {
				if old[server.Address] {
					if err := d.setProxyState(ctx, p, plan.Proxy, server.Name, "drain"); err != nil {
						return err
					}
				}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return model.Uncertain("SERVICE_PROXY_NOT_READY")
		case <-time.After(time.Second):
		}
	}
}

// Check current sessions after the configured drain interval. Do not stop an
// old replica with requests still in flight, even when the interval elapsed.
func (d Docker) DrainedServiceProxy(ctx context.Context, p model.Project, plan model.ServiceUpdate, restore bool) error {
	if plan.Proxy == nil || plan.Previous == nil {
		return model.Fail("SERVICE_UPDATE_INVALID")
	}
	keep, retire := plan.Instance, *plan.Previous
	if restore {
		keep, retire = retire, keep
	}
	ready, err := d.proxyInstanceAddresses(ctx, p, plan, keep)
	if err != nil {
		return err
	}
	old, err := d.proxyRetiringAddresses(ctx, p, plan, retire)
	if err != nil {
		return err
	}
	servers, err := d.proxyStats(ctx, p, plan.Proxy)
	if err != nil {
		return err
	}
	if !proxyGroupReady(servers, ready) {
		return model.Uncertain("SERVICE_PROXY_NOT_READY")
	}
	found := map[string]bool{}
	for _, server := range servers {
		if server.Status == "UP" && !ready[server.Address] && !old[server.Address] {
			return model.Uncertain("SERVICE_PROXY_BACKEND_CHANGED")
		}
		if old[server.Address] && (server.Sessions != 0 || server.Status == "UP" || strings.HasPrefix(server.Status, "UP ")) {
			return model.Uncertain("SERVICE_PROXY_DRAIN_REQUIRED")
		}
		if old[server.Address] {
			found[server.Address] = true
		}
	}
	if len(found) != len(old) {
		return model.Uncertain("SERVICE_PROXY_DRAIN_REQUIRED")
	}
	return nil
}
