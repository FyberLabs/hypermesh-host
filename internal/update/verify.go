package update

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrRejected means the candidate must not be installed.
var ErrRejected = errors.New("hypermesh update: rejected")

// checksumFor returns the sha256 hex for name from a sha256sum file.
func checksumFor(text, name string) (string, error) {
	var found string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != name {
			continue
		}
		sum := strings.ToLower(fields[0])
		if found != "" && found != sum {
			return "", fmt.Errorf("%w: duplicate checksum for %s", ErrRejected, name)
		}
		found = sum
	}
	if found == "" {
		return "", fmt.Errorf("%w: checksum missing for %s", ErrRejected, name)
	}
	if len(found) != 64 || strings.IndexFunc(found, func(r rune) bool {
		return !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'))
	}) >= 0 {
		return "", fmt.Errorf("%w: checksum for %s is not sha256", ErrRejected, name)
	}
	return found, nil
}

// CheckHash reports whether payload matches the signed checksum line for name.
func CheckHash(checksums []byte, name string, payload []byte) error {
	want, err := checksumFor(string(checksums), name)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	got := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return fmt.Errorf("%w: checksum mismatch for %s", ErrRejected, name)
	}
	return nil
}

// SelectBinary is the unique hypermesh-agent linux asset for arch in
// the signed checksum file. Deploy archives are ignored.
func SelectBinary(checksums, arch string) (string, error) {
	arch = strings.TrimSpace(arch)
	if arch != "arm64" && arch != "amd64" {
		return "", fmt.Errorf("%w: unsupported arch %q", ErrRejected, arch)
	}
	suffix := "_linux_" + arch
	var match string
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		name := fields[1]
		if !strings.HasPrefix(name, "hypermesh-agent_") || !strings.HasSuffix(name, suffix) {
			continue
		}
		if match != "" && match != name {
			return "", fmt.Errorf("%w: more than one %s agent binary", ErrRejected, arch)
		}
		match = name
	}
	if match == "" {
		return "", fmt.Errorf("%w: no linux/%s agent binary in checksums", ErrRejected, arch)
	}
	return match, nil
}

// GPGVerify checks a detached signature over checksums with an armored
// public key. It uses gpg and a temporary keyring. A missing gpg, a bad
// key, or a bad signature is a rejection.
func GPGVerify(checksums, sig, pubkey []byte) error {
	if len(checksums) == 0 || len(sig) == 0 || len(pubkey) == 0 {
		return fmt.Errorf("%w: checksums, signature, and pubkey are required", ErrRejected)
	}
	if _, err := exec.LookPath("gpg"); err != nil {
		return fmt.Errorf("%w: gpg is not installed", ErrRejected)
	}
	dir, err := os.MkdirTemp("", "hm-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	gnupg := filepath.Join(dir, "gnupg")
	if err := os.Mkdir(gnupg, 0o700); err != nil {
		return err
	}
	pubPath := filepath.Join(dir, "pub.asc")
	sumPath := filepath.Join(dir, "checksums.txt")
	sigPath := filepath.Join(dir, "checksums.txt.sig")
	for _, pair := range []struct {
		path string
		body []byte
	}{
		{pubPath, pubkey},
		{sumPath, checksums},
		{sigPath, sig},
	} {
		if err := os.WriteFile(pair.path, pair.body, 0o600); err != nil {
			return err
		}
	}
	ring := filepath.Join(dir, "ring.gpg")
	if err := gpg(gnupg, "--no-default-keyring", "--keyring", ring, "--import", pubPath); err != nil {
		return fmt.Errorf("%w: pubkey was not imported", ErrRejected)
	}
	if err := gpg(gnupg, "--no-default-keyring", "--keyring", ring, "--verify", sigPath, sumPath); err != nil {
		return fmt.Errorf("%w: checksum signature", ErrRejected)
	}
	return nil
}

func gpg(home string, args ...string) error {
	cmd := exec.Command("gpg", append([]string{"--batch", "--homedir", home}, args...)...)
	cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
	// gpg writes key ids to stderr. Do not return that text: it is not
	// secret, but it is not useful in the agent log either.
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
