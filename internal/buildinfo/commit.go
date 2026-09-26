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
	"strconv"
	"strings"
)

// Commit is the git commit injected with -X. Tests may set it.
var Commit string

// Version is the semver injected with -X, without a leading v.
// Empty means this binary is not a tagged release. Tests may set it.
//
//	-X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Version=1.2.3
var Version string

// Build is a rising integer injected with -X. The publisher sets it
// to one more than the max of the test index, the prod index, and the
// workflow run number, so both channels share one sequence. A host
// that switches channel only moves to a strictly newer build. Empty
// or non-numeric means 0 (a development binary).
//
//	-X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Build=12
var Build string

// CurrentVersion is the link-time semver, or empty.
func CurrentVersion() string {
	return strings.TrimPrefix(strings.TrimSpace(Version), "v")
}

// CurrentBuild is the link-time build number, or 0.
func CurrentBuild() int {
	n, err := strconv.Atoi(strings.TrimSpace(Build))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

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
