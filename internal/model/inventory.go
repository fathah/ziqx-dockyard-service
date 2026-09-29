package model

import "time"

// Observed metadata grants no Docker/Caddy ownership or deployment authority.
type ExistingService struct {
	Name           string `json:"name"`
	Image          string `json:"image,omitempty"`
	PublishedPorts []int  `json:"published_ports"`
}
type ExistingProject struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Environment  string            `json:"environment"`
	Managed      bool              `json:"managed"`
	Present      bool              `json:"present"`
	ComposeFiles []string          `json:"compose_files"`
	Services     []ExistingService `json:"services"`
	Warnings     []string          `json:"warnings"`
}
type ExistingSite struct {
	HostMatcher string   `json:"host_matcher"`
	Upstreams   []string `json:"upstreams"`
	ProjectIDs  []string `json:"project_ids"`
}
type Inventory struct {
	LastSyncAt         *time.Time        `json:"last_sync_at,omitempty"`
	ProjectsObservedAt *time.Time        `json:"projects_observed_at,omitempty"`
	CaddyObservedAt    *time.Time        `json:"caddy_observed_at,omitempty"`
	DockerObservedAt   *time.Time        `json:"docker_observed_at,omitempty"`
	Projects           []ExistingProject `json:"projects"`
	Sites              []ExistingSite    `json:"sites"`
	ReservedPorts      []int             `json:"reserved_ports"`
	Warnings           []string          `json:"warnings"`
}
