// Package metrics serves Hypermesh host health as Prometheus text.
// The listener is loopback-only unless an explicit opt-in is set.
// There is no scrape path through the WireGuard tunnel.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
)

// DefaultAddr is the listen address when HYPERMESH_METRICS_ADDR is unset.
// It is 127.0.0.1, never 0.0.0.0 and never a tunnel address.
const DefaultAddr = "127.0.0.1:9464"

// Config is the /metrics server.
type Config struct {
	Addr             string
	AllowNonLoopback bool
	Log              *slog.Logger
	Gather           func() []byte
}

// AuthorizeBind accepts a literal loopback IP. A non-loopback address,
// including 0.0.0.0, ::, and hostnames, is refused unless allow is set.
// The check runs before listen.
func AuthorizeBind(addr string, allow bool) error {
	loop, err := literalLoopback(addr)
	if err != nil {
		return fmt.Errorf("hypermesh metrics: %w", err)
	}
	if loop {
		return nil
	}
	if !allow {
		return fmt.Errorf("hypermesh metrics: refusing %s: not a loopback address (set HYPERMESH_METRICS_ALLOW_NON_LOOPBACK=1 to allow)", addr)
	}
	return nil
}

func literalLoopback(addr string) (bool, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false, fmt.Errorf("address %q: %w", addr, err)
	}
	if port == "" {
		return false, fmt.Errorf("address %q: missing port", addr)
	}
	if host == "" {
		return false, nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, nil
	}
	return ip.IsLoopback(), nil
}

// Listen binds addr when it is a literal loopback address.
// Non-loopback addresses are refused and no socket is opened.
func Listen(addr string) (net.Listener, error) {
	return listen(addr, false)
}

func listen(addr string, allow bool) (net.Listener, error) {
	if err := AuthorizeBind(addr, allow); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return ln, nil
}

// Serve listens and answers GET /metrics until ctx is cancelled.
// It returns the bound address after the listener is up.
// A refused non-loopback address returns an error and does not listen.
// When a non-loopback address is allowed, a warning is logged after listen.
func Serve(ctx context.Context, cfg Config) (string, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		addr = DefaultAddr
	}
	loop, err := literalLoopback(addr)
	if err != nil {
		return "", fmt.Errorf("hypermesh metrics: %w", err)
	}
	if err := AuthorizeBind(addr, cfg.AllowNonLoopback); err != nil {
		return "", err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	if !loop {
		cfg.Log.Warn("metrics bound to a non-loopback address; /metrics is reachable beyond localhost", "addr", ln.Addr().String())
	}
	srv := &http.Server{
		Handler:           handler(cfg.Gather),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	go func() {
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			cfg.Log.Error("metrics server stopped", "err", err)
		}
	}()
	bound := ln.Addr().String()
	cfg.Log.Info("metrics listening", "addr", bound)
	return bound, nil
}

func handler(gather func() []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body []byte
		if gather != nil && r.Method != http.MethodHead {
			body = gather()
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}

// Sample is the host health series. Maps may be nil.
// Empty ErrClass is the none bucket. TunnelState empty omits that gauge.
type Sample struct {
	HeartbeatSuccess uint64
	HeartbeatFailure map[string]uint64
	SyncSuccess      uint64
	SyncFailure      map[string]uint64
	TunnelState      string
	HandshakeAgeS    *int
	WGPID            *int
	WGPort           *int
	LeaseCount       *int
	DiskFreeBytes    *int64
	LastSyncUnix     *int64
	LastHTTP         int
	ErrClass         string
	Commit           string
}

// Render writes Prometheus text for Sample. No keys, tokens, or names.
func Render(s Sample) []byte {
	var b strings.Builder
	counter(&b, "hypermesh_heartbeat_success_total", "Heartbeats the control plane accepted.", nil, s.HeartbeatSuccess)
	counterClasses(&b, "hypermesh_heartbeat_failure_total", "Heartbeats that failed, by error class.", s.HeartbeatFailure)
	counter(&b, "hypermesh_tunnel_sync_success_total", "Tunnel syncs that completed.", nil, s.SyncSuccess)
	counterClasses(&b, "hypermesh_tunnel_sync_failure_total", "Tunnel syncs that failed, by error class.", s.SyncFailure)
	if s.TunnelState != "" {
		writeHelp(&b, "hypermesh_tunnel_state", "Current tunnel state. The active state is 1.", "gauge")
		for _, st := range api.TunnelHealthStates() {
			v := 0
			if st == s.TunnelState {
				v = 1
			}
			sample(&b, "hypermesh_tunnel_state", map[string]string{"state": st}, strconv.Itoa(v))
		}
	}
	if s.HandshakeAgeS != nil {
		gauge(&b, "hypermesh_handshake_age_seconds", "Seconds since the latest WireGuard handshake.", nil, strconv.Itoa(*s.HandshakeAgeS))
	}
	if s.WGPID != nil {
		gauge(&b, "hypermesh_wg_pid", "Process id of wireguard-go for this interface.", nil, strconv.Itoa(*s.WGPID))
	}
	if s.WGPort != nil {
		gauge(&b, "hypermesh_wg_port", "WireGuard listen port.", nil, strconv.Itoa(*s.WGPort))
	}
	if s.LeaseCount != nil {
		gauge(&b, "hypermesh_lease_container_count", "Running containers named with the hypermesh- lease prefix. Names are not labels.", nil, strconv.Itoa(*s.LeaseCount))
	}
	if s.DiskFreeBytes != nil {
		gauge(&b, "hypermesh_disk_free_bytes", "Free bytes for non-root users on the filesystem holding the GGUF path (statfs Bavail*Bsize).", nil, strconv.FormatInt(*s.DiskFreeBytes, 10))
	}
	if s.LastSyncUnix != nil {
		gauge(&b, "hypermesh_last_sync_timestamp_seconds", "Unix time of the last successful tunnel sync.", nil, strconv.FormatInt(*s.LastSyncUnix, 10))
	}
	gauge(&b, "hypermesh_last_sync_http_status", "HTTP status from the last failed tunnel sync, or 0.", nil, strconv.Itoa(s.LastHTTP))
	writeHelp(&b, "hypermesh_last_sync_error_class", "Current tunnel sync error class. The active class is 1. Empty class is none.", "gauge")
	for _, class := range api.HealthErrorClasses() {
		v := 0
		if class == s.ErrClass {
			v = 1
		}
		sample(&b, "hypermesh_last_sync_error_class", map[string]string{"class": class}, strconv.Itoa(v))
	}
	if s.Commit != "" {
		gauge(&b, "hypermesh_build_info", "Build commit injected at link time, or the VCS revision.", map[string]string{"commit": s.Commit}, "1")
	}
	return []byte(b.String())
}

func counterClasses(b *strings.Builder, name, help string, counts map[string]uint64) {
	writeHelp(b, name, help, "counter")
	for _, class := range api.HealthErrorClasses() {
		sample(b, name, map[string]string{"class": class}, strconv.FormatUint(counts[class], 10))
	}
}

func counter(b *strings.Builder, name, help string, labels map[string]string, v uint64) {
	writeHelp(b, name, help, "counter")
	sample(b, name, labels, strconv.FormatUint(v, 10))
}

func gauge(b *strings.Builder, name, help string, labels map[string]string, value string) {
	writeHelp(b, name, help, "gauge")
	sample(b, name, labels, value)
}

func writeHelp(b *strings.Builder, name, help, kind string) {
	b.WriteString("# HELP ")
	b.WriteString(name)
	b.WriteByte(' ')
	b.WriteString(help)
	b.WriteByte('\n')
	b.WriteString("# TYPE ")
	b.WriteString(name)
	b.WriteByte(' ')
	b.WriteString(kind)
	b.WriteByte('\n')
}

func sample(b *strings.Builder, name string, labels map[string]string, value string) {
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		first := true
		// Fixed label order for the labels this process emits.
		for _, key := range []string{"class", "state", "commit"} {
			v, ok := labels[key]
			if !ok {
				continue
			}
			if !first {
				b.WriteByte(',')
			}
			first = false
			b.WriteString(key)
			b.WriteString(`="`)
			b.WriteString(escapeLabel(v))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(value)
	b.WriteByte('\n')
}

func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
