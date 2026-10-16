package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

// PlanConfigurationUpdates compares Docker-resolved service definitions, so
// .env interpolation and env_file changes are considered, without logging values.
// Only routed, stateless services can be replaced while dependencies stay live.
func (d Docker) PlanConfigurationUpdates(ctx context.Context, p model.Project, release model.Release) ([]model.ServiceUpdate, error) {
	if !p.NativeCompose() || p.ZeroDowntime || p.State != "running" || p.Environment != model.Production || len(p.Domains) == 0 {
		return nil, model.Fail("CONFIGURATION_SEAMLESS_UNAVAILABLE")
	}
	current, _ := p.Current()
	_, before, err := nativeRelease(d.Config, p, current)
	if err != nil {
		return nil, err
	}
	_, after, err := nativeRelease(d.Config, p, release)
	if err != nil {
		return nil, err
	}
	var old, next map[string]any
	if json.Unmarshal(before, &old) != nil || json.Unmarshal(after, &next) != nil {
		return nil, model.Fail("COMPOSE_CONFIG_DIVERGED")
	}
	services, candidates := object(old["services"]), object(next["services"])
	if len(services) != len(candidates) {
		return nil, model.Fail("CONFIGURATION_SEAMLESS_TOPOLOGY_CHANGED")
	}
	// Shared resource edits cannot be applied by an isolated app replacement.
	for _, doc := range []map[string]any{old, next} {
		delete(doc, "services")
		delete(doc, "x-dockyard-env-sha256")
		delete(doc, "x-dockyard-env-files-sha256")
		delete(doc, "x-dockyard-source-sha256")
	}
	if !reflect.DeepEqual(old, next) {
		return nil, model.Fail("CONFIGURATION_SEAMLESS_TOPOLOGY_CHANGED")
	}
	names := []string{}
	for name, svc := range candidates {
		previous, exists := services[name]
		if !exists {
			return nil, model.Fail("CONFIGURATION_SEAMLESS_TOPOLOGY_CHANGED")
		}
		if !reflect.DeepEqual(previous, svc) {
			names = append(names, name)
		}
	}
	// A Deploy with unchanged configuration can still pull a newer app image.
	if len(names) == 0 {
		for name := range candidates {
			_, domains, err := ServiceUpdateTarget(p, name)
			if err == nil && len(domains) > 0 {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return nil, model.Fail("CONFIGURATION_SEAMLESS_UNAVAILABLE")
	}
	sort.Strings(names)
	plans := []model.ServiceUpdate{}
	for _, name := range names {
		svc := object(candidates[name])
		previous := object(services[name])
		port, domains, err := ServiceUpdateTarget(p, name)
		image, _ := svc["image"].(string)
		if err != nil || port == 0 || len(domains) == 0 || serviceSeamlessReason(svc) != "" || image == "" || svc["build"] != nil {
			return nil, model.Fail("CONFIGURATION_SEAMLESS_INCOMPATIBLE")
		}
		// Candidate networks come from the verified running app. Reject edits
		// to network membership/aliases or externally supplied files/mounts.
		for _, key := range []string{"networks", "volumes", "configs", "secrets", "depends_on", "container_name", "hostname"} {
			if !reflect.DeepEqual(previous[key], svc[key]) {
				return nil, model.Fail("CONFIGURATION_SEAMLESS_TOPOLOGY_CHANGED")
			}
		}
		for _, raw := range array(svc["ports"]) {
			binding := object(raw)
			if !reflect.DeepEqual(binding["target"], float64(port)) || binding["protocol"] != nil && binding["protocol"] != "tcp" {
				return nil, model.Fail("SERVICE_EXTRA_PORTS")
			}
		}
		readiness := ""
		if name == p.RouteService {
			readiness = p.ReadinessPath
		}
		if instance, ok := p.ServiceInstances[name]; ok {
			readiness = instance.ReadinessPath
		}
		plan, err := d.PlanServiceUpdate(ctx, p, name, "seamless", port, 0, readiness)
		if err != nil {
			return nil, err
		}
		plan.SourceRelease = &release
		plans = append(plans, plan)
	}
	return plans, nil
}
