// Disposable loopback test agent; never packaged with the desktop app.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"fmt"
	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func key() *ecdsa.PrivateKey {
	k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		panic(e)
	}
	return k
}
func cert(template, parent *x509.Certificate, k, issuer *ecdsa.PrivateKey) (string, []byte) {
	b, e := x509.CreateCertificate(rand.Reader, template, parent, &k.PublicKey, issuer)
	if e != nil {
		panic(e)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b})), b
}
func private(k *ecdsa.PrivateKey) string {
	b, e := x509.MarshalPKCS8PrivateKey(k)
	if e != nil {
		panic(e)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}))
}
func main() {
	now := time.Now()
	caKey := key()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caPEM, caDER := cert(ca, ca, caKey, caKey)
	ca, _ = x509.ParseCertificate(caDER)
	serverKey := key()
	serverPEM, serverDER := cert(&x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}, ca, serverKey, caKey)
	clientKey := key()
	clientPEM, clientDER := cert(&x509.Certificate{SerialNumber: big.NewInt(3), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}, ca, clientKey, caKey)
	wrongKey := key()
	wrongPEM, _ := cert(&x509.Certificate{SerialNumber: big.NewInt(4), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}, ca, wrongKey, caKey)
	otherKey := key()
	otherCA := &x509.Certificate{SerialNumber: big.NewInt(5), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	otherPEM, _ := cert(otherCA, otherCA, otherKey, otherKey)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(caPEM))
	serverCert, _ := tls.X509KeyPair([]byte(serverPEM), []byte(private(serverKey)))
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		panic(e)
	}
	secret := bytes.Repeat([]byte{42}, 32)
	fingerprint := sha256.Sum256(clientDER)
	var mu sync.Mutex
	accepted := map[string]bool{}
	requests := 0
	leaked := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(io.LimitReader(r.Body, 128<<10))
		peer := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
		r.RequestURI = r.URL.RequestURI()
		if peer != fingerprint || r.Header.Get("X-Deploy-Signature") != auth.Sign(secret, "vps-01", r, body) || r.Header.Get("X-Deploy-Key-ID") != "desktop-01" {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"code": "UNAUTHORIZED"})
			return
		}
		switch r.URL.Path {
		case "/v1/projects":
			if r.Header.Get("Idempotency-Key") == "read-scale" {
				projects := make([]model.Project, 100)
				for i := range projects {
					releases := make([]model.Release, 200)
					for j := range releases {
						releases[j] = model.Release{ID: fmt.Sprintf("rel-%03d-%03d", i, j), Image: "ghcr.io/ziqx/test@sha256:" + strings.Repeat("a", 64), Environment: "env-" + strings.Repeat("b", 64), Compose: "cmp-" + strings.Repeat("c", 64), Created: now}
					}
					projects[i] = model.Project{ID: fmt.Sprintf("app-%03d-production", i), AppID: fmt.Sprintf("app-%03d", i), Environment: model.Production, Template: "node", Domains: []string{fmt.Sprintf("app-%03d.example.com", i)}, ZeroDowntime: true, BluePort: 3000 + i*2, GreenPort: 3001 + i*2, State: "running", Active: "blue", Slots: map[string]model.Release{"blue": releases[199], "green": releases[198]}, Releases: releases}
				}
				json.NewEncoder(w).Encode(map[string]any{"projects": projects})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"projects": []any{}})
		case "/v1/projects/demo/restart":
			requests++
			if string(body) != "{}" || r.Header.Get("X-Deploy-Scopes") != "deploy.execute" {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"code": "BAD_BODY"})
				return
			}
			op := r.Header.Get("Idempotency-Key")
			if !accepted[op] {
				accepted[op] = true
				w.WriteHeader(500)
				json.NewEncoder(w).Encode(map[string]string{"code": "TEST_UNCERTAIN"})
				return
			}
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(map[string]any{"job_id": "job-test", "admissions": len(accepted), "requests": requests})
		case "/v1/redirect":
			w.Header().Set("Location", "/v1/leak")
			w.WriteHeader(302)
			json.NewEncoder(w).Encode(map[string]string{"code": "REDIRECT"})
		case "/v1/leak":
			leaked = true
			json.NewEncoder(w).Encode(map[string]bool{"leaked": true})
		case "/v1/large":
			json.NewEncoder(w).Encode(map[string]string{"data": strings.Repeat("a", 3<<20)})
		case "/v1/check":
			json.NewEncoder(w).Encode(map[string]bool{"leaked": leaked})
		default:
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND"})
		}
	})

	extraServers := []*http.Server{}
	extra := map[string]string{}
	for _, variant := range []string{"wrong_name", "expired", "tls12"} {
		k := key()
		t := &x509.Certificate{SerialNumber: big.NewInt(100 + int64(len(extraServers))), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
		if variant == "wrong_name" {
			t.IPAddresses = []net.IP{net.ParseIP("127.0.0.2")}
		}
		if variant == "expired" {
			t.NotBefore = now.Add(-3 * time.Hour)
			t.NotAfter = now.Add(-2 * time.Hour)
		}
		pemCert, derCert := cert(t, ca, k, caKey)
		pair, _ := tls.X509KeyPair([]byte(pemCert), []byte(private(k)))
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			panic(err)
		}
		version := uint16(tls.VersionTLS13)
		if variant == "tls12" {
			version = tls.VersionTLS12
		}
		srv := &http.Server{Handler: handler, TLSConfig: &tls.Config{Certificates: []tls.Certificate{pair}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: version, MaxVersion: version}, ReadHeaderTimeout: 5 * time.Second}
		extra[variant+"_origin"] = "https://" + ln.Addr().String()
		extra[variant+"_pin"] = hex.EncodeToString(sha256Sum(derCert))
		extraServers = append(extraServers, srv)
		go srv.Serve(tls.NewListener(ln, srv.TLSConfig))
	}
	// Test harness credentials only: stdout is consumed privately by the Rust test process.
	json.NewEncoder(os.Stdout).Encode(map[string]any{"enrollment": map[string]string{"name": "TLS test", "origin": "https://" + listener.Addr().String(), "server_id": "vps-01", "key_id": "desktop-01", "actor_id": "owner", "server_ca_pem": caPEM, "server_certificate_sha256": hex.EncodeToString(sha256Sum(serverDER)), "client_identity_pem": clientPEM + private(clientKey), "hmac_base64": base64.StdEncoding.EncodeToString(secret)}, "wrong_client": wrongPEM + private(wrongKey), "wrong_ca": otherPEM, "fingerprint": hex.EncodeToString(fingerprint[:]), "extra": extra, "server_cert": serverPEM})
	server := &http.Server{Handler: handler, TLSConfig: &tls.Config{Certificates: []tls.Certificate{serverCert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13}, ReadHeaderTimeout: 5 * time.Second}
	// Terminate when the parent closes stdin, including on failed tests.
	go func() { b := make([]byte, 1); os.Stdin.Read(b); server.Close() }()
	_ = server.Serve(tls.NewListener(listener, server.TLSConfig))
}

func sha256Sum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }
