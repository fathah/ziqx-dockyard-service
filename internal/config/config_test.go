package config

import (
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"os"
	"strings"
	"testing"
)

func TestRootPolicyBoundaries(t *testing.T) {
	b, err := os.ReadFile("../../deploy/config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.ReplaceAll(strings.ReplaceAll(string(b), "REPLACE_WITH_CLIENT_CERTIFICATE_DER_SHA256_LOWERCASE_HEX", strings.Repeat("a", 64)), "REPLACE_WITH_32_HEX_ZONE_ID", strings.Repeat("a", 32)))
	var c Config
	if err = secure.Decode(b, &c); err != nil {
		t.Fatal(err)
	}
	if err = c.Validate(); err != nil {
		t.Fatal("resolved sample invalid", err)
	}
	for name, mutate := range map[string]func(*Config){
		"wildcard listener":     func(c *Config) { c.Listen = "0.0.0.0:9123" },
		"public listener":       func(c *Config) { c.Listen = "203.0.113.10:9123" },
		"overlapping roots":     func(c *Config) { c.StateDir = "/docker/state" },
		"unsafe binding path":   func(c *Config) { c.ProjectsRoot = "/docker/${INJECT}" },
		"empty hostname policy": func(c *Config) { c.AllowedDomains = nil },
		"unbounded queue":       func(c *Config) { c.MaxQueuedJobs = 1000000 },
	} {
		t.Run(name, func(t *testing.T) {
			copy := c
			mutate(&copy)
			if copy.Validate() == nil {
				t.Fatal("invalid root policy accepted")
			}
		})
	}
}

func TestDomainPolicy(t *testing.T) {
	c := Config{AllowedDomains: []string{"example.com"}, ReservedDomains: []string{"agent.example.com"}}
	for _, d := range []string{"app.example.com", "example.com", "xn--caf-dma.example.com"} {
		if !c.DomainAllowed(d) {
			t.Fatal(d)
		}
	}
	for _, d := range []string{"evil-example.com", "example.com.evil.org", "agent.example.com", "sub.agent.example.com", "EXAMPLE.com", "foo.example.com\nimport /etc/passwd", "*.example.com", "127.0.0.1", "foo..example.com", "-x.example.com"} {
		if c.DomainAllowed(d) {
			t.Fatalf("allowed %q", d)
		}
	}
}
func TestImageDigest(t *testing.T) {
	for _, v := range []string{"ghcr.io/org/app:latest", "ghcr.io/org/app@sha256:abc", "-option", "ghcr.io/org/app@sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if Digest.MatchString(v) {
			t.Fatal(v)
		}
	}
}
