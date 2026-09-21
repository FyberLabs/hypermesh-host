package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

var (
	// ErrNoGGUF means lease_start has no HYPERMESH_GGUF_PATH.
	ErrNoGGUF = errors.New("hypermesh: no GGUF path")
	// ErrGGUFNotFile means the path is missing or is not a regular file.
	// Docker -v of a missing path creates a directory; refuse that.
	ErrGGUFNotFile = errors.New("hypermesh: GGUF is not a file")
)

// RequireGGUFFile fails closed unless path is a regular file.
// Do not invent a model. Do not let docker create a directory mount.
func RequireGGUFFile(path string) error {
	p := strings.TrimSpace(path)
	if p == "" {
		return ErrNoGGUF
	}
	fi, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrGGUFNotFile, p)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", ErrGGUFNotFile, p)
	}
	return nil
}

// HashGGUFFile is sha256 of the mounted weights. lease_start POSTs this
// as image_hash so the plane can stamp catalog file_hash. Not the OCI pin.
func HashGGUFFile(path string) (string, error) {
	if err := RequireGGUFFile(path); err != nil {
		return "", err
	}
	f, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrGGUFNotFile, path)
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", fmt.Errorf("hypermesh: GGUF hash: %w", err)
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil)), nil
}
