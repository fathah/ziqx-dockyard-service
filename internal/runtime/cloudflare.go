package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type DNS struct {
	Config   config.Config
	Client   *http.Client
	endpoint string
}
type record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
	Comment string `json:"comment"`
}

func NewDNS(c config.Config) *DNS {
	return &DNS{Config: c, endpoint: "https://api.cloudflare.com/client/v4", Client: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (d *DNS) request(ctx context.Context, method, path string, body any, result any) error {
	b, e := os.ReadFile(d.Config.Cloudflare.TokenFile)
	if e != nil {
		return model.Fail("DNS_UNAVAILABLE")
	}
	token := strings.TrimSpace(string(b))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return model.Fail("DNS_UNAVAILABLE")
	}
	var data []byte
	if body != nil {
		data, e = json.Marshal(body)
		if e != nil {
			return model.Fail("DNS_UNAVAILABLE")
		}
	}
	r, e := http.NewRequestWithContext(ctx, method, d.endpoint+path, bytes.NewReader(data))
	if e != nil {
		return model.Fail("DNS_UNAVAILABLE")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	resp, e := d.Client.Do(r)
	if e != nil {
		if method != "GET" {
			return model.Uncertain("DNS_OUTCOME_UNKNOWN")
		}
		return model.Fail("DNS_UNAVAILABLE")
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return model.Uncertain("DNS_OUTCOME_UNKNOWN")
	}
	var envelope struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return model.Uncertain("DNS_OUTCOME_UNKNOWN")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.Success {
		return model.Fail("DNS_REQUEST_REJECTED")
	}
	if json.Unmarshal(envelope.Result, result) != nil {
		return model.Uncertain("DNS_OUTCOME_UNKNOWN")
	}
	return nil
}
func (d *DNS) Create(ctx context.Context, project, host string) (string, error) {
	cf := d.Config.Cloudflare
	if cf == nil {
		return "", model.Fail("DNS_NOT_CONFIGURED")
	}
	zone := ""
	longest := 0
	for domain, id := range cf.Zones {
		if strings.HasSuffix(host, "."+domain) && len(domain) > longest {
			longest = len(domain)
			zone = id
		}
	}
	if zone == "" || !d.Config.DomainAllowed(host) {
		return "", model.Fail("HOSTNAME_NOT_ALLOWED")
	}
	path := "/zones/" + zone + "/dns_records"
	var existing []record
	if e := d.request(ctx, "GET", path+"?name="+url.QueryEscape(host)+"&per_page=100", nil, &existing); e != nil {
		return "", e
	}
	typeName := "AAAA"
	if net.ParseIP(cf.OriginIP).To4() != nil {
		typeName = "A"
	}
	marker := "dockyard:" + d.Config.ServerID + ":" + project
	if len(existing) > 0 {
		if len(existing) == 1 {
			r := existing[0]
			if r.Name == host && r.Type == typeName && r.Content == cf.OriginIP && r.Proxied == cf.Proxied && r.Comment == marker {
				return r.ID, nil
			}
		}
		return "", model.Fail("DNS_RECORD_NOT_MANAGED")
	}
	var created record
	e := d.request(ctx, "POST", path, record{Type: typeName, Name: host, Content: cf.OriginIP, Proxied: cf.Proxied, TTL: 1, Comment: marker}, &created)
	if e != nil {
		return "", e
	}
	if created.ID == "" {
		return "", model.Uncertain("DNS_OUTCOME_UNKNOWN")
	}
	return created.ID, nil
}
