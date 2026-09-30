package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"github.com/ziqx/ziqx-dockyard-service/internal/api"
	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/buildinfo"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/engine"
	"github.com/ziqx/ziqx-dockyard-service/internal/inventory"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
	adapter "github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"github.com/ziqx/ziqx-dockyard-service/internal/state"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("dockyard stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "/etc/dockyard/config.json", "root-owned policy file")
	showVersion := flag.Bool("version", false, "print build version and exit")
	versionJSON := flag.Bool("version-json", false, "print machine-readable build version and exit")
	check := flag.Bool("check", false, "validate policy and credentials without side effects")
	reconcile := flag.String("reconcile-job", "", "offline root recovery; stop the daemon first")
	syncExisting := flag.Bool("sync-existing", false, "offline read-only scan of existing Compose/Caddy metadata into SQLite; stop the daemon first")
	flag.Parse()
	if *showVersion || *versionJSON {
		return buildinfo.Print(os.Stdout, *versionJSON)
	}
	if (*check && (*syncExisting || *reconcile != "")) || (*syncExisting && *reconcile != "") {
		return errors.New("choose only one check, sync-existing or reconcile-job mode")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("the agent requires Linux and root; development tests run without root")
	}
	syscall.Umask(0077)
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
	if err != nil {
		return errors.New("invalid agent TLS certificate/key")
	}
	ca, err := os.ReadFile(c.ClientCA)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return errors.New("invalid client CA")
	}
	v, err := auth.New(c)
	if err != nil {
		return err
	}
	fp, err := auth.ReadSecret(c.FingerprintKeyFile)
	if err != nil {
		return err
	}
	if *check {
		slog.Info("policy and credentials validated")
		return nil
	}
	lockPath := filepath.Join(c.StateDir, "agent.lock")
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	defer lock.Close()
	if err = secure.Check(lockPath, true); err != nil {
		return err
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another agent owns this state directory")
	}
	s, err := state.Open(c.StateDir)
	if err != nil {
		return err
	}
	defer s.Close()
	runner := process.Exec{Timeout: time.Duration(c.CommandSeconds) * time.Second, Limit: 2 << 20, DockerConfig: c.DockerConfig}
	docker := adapter.Docker{Config: c, Runner: runner, Store: s}
	caddy := adapter.Caddy{Config: c, Runner: runner}
	dns := adapter.NewDNS(c)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	scanner := inventory.Scanner{Config: c, Store: s, Runner: runner}
	if *syncExisting {
		result, err := scanner.Sync(ctx)
		if err != nil {
			return err
		}
		slog.Info("existing inventory synced", "projects", len(result.Projects), "sites", len(result.Sites), "warnings", result.Warnings)
		return inventory.CheckSources(result)
	}
	// Reject an unavailable/old Compose before accepting privileged operations.
	version, err := runner.Run(ctx, c.DockerBinary, c.ProjectsRoot, []string{"compose", "version", "--short"})
	if err != nil {
		return errors.New("Docker Compose is unavailable")
	}
	if !adapter.ComposeSupported(string(version.Output)) {
		return errors.New("Docker Compose >=2.30 is required")
	}
	if err = secure.CaddySocket(c.CaddyAdminSocket); err != nil {
		return err
	}
	e := engine.New(c, s, docker, caddy, dns)
	if err = e.Initialize(); err != nil {
		return err
	}
	if *reconcile != "" {
		return e.Reconcile(ctx, *reconcile)
	}
	if result, err := scanner.Sync(ctx); err != nil {
		slog.Warn("initial inventory sync failed")
	} else if len(result.Warnings) > 0 {
		slog.Warn("initial inventory sources unavailable", "warnings", result.Warnings)
	}
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return err
	}
	listener = api.LimitConnections(listener, 128)
	defer listener.Close()
	server := &http.Server{Addr: c.Listen, Handler: api.New(e, v, fp), ErrorLog: log.New(&api.TransportLog{}, "", 0), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, NextProtos: []string{"http/1.1"}},
	}
	workerDone := make(chan error, 1)
	inventoryDone := make(chan struct{})
	go func() { defer close(inventoryDone); scanner.Run(ctx) }()
	go func() { workerDone <- e.Run(ctx) }()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ServeTLS(listener, "", "") }()
	slog.Info("dockyard listening with mTLS", "server_id", c.ServerID, "address", c.Listen)
	var result error
	var workerStopped bool
	select {
	case <-ctx.Done():
	case result = <-workerDone:
		workerStopped = true
	case result = <-serverDone:
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	server.Shutdown(shutdown)
	<-inventoryDone
	if !workerStopped {
		if err := <-workerDone; result == nil {
			result = err
		}
	}
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}
