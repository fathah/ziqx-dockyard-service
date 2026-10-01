package model

import "time"

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

type Project struct {
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
	if p.Adoption != nil && (!p.NativeCompose() || p.ZeroDowntime || !p.ServiceMode && (len(p.Domains) != 0 || p.RouteService != "" || p.BluePort != 0 || p.GreenPort != 0)) {
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
	Mode   uint32 `json:"mode"`
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type Input struct {
	ServiceUpdate *ServiceUpdate `json:"service_update,omitempty"`
	Previous      *Project       `json:"previous,omitempty"`
	RouteEdits    []RouteEdit    `json:"route_edits,omitempty"`
	Project       *Project       `json:"project,omitempty"`
	Release       *Release       `json:"release,omitempty"`
	Hostname      string         `json:"hostname,omitempty"`
	DNSRecordID   string         `json:"dns_record_id,omitempty"`
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
	Updatable      bool   `json:"updatable,omitempty"`
	ContainerPorts []int  `json:"container_ports,omitempty"`
	UpdateReason   string `json:"update_reason,omitempty"`
	Seamless       bool   `json:"seamless,omitempty"`
	Slot           string `json:"slot"`
	Name           string `json:"name"`
	Image          string `json:"image"`
	Template       string `json:"template_id"`
	Compose        string `json:"compose_revision,omitempty"`
	Environment    string `json:"environment_revision"`
}

// A service version runs under the original Compose project identity. Its
// immutable manifest is separate so pulls cannot recreate sibling services.
type ServiceInstance struct {
	Name           string              `json:"name"`
	ContainerID    string              `json:"container_id,omitempty"`
	Release        Release             `json:"release"`
	Port           int                 `json:"port,omitempty"`
	NetworkAliases map[string][]string `json:"network_aliases,omitempty"`
}
type ServiceUpdate struct {
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
	Input     Input      `json:"-"`
}

type Fault struct {
	Code     string
	Recovery bool
}

func (e *Fault) Error() string    { return e.Code }
func Fail(code string) error      { return &Fault{Code: code} }
func Uncertain(code string) error { return &Fault{Code: code, Recovery: true} }
