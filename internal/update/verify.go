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
	"strconv"
	"strings"
	"time"
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

// GPGVerify checks a detached signature over checksums. pubkey is one
// or more ASCII-armored public keys concatenated in one file (the
// rotation overlap). allow is the configured fingerprint list. A
// signature counts only when gpg prints GOODSIG and VALIDSIG, none of
// EXPKEYSIG, REVKEYSIG, EXPSIG, BADSIG, or ERRSIG, and the VALIDSIG
// fingerprint is in allow. A key that is only in the keyring is not
// enough. An expired or revoked key is rejected. The process exit
// code alone is not enough. A missing gpg, a bad key, or a bad
// signature is a rejection. The agent does not fetch this file from
// the release.
func GPGVerify(checksums, sig, pubkey []byte, allow []string) error {
	if len(checksums) == 0 || len(sig) == 0 || len(pubkey) == 0 {
		return fmt.Errorf("%w: checksums, signature, and pubkey are required", ErrRejected)
	}
	trusted, err := fingerprintSet(allow)
	if err != nil || len(trusted) == 0 {
		return fmt.Errorf("%w: update fingerprints are required", ErrRejected)
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
	imported, colons, err := importedFingerprints(gnupg, ring)
	if err != nil || len(imported) == 0 {
		return fmt.Errorf("%w: pubkey was not imported", ErrRejected)
	}
	status, _ := gpgStatus(gnupg, "--no-default-keyring", "--keyring", ring, "--status-fd", "1", "--verify", sigPath, sumPath)
	if !signatureTrusted(status, trusted) {
		return fmt.Errorf("%w: checksum signature", ErrRejected)
	}
	for _, fp := range validsigFingerprints(status) {
		if _, ok := imported[fp]; !ok {
			return fmt.Errorf("%w: checksum signature", ErrRejected)
		}
		if !signingKeyUsable(colons, fp, time.Now()) {
			return fmt.Errorf("%w: signing key is expired or revoked", ErrRejected)
		}
	}
	return nil
}

func gpg(home string, args ...string) error {
	_, err := gpgStatus(home, args...)
	return err
}

func gpgStatus(home string, args ...string) (string, error) {
	cmd := exec.Command("gpg", append([]string{"--batch", "--homedir", home}, args...)...)
	cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
	// Status is on stdout (--status-fd 1). Key ids on stderr stay out
	// of the error returned to the agent log.
	out, err := cmd.Output()
	return string(out), err
}

func importedFingerprints(home, ring string) (map[string]struct{}, string, error) {
	out, err := gpgStatus(home, "--no-default-keyring", "--keyring", ring, "--with-colons", "--fingerprint")
	set := parseColonFingerprints(out)
	if len(set) == 0 {
		if err != nil {
			return nil, out, err
		}
		return nil, out, fmt.Errorf("no fingerprints")
	}
	return set, out, nil
}

// ParseFingerprints splits a config value on commas and whitespace.
// Each entry must be 40 or 64 hex characters. An empty value is an
// empty list, which fails closed at verify time.
func ParseFingerprints(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ' ', '\n', '\t', ';':
			return true
		default:
			return false
		}
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		fp := normalizeFingerprint(p)
		if !fingerprintHex(fp) {
			return nil, fmt.Errorf("hypermesh: fingerprint %q is not 40 or 64 hex characters", strings.TrimSpace(p))
		}
		out = append(out, fp)
	}
	return out, nil
}

func fingerprintSet(allow []string) (map[string]struct{}, error) {
	set := map[string]struct{}{}
	for _, fp := range allow {
		norm := normalizeFingerprint(fp)
		if !fingerprintHex(norm) {
			return nil, fmt.Errorf("hypermesh: fingerprint %q is not 40 or 64 hex characters", fp)
		}
		set[norm] = struct{}{}
	}
	return set, nil
}

func fingerprintHex(fp string) bool {
	if len(fp) != 40 && len(fp) != 64 {
		return false
	}
	for _, r := range fp {
		if (r < '0' || r > '9') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

// parseColonFingerprints reads field 10 of GnuPG fpr records.
func parseColonFingerprints(colons string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, line := range strings.Split(colons, "\n") {
		parts := strings.Split(line, ":")
		if len(parts) < 10 || parts[0] != "fpr" {
			continue
		}
		fp := normalizeFingerprint(parts[9])
		if fp != "" {
			set[fp] = struct{}{}
		}
	}
	return set
}

// signatureTrusted is true when status contains GOODSIG and a VALIDSIG
// line whose signing fingerprint or primary-key fingerprint is in
// trusted. EXPKEYSIG, REVKEYSIG, EXPSIG, BADSIG, and ERRSIG reject the
// status even if VALIDSIG is also present. GOODSIG without VALIDSIG is
// not enough. An empty trust set is not enough.
func signatureTrusted(status string, trusted map[string]struct{}) bool {
	if len(trusted) == 0 {
		return false
	}
	good := false
	valid := false
	for _, line := range strings.Split(status, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || fields[0] != "[GNUPG:]" {
			continue
		}
		switch fields[1] {
		case "EXPKEYSIG", "REVKEYSIG", "EXPSIG", "BADSIG", "ERRSIG":
			return false
		case "GOODSIG":
			good = true
		case "VALIDSIG":
			if len(fields) < 3 {
				return false
			}
			if fingerprintTrusted(fields[2], trusted) {
				valid = true
				continue
			}
			primary := fields[len(fields)-1]
			if primary != fields[2] && fingerprintTrusted(primary, trusted) {
				valid = true
				continue
			}
			return false
		}
	}
	return good && valid
}

func validsigFingerprints(status string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, line := range strings.Split(status, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 || fields[0] != "[GNUPG:]" || fields[1] != "VALIDSIG" {
			continue
		}
		for _, fp := range []string{fields[2], fields[len(fields)-1]} {
			fp = normalizeFingerprint(fp)
			if !fingerprintHex(fp) {
				continue
			}
			if _, ok := seen[fp]; ok {
				continue
			}
			seen[fp] = struct{}{}
			out = append(out, fp)
		}
	}
	return out
}

// signingKeyUsable reports whether the colon listing shows fp as a
// public key that is not expired or revoked. A missing record fails
// closed. now is compared with the key's expiry timestamp.
func signingKeyUsable(colons, fp string, now time.Time) bool {
	fp = normalizeFingerprint(fp)
	if !fingerprintHex(fp) {
		return false
	}
	type rec struct {
		validity string
		expires  string
	}
	var cur *rec
	found := false
	var got rec
	for _, line := range strings.Split(colons, "\n") {
		parts := strings.Split(line, ":")
		if len(parts) == 0 {
			continue
		}
		switch parts[0] {
		case "pub", "sub":
			cur = &rec{}
			if len(parts) > 1 {
				cur.validity = parts[1]
			}
			if len(parts) > 6 {
				cur.expires = parts[6]
			}
		case "fpr":
			if cur == nil || len(parts) < 10 {
				continue
			}
			if normalizeFingerprint(parts[9]) == fp {
				got = *cur
				found = true
			}
			cur = nil
		}
	}
	if !found {
		return false
	}
	switch got.validity {
	case "e", "r", "i", "d", "n":
		return false
	}
	if got.expires != "" {
		sec, err := strconv.ParseInt(got.expires, 10, 64)
		if err != nil || !now.Before(time.Unix(sec, 0)) {
			return false
		}
	}
	return true
}

func fingerprintTrusted(fp string, trusted map[string]struct{}) bool {
	fp = normalizeFingerprint(fp)
	if len(fp) != 40 && len(fp) != 64 {
		return false
	}
	_, ok := trusted[fp]
	return ok
}

func normalizeFingerprint(fp string) string {
	return strings.ToUpper(strings.TrimSpace(fp))
}
