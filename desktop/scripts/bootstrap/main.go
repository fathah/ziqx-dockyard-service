// Generate a fresh installation kit locally. Never run this to rotate a live agent.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"
	"unicode"
)

type options struct {
	Out, Name, Server, Key, Actor, IP string
	Port                              int
}
type identity struct {
	Cert         *x509.Certificate
	Key          *ecdsa.PrivateKey
	PEM, Private []byte
}

var token = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

func issue(name string, parent *identity, ip net.IP) (identity, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return identity{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return identity{}, err
	}
	serial.Add(serial, big.NewInt(1))
	now := time.Now()
	t := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(0, 0, 90), KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true}
	p, signer := t, k
	if parent == nil {
		t.IsCA = true
		t.NotAfter = now.AddDate(5, 0, 0)
		t.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		t.MaxPathLenZero = true
	} else {
		p, signer = parent.Cert, parent.Key
		if ip == nil {
			t.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		} else {
			t.IPAddresses = []net.IP{ip}
			t.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, t, p, &k.PublicKey, signer)
	if err != nil {
		return identity{}, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return identity{}, err
	}
	private, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return identity{}, err
	}
	return identity{cert, k, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})}, nil
}

func fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}
func secret() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return []byte(base64.StdEncoding.EncodeToString(b) + "\n"), nil
}
func jsonBytes(v any) ([]byte, error) {
	b, e := json.MarshalIndent(v, "", "  ")
	return append(b, '\n'), e
}

func generate(o options) error {
	addr, err := netip.ParseAddr(o.IP)
	if err != nil || addr.Is4In6() || !(addr.IsPrivate() || addr.IsLoopback()) || o.Port < 1 || o.Port > 65535 {
		return errors.New("use a private/loopback IP and a valid port")
	}
	for _, s := range []string{o.Server, o.Key, o.Actor} {
		if !token.MatchString(s) {
			return errors.New("IDs must start with a lowercase letter and use 1–48 lowercase letters, digits or hyphens")
		}
	}
	if len(o.Name) == 0 || len(o.Name) > 80 {
		return errors.New("choose a connection name up to 80 bytes")
	}
	for _, r := range o.Name {
		if unicode.IsControl(r) {
			return errors.New("connection names cannot contain control characters")
		}
	}
	if o.Out == "" {
		return errors.New("-out is required; choose a new directory inside a private local folder")
	}
	out, err := filepath.Abs(o.Out)
	if err != nil {
		return err
	}
	parent := filepath.Dir(out)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return errors.New("output parent must exist and must not use symlinks")
	}
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int(owner.Uid) != os.Getuid() || info.Mode().Perm()&0022 != 0 {
		return errors.New("output parent must be owned by you and not writable by other users")
	}
	serverCA, err := issue(o.Server+" server CA", nil, nil)
	if err != nil {
		return err
	}
	controlCA, err := issue(o.Server+" client CA", nil, nil)
	if err != nil {
		return err
	}
	server, err := issue(o.Server, &serverCA, net.IP(addr.AsSlice()))
	if err != nil {
		return err
	}
	client, err := issue(o.Key, &controlCA, nil)
	if err != nil {
		return err
	}
	hmac, err := secret()
	if err != nil {
		return err
	}
	retry, err := secret()
	if err != nil {
		return err
	}
	origin := "https://" + net.JoinHostPort(addr.String(), strconv.Itoa(o.Port))
	enrollment, err := jsonBytes(map[string]string{"name": o.Name, "origin": origin, "server_id": o.Server, "key_id": o.Key, "actor_id": o.Actor, "server_ca_pem": string(serverCA.PEM), "server_certificate_sha256": fingerprint(server.Cert), "client_identity_pem": string(client.PEM) + string(client.Private), "hmac_base64": string(hmac[:len(hmac)-1])})
	if err != nil {
		return err
	}
	credentials, err := jsonBytes(map[string]any{"server_id": o.Server, "listen": net.JoinHostPort(addr.String(), strconv.Itoa(o.Port)), "tls_cert": "/etc/dockyard/tls/server.crt", "tls_key": "/etc/dockyard/tls/server.key", "client_ca": "/etc/dockyard/tls/control-ca.crt", "fingerprint_key_file": "/etc/dockyard/fingerprint.key", "keys": []any{map[string]any{"id": o.Key, "secret_file": "/etc/dockyard/" + o.Key + ".key", "certificate_sha256": fingerprint(client.Cert), "scopes": []string{"compose.admin", "projects.write", "sites.write", "dns.write", "deploy.read", "deploy.logs", "deploy.execute", "deploy.environment", "deploy.rollback", "deploy.lifecycle", "deploy.stop"}, "projects": []string{"*"}}}})
	if err != nil {
		return err
	}
	// Mkdir is exclusive: an existing kit (even empty) can never be overwritten.
	if err := os.Mkdir(out, 0700); err != nil {
		return errors.New("cannot create output directory; existing kits are never overwritten")
	}
	for _, dir := range []string{"vps", "mac", "offline"} {
		if err := os.Mkdir(filepath.Join(out, dir), 0700); err != nil {
			return err
		}
	}
	readme := fmt.Sprintf(`FRESH INSTALLATION ONLY — not live certificate/key rotation.

Keep mac/ on this Mac. Import mac/mac.enrollment.json only AFTER the VPS is configured.
Transfer only vps/ over your authenticated SSH connection. Never upload offline/ or mac/.
Keep offline/ in a protected offline backup; it contains both CA signing keys.
The enrollment contains the Mac private key and HMAC secret. Do not paste it into chat.

On the VPS, from the transferred vps/ directory:
sudo install -d -o root -g root -m 0700 /etc/dockyard /etc/dockyard/tls
sudo install -o root -g root -m 0600 server.crt server.key control-ca.crt /etc/dockyard/tls/
sudo install -o root -g root -m 0600 %s.key fingerprint.key /etc/dockyard/

Merge credentials.json fields into /etc/dockyard/config.json for a NEW installation.
This is a credentials fragment, not a complete policy. Configure storage,
domain suffixes, Docker/Caddy paths and optional Cloudflare separately using docs/INSTALL.md.
The generated administrative key has full Compose (root-equivalent) management scopes and projects ["*"]; narrow if needed.
Do not replace a live policy, its stable fingerprint key or its client CA with this kit.

Then run dockyard -config /etc/dockyard/config.json -check and start its systemd service.
Connection: %s — server ID %s — key ID %s
Leaf certificates expire: %s. Plan certificate renewal before this date.
See desktop/FIRST_TIME.md for transfer, configuration and enrollment instructions.
`, o.Key, origin, o.Server, o.Key, server.Cert.NotAfter.UTC().Format(time.RFC3339))
	files := map[string][]byte{"vps/server.crt": server.PEM, "vps/server.key": server.Private, "vps/control-ca.crt": controlCA.PEM, "vps/" + o.Key + ".key": hmac, "vps/fingerprint.key": retry, "vps/credentials.json": credentials, "mac/mac.enrollment.json": enrollment, "offline/server-ca.crt": serverCA.PEM, "offline/server-ca.key": serverCA.Private, "offline/control-ca.crt": controlCA.PEM, "offline/control-ca.key": controlCA.Private, "README.txt": []byte(readme)}
	for name, data := range files {
		f, e := os.OpenFile(filepath.Join(out, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(data)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func main() {
	var o options
	flag.StringVar(&o.Out, "out", "", "new private setup-kit directory (parent must exist)")
	flag.StringVar(&o.Name, "name", "Production VPS", "connection display name")
	flag.StringVar(&o.Server, "server-id", "vps-01", "new agent server ID")
	flag.StringVar(&o.Key, "key-id", "desktop-01", "dedicated Mac key ID")
	flag.StringVar(&o.Actor, "actor-id", "mac-owner", "audit actor ID")
	flag.StringVar(&o.IP, "ip", "127.0.0.1", "private/loopback API IP; default uses an SSH tunnel")
	flag.IntVar(&o.Port, "port", 9123, "API port")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Unexpected arguments")
		os.Exit(1)
	}
	if err := generate(o); err != nil {
		fmt.Fprintln(os.Stderr, "Setup kit failed:", err)
		os.Exit(1)
	}
	fmt.Println("Private setup kit created. Install only vps/ on the VPS, then import mac/mac.enrollment.json. Read README.txt; no credentials were printed.")
}
