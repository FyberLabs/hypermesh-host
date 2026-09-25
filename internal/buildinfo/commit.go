// Package buildinfo is the Hypermesh agent build commit.
//
// Set Commit at link time:
//
//	go build -ldflags "-X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Commit=$(git rev-parse HEAD)" -o hypermesh-agent ./cmd/agent
//
// When Commit is empty, Current uses the VCS revision Go recorded
// (debug.ReadBuildInfo, vcs.revision). If that is also empty, Current
// is "unknown".
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Commit is the git commit injected with -X. Tests may set it.
var Commit string

// Current is the build commit to report. It is never a secret.
func Current() string {
	if c := strings.TrimSpace(Commit); c != "" {
		return c
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				if v := strings.TrimSpace(s.Value); v != "" {
					return v
				}
			}
		}
	}
	return "unknown"
}
