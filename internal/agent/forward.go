package agent

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

// leaseForwarder copies TCP from the WireGuard address to the container
// address on the internal workload network. Docker does not publish host
// ports for a container whose only network is internal, so this process
// is the listener the gateway dials.
type leaseForwarder struct {
	ln     net.Listener
	dial   string
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
	wg     sync.WaitGroup
}

func startLeaseForwarder(listen, dial string) (*leaseForwarder, error) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &leaseForwarder{
		ln:     ln,
		dial:   dial,
		ctx:    ctx,
		cancel: cancel,
		conns:  map[net.Conn]struct{}{},
	}
	go f.accept()
	return f, nil
}

func (f *leaseForwarder) accept() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		if !f.track(conn) {
			_ = conn.Close()
			return
		}
		go f.pipe(conn)
	}
}

func (f *leaseForwarder) track(conn net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.conns[conn] = struct{}{}
	f.wg.Add(1)
	return true
}

func (f *leaseForwarder) pipe(conn net.Conn) {
	defer f.wg.Done()
	defer func() {
		f.mu.Lock()
		delete(f.conns, conn)
		f.mu.Unlock()
		_ = conn.Close()
	}()
	dialer := net.Dialer{Timeout: 5 * time.Second}
	upstream, err := dialer.DialContext(f.ctx, "tcp", f.dial)
	if err != nil {
		return
	}
	defer upstream.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, conn)
		_ = upstream.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, upstream)
		_ = conn.Close()
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-f.ctx.Done():
	}
	_ = conn.Close()
	_ = upstream.Close()
}

// Alive reports that the listener still accepts a connection.
func (f *leaseForwarder) Alive() bool {
	f.mu.Lock()
	closed := f.closed
	addr := ""
	if f.ln != nil {
		addr = f.ln.Addr().String()
	}
	f.mu.Unlock()
	if closed || addr == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Close stops the listener and in-flight copies.
func (f *leaseForwarder) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	conns := f.conns
	f.conns = map[net.Conn]struct{}{}
	f.mu.Unlock()
	f.cancel()
	_ = f.ln.Close()
	for conn := range conns {
		_ = conn.Close()
	}
	f.wg.Wait()
}

func (a *Agent) startLeaseForwarder(listen, dial string) error {
	a.fwdMu.Lock()
	defer a.fwdMu.Unlock()
	if a.fwd != nil {
		a.fwd.Close()
		a.fwd = nil
	}
	fwd, err := startLeaseForwarder(listen, dial)
	if err != nil {
		return err
	}
	a.fwd = fwd
	return nil
}

func (a *Agent) stopLeaseForwarder() {
	a.fwdMu.Lock()
	fwd := a.fwd
	a.fwd = nil
	a.fwdMu.Unlock()
	if fwd != nil {
		fwd.Close()
	}
}

func (a *Agent) forwarderAlive() bool {
	a.fwdMu.Lock()
	fwd := a.fwd
	a.fwdMu.Unlock()
	if fwd == nil {
		return false
	}
	return fwd.Alive()
}
