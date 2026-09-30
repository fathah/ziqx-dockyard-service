package config

import (
	"errors"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var ID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
var EnvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
var ServiceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
var Digest = regexp.MustCompile(`^ghcr\.io/[a-z0-9][a-z0-9._/-]*@sha256:[0-9a-f]{64}$`)

type Key struct {
	ID                string   `json:"id"`
	SecretFile        string   `json:"secret_file"`
	Scopes            []string `json:"scopes"`
	Projects          []string `json:"projects"`
	CertificateSHA256 string   `json:"certificate_sha256"`
}
type Template struct {
	ImageRepository      string   `json:"image_repository"`
	ContainerPort        int      `json:"container_port"`
	HealthCommand        []string `json:"health_command"`
	ReadinessPath        string   `json:"readiness_path"`
	AllowedEnvironment   []string `json:"allowed_environment"`
	RequiredEnvironment  []string `json:"required_environment"`
	User                 string   `json:"user"`
	MemoryMB             int      `json:"memory_mb"`
	CPUs                 float64  `json:"cpus"`
	AllowedVolumeTargets []string `json:"allowed_volume_targets,omitempty"`
}
type Cloudflare struct {
	TokenFile string            `json:"token_file"`
	Zones     map[string]string `json:"zones"`
	OriginIP  string            `json:"origin_ip"`
	Proxied   bool              `json:"proxied"`
}
type Config struct {
	ServerID           string              `json:"server_id"`
	Listen             string              `json:"listen"`
	TLSCert            string              `json:"tls_cert"`
	TLSKey             string              `json:"tls_key"`
	ClientCA           string              `json:"client_ca"`
	StateDir           string              `json:"state_dir"`
	ProjectsRoot       string              `json:"projects_root"`
	DockerBinary       string              `json:"docker_binary"`
	DockerConfig       string              `json:"docker_config"`
	CaddyBinary        string              `json:"caddy_binary"`
	Caddyfile          string              `json:"caddyfile"`
	CaddySites         string              `json:"caddy_sites"`
	CaddyAdminSocket   string              `json:"caddy_admin_socket"`
	AllowedDomains     []string            `json:"allowed_domains"`
	ReservedDomains    []string            `json:"reserved_domains"`
	PortMin            int                 `json:"port_min"`
	PortMax            int                 `json:"port_max"`
	ReservedPorts      []int               `json:"reserved_ports"`
	MaxProjects        int                 `json:"max_projects"`
	MaxQueuedJobs      int                 `json:"max_queued_jobs"`
	DrainSeconds       int                 `json:"drain_seconds"`
	HealthSeconds      int                 `json:"health_seconds"`
	CommandSeconds     int                 `json:"command_seconds"`
	Keys               []Key               `json:"keys"`
	FingerprintKeyFile string              `json:"fingerprint_key_file"`
	Templates          map[string]Template `json:"templates"`
	MaxStackMemoryMB   int                 `json:"max_stack_memory_mb,omitempty"`
	MaxStackCPUs       float64             `json:"max_stack_cpus,omitempty"`
	Cloudflare         *Cloudflare         `json:"cloudflare,omitempty"`
}

func Load(path string) (Config, error) {
	var c Config
	if err := secure.Check(path, true); err != nil {
		return c, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if len(b) > 262144 {
		return c, errors.New("config too large")
	}
	if err = secure.Decode(b, &c); err != nil {
		return c, err
	}
	if err = c.Validate(); err != nil {
		return c, err
	}
	for _, p := range []string{c.StateDir, c.ProjectsRoot, c.DockerConfig, c.CaddySites} {
		if err = secure.Check(p, false); err != nil {
			return c, err
		}
	}
	for _, p := range []string{c.TLSKey, c.FingerprintKeyFile} {
		if err = secure.Check(p, true); err != nil {
			return c, err
		}
	}
	for _, p := range []string{c.TLSCert, c.ClientCA, c.Caddyfile, c.CaddyBinary, c.DockerBinary} {
		if err = secure.Check(p, false); err != nil {
			return c, err
		}
	}
	for _, k := range c.Keys {
		if !filepath.IsAbs(k.SecretFile) || filepath.Clean(k.SecretFile) != k.SecretFile {
			return c, errors.New("invalid credential path")
		}
		if err = secure.Check(k.SecretFile, true); err != nil {
			return c, err
		}
	}
	if c.Cloudflare != nil {
		if !filepath.IsAbs(c.Cloudflare.TokenFile) || filepath.Clean(c.Cloudflare.TokenFile) != c.Cloudflare.TokenFile {
			return c, errors.New("invalid credential path")
		}
		if err = secure.Check(c.Cloudflare.TokenFile, true); err != nil {
			return c, err
		}
	}
	return c, nil
}

func (c Config) Validate() error {
	bad := errors.New("invalid root policy")
	if c.MaxStackMemoryMB < 0 || c.MaxStackMemoryMB > 524288 || c.MaxStackCPUs < 0 || c.MaxStackCPUs > 512 {
		return bad
	}
	h, port, err := net.SplitHostPort(c.Listen)
	if err != nil || net.ParseIP(h) == nil || net.ParseIP(h).IsUnspecified() || port != "9123" {
		return bad
	}
	if ip := net.ParseIP(h); !ip.IsLoopback() && !ip.IsPrivate() {
		return bad
	}
	if !ID.MatchString(c.ServerID) || c.PortMin < 1024 || c.PortMax > 65535 || c.PortMin > c.PortMax || c.MaxProjects < 1 || c.MaxProjects > 1000 || c.MaxQueuedJobs < 1 || c.MaxQueuedJobs > 1000 || c.DrainSeconds < 1 || c.DrainSeconds > 600 || c.HealthSeconds < 5 || c.HealthSeconds > 600 || c.CommandSeconds < 5 || c.CommandSeconds > 600 {
		return bad
	}
	for _, p := range []string{c.StateDir, c.ProjectsRoot, c.DockerBinary, c.DockerConfig, c.CaddyBinary, c.Caddyfile, c.CaddySites, c.CaddyAdminSocket, c.TLSCert, c.TLSKey, c.ClientCA, c.FingerprintKeyFile} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsAny(p, "\n\r\x00${}\"'\\ \t") || p == "/" {
			return bad
		}
	}
	// Managed trees cannot overlap each other or the operator's Caddyfile.
	roots := []string{c.StateDir, c.ProjectsRoot, c.CaddySites}
	for i, p := range roots {
		for j, q := range roots {
			if i != j && (p == q || strings.HasPrefix(p, q+"/")) {
				return bad
			}
		}
		if strings.HasPrefix(c.Caddyfile, p+"/") {
			return bad
		}
	}
	if len(c.Keys) < 1 || len(c.Keys) > 10 {
		return bad
	}
	seen := map[string]bool{}
	for _, k := range c.Keys {
		if !ID.MatchString(k.ID) || seen[k.ID] || len(k.Scopes) == 0 || len(k.Projects) == 0 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(k.CertificateSHA256) {
			return bad
		}
		seen[k.ID] = true
		for _, s := range k.Scopes {
			if !KnownScope(s) {
				return bad
			}
		}
		for _, p := range k.Projects {
			if p != "*" && !ID.MatchString(p) {
				return bad
			}
		}
	}
	for _, domain := range append(append([]string{}, c.AllowedDomains...), c.ReservedDomains...) {
		if !Hostname(domain) {
			return bad
		}
	}
	for id, t := range c.Templates {
		if !ID.MatchString(id) || !Digest.MatchString(t.ImageRepository+"@sha256:"+strings.Repeat("a", 64)) || t.ContainerPort < 1024 || t.ContainerPort > 65535 || len(t.HealthCommand) < 1 || len(t.HealthCommand) > 20 || !strings.HasPrefix(t.ReadinessPath, "/") || strings.ContainsAny(t.ReadinessPath, "?#\r\n") || t.MemoryMB < 64 || t.MemoryMB > 65536 || t.CPUs <= 0 || t.CPUs > 64 || !regexp.MustCompile(`^[1-9][0-9]{0,8}:[1-9][0-9]{0,8}$`).MatchString(t.User) {
			return bad
		}
		allowed := map[string]bool{}
		for _, target := range t.AllowedVolumeTargets {
			if !filepath.IsAbs(target) || filepath.Clean(target) != target || target == "/" || strings.ContainsAny(target, "\r\n\x00:$\\") {
				return bad
			}
		}
		for _, e := range t.AllowedEnvironment {
			if !EnvKey.MatchString(e) || allowed[e] || strings.HasPrefix(e, "DOCKYARD_") || strings.HasPrefix(e, "COMPOSE_") {
				return bad
			}
			allowed[e] = true
		}
		for _, e := range t.RequiredEnvironment {
			if !allowed[e] {
				return bad
			}
		}
	}
	if c.Cloudflare != nil {
		ip := net.ParseIP(c.Cloudflare.OriginIP)
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsMulticast() || len(c.Cloudflare.Zones) == 0 {
			return bad
		}
		for d, z := range c.Cloudflare.Zones {
			if !Hostname(d) || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(z) {
				return bad
			}
		}
	}
	return nil
}

func Hostname(s string) bool {
	if len(s) > 253 || strings.ToLower(s) != s || !strings.Contains(s, ".") || net.ParseIP(s) != nil {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if len(l) < 1 || len(l) > 63 || !regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`).MatchString(l) {
			return false
		}
	}
	return true
}
func (c Config) DomainAllowed(s string) bool {
	if !Hostname(s) {
		return false
	}
	for _, d := range c.ReservedDomains {
		if s == d || strings.HasSuffix(s, "."+d) {
			return false
		}
	}
	for _, d := range c.AllowedDomains {
		if s == d || strings.HasSuffix(s, "."+d) {
			return true
		}
	}
	return false
}
func (c Config) PortAllowed(p int) bool {
	if p < c.PortMin || p > c.PortMax || p == 9123 {
		return false
	}
	for _, r := range c.ReservedPorts {
		if p == r {
			return false
		}
	}
	return true
}
func KnownScope(s string) bool {
	switch s {
	case "compose.admin", "deploy.read", "deploy.logs", "deploy.execute", "deploy.rollback", "deploy.lifecycle", "deploy.stop", "deploy.environment", "projects.write", "sites.write", "dns.write":
		return true
	}
	return false
}
