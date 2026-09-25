package tunnel

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const hostKeyFile = "hypermesh.key"

// DefaultKeyDir holds hypermesh.key. The agent creates it; nobody copies the key.
const DefaultKeyDir = "/etc/wireguard"

var ErrNoHostKey = errors.New("hypermesh tunnel: host WireGuard private key missing")

// GenerateKeypair returns a WireGuard X25519 pair, standard base64 (44 chars).
func GenerateKeypair() (privB64, pubB64 string, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(key.Bytes()),
		base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()),
		nil
}

func PublicFromPrivate(privB64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privB64))
	if err != nil || len(raw) != 32 {
		return "", ErrNoHostKey
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// LoadOrCreatePrivateKey returns this box's WireGuard private key.
// A valid on-disk key is reused across restart and redeploy. Missing or
// invalid contents are replaced. The file is 0600 inside a 0700 directory.
// When this process may chown, both are root-owned. Callers send only pubB64
// to the control plane.
func LoadOrCreatePrivateKey(dir string) (privB64, pubB64 string, err error) {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultKeyDir
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	if err := tightenDir(dir); err != nil {
		return "", "", err
	}
	path := filepath.Join(dir, hostKeyFile)
	if raw, readErr := os.ReadFile(path); readErr == nil {
		candidate := strings.TrimSpace(string(raw))
		if pub, pubErr := PublicFromPrivate(candidate); pubErr == nil {
			if err := tightenKey(path); err != nil {
				return "", "", err
			}
			return candidate, pub, nil
		}
		// Invalid file: fall through and replace it. Do not return the bytes.
	} else if !os.IsNotExist(readErr) {
		return "", "", readErr
	}
	privB64, pubB64, err = GenerateKeypair()
	if err != nil {
		return "", "", err
	}
	if err := writeKeyFile(path, privB64); err != nil {
		return "", "", err
	}
	return privB64, pubB64, nil
}

func tightenDir(dir string) error {
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	chownRoot(dir)
	return nil
}

func tightenKey(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	chownRoot(path)
	return nil
}

// chownRoot is best-effort. The unit runs as root. A non-root test keeps
// the current owner; mode 0600/0700 still excludes group and other.
func chownRoot(path string) {
	_ = os.Chown(path, 0, 0)
}

func writeKeyFile(path, privB64 string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(privB64+"\n"), 0o600); err != nil {
		return err
	}
	if err := tightenKey(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return tightenKey(path)
}
