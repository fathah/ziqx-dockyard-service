package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type caddyRunner struct {
	path        string
	live        any
	mode        string
	unavailable bool
}

func (f *caddyRunner) config() any {
	b, _ := os.ReadFile(f.path)
	return map[string]any{"host": []any{"app.example.com"}, "snippet": string(b)}
}
func (f *caddyRunner) Run(ctx context.Context, binary, dir string, args []string) (process.Result, error) {
	switch args[0] {
	case "adapt":
		b, _ := json.Marshal(f.config())
		return process.Result{Output: b}, nil
	case "validate":
		if f.mode == "invalid" {
			return process.Result{}, errors.New("invalid")
		}
	case "reload":
		switch f.mode {
		case "reject":
			return process.Result{}, errors.New("rejected")
		case "unknown":
			f.unavailable = true
			return process.Result{}, errors.New("timeout")
		default:
			f.live = f.config()
			if f.mode == "applied-timeout" {
				return process.Result{}, errors.New("timeout")
			}
		}
	}
	return process.Result{}, nil
}
func caddyFixture(t *testing.T, mode string) (Caddy, model.Project, *caddyRunner) {
	t.Helper()
	dir := t.TempDir()
	p := model.Project{ID: "demo", Domains: []string{"app.example.com"}, BluePort: 3001, GreenPort: 3002, Active: "blue", State: "running"}
	path := filepath.Join(dir, "demo.caddy")
	os.WriteFile(path, Snippet(p, "blue"), 0644)
	f := &caddyRunner{path: path, mode: mode}
	f.live = f.config()
	c := Caddy{Config: config.Config{CaddySites: dir, Caddyfile: filepath.Join(dir, "Caddyfile"), CaddyBinary: "/usr/bin/caddy", CaddyAdminSocket: filepath.Join(dir, "socket")}, Runner: f, client: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if f.unavailable {
			return nil, errors.New("offline")
		}
		b, _ := json.Marshal(f.live)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: http.Header{}}, nil
	})}}
	return c, p, f
}
func TestCaddySwitchClassification(t *testing.T) {
	for _, mode := range []string{"success", "applied-timeout", "reject", "invalid", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			c, p, f := caddyFixture(t, mode)
			err := c.Set(context.Background(), p, "green")
			b, _ := os.ReadFile(f.path)
			switch mode {
			case "success", "applied-timeout":
				if err != nil || string(b) != string(Snippet(p, "green")) {
					t.Fatal("verified activation failed", err)
				}
			case "reject", "invalid":
				if err == nil || string(b) != string(Snippet(p, "blue")) {
					t.Fatal("failed to restore prior disk config", err)
				}
			case "unknown":
				var fault *model.Fault
				if !errors.As(err, &fault) || !fault.Recovery || string(b) != string(Snippet(p, "green")) {
					t.Fatal("ambiguous switch was destructively compensated", err)
				}
			}
		})
	}
}
func TestCaddyDivergenceBlocksSwitch(t *testing.T) {
	c, p, f := caddyFixture(t, "success")
	f.live = map[string]any{"changed": true}
	if c.Set(context.Background(), p, "green") == nil {
		t.Fatal("overwrote out-of-band config")
	}
	b, _ := os.ReadFile(f.path)
	if string(b) != string(Snippet(p, "blue")) {
		t.Fatal("disk modified")
	}
}

func TestDNSDoesNotOverwriteForeignRecord(t *testing.T) {
	c := config.Config{ServerID: "vps-01", AllowedDomains: []string{"example.com"}, Cloudflare: &config.Cloudflare{Zones: map[string]string{"example.com": "zone"}, OriginIP: "203.0.113.10", TokenFile: filepath.Join(t.TempDir(), "token")}}
	os.WriteFile(c.Cloudflare.TokenFile, []byte("secret-token"), 0600)
	writes := 0
	d := NewDNS(c)
	d.Client = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			writes++
		}
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Fatal("missing scoped token")
		}
		body := `{"success":true,"result":[{"id":"foreign","name":"app.example.com","type":"A","content":"203.0.113.20"}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	if _, e := d.Create(context.Background(), "demo", "app.example.com"); e == nil || writes != 0 {
		t.Fatal("foreign DNS overwritten")
	}
	if _, e := d.Create(context.Background(), "demo", "evil-example.com"); e == nil {
		t.Fatal("DNS escaped zone policy")
	}
}
func TestDNSRetryRecognizesExactOwnedRecord(t *testing.T) {
	c := config.Config{ServerID: "vps-01", AllowedDomains: []string{"example.com"}, Cloudflare: &config.Cloudflare{Zones: map[string]string{"example.com": "zone"}, OriginIP: "203.0.113.10", TokenFile: filepath.Join(t.TempDir(), "token")}}
	os.WriteFile(c.Cloudflare.TokenFile, []byte("secret-token"), 0600)
	d := NewDNS(c)
	d.Client = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("retry created a duplicate")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"success":true,"result":[{"id":"owned","name":"app.example.com","type":"A","content":"203.0.113.10","proxied":false,"comment":"dockyard:vps-01:demo"}]}`)), Header: http.Header{}}, nil
	})}
	if id, e := d.Create(context.Background(), "demo", "app.example.com"); e != nil || id != "owned" {
		t.Fatal(id, e)
	}
}
