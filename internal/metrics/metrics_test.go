package metrics

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDefaultAddrIsLoopback(t *testing.T) {
	if DefaultAddr != "127.0.0.1:9464" {
		t.Fatalf("default %q", DefaultAddr)
	}
	host, _, err := net.SplitHostPort(DefaultAddr)
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || ip.IsUnspecified() {
		t.Fatalf("default host %q is not a specific loopback address", host)
	}
	if err := AuthorizeBind(DefaultAddr, false); err != nil {
		t.Fatal(err)
	}

	ln, err := Listen(net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("addr %T", ln.Addr())
	}
	if !tcp.IP.Equal(net.ParseIP("127.0.0.1")) || tcp.IP.IsUnspecified() {
		t.Fatalf("bound %v", tcp.IP)
	}
}

func TestListenRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "0.0.0.0:9464", "[::]:9464", "10.77.0.2:9464", ":9464", "localhost:9464"} {
		ln, err := Listen(addr)
		if err == nil {
			ln.Close()
			t.Fatalf("Listen(%s) opened a socket", addr)
		}
		if !strings.Contains(err.Error(), "not a loopback") && !strings.Contains(err.Error(), "address") {
			t.Fatalf("Listen(%s): %v", addr, err)
		}
	}
}

func TestServeDefaultHostMetrics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bound, err := Serve(ctx, Config{
		Addr: "127.0.0.1:0",
		Gather: func() []byte {
			return []byte("hypermesh_build_info{commit=\"abc\"} 1\n")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(bound)
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || ip.IsUnspecified() {
		t.Fatalf("served on %s", bound)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	var resp *http.Response
	for i := 0; i < 20; i++ {
		resp, err = client.Get("http://" + bound + "/metrics")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("content-type %q", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "hypermesh_build_info") {
		t.Fatalf("body %s", body)
	}
	other, err := client.Get("http://" + bound + "/agent/tunnel")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Fatalf("non-metrics path %d", other.StatusCode)
	}
}

func TestServeRefusesZeroAndWarnsOnOptIn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Serve(ctx, Config{Addr: "0.0.0.0:0"}); err == nil {
		t.Fatal("0.0.0.0 without opt-in must not listen")
	}
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	bound, err := Serve(ctx, Config{
		Addr:             "0.0.0.0:0",
		AllowNonLoopback: true,
		Log:              log,
		Gather:           func() []byte { return []byte("# eof\n") },
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if !strings.Contains(buf.String(), "non-loopback") {
		t.Fatalf("opt-in must warn, log %q", buf.String())
	}
	// Shut the opt-in listener down; do not leave 0.0.0.0 open.
	time.Sleep(20 * time.Millisecond)
	_ = bound
}
