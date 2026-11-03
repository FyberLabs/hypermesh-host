package installer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifiedDarwinServiceDownloads(t *testing.T) {
	for _, artifact := range []string{"darwin-agent-plist", "darwin-wg-plist", "darwin-env"} {
		for _, mode := range []string{"valid", "signature", "payload", "unsigned-file", "missing-file", "rollback", "persisted-rollback", "exists"} {
			t.Run(artifact+"/"+mode, func(t *testing.T) {
				name := darwinServiceFile(artifact)
				payload, err := os.ReadFile(filepath.Join("..", "..", "deploy", "mac", name))
				if err != nil {
					t.Fatal(err)
				}
				opt := downloadOptions{Artifact: artifact, TargetOS: "darwin", Arch: "arm64",
					BaseURL: "https://example.invalid/release", Channel: "test", MinimumBuild: 21,
					Output: filepath.Join(t.TempDir(), name)}
				meta := releaseMetadata{Channel: "test", Version: "0.0.21", Build: 21,
					Commit: strings.Repeat("a", 40), ReleaseID: "21-" + strings.Repeat("a", 12)}
				raw, _ := json.Marshal(meta)
				sums := []byte(fmt.Sprintf("%x  release.json\n%x  %s\n", sha256.Sum256(raw), sha256.Sum256(payload), name))
				if mode == "unsigned-file" {
					sums = []byte(fmt.Sprintf("%x  release.json\n", sha256.Sum256(raw)))
				}
				if mode == "rollback" {
					opt.MinimumBuild = 22
				}
				if mode == "persisted-rollback" {
					opt.StateFile = filepath.Join(t.TempDir(), "release-state.json")
					_, persist, unlock, err := lockBuildState(opt.StateFile)
					if err != nil {
						t.Fatal(err)
					}
					if err := persist(22); err != nil {
						t.Fatal(err)
					}
					unlock()
				}
				if mode == "exists" {
					if err := os.WriteFile(opt.Output, []byte("preserve owner edits"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				verified := false
				fetch := func(_ context.Context, address string, limit int64) ([]byte, error) {
					switch filepath.Base(address) {
					case "checksums.txt":
						return sums, nil
					case "checksums.txt.sig":
						return []byte("signature"), nil
					case "release.json":
						if !verified {
							t.Fatal("used metadata before signature verification")
						}
						return raw, nil
					case name:
						if limit != 64<<10 {
							t.Fatalf("service data limit = %d", limit)
						}
						if mode == "missing-file" {
							return nil, fmt.Errorf("artifact download returned HTTP 404")
						}
						if mode == "payload" {
							return append(append([]byte(nil), payload...), 'x'), nil
						}
						return payload, nil
					default:
						return nil, fmt.Errorf("unexpected download %s", address)
					}
				}
				verify := func(_, _, _ []byte, _ []string) error {
					if mode == "signature" {
						return fmt.Errorf("untrusted signature")
					}
					verified = true
					return nil
				}
				_, err = downloadCLI(context.Background(), opt, fetch, verify)
				if mode == "valid" {
					if err != nil {
						t.Fatal(err)
					}
					got, err := os.ReadFile(opt.Output)
					if err != nil || string(got) != string(payload) {
						t.Fatalf("wrong verified file: %v", err)
					}
					info, err := os.Stat(opt.Output)
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatalf("service data must be private and non-executable: %v", err)
					}
				} else {
					if err == nil {
						t.Fatal("unsafe service download was accepted")
					}
					got, statErr := os.ReadFile(opt.Output)
					if mode == "exists" {
						if statErr != nil || string(got) != "preserve owner edits" {
							t.Fatal("overwrote existing service file")
						}
					} else if !os.IsNotExist(statErr) {
						t.Fatalf("exposed unverified output: %v", statErr)
					}
				}
			})
		}
	}
}

func TestDarwinServiceFilesRejectOtherTargetsBeforeFetching(t *testing.T) {
	for _, artifact := range []string{"darwin-agent-plist", "darwin-wg-plist", "darwin-env"} {
		for _, target := range []struct{ os, arch string }{{"linux", "arm64"}, {"linux", "amd64"}, {"darwin", "amd64"}} {
			opt := downloadOptions{Artifact: artifact, TargetOS: target.os, Arch: target.arch}
			_, err := downloadCLI(context.Background(), opt, func(context.Context, string, int64) ([]byte, error) {
				t.Fatal("invalid target reached network")
				return nil, nil
			}, nil)
			if err == nil || !strings.Contains(err.Error(), "Darwin service files require") {
				t.Fatalf("accepted %s/%s %s: %v", target.os, target.arch, artifact, err)
			}
		}
	}
}
