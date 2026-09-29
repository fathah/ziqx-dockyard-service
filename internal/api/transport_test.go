package api

import (
	"net"
	"sync"
	"testing"
	"time"
)

type fakeListener struct {
	queue chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *fakeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.queue:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *fakeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *fakeListener) Addr() net.Addr { return &net.TCPAddr{} }
func TestTransportConnectionCapAndShutdown(t *testing.T) {
	base := &fakeListener{queue: make(chan net.Conn, 2), done: make(chan struct{})}
	a, ap := net.Pipe()
	b, bp := net.Pipe()
	defer ap.Close()
	defer bp.Close()
	base.queue <- a
	base.queue <- b
	l := LimitConnections(base, 1)
	first, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); accepted <- c }()
	select {
	case <-accepted:
		t.Fatal("accepted beyond cap")
	case <-time.After(20 * time.Millisecond):
	}
	first.Close()
	var second net.Conn
	select {
	case second = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("slot not released")
	}
	if second == nil {
		t.Fatal("second accept failed")
	}
	closed := make(chan error, 1)
	go func() { _, err := l.Accept(); closed <- err }()
	l.Close()
	select {
	case err := <-closed:
		if err != net.ErrClosed {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked by full limiter")
	}
	second.Close()
}
