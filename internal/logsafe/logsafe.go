// Package logsafe provides a slog logger that redacts enroll tokens,
// credentials, and renter prompts. The control plane forbids those
// values in Kafka and logs; the host agent follows the same rule.
package logsafe

import (
	"log/slog"
	"os"
	"strings"
)

var redactKeys = map[string]struct{}{
	"enroll_token":  {},
	"token":         {},
	"authorization": {},
	"bearer":        {},
	"secret":        {},
	"password":      {},
	"prompt":        {},
	"prompts":       {},
	"renter_prompt": {},
	"device_secret": {},
	"credential":    {},
	"enroll":        {},
}

// New returns a text logger on stderr that redacts forbidden keys.
func New() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		ReplaceAttr: RedactAttr,
	}))
}

// RedactAttr replaces values for keys that must never appear in logs.
func RedactAttr(_ []string, a slog.Attr) slog.Attr {
	if _, ok := redactKeys[strings.ToLower(a.Key)]; ok {
		return slog.String(a.Key, "[redacted]")
	}
	return a
}
