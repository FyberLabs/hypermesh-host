// Package update checks a release channel, verifies the candidate, and
// installs it without touching WireGuard.
//
// test tracks builds of main. Each build is an immutable blob prefix
// test/<UTC yyyymmddHHMMSS>-<12 hex of the commit>/.
// prod tracks tagged semver releases at prod/<X.Y.Z>/.
// The agent learns the candidate from the control plane, not from GitHub.
// An empty channel does not check.
package update

import (
	"fmt"
	"strings"
)

const (
	// ChannelTest tracks builds of main. The commit in release.json is
	// the identity. The blob directory name is not the version.
	ChannelTest = "test"
	// ChannelProd tracks tagged semver releases (vX.Y.Z). A newer version
	// installs. An older one does not.
	ChannelProd = "prod"

	ChecksumsName = "checksums.txt"
	SignatureName = "checksums.txt.sig"
	// SignatureArmorName is the ASCII-armored detach-sign of checksums.txt.
	// A host accepts either signature file. One valid signature is enough.
	SignatureArmorName = "checksums.txt.asc"
	ManifestName       = "release.json"
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
