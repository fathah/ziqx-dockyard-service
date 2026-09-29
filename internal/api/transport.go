package api

import (
	"log/slog"
	"net"
	"sync"
	"time"
)

type limitedListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}
type limitedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func LimitConnections(l net.Listener, n int) net.Listener {
	return &limitedListener{Listener: l, slots: make(chan struct{}, n), done: make(chan struct{})}
}
func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.slots }}, nil
}
func (l *limitedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}
func (c *limitedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }

// The HTTP server's default logger emits every bad TLS handshake. Aggregate
// transport failures to bound logs, and never persist peer-controlled messages.
type TransportLog struct {
	mu    sync.Mutex
	count uint64
	last  time.Time
}

func (l *TransportLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.count++
	if time.Since(l.last) >= time.Minute {
		slog.Warn("TLS/HTTP transport errors", "count", l.count)
		l.count = 0
		l.last = time.Now()
	}
	return len(b), nil
}
