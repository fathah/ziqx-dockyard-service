package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Result struct {
	Output    []byte
	Stderr    []byte
	Truncated bool
}
type Runner interface {
	Run(context.Context, string, string, []string) (Result, error)
}
type Exec struct {
	Timeout      time.Duration
	Limit        int
	DockerConfig string
}
type bounded struct {
	mu        sync.Mutex
	b         []byte
	limit     int
	truncated bool
}

func (b *bounded) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	room := b.limit - len(b.b)
	if len(p) > room {
		b.truncated = true
		p = p[:room]
	}
	b.b = append(b.b, p...)
	return n, nil
}
func (r Exec) Run(ctx context.Context, binary, dir string, args []string) (Result, error) {
	if !filepath.IsAbs(binary) || !filepath.IsAbs(dir) || r.Timeout <= 0 || r.Limit <= 0 {
		return Result{}, errors.New("invalid process policy")
	}
	if pulls(args) {
		// Commands that may pull need the administrator's registry logins.
		if summary := syncRegistryAuth(r.DockerConfig); summary != "" {
			if c, ok := JobLog(ctx).(*Capped); ok && c.note(summary) {
				fmt.Fprintf(c, "\n%s\n", summary)
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	c := exec.CommandContext(ctx, binary, args...)
	c.Dir = dir
	c.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "HOME=/nonexistent", "DOCKER_HOST=unix:///var/run/docker.sock", "DOCKER_CONFIG=" + r.DockerConfig}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		e := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		if e == syscall.ESRCH {
			return nil
		}
		return e
	}
	c.WaitDelay = 2 * time.Second
	out := &bounded{limit: r.Limit}
	diagnostic := &bounded{limit: r.Limit}
	c.Stdout = out
	c.Stderr = diagnostic
	started := time.Now()
	err := c.Run()
	res := Result{Output: out.b, Stderr: diagnostic.b, Truncated: out.truncated || diagnostic.truncated}
	if w := JobLog(ctx); w != nil && logged(args) {
		writeCommand(w, binary, args, res, err, time.Since(started))
	}
	return res, err
}

var _ io.Writer = (*bounded)(nil)
