package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func privateParent(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func read(t *testing.T, p string) []byte {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func parse(t *testing.T, b []byte) *x509.Certificate {
	t.Helper()
	p, _ := pem.Decode(b)
	if p == nil {
		t.Fatal("missing PEM")
	}
	c, e := x509.ParseCertificate(p.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func defaults(out string) options {
	return options{out, "Test VPS", "vps-01", "desktop-01", "mac-owner", "127.0.0.1", 9123}
}

func TestKitIdentityAndPolicy(t *testing.T) {
	out := filepath.Join(privateParent(t), "kit")
	if e := generate(defaults(out)); e != nil {
		t.Fatal(e)
	}
	var e map[string]string
	if err := json.Unmarshal(read(t, filepath.Join(out, "mac/mac.enrollment.json")), &e); err != nil {
		t.Fatal(err)
	}
	if e["origin"] != "https://127.0.0.1:9123" || e["server_id"] != "vps-01" {
		t.Fatal("connection identity mismatch")
	}
	serverPair, err := tls.X509KeyPair(read(t, filepath.Join(out, "vps/server.crt")), read(t, filepath.Join(out, "vps/server.key")))
	if err != nil {
		t.Fatal(err)
	}
	server := parse(t, read(t, filepath.Join(out, "vps/server.crt")))
	serverRoots := x509.NewCertPool()
	if !serverRoots.AppendCertsFromPEM([]byte(e["server_ca_pem"])) {
		t.Fatal("bad server CA")
	}
	if _, err := server.Verify(x509.VerifyOptions{Roots: serverRoots, DNSName: "127.0.0.1", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Verify(x509.VerifyOptions{Roots: serverRoots, DNSName: "127.0.0.2"}); err == nil {
		t.Fatal("wrong IP must fail")
	}
	if fingerprint(server) != e["server_certificate_sha256"] {
		t.Fatal("server pin mismatch")
	}
	clientPair, err := tls.X509KeyPair([]byte(e["client_identity_pem"]), []byte(e["client_identity_pem"]))
	if err != nil {
		t.Fatal(err)
	}
	client, err := x509.ParseCertificate(clientPair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	clientRoots := x509.NewCertPool()
	clientRoots.AppendCertsFromPEM(read(t, filepath.Join(out, "vps/control-ca.crt")))
	if _, err := client.Verify(x509.VerifyOptions{Roots: clientRoots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Verify(x509.VerifyOptions{Roots: serverRoots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Fatal("separate trust domains must reject other CA")
	}
	if _, err := server.Verify(x509.VerifyOptions{Roots: serverRoots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Fatal("server cannot authenticate as client")
	}
	if len(serverPair.Certificate) != 1 || client.IsCA || server.IsCA {
		t.Fatal("leaf constraints")
	}
	// Field names in the root policy use snake_case.
	var p map[string]json.RawMessage
	if err := json.Unmarshal(read(t, filepath.Join(out, "vps/credentials.json")), &p); err != nil {
		t.Fatal(err)
	}
	var keys []map[string]any
	if err := json.Unmarshal(p["keys"], &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0]["certificate_sha256"] != fingerprint(client) || keys[0]["id"] != e["key_id"] {
		t.Fatal("client policy pin mismatch")
	}
	hmac := read(t, filepath.Join(out, "vps/desktop-01.key"))
	if string(hmac) != e["hmac_base64"]+"\n" {
		t.Fatal("HMAC mismatch")
	}
	decoded, err := base64.StdEncoding.DecodeString(e["hmac_base64"])
	if err != nil || len(decoded) != 32 {
		t.Fatal("invalid HMAC")
	}
	if string(hmac) == string(read(t, filepath.Join(out, "vps/fingerprint.key"))) {
		t.Fatal("retry secret must be independent")
	}
	if err := filepath.Walk(out, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Errorf("unsafe mode on %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := generate(defaults(out)); err == nil {
		t.Fatal("overwrite must fail")
	}
	if string(read(t, filepath.Join(out, "vps/desktop-01.key"))) != string(hmac) {
		t.Fatal("kit changed after refused overwrite")
	}
}

func TestInvalidDestinationAndOrigin(t *testing.T) {
	parent := privateParent(t)
	for _, ip := range []string{"8.8.8.8", "0.0.0.0", "::", "::ffff:127.0.0.1", "example.com"} {
		o := defaults(filepath.Join(parent, "kit"))
		o.IP = ip
		if err := generate(o); err == nil {
			t.Fatalf("accepted origin %s", ip)
		}
	}
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if err := generate(defaults(filepath.Join(parent, "kit"))); err == nil {
		t.Fatal("writable parent accepted")
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(privateParent(t), "link")
	if err := os.Symlink(parent, symlink); err != nil {
		t.Fatal(err)
	}
	if err := generate(defaults(filepath.Join(symlink, "kit"))); err == nil {
		t.Fatal("symlink parent accepted")
	}
	o := defaults(filepath.Join(parent, "bad"))
	o.Key = "../../other"
	if err := generate(o); err == nil {
		t.Fatal("unsafe key ID accepted")
	}
	if _, err := os.Stat(filepath.Join(parent, "kit")); !os.IsNotExist(err) {
		t.Fatal("invalid input wrote files")
	}
}
