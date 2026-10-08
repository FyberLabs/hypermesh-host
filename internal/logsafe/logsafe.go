// Package logsafe provides a slog logger that redacts enroll tokens,
// device secrets, peer keys, and renter prompts.
package logsafe

import (
	"log/slog"
	"os"
	"strings"
)

var redactKeys = map[string]struct{}{
	"enroll_token":        {},
	"token":               {},
	"authorization":       {},
	"bearer":              {},
	"secret":              {},
	"password":            {},
	"prompt":              {},
	"prompts":             {},
	"renter_prompt":       {},
	"device_secret":       {},
	"credential":          {},
	"enroll":              {},
	"peer_private_key":    {},
	"private_key":         {},
	"privatekey":          {},
	"machine_private_key": {},
	"access_token":        {},
}

// New returns a text logger on stderr that redacts forbidden keys.
func New() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		ReplaceAttr: RedactAttr,
	}))
}

// RedactAttr replaces values that must never appear in logs or journald.
func RedactAttr(_ []string, a slog.Attr) slog.Attr {
	if _, ok := redactKeys[strings.ToLower(a.Key)]; ok {
		return slog.String(a.Key, "[redacted]")
	}
	if s, ok := a.Value.Any().(string); ok {
		if strings.HasPrefix(s, "hm_dev_") || strings.HasPrefix(s, "hm_msi_") || strings.HasPrefix(s, "hm_ct_") {
			return slog.String(a.Key, "[redacted]")
		}
	}
	return a
}
