package runtime

// Conversion only imports plain local reverse-proxy site blocks. Keeping the
// grammar deliberately small prevents silently dropping authentication, headers,
// matchers, or routing rules. The complete source is journaled before any write.
import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
)

var plainSite = regexp.MustCompile(`(?m)^([a-zA-Z0-9., \t-]+)\s*\{\s*reverse_proxy\s+((?:localhost|127\.0\.0\.1):[0-9]+)\s*\}`)

func (c Caddy) PlanRouteImport(ctx context.Context, p model.Project, upstream string) ([]model.RouteEdit, error) {
	if _, err := c.coherent(ctx); err != nil {
		return nil, err
	}
	// Only the configured operator Caddyfile is editable. Imports with custom
	// routing must be reviewed manually, rather than guessing another file.
	b, err := adoptionRead(filepath.Dir(c.Config.Caddyfile), c.Config.Caddyfile, 2<<20)
	if err != nil {
		return nil, err
	}
	next, err := extractSites(string(b), p.Domains, upstream)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(c.Config.Caddyfile)
	if err != nil {
		return nil, err
	}
	// Atomic replacement needs write/search access to both parent directories.
	// Check without modifying the reviewed files. In particular, access returns
	// EROFS inside a read-only systemd mount even when the process runs as root.
	// This is only a preflight; actual writes and recovery checks remain required.
	const writeAndSearch = 2 | 1 // POSIX W_OK | X_OK
	for _, dir := range []string{filepath.Dir(c.Config.Caddyfile), c.Config.CaddySites} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() || syscall.Access(dir, writeAndSearch) != nil {
			return nil, model.Fail("CADDY_CONFIG_WRITE_REQUIRED")
		}
	}
	return []model.RouteEdit{{Mode: uint32(st.Mode().Perm()), Path: c.Config.Caddyfile, Before: string(b), After: next}}, nil
}
func extractSites(source string, domains []string, upstream string) (string, error) {
	wanted := map[string]bool{}
	for _, d := range domains {
		wanted[d] = true
	}
	found := map[string]bool{}
	result := source
	matches := plainSite.FindAllStringSubmatchIndex(source, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		names := strings.Fields(strings.ReplaceAll(source[m[2]:m[3]], ",", " "))
		relevant := false
		for _, d := range names {
			if wanted[d] {
				relevant = true
			}
		}
		if !relevant {
			continue
		}
		// The block must be top-level; reject nested directives or quoted braces.
		prefix := source[:m[0]]
		depth := 0
		inComment := false
		quote := false
		for _, r := range prefix {
			if r == '\n' {
				inComment = false
			}
			if inComment {
				continue
			}
			if r == '#' && !quote {
				inComment = true
				continue
			}
			if r == '"' || r == '`' {
				quote = !quote
			}
			if !quote {
				if r == '{' {
					depth++
				}
				if r == '}' {
					depth--
				}
			}
		}
		if depth != 0 || quote {
			return "", model.Fail("BLUE_GREEN_ROUTE_REVIEW_REQUIRED")
		}
		dial := source[m[4]:m[5]]
		if strings.Replace(dial, "localhost:", "127.0.0.1:", 1) != upstream {
			return "", model.Fail("BLUE_GREEN_ROUTE_TARGET_MISMATCH")
		}
		for _, d := range names {
			if !wanted[d] || found[d] {
				return "", model.Fail("BLUE_GREEN_ROUTE_REVIEW_REQUIRED")
			}
			found[d] = true
		}
		result = result[:m[0]] + result[m[1]:]
	}
	if len(found) != len(wanted) || len(found) == 0 {
		return "", model.Fail("BLUE_GREEN_ROUTE_REVIEW_REQUIRED")
	}
	return result, nil
}
func (c Caddy) VerifyRouteImport(edits []model.RouteEdit) error {
	for _, edit := range edits {
		if edit.Path != c.Config.Caddyfile {
			return model.Fail("BLUE_GREEN_ROUTE_REVIEW_REQUIRED")
		}
		b, err := adoptionRead(filepath.Dir(edit.Path), edit.Path, 2<<20)
		if err != nil || string(b) != edit.Before {
			return model.Fail("BLUE_GREEN_REVIEW_CHANGED")
		}
	}
	return nil
}
func (c Caddy) ImportRoutes(ctx context.Context, edits []model.RouteEdit, p model.Project) error {
	if err := c.VerifyRouteImport(edits); err != nil {
		return err
	}
	previous, err := c.coherent(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(c.Config.CaddySites, p.ID+".caddy")
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		return model.Fail("BLUE_GREEN_ROUTE_REVIEW_REQUIRED")
	}
	restore := func() error {
		for _, edit := range edits {
			if e := secure.Atomic(edit.Path, []byte(edit.Before), os.FileMode(edit.Mode)); e != nil {
				return e
			}
		}
		if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
			return e
		}
		return nil
	}
	for _, edit := range edits {
		if err = secure.Atomic(edit.Path, []byte(edit.After), os.FileMode(edit.Mode)); err != nil {
			return model.Uncertain("CADDY_FILE_WRITE_FAILED")
		}
	}
	if err = secure.Atomic(path, Snippet(p, importSlot(p)), 0644); err != nil {
		return model.Uncertain("CADDY_FILE_WRITE_FAILED")
	}
	expected, err := c.adapted(ctx)
	if err == nil {
		_, err = c.Runner.Run(ctx, c.Config.CaddyBinary, filepath.Dir(c.Config.Caddyfile), []string{"validate", "--config", c.Config.Caddyfile, "--adapter", "caddyfile"})
	}
	// Verify that only the reviewed upstreams changed in the adapted config.
	if err == nil {
		var a, b any
		x, _ := json.Marshal(previous)
		y, _ := json.Marshal(expected)
		json.Unmarshal(x, &a)
		json.Unmarshal(y, &b)
		if !routeEquivalent(a, b, p.Domains) {
			err = model.Fail("BLUE_GREEN_ROUTE_REVIEW_REQUIRED")
		}
	}
	if err != nil {
		if restore() != nil {
			return model.Uncertain("CADDY_RESTORE_FAILED")
		}
		return err
	}
	_, reloadErr := c.Runner.Run(ctx, c.Config.CaddyBinary, filepath.Dir(c.Config.Caddyfile), []string{"reload", "--config", c.Config.Caddyfile, "--adapter", "caddyfile", "--address", "unix/" + c.Config.CaddyAdminSocket})
	observe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	live, err := c.live(observe)
	if err == nil && reflect.DeepEqual(live, expected) {
		return nil
	}
	if err == nil && reflect.DeepEqual(live, previous) && reloadErr != nil {
		if restore() != nil {
			return model.Uncertain("CADDY_RESTORE_FAILED")
		}
		return model.Fail("CADDY_RELOAD_REJECTED")
	}
	return model.Uncertain("ROUTE_OUTCOME_UNKNOWN")
}

// Remove exact reviewed site routes from both configs before comparison. Each
// removed route must be a plain proxy with exactly those host matchers.
func routeEquivalent(a, b any, domains []string) bool {
	normalize := func(v any) (any, bool) {
		count := map[string]int{}
		var walk func(any) any
		walk = func(v any) any {
			switch n := v.(type) {
			case []any:
				out := []any{}
				for _, x := range n {
					m := object(x)
					match := array(m["match"])
					if len(match) == 1 && len(object(match[0])) == 1 {
						hs := array(object(match[0])["host"])
						owned := len(hs) > 0
						for _, h := range hs {
							ok := false
							for _, d := range domains {
								if h == d {
									ok = true
								}
							}
							owned = owned && ok
						}
						if owned { // Plain site handlers are nested by Caddy's adapter.
							data, _ := json.Marshal(m)
							if strings.Contains(string(data), `"handler":"reverse_proxy"`) {
								for _, h := range hs {
									count[h.(string)]++
								}
								continue
							}
						}
					}
					out = append(out, walk(x))
				}
				return out
			case map[string]any:
				for k, x := range n {
					n[k] = walk(x)
				}
				return n
			}
			return v
		}
		result := walk(v)
		for _, d := range domains {
			if count[d] != 1 {
				return nil, false
			}
		}
		return result, true
	}
	x, ok := normalize(a)
	y, ok2 := normalize(b)
	return ok && ok2 && reflect.DeepEqual(x, y)
}

func (c Caddy) VerifyOriginalRoutes(ctx context.Context, edits []model.RouteEdit) error {
	if err := c.VerifyRouteImport(edits); err != nil {
		return err
	}
	_, err := c.coherent(ctx)
	return err
}

// Restore only the exact files this conversion wrote. Concurrent operator edits
// are never overwritten. Live state is observed after reload, including timeout.
func (c Caddy) RestoreImportedRoutes(ctx context.Context, edits []model.RouteEdit, p model.Project) error {
	for _, edit := range edits {
		b, err := os.ReadFile(edit.Path)
		if err != nil || string(b) != edit.After {
			return model.Uncertain("BLUE_GREEN_ROUTE_CHANGED")
		}
	}
	path := filepath.Join(c.Config.CaddySites, p.ID+".caddy")
	b, err := os.ReadFile(path)
	if err != nil || string(b) != string(Snippet(p, importSlot(p))) {
		return model.Uncertain("STATE_DIVERGED")
	}
	if _, err = c.coherent(ctx); err != nil {
		return err
	}
	for _, edit := range edits {
		if err = secure.Atomic(edit.Path, []byte(edit.Before), os.FileMode(edit.Mode)); err != nil {
			return model.Uncertain("CADDY_RESTORE_FAILED")
		}
	}
	if err = os.Remove(path); err != nil {
		return model.Uncertain("CADDY_RESTORE_FAILED")
	}
	expected, err := c.adapted(ctx)
	if err != nil {
		return model.Uncertain("CADDY_RESTORE_FAILED")
	}
	_, _ = c.Runner.Run(ctx, c.Config.CaddyBinary, filepath.Dir(c.Config.Caddyfile), []string{"reload", "--config", c.Config.Caddyfile, "--adapter", "caddyfile", "--address", "unix/" + c.Config.CaddyAdminSocket})
	observe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	live, err := c.live(observe)
	if err == nil && reflect.DeepEqual(live, expected) {
		return nil
	}
	return model.Uncertain("ROUTE_OUTCOME_UNKNOWN")
}

func importSlot(p model.Project) string {
	if p.ServiceMode {
		return p.Active
	}
	return "green"
}
