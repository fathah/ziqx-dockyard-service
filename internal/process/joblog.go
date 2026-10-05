package process

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type jobLogKey struct{}

// WithJobLog attaches a deployment log; Exec.Run appends every state-changing
// Docker command and its full output to it.
func WithJobLog(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, jobLogKey{}, w)
}

// JobLog returns the deployment log attached to ctx, if any.
func JobLog(ctx context.Context) io.Writer {
	w, _ := ctx.Value(jobLogKey{}).(io.Writer)
	return w
}

// Read-only queries (ps, inspect, config) would drown the log in JSON.
var mutating = map[string]bool{"up": true, "pull": true, "stop": true, "start": true, "restart": true, "build": true, "down": true, "rm": true, "create": true, "kill": true}

// pulls reports whether a command may pull images and so needs registry logins.
func pulls(args []string) bool {
	for _, a := range args {
		if a == "up" || a == "pull" || a == "build" || a == "create" {
			return true
		}
	}
	return false
}

func logged(args []string) bool {
	for _, a := range args {
		if mutating[a] {
			return true
		}
	}
	return false
}

func writeCommand(w io.Writer, binary string, args []string, res Result, err error, took time.Duration) {
	fmt.Fprintf(w, "\n$ %s %s\n", filepath.Base(binary), strings.Join(args, " "))
	w.Write(res.Output)
	w.Write(res.Stderr)
	if err != nil {
		fmt.Fprintf(w, "✗ failed after %s: %v\n", took.Round(100*time.Millisecond), err)
	} else {
		fmt.Fprintf(w, "✓ done in %s\n", took.Round(100*time.Millisecond))
	}
}

// Capped is a size-limited, concurrency-safe log writer.
type Capped struct {
	mu    sync.Mutex
	W     io.Writer
	Limit int
	n     int
	last  string
}

// note reports whether s differs from the last note, recording it, so the
// registry summary appears once above the first pulling command.
func (c *Capped) note(s string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == s {
		return false
	}
	c.last = s
	return true
}

func (c *Capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	if c.n >= c.Limit {
		return n, nil
	}
	if room := c.Limit - c.n; len(p) > room {
		p = append(p[:room:room], []byte("\n… log truncated\n")...)
	}
	c.n += n
	c.W.Write(p)
	return n, nil
}
