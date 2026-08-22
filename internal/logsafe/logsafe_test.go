package logsafe

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactAttr(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: RedactAttr}))

	log.Info("enroll",
		"enroll_token", "hm_secret_value",
		"prompt", "ignore this renter text",
		"device_id", "11111111-1111-1111-1111-111111111111",
		"status", 200,
	)

	out := buf.String()
	if strings.Contains(out, "hm_secret_value") {
		t.Fatal("enroll token leaked into logs")
	}
	if strings.Contains(out, "ignore this renter text") {
		t.Fatal("prompt leaked into logs")
	}
	if !strings.Contains(out, "11111111-1111-1111-1111-111111111111") {
		t.Fatal("device_id should remain visible")
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatal("expected redacted markers")
	}
}
