package runtime

import (
	"context"
	"encoding/json"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

type Caddy struct {
	Config config.Config
	Runner process.Runner
	client *http.Client
}

func (c Caddy) live(ctx context.Context) (any, error) {
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", c.Config.CaddyAdminSocket)
	}}
	defer tr.CloseIdleConnections()
	h := &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if c.client != nil {
		h = c.client
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost/config/", nil)
	resp, e := h.Do(req)
	if e != nil {
		return nil, model.Uncertain("CADDY_UNAVAILABLE")
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if e != nil || len(b) > 2<<20 || resp.StatusCode != 200 {
		return nil, model.Uncertain("CADDY_UNAVAILABLE")
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil, model.Uncertain("CADDY_UNAVAILABLE")
	}
	return v, nil
}
func (c Caddy) adapted(ctx context.Context) (any, error) {
	r, e := c.Runner.Run(ctx, c.Config.CaddyBinary, filepath.Dir(c.Config.Caddyfile), []string{"adapt", "--config", c.Config.Caddyfile, "--adapter", "caddyfile"})
	var v any
	if e != nil || r.Truncated || json.Unmarshal(r.Output, &v) != nil {
		return nil, model.Fail("CADDY_CONFIG_INVALID")
	}
	return v, nil
}
func (c Caddy) coherent(ctx context.Context) (any, error) {
	disk, e := c.adapted(ctx)
	if e != nil {
		return nil, e
	}
	live, e := c.live(ctx)
	if e != nil {
		return nil, e
	}
	if !reflect.DeepEqual(disk, live) {
		return nil, model.Uncertain("STATE_DIVERGED")
	}
	return live, nil
}
func (c Caddy) Ensure(ctx context.Context, p model.Project) error {
	b, e := os.ReadFile(filepath.Join(c.Config.CaddySites, p.ID+".caddy"))
	if e != nil || string(b) != string(Snippet(p, routeSlot(p))) {
		return model.Uncertain("STATE_DIVERGED")
	}
	_, e = c.coherent(ctx)
	return e
}
func routeSlot(p model.Project) string {
	if p.State == "running" {
		return p.Active
	}
	return ""
}

func matches(pattern, host string) bool {
	if pattern == host {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:]
		return strings.HasSuffix(host, suffix) && !strings.Contains(strings.TrimSuffix(host, suffix), ".")
	}
	return pattern == "*"
}

// Observe accepts only an exact generated snippet and matching live configuration.
func (c Caddy) Observe(ctx context.Context, p model.Project) (string, error) {
	if _, err := c.coherent(ctx); err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(c.Config.CaddySites, p.ID+".caddy"))
	if err != nil {
		return "", model.Uncertain("STATE_DIVERGED")
	}
	for _, slot := range []string{"", "blue", "green"} {
		if slot == "green" && !p.ZeroDowntime {
			continue
		}
		if string(b) == string(Snippet(p, slot)) {
			return slot, nil
		}
	}
	return "", model.Uncertain("STATE_DIVERGED")
}
func hosts(v any, out *[]string) {
	switch n := v.(type) {
	case map[string]any:
		for k, x := range n {
			if k == "host" {
				if a, ok := x.([]any); ok {
					for _, s := range a {
						if h, ok := s.(string); ok {
							*out = append(*out, h)
						}
					}
				}
			}
			hosts(x, out)
		}
	case []any:
		for _, x := range n {
			hosts(x, out)
		}
	}
}
func (c Caddy) DomainsAvailable(ctx context.Context, domains, owned []string) error {
	live, e := c.coherent(ctx)
	if e != nil {
		return e
	}
	var patterns []string
	hosts(live, &patterns)
	for _, host := range domains {
		for _, pattern := range patterns {
			if !matches(pattern, host) {
				continue
			}
			own := false
			for _, d := range owned {
				if d == pattern {
					own = true
				}
			}
			if !own {
				return model.Fail("SITE_NOT_MANAGED")
			}
		}
	}
	return nil
}

// Set modifies only a generated snippet. The shared worker serializes all reloads.
// A reload result is classified against live JSON; a timeout never implies rollback.
func (c Caddy) Set(ctx context.Context, p model.Project, slot string) error {
	previous, e := c.coherent(ctx)
	if e != nil {
		return e
	}
	path := filepath.Join(c.Config.CaddySites, p.ID+".caddy")
	old, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return model.Fail("CADDY_CONFIG_INVALID")
	}
	if e = secure.Atomic(path, Snippet(p, slot), 0644); e != nil {
		return model.Uncertain("CADDY_FILE_WRITE_FAILED")
	}
	restore := func() error {
		if readErr == nil {
			return secure.Atomic(path, old, 0644)
		}
		if e := os.Remove(path); e != nil {
			return e
		}
		d, e := os.Open(c.Config.CaddySites)
		if e != nil {
			return e
		}
		defer d.Close()
		return d.Sync()
	}
	expected, e := c.adapted(ctx)
	if e == nil {
		_, e = c.Runner.Run(ctx, c.Config.CaddyBinary, filepath.Dir(c.Config.Caddyfile), []string{"validate", "--config", c.Config.Caddyfile, "--adapter", "caddyfile"})
	}
	if e != nil {
		if restore() != nil {
			return model.Uncertain("CADDY_RESTORE_FAILED")
		}
		return model.Fail("CADDY_CONFIG_INVALID")
	}
	// The include must actually incorporate the generated domain(s).
	var patterns []string
	hosts(expected, &patterns)
	for _, domain := range p.Domains {
		found := false
		for _, h := range patterns {
			if h == domain {
				found = true
			}
		}
		if !found {
			if restore() != nil {
				return model.Uncertain("CADDY_RESTORE_FAILED")
			}
			return model.Fail("CADDY_INCLUDE_MISSING")
		}
	}
	_, reloadErr := c.Runner.Run(ctx, c.Config.CaddyBinary, filepath.Dir(c.Config.Caddyfile), []string{"reload", "--config", c.Config.Caddyfile, "--adapter", "caddyfile", "--address", "unix/" + c.Config.CaddyAdminSocket})
	// Use an independent bounded observation even if shutdown cancelled the command.
	observe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	live, e := c.live(observe)
	if e == nil && reflect.DeepEqual(live, expected) {
		return nil
	}
	if e == nil && reflect.DeepEqual(live, previous) && reloadErr != nil {
		if restore() != nil {
			return model.Uncertain("CADDY_RESTORE_FAILED")
		}
		return model.Fail("CADDY_RELOAD_REJECTED")
	}
	return model.Uncertain("ROUTE_OUTCOME_UNKNOWN")
}
