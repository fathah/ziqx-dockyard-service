// Package inventory observes existing deployments without executing submitted
// Compose or changing ownership, files, routes, containers or secrets.
package inventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
	"go.yaml.in/yaml/v3"
)

type Scanner struct {
	Config config.Config
	Store  *state.Store
	Runner process.Runner
	// Production uses secure.Check; test fixtures can supply their ownership check.
	Check func(string, bool) error
}

var composeNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml", "compose.override.yaml", "compose.override.yml", "docker-compose.override.yaml", "docker-compose.override.yml"}
var imagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(?::[0-9]{1,5})?(?:/[a-z0-9][a-z0-9._-]*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|@sha256:[0-9a-f]{64})?$`)
var servicePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var publishedPattern = regexp.MustCompile(`:([0-9]{1,5})(?:-([0-9]{1,5}))?->`)

func (s Scanner) check(path string) error {
	if s.Check != nil {
		return s.Check(path, false)
	}
	return secure.Check(path, false)
}

// Sync returns recorded safe metadata, including source failure warnings. A
// partial/failed source preserves its last successful database observation.
func (s Scanner) Sync(ctx context.Context) (model.Inventory, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	v := model.Inventory{Projects: []model.ExistingProject{}, Sites: []model.ExistingSite{}, ReservedPorts: []int{}, Warnings: []string{}}
	managed, err := s.Store.Projects()
	if err != nil {
		return v, err
	}
	projectsOK := s.projects(ctx, &v, managed)
	if !projectsOK {
		v.Warnings = append(v.Warnings, "PROJECT_SCAN_FAILED")
	}
	for _, p := range v.Projects {
		if len(p.Warnings) > 0 {
			v.Warnings = append(v.Warnings, "PROJECT_METADATA_INCOMPLETE")
			break
		}
	}
	dockerOK := s.dockerPorts(ctx, &v)
	if !dockerOK {
		v.Warnings = append(v.Warnings, "DOCKER_SCAN_FAILED")
	}
	caddyOK := s.caddySites(ctx, &v)
	if !caddyOK {
		v.Warnings = append(v.Warnings, "CADDY_SCAN_FAILED")
	}
	if err := s.Store.SyncInventory(v, projectsOK, caddyOK, dockerOK); err != nil {
		return v, err
	}
	return s.Store.Inventory()
}

func (s Scanner) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if result, err := s.Sync(ctx); err != nil {
				slog.Warn("inventory sync failed")
			} else if len(result.Warnings) > 0 {
				slog.Warn("inventory sources incomplete", "warnings", result.Warnings)
			}
		}
	}
}

func (s Scanner) projects(ctx context.Context, v *model.Inventory, managed []model.Project) bool {
	if s.check(s.Config.ProjectsRoot) != nil {
		return false
	}
	entries, err := os.ReadDir(s.Config.ProjectsRoot)
	if err != nil || len(entries) > 1000 {
		return false
	}
	known := map[string]model.Project{}
	for _, p := range managed {
		known[p.ID] = p
		if p.Adoption != nil {
			known[p.Adoption.SourceName] = p
		}
	}
	total := 0
	for _, entry := range entries {
		if ctx.Err() != nil {
			return false
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		name := entry.Name()
		if len(name) > 255 || strings.ContainsAny(name, "\r\n\x00") {
			continue
		}
		hash := sha256.Sum256([]byte(name))
		id := "existing-" + hex.EncodeToString(hash[:12])
		p := model.ExistingProject{ID: id, Name: name, Environment: model.Production, Present: true, ComposeFiles: []string{}, Services: []model.ExistingService{}, Warnings: []string{}}
		if own, ok := known[name]; ok {
			if own.Adoption != nil && name == own.ID {
				continue
			}
			p.ID = own.ID
			p.Environment = own.Environment
			p.Managed = true
			items, err := s.Store.Services(own.ID)
			if err != nil {
				p.Warnings = append(p.Warnings, "MANAGED_METADATA_UNAVAILABLE")
			}
			for _, item := range items {
				ports := []int{}
				if item.Name == "app" {
					ports = append(ports, own.Port(item.Slot))
				}
				p.Services = append(p.Services, model.ExistingService{Name: item.Slot + "/" + item.Name, Image: item.Image, PublishedPorts: ports})
			}
			v.Projects = append(v.Projects, p)
			continue
		}
		dir := filepath.Join(s.Config.ProjectsRoot, name)
		for _, file := range composeNames {
			path := filepath.Join(dir, file)
			st, err := os.Lstat(path)
			if os.IsNotExist(err) {
				continue
			}
			p.ComposeFiles = append(p.ComposeFiles, file)
			if err != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 || st.Size() > 1<<20 || s.check(path) != nil {
				p.Warnings = append(p.Warnings, "COMPOSE_FILE_UNSAFE_OR_TOO_LARGE")
				continue
			}
			f, err := os.Open(path)
			if err != nil {
				p.Warnings = append(p.Warnings, "COMPOSE_FILE_UNREADABLE")
				continue
			}
			b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
			f.Close()
			total += len(b)
			if total > 32<<20 {
				return false
			}
			if err != nil || len(b) > 1<<20 {
				p.Warnings = append(p.Warnings, "COMPOSE_FILE_UNREADABLE")
				continue
			}
			services, ok := parseCompose(b)
			if !ok {
				p.Warnings = append(p.Warnings, "COMPOSE_METADATA_UNRESOLVED")
			}
			p.Services = append(p.Services, services...)
		}
		if len(p.ComposeFiles) == 0 {
			continue
		}
		if len(p.ComposeFiles) > 1 {
			p.Warnings = append(p.Warnings, "MULTIPLE_COMPOSE_FILES_NOT_MERGED")
		}
		for _, service := range p.Services {
			v.ReservedPorts = append(v.ReservedPorts, service.PublishedPorts...)
		}
		v.Projects = append(v.Projects, p)
	}
	return true
}

func scalar(n *yaml.Node) string {
	if n != nil && n.Kind == yaml.ScalarNode {
		return n.Value
	}
	return ""
}
func field(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func parseCompose(b []byte) ([]model.ExistingService, bool) {
	out := []model.ExistingService{}
	var doc, extra yaml.Node
	d := yaml.NewDecoder(strings.NewReader(string(b)))
	if d.Decode(&doc) != nil || len(doc.Content) != 1 || d.Decode(&extra) != io.EOF {
		return out, false
	}
	nodes := 0
	var safe func(*yaml.Node, int) bool
	safe = func(n *yaml.Node, depth int) bool {
		nodes++
		if depth > 32 || nodes > 32768 || n.Kind == yaml.AliasNode {
			return false
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i]
				if key.Kind != yaml.ScalarNode || seen[key.Value] {
					return false
				}
				seen[key.Value] = true
			}
		}
		for _, c := range n.Content {
			if !safe(c, depth+1) {
				return false
			}
		}
		return true
	}
	if !safe(&doc, 0) {
		return out, false
	}
	services := field(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode || len(services.Content) > 2000 {
		return out, false
	}
	complete := true
	for i := 0; i < len(services.Content); i += 2 {
		name := scalar(services.Content[i])
		if !servicePattern.MatchString(name) {
			complete = false
			continue
		}
		service := services.Content[i+1]
		item := model.ExistingService{Name: name, PublishedPorts: []int{}}
		image := scalar(field(service, "image"))
		if imagePattern.MatchString(image) && len(image) <= 512 {
			item.Image = image
		} else if image != "" {
			complete = false
		}
		ports := field(service, "ports")
		if ports != nil {
			if ports.Kind != yaml.SequenceNode {
				complete = false
			} else {
				for _, port := range ports.Content {
					n, ok := composePort(port)
					if !ok {
						complete = false
						continue
					}
					if n > 0 {
						item.PublishedPorts = append(item.PublishedPorts, n)
					}
				}
			}
		}
		out = append(out, item)
	}
	return out, complete
}

func composePort(n *yaml.Node) (int, bool) {
	if n.Kind == yaml.MappingNode {
		return portNumber(scalar(field(n, "published")))
	}
	text := scalar(n)
	if strings.ContainsAny(text, "${}\r\n") {
		return 0, false
	}
	text = strings.TrimSuffix(strings.TrimSuffix(text, "/tcp"), "/udp")
	parts := strings.Split(text, ":")
	if len(parts) == 1 {
		return 0, true
	} // Container-only port; host allocation is observed via Docker.
	return portNumber(parts[len(parts)-2])
}
func portNumber(text string) (int, bool) {
	n, err := strconv.Atoi(text)
	return n, err == nil && n > 0 && n <= 65535
}

func (s Scanner) dockerPorts(ctx context.Context, v *model.Inventory) bool {
	r, err := s.Runner.Run(ctx, s.Config.DockerBinary, s.Config.ProjectsRoot, []string{"ps", "--all", "--no-trunc", "--format", "{{json .Ports}}"})
	if err != nil || r.Truncated || len(r.Output) > 2<<20 {
		return false
	}
	var ports []int
	for _, line := range strings.Split(strings.TrimSpace(string(r.Output)), "\n") {
		if line == "" {
			continue
		}
		var text string
		if json.Unmarshal([]byte(line), &text) != nil {
			return false
		}
		for _, match := range publishedPattern.FindAllStringSubmatch(text, -1) {
			start, ok := portNumber(match[1])
			if !ok {
				return false
			}
			end := start
			if match[2] != "" {
				var valid bool
				end, valid = portNumber(match[2])
				if !valid || end < start || end-start > 1024 {
					return false
				}
			}
			for p := start; p <= end; p++ {
				ports = append(ports, p)
			}
		}
	}
	v.ReservedPorts = append(v.ReservedPorts, ports...)
	return true
}

func (s Scanner) caddySites(ctx context.Context, v *model.Inventory) bool {
	if s.check(s.Config.Caddyfile) != nil {
		return false
	}
	r, err := s.Runner.Run(ctx, s.Config.CaddyBinary, filepath.Dir(s.Config.Caddyfile), []string{"adapt", "--config", s.Config.Caddyfile, "--adapter", "caddyfile"})
	if err != nil || r.Truncated || len(r.Output) > 2<<20 {
		return false
	}
	var config map[string]any
	if json.Unmarshal(r.Output, &config) != nil {
		return false
	}
	apps, _ := config["apps"].(map[string]any)
	http, _ := apps["http"].(map[string]any)
	servers, _ := http["servers"].(map[string]any)
	sites := map[string]map[string]bool{}
	var visit func(any, []string, int)
	visit = func(value any, inherited []string, depth int) {
		if depth > 64 {
			return
		}
		switch n := value.(type) {
		case map[string]any:
			hosts := inherited
			if match, ok := n["match"].([]any); ok {
				local := []string{}
				for _, raw := range match {
					m, _ := raw.(map[string]any)
					h, _ := m["host"].([]any)
					for _, rawHost := range h {
						host, ok := rawHost.(string)
						if ok && safeHost(host) {
							local = append(local, host)
						}
					}
				}
				if len(local) > 0 {
					hosts = local
				}
			}
			for _, host := range hosts {
				if sites[host] == nil {
					sites[host] = map[string]bool{}
				}
			}
			if n["handler"] == "reverse_proxy" {
				upstreams, _ := n["upstreams"].([]any)
				for _, raw := range upstreams {
					up, _ := raw.(map[string]any)
					dial, _ := up["dial"].(string)
					host, port, err := net.SplitHostPort(dial)
					_, ok := portNumber(port)
					if err == nil && ok && (net.ParseIP(host) != nil || safeHost(host) && !strings.Contains(host, "*")) {
						for _, matcher := range hosts {
							sites[matcher][dial] = true
						}
					}
				}
			}
			for key, child := range n {
				if key != "match" && key != "upstreams" {
					visit(child, hosts, depth+1)
				}
			}
		case []any:
			for _, child := range n {
				visit(child, inherited, depth+1)
			}
		}
	}
	for _, raw := range servers {
		server, _ := raw.(map[string]any)
		visit(server["routes"], []string{"*"}, 0)
	}
	for host, up := range sites {
		item := model.ExistingSite{HostMatcher: host, Upstreams: []string{}, ProjectIDs: []string{}}
		for dial := range up {
			item.Upstreams = append(item.Upstreams, dial)
		}
		sort.Strings(item.Upstreams)
		v.Sites = append(v.Sites, item)
	}
	sort.Slice(v.Sites, func(i, j int) bool { return v.Sites[i].HostMatcher < v.Sites[j].HostMatcher })
	return true
}

func safeHost(host string) bool {
	return len(host) > 0 && len(host) <= 253 && regexp.MustCompile(`^[a-zA-Z0-9.*:_-]+$`).MatchString(host)
}
