// dockyardctl is a backend/operator client. Never distribute its credentials to browsers.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	base := flag.String("url", "https://127.0.0.1:9123", "agent HTTPS origin")
	server := flag.String("server", "", "VPS server ID")
	keyID := flag.String("key-id", "", "HMAC key ID")
	keyFile := flag.String("key-file", "", "base64 HMAC key file")
	certFile := flag.String("cert", "", "client certificate PEM")
	privateKey := flag.String("key", "", "client TLS key PEM")
	caFile := flag.String("ca", "", "server CA PEM")
	method := flag.String("method", "GET", "HTTP method")
	target := flag.String("path", "/v1/projects", "exact path/query")
	scopes := flag.String("scopes", "deploy.read", "space separated signed scopes")
	actor := flag.String("actor", "operator", "attributable actor ID")
	idempotency := flag.String("idempotency", "", "stable key for retrying a mutation")
	requestID := flag.String("request-id", "", "stable request ID for retrying a mutation")
	bodyFile := flag.String("body", "", "JSON body file; never include secrets in arguments")
	environment := flag.String("environment", "", "development, staging or production; otherwise prompt when omitted from a create/deploy body")
	flag.Parse()
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Path != "" || u.Fragment != "" || u.Host == "" {
		return errors.New("invalid HTTPS origin")
	}
	if *server == "" || *keyID == "" {
		return errors.New("server and key-id are required")
	}
	if *method != "GET" && (*idempotency == "" || *requestID == "") {
		return errors.New("mutations require explicit idempotency and request-id; reuse both for retries")
	}
	if *idempotency == "" {
		*idempotency = state.NewID("read-")
	}
	if *requestID == "" {
		*requestID = state.NewID("req-")
	}
	var body []byte
	if *bodyFile != "" {
		body, err = os.ReadFile(*bodyFile)
		if err != nil {
			return err
		}
		if len(body) > 128<<10 {
			return errors.New("body exceeds limit")
		}
	}
	body, err = prepareDeploymentBody(*method, *target, body, *environment, os.Stdin, os.Stderr)
	if err != nil {
		return err
	}
	secret, err := auth.ReadSecret(*keyFile)
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(*certFile, *privateKey)
	if err != nil {
		return errors.New("invalid client TLS credentials")
	}
	ca, err := os.ReadFile(*caFile)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return errors.New("invalid server CA")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, *method, *base+*target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.RequestURI = r.URL.RequestURI()
	if !auth.TargetOK(r) {
		return errors.New("invalid exact path/query")
	}
	r.RequestURI = ""
	ss := strings.Fields(*scopes)
	sort.Strings(ss)
	r.Header.Set("X-Deploy-Key-ID", *keyID)
	r.Header.Set("X-Deploy-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	r.Header.Set("X-Deploy-Scopes", strings.Join(ss, " "))
	r.Header.Set("Idempotency-Key", *idempotency)
	r.Header.Set("X-Actor-ID", *actor)
	r.Header.Set("X-Request-ID", *requestID)
	r.Header.Set("X-Deploy-Signature", auth.Sign(secret, *server, r, body))
	if len(body) > 0 {
		r.Header.Set("Content-Type", "application/json")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(r)
	if err != nil {
		return errors.New("agent request failed")
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, io.LimitReader(resp.Body, 3<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("agent returned HTTP %d", resp.StatusCode)
	}
	return nil
}
