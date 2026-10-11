package model

import (
	"strings"
	"time"
)

type Release struct {
	ID          string    `json:"id"`
	Image       string    `json:"image"`
	Environment string    `json:"environment_revision"`
	Compose     string    `json:"compose_revision,omitempty"`
	Created     time.Time `json:"created_at"`
}

type Adoption struct {
	SourceName     string   `json:"source_name"`
	ComposeProject string   `json:"compose_project"`
	ConfigFiles    []string `json:"config_files"`
	ContainerIDs   []string `json:"container_ids"`
}

// PublishedRoute pins an existing Compose binding when only its Caddy route
// is brought under management. Configuring a route does not recreate the stack.
type PublishedRoute struct {
	Service       string `json:"service"`
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
}

type Project struct {
	CaddyRoutes      []CaddyRoute               `json:"caddy_routes,omitempty" api:"private"`
	ServiceRoutes    []ServiceRoute             `json:"service_routes,omitempty"`
	PublishedRoute   *PublishedRoute            `json:"published_route,omitempty"`
	ServiceInstances map[string]ServiceInstance `json:"service_instances,omitempty"`
	ServicePorts     []int                      `json:"service_ports,omitempty"`
	ServiceMode      bool                       `json:"service_updates,omitempty"`
	Adoption         *Adoption                  `json:"adoption,omitempty"`
	ExternalDomains  []string                   `json:"external_domains,omitempty"`
	Mode             string                     `json:"mode,omitempty"`
	RouteService     string                     `json:"route_service,omitempty"`
	RoutePort        int                        `json:"route_port,omitempty"`
	ReadinessPath    string                     `json:"readiness_path,omitempty"`
	ID               string                     `json:"id"`
	AppID            string                     `json:"app_id"`
	Environment      string                     `json:"environment"`
	Template         string                     `json:"template_id"`
	TemplateRevision string                     `json:"template_revision"`
	Domains          []string                   `json:"domains"`
	ZeroDowntime     bool                       `json:"zerodowntime"`
	BluePort         int                        `json:"blue_port"`
	GreenPort        int                        `json:"green_port,omitempty"`
	Active           string                     `json:"active_slot,omitempty"`
	State            string                     `json:"state"`
	Slots            map[string]Release         `json:"slots"`
	Releases         []Release                  `json:"releases"`
	DNS              []string                   `json:"dns_records,omitempty"`
	RecoveryDrain    bool                       `json:"recovery_drain_pending,omitempty"`
}

// CaddyRoute keeps reviewed site rules imported from the operator Caddyfile.
// The upstream marker is rendered from the verified service binding, never
// from a hostname or port supplied as arbitrary Caddy configuration.
const CaddyUpstream = "{dockyard.upstream}"

type CaddyRoute struct {
	Domain  string `json:"domain"`
	Address string `json:"address"`
	Body    string `json:"body"`
}

// ServiceRoute assigns a group of hosts to a service's verified local binding.
// Managed bindings are compiled into every future full-stack release.
type ServiceRoute struct {
	Service       string   `json:"service"`
	ContainerPort int      `json:"container_port"`
	HostPort      int      `json:"host_port"`
	Domains       []string `json:"domains"`
	Managed       bool     `json:"managed,omitempty"`
}

func (p Project) NativeCompose() bool { return p.Mode == "compose" }

const (
	Development = "development"
	Staging     = "staging"
	Production  = "production"
)

func ValidEnvironment(value string) bool {
	return value == Development || value == Staging || value == Production
}

func (p Project) ValidateTarget() error {
	templates := map[string]bool{}
	for _, route := range p.CaddyRoutes {
		if !p.NativeCompose() || templates[route.Domain] || route.Domain == "" || route.Address == "" || strings.ContainsAny(route.Address, "\r\n\t {}") || len(route.Body) > 2<<20 || strings.Count(route.Body, CaddyUpstream) != 1 {
			return Uncertain("CADDY_ROUTE_INVALID")
		}
		templates[route.Domain] = true
	}
	seen := map[string]bool{}
	for _, route := range p.ServiceRoutes {
		if !p.NativeCompose() || p.ZeroDowntime || route.Service == "" || route.ContainerPort < 1 || route.ContainerPort > 65535 || route.HostPort < 0 || route.HostPort == 0 && !route.Managed || route.HostPort > 65535 || len(route.Domains) == 0 {
			return Uncertain("SERVICE_ROUTE_INVALID")
		}
		for _, domain := range route.Domains {
			found := false
			for _, owned := range p.Domains {
				found = found || owned == domain
			}
			if !found || seen[domain] {
				return Uncertain("SERVICE_ROUTE_INVALID")
			}
			seen[domain] = true
		}
	}
	if p.Port(p.Active) == 0 && p.RouteService == "" && p.PublishedRoute == nil {
		for _, domain := range p.Domains {
			if !seen[domain] {
				return Uncertain("SERVICE_ROUTE_INVALID")
			}
		}
	}
	if pin := p.PublishedRoute; pin != nil && (!p.NativeCompose() || p.Adoption == nil || p.ZeroDowntime || p.ServiceMode || p.RouteService != "" || pin.Service == "" || pin.ContainerPort < 1 || pin.ContainerPort > 65535 || pin.HostPort < 1 || pin.HostPort > 65535 || pin.HostPort != p.BluePort || p.GreenPort != 0 || len(p.Domains) == 0) {
		return Uncertain("PUBLISHED_ROUTE_INVALID")
	}
	if p.Adoption != nil && (!p.NativeCompose() || p.ZeroDowntime || !p.ServiceMode && p.PublishedRoute == nil && len(p.ServiceRoutes) == 0 && (len(p.Domains) != 0 || p.RouteService != "" || p.BluePort != 0 || p.GreenPort != 0)) {
		return Uncertain("ADOPTED_ROUTES_PRESERVED")
	}
	if p.AppID == "" || !ValidEnvironment(p.Environment) {
		return Uncertain("PROJECT_TARGET_INVALID")
	}
	_, green := p.Slots["green"]
	if p.Environment != Production && (p.ZeroDowntime || p.GreenPort != 0 || p.Active == "green" || green) {
		return Uncertain("ENVIRONMENT_MODE_INVALID")
	}
	return nil
}

func (p Project) Port(slot string) int {
	if slot == p.Active {
		if instance, ok := p.ServiceInstances[p.RouteService]; ok && instance.Port > 0 {
			return instance.Port
		}
	}
	if slot == "green" {
		return p.GreenPort
	}
	return p.BluePort
}
func (p Project) Current() (Release, bool) {
	// A failed single-slot candidate can already occupy Slots during maintenance.
	// Restore uses the last activated release rather than that failed candidate.
	if p.State != "running" && len(p.Releases) > 0 {
		return p.Releases[len(p.Releases)-1], true
	}
	r, ok := p.Slots[p.Active]
	return r, ok
}

type RouteEdit struct {
	LiveSHA256    string   `json:"live_sha256,omitempty"`
	Domains       []string `json:"domains,omitempty"`
	ManagedBefore *string  `json:"managed_before,omitempty"`
	Mode          uint32   `json:"mode"`
	Path          string   `json:"path"`
	Before        string   `json:"before"`
	After         string   `json:"after"`
}

type Input struct {
	ConfigurationUpdates []ServiceUpdate `json:"configuration_updates,omitempty"`
	RouteTarget          *PublishedRoute `json:"route_target,omitempty"`
	ServiceUpdate        *ServiceUpdate  `json:"service_update,omitempty"`
	Previous             *Project        `json:"previous,omitempty"`
	RouteEdits           []RouteEdit     `json:"route_edits,omitempty"`
	Project              *Project        `json:"project,omitempty"`
	Release              *Release        `json:"release,omitempty"`
	Hostname             string          `json:"hostname,omitempty"`
	DNSRecordID          string          `json:"dns_record_id,omitempty"`
	// DraftSHA identifies the saved draft this deployment was made from.
	DraftSHA string `json:"draft_sha,omitempty"`
}

// ServiceSpec is safe revision metadata, not raw user YAML or secret values.
type ServiceSpec struct {
	Image       string            `json:"image"`
	Template    string            `json:"template_id"`
	Volumes     []string          `json:"volumes"`
	Labels      map[string]string `json:"labels"`
	Healthcheck struct {
		Test []string `json:"test"`
	} `json:"healthcheck"`
}

type ServiceInfo struct {
	HostPorts      []int    `json:"host_ports"`
	DependsOn      []string `json:"depends_on"`
	Updatable      bool     `json:"updatable,omitempty"`
	ContainerPorts []int    `json:"container_ports,omitempty"`
	UpdateReason   string   `json:"update_reason,omitempty"`
	Seamless       bool     `json:"seamless,omitempty"`
	Slot           string   `json:"slot"`
	Name           string   `json:"name"`
	Image          string   `json:"image"`
	Template       string   `json:"template_id"`
	Compose        string   `json:"compose_revision,omitempty"`
	Environment    string   `json:"environment_revision"`
}

// A service version runs under the original Compose project identity. Its
// immutable manifest is separate so pulls cannot recreate sibling services.
type ServiceInstance struct {
	Name           string              `json:"name"`
	ContainerPort  int                 `json:"container_port,omitempty"`
	ReadinessPath  string              `json:"readiness_path,omitempty"`
	ContainerID    string              `json:"container_id,omitempty"`
	Release        Release             `json:"release"`
	Port           int                 `json:"port,omitempty"`
	NetworkAliases map[string][]string `json:"network_aliases,omitempty"`
}
type ServiceUpdate struct {
	// Configuration deployments use this submitted full release, even if the image is unchanged.
	SourceRelease  *Release            `json:"source_release,omitempty"`
	Service        string              `json:"service"`
	Mode           string              `json:"mode"`
	Instance       ServiceInstance     `json:"instance"`
	Previous       *ServiceInstance    `json:"previous,omitempty"`
	NetworkAliases map[string][]string `json:"network_aliases,omitempty"`
	NetworkIDs     map[string]string   `json:"network_ids,omitempty"`
}

type DomainInfo struct {
	Hostname    string `json:"hostname"`
	Assigned    bool   `json:"assigned"`
	DNSRecordID string `json:"dns_record_id,omitempty"`
}

type Job struct {
	ID        string     `json:"job_id"`
	ProjectID string     `json:"project_id"`
	Action    string     `json:"action"`
	Status    string     `json:"status"`
	Phase     string     `json:"phase"`
	Error     string     `json:"error_code,omitempty"`
	Warning   string     `json:"warning_code,omitempty"`
	Actor     string     `json:"actor_id"`
	RequestID string     `json:"request_id"`
	Created   time.Time  `json:"created_at"`
	Finished  *time.Time `json:"finished_at,omitempty"`
	// Diagnostic is redacted Docker output from a failed command.
	Diagnostic string `json:"diagnostic,omitempty"`
	Input      Input  `json:"-"`
}

type Fault struct {
	Code     string
	Recovery bool
	// Detail is redacted command output explaining the failure, if any.
	Detail string
}

func (e *Fault) Error() string    { return e.Code }
func Fail(code string) error      { return &Fault{Code: code} }
func Uncertain(code string) error { return &Fault{Code: code, Recovery: true} }
func FailDetail(code, detail string) error {
	return &Fault{Code: code, Detail: detail}
}
