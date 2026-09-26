// Package update checks a release channel, verifies the candidate, and
// installs it without touching WireGuard.
//
// test tracks the rolling prerelease tag "test" (builds of main).
// prod tracks the latest non-prerelease semver tag (vX.Y.Z).
// An empty channel does not check.
package update

import (
	"fmt"
	"strings"
)

const (
	// ChannelTest tracks builds of main, published as the rolling
	// prerelease tag "test".
	ChannelTest = "test"
	// ChannelProd tracks tagged semver releases. GitHub's latest
	// release ignores prereleases, so the test tag is not prod.
	ChannelProd = "prod"

	// Repo is the private GitHub repo that publishes both channels.
	Repo = "FyberLabs/hypermesh-host"
	// TestTag is the rolling prerelease updated on every main build.
	TestTag = "test"

	ChecksumsName = "checksums.txt"
	SignatureName = "checksums.txt.sig"
	ManifestName  = "release.json"
	// PubKeyAsset is uploaded so an operator can install it once.
	// The agent does not trust a pubkey fetched from the release.
	PubKeyAsset = "hypermesh-release.pub"

	// AgentUnit is the only unit a successful update restarts.
	AgentUnit = "hypermesh-agent"

	DefaultPubKey = "/etc/hypermesh/update-pubkey.asc"
	MarkerName    = "update-pending"
)

// IdleGate is the only agent surface this package needs.
// Busy is true while a lease is loaded or a job result is still unacked.
// The agent implements it. Callers pass the bool into Tick so a lease
// change in the agent loop does not have to grow this package.
type IdleGate interface {
	Busy() bool
}

// ParseChannel accepts "", "test", and "prod".
func ParseChannel(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "", nil
	case ChannelTest:
		return ChannelTest, nil
	case ChannelProd:
		return ChannelProd, nil
	default:
		return "", fmt.Errorf("hypermesh update: channel must be test or prod")
	}
}

// ReleaseAPI is the GitHub release document for the channel.
// test is the rolling prerelease. prod is the latest stable release.
func ReleaseAPI(channel string) (string, error) {
	switch channel {
	case ChannelTest:
		return "https://api.github.com/repos/" + Repo + "/releases/tags/" + TestTag, nil
	case ChannelProd:
		return "https://api.github.com/repos/" + Repo + "/releases/latest", nil
	default:
		return "", fmt.Errorf("hypermesh update: channel must be test or prod")
	}
}
