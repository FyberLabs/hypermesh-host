// Package terms is the Hypermesh Terms text shipped in the agent.
// Enrollment records Version and the SHA-256 of Text.
package terms

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// Version is the id of this Terms text. It changes when the text changes.
const Version = "hypermesh-terms-inline-2026-11-05-r1"

//go:embed hypermesh-terms-inline-2026-11-05-r1.md
var Text []byte

// SHA256 is the hex SHA-256 of Text, including its trailing newline.
func SHA256() string {
	sum := sha256.Sum256(Text)
	return hex.EncodeToString(sum[:])
}
