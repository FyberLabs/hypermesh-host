package installer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifiedDownload(t *testing.T) {
	for _, mode := range []string{"valid", "signature", "metadata", "payload", "rollback", "channel", "exists", "persisted-rollback", "discovery", "discovery-mismatch", "discovery-rollback"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			opt := downloadOptions{BaseURL: "https://example.invalid/release", Channel: "test", Arch: "arm64",
				MinimumBuild: 21, Output: filepath.Join(dir, "deploy")}
			meta := releaseMetadata{Channel: "test", Version: "0.0.0", Commit: strings.Repeat("a", 40),
				Build: 21, ReleaseID: "21-"+strings.Repeat("a", 12)}
			raw, _ := json.Marshal(meta)
			pointer := append([]byte(nil), raw...)
			if strings.HasPrefix(mode, "discovery") {
				opt.ChannelRoot = "https://example.invalid/root"
				opt.BaseURL = ""
				opt.StateFile = filepath.Join(dir, "build-state.json")
				if mode == "discovery-mismatch" {
					other := meta
					other.Version = "0.0.1"
					pointer, _ = json.Marshal(other)
				}
				if mode == "discovery-rollback" {
					_, commit, unlock, err := lockBuildState(opt.StateFile)
					if err != nil { t.Fatal(err) }
					if err := commit(22); err != nil { t.Fatal(err) }
					unlock()
				}
			}
			payload := []byte("verified binary bytes")
			name := "hypermesh-deploy_0.0.0_linux_arm64"
			sums := []byte(fmt.Sprintf("%x  release.json\n%x  %s\n", sha256.Sum256(raw), sha256.Sum256(payload), name))
			switch mode {
			case "metadata": raw = append(raw, ' ')
			case "payload": payload = []byte("tampered")
			case "rollback": opt.MinimumBuild = 22
			case "persisted-rollback":
				opt.StateFile = filepath.Join(dir, "state.json")
				_, commit, unlock, err := lockBuildState(opt.StateFile)
				if err != nil { t.Fatal(err) }
				if err := commit(22); err != nil { t.Fatal(err) }
				unlock()
			case "channel": opt.Channel = "prod"
			case "exists":
				if err := os.WriteFile(opt.Output, []byte("keep"), 0o600); err != nil { t.Fatal(err) }
			}
			verified := false
			fetch := func(_ context.Context, address string, _ int64) ([]byte, error) {
				switch filepath.Base(address) {
				case "test.json": return pointer, nil
				case "checksums.txt": return sums, nil
				case "checksums.txt.sig": return []byte("signature"), nil
				case "release.json":
					if !verified { t.Fatal("metadata used before signature verification") }
					return raw, nil
				case name: return payload, nil
				default: return nil, fmt.Errorf("unexpected artifact")
				}
			}
			verify := func(_, _, _ []byte, _ []string) error {
				if mode == "signature" { return fmt.Errorf("untrusted") }
				verified = true
				return nil
			}
			_, err := downloadCLI(context.Background(), opt, fetch, verify)
			if mode == "valid" || mode == "discovery" {
				if err != nil { t.Fatal(err) }
				got, err := os.ReadFile(opt.Output)
				if err != nil || string(got) != string(payload) { t.Fatal("wrong output") }
				info, _ := os.Stat(opt.Output)
				if info.Mode().Perm() != 0o700 { t.Fatal("wrong executable permissions") }
			} else {
				if err == nil { t.Fatal("accepted invalid download") }
				got, readErr := os.ReadFile(opt.Output)
				if mode == "exists" {
					if readErr != nil || string(got) != "keep" { t.Fatal("overwrote existing output") }
				} else if !os.IsNotExist(readErr) { t.Fatal("published rejected bytes") }
			}
			files, _ := filepath.Glob(filepath.Join(dir, ".hypermesh-download-*"))
			if len(files) != 0 { t.Fatal("left staged files") }
		})
	}
}

func TestDownloadURLPolicy(t *testing.T) {
	for _, address := range []string{"http://example.com", "https://u:p@example.com", "https://example.com/?secret=x", "https://example.com/#x"} {
		if validateDownload(downloadOptions{BaseURL: address, Channel: "test", Arch: "arm64", Output: "x"}) == nil {
			t.Fatalf("accepted %q", address)
		}
	}
}

func TestDownloadTransferLimits(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" { w.WriteHeader(404); return }
		_, _ = w.Write([]byte("12345"))
	}))
	defer server.Close()
	for _, tc := range []struct{ path string; limit int64; pass bool }{
		{"/ok", 5, true}, {"/ok", 4, false}, {"/missing", 5, false},
	} {
		_, err := fetchWithClient(context.Background(), server.URL+tc.path, tc.limit, server.Client())
		if (err == nil) != tc.pass { t.Fatalf("path=%s limit=%d err=%v", tc.path, tc.limit, err) }
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fetchWithClient(ctx, server.URL, 5, server.Client()); err == nil { t.Fatal("ignored cancellation") }
}

func TestDiscoveryRejectsUnsafePointers(t *testing.T) {
	opt := downloadOptions{ChannelRoot: "https://example.invalid/root", Channel: "test", MinimumBuild: 10}
	for _, raw := range []string{
		"{}",
		"{\"channel\":\"test\",\"build\":true}",
		"{\"channel\":\"prod\",\"build\":20}",
		"{\"channel\":\"test\",\"build\":20,\"version\":\"0.0.0\",\"commit\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"release_id\":\"../../elsewhere\"}",
	} {
		fetch := func(context.Context, string, int64) ([]byte, error) { return []byte(raw), nil }
		if _, _, err := discoverRelease(context.Background(), opt, fetch); err == nil {
			t.Fatalf("accepted pointer %s", raw)
		}
	}
}
