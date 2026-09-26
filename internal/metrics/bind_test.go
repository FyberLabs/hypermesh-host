package metrics

import (
	"net"
	"strings"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/api"
)

func TestResolveAddrNeverPublicOrUnspecified(t *testing.T) {
	cases := []struct {
		override string
		tunnel   string
		want     string
		refused  bool
	}{
		{want: DefaultAddr},
		{tunnel: "10.77.0.3", want: "10.77.0.3:9464"},
		{tunnel: "10.77.0.3/32", want: "10.77.0.3:9464"},
		{override: "127.0.0.1:19064", tunnel: "10.77.0.3", want: "127.0.0.1:19064"},
		{override: "10.77.0.3:9464", tunnel: "10.77.0.3", want: "10.77.0.3:9464"},
		{override: "0.0.0.0:9464", tunnel: "10.77.0.3", want: "10.77.0.3:9464", refused: true},
		{override: "[::]:9464", want: DefaultAddr, refused: true},
		{override: "8.8.8.8:9464", tunnel: "10.77.0.3", want: "10.77.0.3:9464", refused: true},
		{override: "8.8.8.8:9464", tunnel: "8.8.8.8", want: DefaultAddr, refused: true},
		{override: "192.168.1.9:9464", tunnel: "10.77.0.3", want: "10.77.0.3:9464", refused: true},
		{tunnel: "0.0.0.0", want: DefaultAddr},
		{tunnel: "8.8.8.8", want: DefaultAddr},
	}
	for _, tc := range cases {
		got, refused := ResolveAddr(tc.override, tc.tunnel)
		if got != tc.want || refused != tc.refused {
			t.Fatalf("override %q tunnel %q got %s refused %v want %s refused %v", tc.override, tc.tunnel, got, refused, tc.want, tc.refused)
		}
		assertNotPublic(t, got)
	}
}

func TestAuthorizeBindMatchesOnlyTunnelHost(t *testing.T) {
	if err := AuthorizeBind("10.77.0.3:9464", "10.77.0.3"); err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeBind("10.77.0.3:9464", "10.77.0.4"); err == nil {
		t.Fatal("a different private address must be refused")
	}
	if err := AuthorizeBind("10.77.0.3:9464", ""); err == nil {
		t.Fatal("tunnel address without a session must be refused")
	}
}

func TestIfaceAddrIsPrivateOrEmpty(t *testing.T) {
	ip := IfaceAddr("hypermesh")
	if ip == "" {
		return
	}
	parsed := net.ParseIP(ip)
	if !usableTunnelIP(parsed) || parsed.IsUnspecified() {
		t.Fatalf("hypermesh address %s", ip)
	}
}

func TestPullAndHealthMetricNames(t *testing.T) {
	free := int64(10)
	size := int64(100)
	age := 180.0
	text := string(Render(Sample{
		HeartbeatSuccess:    2,
		HeartbeatFailure:    map[string]uint64{api.ErrClassHTTP5xx: 1, api.ErrClassNetwork: 1},
		HeartbeatAgeSeconds: &age,
		DiskFreeBytes:       &free,
		DiskSizeBytes:       &size,
		PullBytes:           4096,
		PullSeconds:         1.5,
	}))
	for _, name := range []string{
		"hypermesh_heartbeat_success_total 2",
		"hypermesh_heartbeat_failure_total{class=\"http_5xx\"} 1",
		"hypermesh_heartbeat_failure_total{class=\"network\"} 1",
		"hypermesh_heartbeat_age_seconds 180",
		"hypermesh_disk_free_bytes 10",
		"hypermesh_disk_size_bytes 100",
		"hypermesh_pull_bytes_total 4096",
		"hypermesh_pull_duration_seconds_total 1.5",
	} {
		if !strings.Contains(text, name) {
			t.Fatalf("missing %s\n%s", name, text)
		}
	}
	for _, leak := range []string{"hm_dev_", "private_key", "BEGIN"} {
		if strings.Contains(text, leak) {
			t.Fatalf("metrics leaked %q", leak)
		}
	}
	freeLabels, freeOK := metricLabels(text, "hypermesh_disk_free_bytes")
	sizeLabels, sizeOK := metricLabels(text, "hypermesh_disk_size_bytes")
	if !freeOK || !sizeOK || freeLabels != sizeLabels {
		t.Fatalf("disk label parity free %q size %q", freeLabels, sizeLabels)
	}
	if strings.Contains(freeLabels, "{") || strings.Contains(sizeLabels, "{") {
		t.Fatalf("disk gauges must have no labels, free %q size %q", freeLabels, sizeLabels)
	}
	onlyFree := string(Render(Sample{DiskFreeBytes: &free}))
	if strings.Contains(onlyFree, "hypermesh_disk_free_bytes") || strings.Contains(onlyFree, "hypermesh_disk_size_bytes") {
		t.Fatalf("one disk reading must omit both\n%s", onlyFree)
	}
}

func metricLabels(text, name string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		rest := strings.TrimPrefix(line, name)
		if rest == "" || (rest[0] != ' ' && rest[0] != '{') {
			continue
		}
		if strings.HasPrefix(rest, "{") {
			end := strings.IndexByte(rest, '}')
			if end < 0 {
				return "", false
			}
			return rest[:end+1], true
		}
		return "", true
	}
	return "", false
}

func assertNotPublic(t *testing.T, addr string) {
	t.Helper()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() {
		t.Fatalf("addr %s", addr)
	}
	if !ip.IsLoopback() && !usableTunnelIP(ip) {
		t.Fatalf("public or non-tunnel addr %s", addr)
	}
}
