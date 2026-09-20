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

var ErrNoHostKey = errors.New("hypermesh tunnel: host WireGuard private key missing")

// GenerateKeypair returns a WireGuard X25519 pair, base64-encoded.
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

// LoadOrCreatePrivateKey keeps the host private key on this box only.
func LoadOrCreatePrivateKey(dir string) (privB64, pubB64 string, err error) {
	if strings.TrimSpace(dir) == "" {
		dir = "/etc/wireguard"
	}
	path := filepath.Join(dir, hostKeyFile)
	if raw, readErr := os.ReadFile(path); readErr == nil {
		privB64 = strings.TrimSpace(string(raw))
		pubB64, err = PublicFromPrivate(privB64)
		return privB64, pubB64, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	privB64, pubB64, err = GenerateKeypair()
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, []byte(privB64+"\n"), 0o600); err != nil {
		return "", "", err
	}
	return privB64, pubB64, nil
}
