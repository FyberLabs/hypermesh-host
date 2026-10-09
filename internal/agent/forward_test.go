package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestRetryAccept(t *testing.T) {
	if !retryAccept(tempNetErr{}) {
		t.Fatal("temporary accept error should be retried")
	}
	if retryAccept(net.ErrClosed) || retryAccept(errors.New("closed")) || retryAccept(nil) {
		t.Fatal("permanent accept error should stop the loop")
	}
}

func TestForwarderRetriesTemporaryAccept(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	ln := &stepListener{closed: make(chan struct{})}
	ln.steps = []acceptStep{{err: tempNetErr{}}, {conn: server}}
	ctx, cancel := context.WithCancel(context.Background())
	f := &leaseForwarder{
		ln:     ln,
		dial:   "127.0.0.1:1",
		idle:   time.Second,
		slots:  make(chan struct{}, 2),
		ctx:    ctx,
		cancel: cancel,
		conns:  map[net.Conn]struct{}{},
	}
	defer f.Close()
	done := make(chan struct{})
	go func() {
		f.accept()
		close(done)
	}()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := client.Read(buf); err == nil {
		t.Fatal("accepted connection was not closed")
	}
	ln.mu.Lock()
	n := ln.n
	ln.mu.Unlock()
	if n < 2 {
		t.Fatalf("accept calls %d, temporary error exited the loop", n)
	}
}

func TestForwarderBoundsConcurrency(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	held := make(chan struct{})
	go func() {
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			<-held
			_ = conn.Close()
		}
	}()
	fwd, err := startLeaseForwarderLimit("127.0.0.1:0", backend.Addr().String(), 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()
	defer close(held)
	addr := fwd.ln.Addr().String()
	first, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	deadline := time.Now().Add(time.Second)
	for {
		fwd.mu.Lock()
		n := len(fwd.conns)
		fwd.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first connection was not tracked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	second, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	readDone := make(chan error, 1)
	go func() {
		_, err := second.Read(make([]byte, 1))
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("connection past the limit was served")
		}
	case <-time.After(time.Second):
		t.Fatal("connection past the limit stayed open")
	}
	fwd.mu.Lock()
	n := len(fwd.conns)
	fwd.mu.Unlock()
	if n != 1 {
		t.Fatalf("tracked connections %d", n)
	}
}

func TestForwarderIdleDeadline(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
	}()
	fwd, err := startLeaseForwarderLimit("127.0.0.1:0", backend.Addr().String(), 2, 80*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()
	client, err := net.DialTimeout("tcp", fwd.ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	start := time.Now()
	_, err = client.Read(make([]byte, 1))
	elapsed := time.Since(start)
	if err == nil || elapsed > time.Second {
		t.Fatalf("idle connection err=%v after %s", err, elapsed)
	}
}

type tempNetErr struct{}

func (tempNetErr) Error() string   { return "temporary accept" }
func (tempNetErr) Timeout() bool   { return false }
func (tempNetErr) Temporary() bool { return true }

type acceptStep struct {
	conn net.Conn
	err  error
}

type stepListener struct {
	mu     sync.Mutex
	steps  []acceptStep
	n      int
	closed chan struct{}
}

func (s *stepListener) Accept() (net.Conn, error) {
	s.mu.Lock()
	if s.n < len(s.steps) {
		step := s.steps[s.n]
		s.n++
		s.mu.Unlock()
		return step.conn, step.err
	}
	s.mu.Unlock()
	<-s.closed
	return nil, net.ErrClosed
}

func (s *stepListener) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

func (s *stepListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}
}
