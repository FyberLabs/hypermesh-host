package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const (
	// forwardMaxConns caps simultaneous proxied connections so one lease
	// cannot exhaust the agent process file-descriptor table.
	forwardMaxConns = 64
	// forwardIdleTimeout closes a proxied connection that sends or receives
	// no bytes for this long. Active streams refresh the deadline on each
	// read and write.
	forwardIdleTimeout = 5 * time.Minute
	forwardAcceptMin   = 5 * time.Millisecond
	forwardAcceptMax   = time.Second
)

// leaseForwarder copies TCP from the WireGuard address to the container
// address on the internal workload network. Docker does not publish host
// ports for a container whose only network is internal, so this process
// is the listener the gateway dials.
type leaseForwarder struct {
	ln     net.Listener
	listen string
	dial   string
	idle   time.Duration
	slots  chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
	wg     sync.WaitGroup
}

func startLeaseForwarder(listen, dial string) (*leaseForwarder, error) {
	return startLeaseForwarderLimit(listen, dial, forwardMaxConns, forwardIdleTimeout)
}

func startLeaseForwarderLimit(listen, dial string, maxConns int, idle time.Duration) (*leaseForwarder, error) {
	if maxConns < 1 {
		maxConns = forwardMaxConns
	}
	if idle <= 0 {
		idle = forwardIdleTimeout
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &leaseForwarder{
		ln:     ln,
		listen: listen,
		dial:   dial,
		idle:   idle,
		slots:  make(chan struct{}, maxConns),
		ctx:    ctx,
		cancel: cancel,
		conns:  map[net.Conn]struct{}{},
	}
	go f.accept()
	return f, nil
}

func (f *leaseForwarder) accept() {
	delay := time.Duration(0)
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			if f.done() || !retryAccept(err) {
				return
			}
			if delay == 0 {
				delay = forwardAcceptMin
			} else {
				delay *= 2
				if delay > forwardAcceptMax {
					delay = forwardAcceptMax
				}
			}
			if !f.wait(delay) {
				return
			}
			continue
		}
		delay = 0
		if !f.acquire() {
			_ = conn.Close()
			if f.done() {
				return
			}
			continue
		}
		if !f.track(conn) {
			f.release()
			_ = conn.Close()
			return
		}
		go f.pipe(conn)
	}
}

func (f *leaseForwarder) acquire() bool {
	select {
	case <-f.ctx.Done():
		return false
	case f.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (f *leaseForwarder) release() {
	select {
	case <-f.slots:
	default:
	}
}

func (f *leaseForwarder) done() bool {
	select {
	case <-f.ctx.Done():
		return true
	default:
		return false
	}
}

func (f *leaseForwarder) wait(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-f.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// retryAccept reports whether Accept failed for a transient resource
// error and the listener should keep running.
func retryAccept(err error) bool {
	if err == nil || errors.Is(err, net.ErrClosed) {
		return false
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Temporary()
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
	defer f.release()
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
	_ = conn.SetDeadline(time.Now().Add(f.idle))
	_ = upstream.SetDeadline(time.Now().Add(f.idle))
	local := &idleConn{Conn: conn, idle: f.idle}
	remote := &idleConn{Conn: upstream, idle: f.idle}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(remote, local)
		_ = upstream.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(local, remote)
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

// idleConn refreshes its read or write deadline on each call so a quiet
// connection expires and an active stream does not.
type idleConn struct {
	net.Conn
	idle time.Duration
}

func (c *idleConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(p)
}

func (c *idleConn) Write(p []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(c.idle))
	return c.Conn.Write(p)
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
	return a.startLeaseForwarderLocked(listen, dial)
}

func (a *Agent) startLeaseForwarderLocked(listen, dial string) error {
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
	a.stopLeaseForwarderLocked()
	a.fwdMu.Unlock()
}

func (a *Agent) stopLeaseForwarderLocked() {
	fwd := a.fwd
	a.fwd = nil
	if fwd != nil {
		fwd.Close()
	}
}

// dropCurrentLease clears the in-memory lease and its listener under the
// forwarder lock so a recovery tick cannot bind a new listener afterward.
func (a *Agent) dropCurrentLease() {
	a.fwdMu.Lock()
	a.setLease(nil)
	a.stopLeaseForwarderLocked()
	a.fwdMu.Unlock()
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

// forwarderCurrentLocked reports that the installed listener is up and
// already bound to listen, dialing dial. Caller holds fwdMu.
func (a *Agent) forwarderCurrentLocked(listen, dial string) bool {
	if a.fwd == nil || a.fwd.listen != listen || a.fwd.dial != dial {
		return false
	}
	return a.fwd.Alive()
}
