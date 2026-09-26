package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatAgeSinceSuccessOrProcessStart(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	start := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	a.started = start
	cur := start.Add(40 * time.Second)
	a.now = func() time.Time { return cur }

	text := string(a.metricsText())
	if !strings.Contains(text, "hypermesh_heartbeat_age_seconds 40\n") {
		t.Fatalf("age before first success\n%s", text)
	}

	a.recordHeartbeat(nil)
	cur = start.Add(90 * time.Second)
	text = string(a.metricsText())
	if !strings.Contains(text, "hypermesh_heartbeat_age_seconds 50\n") {
		t.Fatalf("age after success\n%s", text)
	}
	if _, ok := seriesLabels(text, "hypermesh_heartbeat_age_seconds"); !ok {
		t.Fatal("age gauge missing")
	}
	labels, _ := seriesLabels(text, "hypermesh_heartbeat_age_seconds")
	if labels != "" {
		t.Fatalf("age labels %q", labels)
	}
}
