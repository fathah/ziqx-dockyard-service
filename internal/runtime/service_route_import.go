package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

// Only these metadata leave the server. Site bodies can contain credentials.
type ExistingCaddyRoute struct {
	Domains          []string `json:"domains"`
	PreviousUpstream string   `json:"previous_upstream,omitempty"`
	RemainingDomains []string `json:"remaining_domains"`
	RulesPreserved   bool     `json:"rules_preserved"`
}

type ServiceRouteImport struct {
	Edits     []model.RouteEdit
	Templates []model.CaddyRoute
	Existing  []ExistingCaddyRoute
}

type caddyToken struct {
	value      string
	start, end int
	line       int
	quoted     bool
}

func (t caddyToken) brace(value string) bool { return !t.quoted && t.value == value }

// Source offsets make moving a site lossless for the rest of the Caddyfile.
// Braces in quoted strings, comments and placeholders are not block braces.
func caddyTokens(source string) ([]caddyToken, error) {
	var out []caddyToken
	line := 0
	for i := 0; i < len(source); {
		if strings.ContainsRune(" \t\r\n", rune(source[i])) {
			if source[i] == '\n' {
				line++
			}
			i++
			continue
		}
		if source[i] == '#' {
			for i < len(source) && source[i] != '\n' {
				i++
			}
			continue
		}
		start, tokenLine := i, line
		value := ""
		quoted := false
		if source[i] == '"' || source[i] == '`' {
			quoted = true
			quote := source[i]
			i++
			for i < len(source) && source[i] != quote {
				if source[i] == '\n' {
					line++
				}
				if quote == '"' && source[i] == '\\' {
					i++
					if i == len(source) {
						return nil, model.Fail("CADDY_ROUTE_PARSE_FAILED")
					}
				}
				i++
			}
			if i == len(source) {
				return nil, model.Fail("CADDY_ROUTE_PARSE_FAILED")
			}
			i++
			if quote == '"' {
				value = strings.ReplaceAll(source[start+1:i-1], `\"`, `"`)
			} else {
				value = source[start+1 : i-1]
			}
		} else {
			for i < len(source) && !strings.ContainsRune(" \t\r\n", rune(source[i])) {
				i++
			}
			value = source[start:i]
			if strings.HasPrefix(value, "<<") && len(value) > 2 {
				marker := value[2:]
				found := false
				for i < len(source) {
					if source[i] == '\n' {
						line++
						i++
					}
					for i < len(source) && (source[i] == ' ' || source[i] == '\t') {
						i++
					}
					if strings.HasPrefix(source[i:], marker) {
						end := i + len(marker)
						if end == len(source) || strings.ContainsRune(" \t\r\n", rune(source[end])) {
							i = end
							found = true
							break
						}
					}
					for i < len(source) && source[i] != '\n' {
						i++
					}
				}
				if !found {
					return nil, model.Fail("CADDY_ROUTE_PARSE_FAILED")
				}
				quoted = true
			}
		}
		out = append(out, caddyToken{value: value, start: start, end: i, line: tokenLine, quoted: quoted})
	}
	return out, nil
}

type caddySite struct {
	header      []caddyToken
	open, close caddyToken
}

func caddySites(source string) ([]caddySite, error) {
	tokens, err := caddyTokens(source)
	if err != nil {
		return nil, err
	}
	var sites []caddySite
	for i := 0; i < len(tokens); {
		start := i
		// import and snippet invocation at top level consume one logical line.
		if tokens[i].value == "import" {
			line := tokens[i].line
			for i < len(tokens) && tokens[i].line == line {
				i++
			}
			continue
		}
		// Caddy permits a single site without outer braces. Its first line is
		// the address group; the remaining file is that site's body.
		lineEnd := start
		addresses := true
		for lineEnd < len(tokens) && tokens[lineEnd].line == tokens[start].line {
			for _, address := range strings.Split(tokens[lineEnd].value, ",") {
				if address != "" && siteHostname(address) == "" {
					addresses = false
				}
			}
			lineEnd++
		}
		if addresses && lineEnd > start && !strings.HasSuffix(tokens[lineEnd-1].value, ",") {
			end := tokens[lineEnd-1].end
			sites = append(sites, caddySite{header: tokens[start:lineEnd], open: caddyToken{start: end, end: end}, close: caddyToken{start: len(source), end: len(source)}})
			return sites, nil
		}
		for i < len(tokens) && !tokens[i].brace("{") {
			if tokens[i].brace("}") {
				return nil, model.Fail("CADDY_ROUTE_PARSE_FAILED")
			}
			i++
		}
		if i == len(tokens) {
			return nil, model.Fail("CADDY_ROUTE_PARSE_FAILED")
		}
		open := i
		depth := 1
		i++
		for i < len(tokens) && depth > 0 {
			if tokens[i].brace("{") {
				depth++
			}
			if tokens[i].brace("}") {
				depth--
			}
			i++
		}
		if depth != 0 {
			return nil, model.Fail("CADDY_ROUTE_PARSE_FAILED")
		}
		if open > start && !strings.HasPrefix(tokens[start].value, "(") && !strings.HasPrefix(tokens[start].value, "&(") {
			sites = append(sites, caddySite{header: tokens[start:open], open: tokens[open], close: tokens[i-1]})
		}
	}
	return sites, nil
}

func siteHostname(address string) string {
	address = strings.ToLower(strings.TrimSuffix(address, ","))
	if strings.Contains(address, "://") {
		u, err := url.Parse(address)
		if err != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return ""
		}
		address = u.Hostname()
	} else if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	if !config.Hostname(address) {
		return ""
	}
	return address
}

func importSiteBody(body string) (string, string, error) {
	if strings.Contains(body, model.CaddyUpstream) {
		return "", "", model.Fail("CADDY_ROUTE_RULES_REVIEW_REQUIRED")
	}
	tokens, err := caddyTokens(body)
	if err != nil {
		return "", "", err
	}
	var dial *caddyToken
	proxies := 0
	for i, token := range tokens {
		if token.value != "reverse_proxy" || i > 0 && tokens[i-1].line == token.line && !tokens[i-1].brace("{") {
			continue
		}
		proxies++
		j := i + 1
		if j < len(tokens) && tokens[j].line == token.line && strings.HasPrefix(tokens[j].value, "@") {
			j++
		}
		if j < len(tokens) && tokens[j].line == token.line && strings.HasPrefix(tokens[j].value, "/") {
			j++
		}
		if j >= len(tokens) || tokens[j].line != token.line {
			continue
		}
		host, port, err := net.SplitHostPort(tokens[j].value)
		n, parseErr := strconv.Atoi(port)
		if err != nil || parseErr != nil || n < 1 || n > 65535 || host != "localhost" && host != "127.0.0.1" {
			continue
		}
		if j+1 < len(tokens) && tokens[j+1].line == token.line && !tokens[j+1].brace("{") && !tokens[j+1].brace("}") {
			continue
		}
		dial = &tokens[j]
	}
	if proxies != 1 || dial == nil {
		return "", "", model.Fail("CADDY_ROUTE_RULES_REVIEW_REQUIRED")
	}
	return body[:dial.start] + model.CaddyUpstream + body[dial.end:], dial.value, nil
}

func retainImportedPaths(body, source, directory string) (string, error) {
	root, err := caddyTokens(source)
	if err != nil {
		return "", err
	}
	snippets := map[string]bool{}
	depth := 0
	for i, token := range root {
		if depth == 0 && strings.HasPrefix(token.value, "(") && strings.HasSuffix(token.value, ")") && i+1 < len(root) && root[i+1].brace("{") {
			snippets[strings.TrimSuffix(strings.TrimPrefix(token.value, "("), ")")] = true
		}
		if token.brace("{") {
			depth++
		}
		if token.brace("}") {
			depth--
		}
	}
	tokens, err := caddyTokens(body)
	if err != nil {
		return "", err
	}
	for i := len(tokens) - 2; i >= 0; i-- {
		token, arg := tokens[i], tokens[i+1]
		if token.quoted || token.value != "import" || token.line != arg.line || i > 0 && tokens[i-1].line == token.line && !tokens[i-1].brace("{") {
			continue
		}
		if snippets[arg.value] || filepath.IsAbs(arg.value) {
			continue
		}
		// A plain name may be a snippet defined by another root import. Files
		// and globs must keep their original base directory after the site moves.
		path := filepath.Join(directory, arg.value)
		_, fileErr := os.Lstat(path)
		if !strings.ContainsAny(arg.value, "/.*?") && os.IsNotExist(fileErr) {
			continue
		}
		if strings.ContainsAny(arg.value, "{}\r\n") {
			return "", model.Fail("CADDY_ROUTE_RULES_REVIEW_REQUIRED")
		}
		quoted, _ := json.Marshal(path)
		body = body[:arg.start] + string(quoted) + body[arg.end:]
	}
	return body, nil
}

func planServiceSiteMove(source string, domains, owned []string, replaceRules bool) (string, []model.CaddyRoute, []ExistingCaddyRoute, error) {
	sites, err := caddySites(source)
	if err != nil {
		return "", nil, nil, err
	}
	wanted := map[string]bool{}
	for _, name := range domains {
		wanted[name] = true
	}
	for _, name := range owned {
		delete(wanted, name)
	}
	found := map[string]bool{}
	var templates []model.CaddyRoute
	var existing []ExistingCaddyRoute
	result := source
	for i := len(sites) - 1; i >= 0; i-- {
		site := sites[i]
		var moved, remaining, remainingAddresses, addresses []string
		for _, token := range site.header {
			for _, raw := range strings.Split(token.value, ",") {
				if raw == "" {
					continue
				}
				host := siteHostname(raw)
				if wanted[host] {
					if found[host] {
						return "", nil, nil, model.Fail("CADDY_ROUTE_AMBIGUOUS")
					}
					found[host] = true
					moved, addresses = append(moved, host), append(addresses, raw)
				} else {
					remaining, remainingAddresses = append(remaining, raw), append(remainingAddresses, raw)
				}
			}
		}
		if len(moved) == 0 {
			continue
		}
		body := source[site.open.end:site.close.start]
		template, old, bodyErr := importSiteBody(body)
		if bodyErr != nil && !replaceRules {
			return "", nil, nil, bodyErr
		}
		if !replaceRules {
			for j, name := range moved {
				templates = append(templates, model.CaddyRoute{Domain: name, Address: addresses[j], Body: template})
			}
		}
		existing = append(existing, ExistingCaddyRoute{Domains: moved, PreviousUpstream: old, RemainingDomains: remaining, RulesPreserved: !replaceRules})
		replacement := ""
		if len(remainingAddresses) > 0 {
			replacement = strings.Join(remainingAddresses, ", ") + " {" + body + "}"
		}
		result = result[:site.header[0].start] + replacement + result[site.close.end:]
	}
	return result, templates, existing, nil
}

// Review before writes. Ownership, the exact source, and the generated snippet
// are rechecked at execution; neither unrelated sites nor sibling routes move.
func (c Caddy) PlanServiceRouteImport(ctx context.Context, p model.Project, domains []string, replaceRules bool) (ServiceRouteImport, error) {
	var plan ServiceRouteImport
	baseline, err := c.coherent(ctx)
	if err != nil {
		return plan, err
	}
	b, err := adoptionRead(filepath.Dir(c.Config.Caddyfile), c.Config.Caddyfile, 2<<20)
	if err != nil {
		return plan, err
	}
	after, templates, existing, err := planServiceSiteMove(string(b), domains, p.Domains, replaceRules)
	if err != nil {
		return plan, err
	}
	plan.Templates, plan.Existing = templates, existing
	if len(existing) == 0 {
		return plan, nil
	}
	for i := range plan.Templates {
		body, err := retainImportedPaths(plan.Templates[i].Body, string(b), filepath.Dir(c.Config.Caddyfile))
		if err != nil {
			return plan, err
		}
		plan.Templates[i].Body = body
	}
	transferred := []string{}
	for _, route := range existing {
		transferred = append(transferred, route.Domains...)
	}
	if err := c.DomainsAvailable(ctx, domains, append(append([]string{}, p.Domains...), transferred...)); err != nil {
		return plan, err
	}
	st, err := os.Stat(c.Config.Caddyfile)
	if err != nil {
		return plan, err
	}
	for _, dir := range []string{filepath.Dir(c.Config.Caddyfile), c.Config.CaddySites} {
		if err := syscall.Access(dir, 2|1); err != nil {
			return plan, model.Fail("CADDY_CONFIG_WRITE_REQUIRED")
		}
	}
	var before *string
	path := filepath.Join(c.Config.CaddySites, p.ID+".caddy")
	if _, err := os.Lstat(path); err == nil {
		data, err := adoptionRead(c.Config.CaddySites, path, 2<<20)
		if err != nil {
			return plan, err
		}
		if string(data) != string(Snippet(p, routeSlot(p))) {
			return plan, model.Uncertain("STATE_DIVERGED")
		}
		text := string(data)
		before = &text
	} else if !os.IsNotExist(err) {
		return plan, err
	}
	data, _ := json.Marshal(baseline)
	hash := sha256.Sum256(data)
	plan.Edits = []model.RouteEdit{{Path: c.Config.Caddyfile, Mode: uint32(st.Mode().Perm()), Before: string(b), After: after, Domains: transferred, ManagedBefore: before, LiveSHA256: hex.EncodeToString(hash[:])}}
	return plan, nil
}
