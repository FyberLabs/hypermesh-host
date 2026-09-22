// Package pull fetches pinned GGUF weights onto the lab host.
//
// Allowlist mirrors AIuditor crawl.py: Hugging Face plus a small set of
// GGUF publishers. Fail closed on anything else. The crawler never
// downloads; only the host agent does.
package pull

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GGUFPublisherAllowlist matches aiuditor_service.crawl.GGUF_PUBLISHER_ALLOWLIST.
var GGUFPublisherAllowlist = map[string]struct{}{
	"bartowski":           {},
	"thebloke":            {},
	"quantfactory":        {},
	"ggml-org":            {},
	"lmstudio-community":  {},
	"fyberlabs":           {},
}

// TrustedDownloadURL turns a source_ref (repo@rev/file or https HF URL) into
// a single allowlisted download URL. Fail closed on untrusted hosts/publishers.
func TrustedDownloadURL(sourceRef string) (string, error) {
	ref := strings.TrimSpace(sourceRef)
	if ref == "" {
		return "", fmt.Errorf("hypermesh: empty source_ref")
	}
	if strings.HasPrefix(strings.ToLower(ref), "https://") {
		return trustedHTTPS(ref)
	}
	return trustedHFRef(ref)
}

func trustedHTTPS(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("hypermesh: bad source_ref url: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if host != "huggingface.co" && host != "hf.co" {
		return "", fmt.Errorf("hypermesh: untrusted source host %q", host)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("hypermesh: untrusted source path")
	}
	publisher := strings.ToLower(parts[0])
	if _, ok := GGUFPublisherAllowlist[publisher]; !ok {
		return "", fmt.Errorf("hypermesh: untrusted publisher %q", publisher)
	}
	u.Scheme = "https"
	u.Host = "huggingface.co"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// trustedHFRef parses owner/name@revision/file.gguf.
func trustedHFRef(ref string) (string, error) {
	at := strings.Index(ref, "@")
	if at <= 0 || at == len(ref)-1 {
		return "", fmt.Errorf("hypermesh: source_ref must be repo@rev/file")
	}
	repo := ref[:at]
	rest := ref[at+1:]
	slash := strings.Index(rest, "/")
	if slash <= 0 || slash == len(rest)-1 {
		return "", fmt.Errorf("hypermesh: source_ref must be repo@rev/file")
	}
	revision := rest[:slash]
	fileName := rest[slash+1:]
	publisher := strings.ToLower(strings.Split(repo, "/")[0])
	if _, ok := GGUFPublisherAllowlist[publisher]; !ok {
		return "", fmt.Errorf("hypermesh: untrusted publisher %q", publisher)
	}
	if !strings.Contains(repo, "/") || strings.ContainsAny(repo, " \t\n") {
		return "", fmt.Errorf("hypermesh: bad source repo")
	}
	if revision == "" || fileName == "" || !strings.HasSuffix(strings.ToLower(fileName), ".gguf") {
		return "", fmt.Errorf("hypermesh: source_ref must end in .gguf")
	}
	return fmt.Sprintf(
		"https://huggingface.co/%s/resolve/%s/%s",
		repo,
		url.PathEscape(revision),
		strings.TrimPrefix(fileName, "/"),
	), nil
}

// Fetcher downloads bytes. Tests inject a stub; production uses HTTPGet.
type Fetcher func(ctx context.Context, url string) (io.ReadCloser, error)

// HTTPGet is the default network fetcher.
func HTTPGet(client *http.Client) Fetcher {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	return func(ctx context.Context, rawURL string) (io.ReadCloser, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("hypermesh: fetch status %d", resp.StatusCode)
		}
		return resp.Body, nil
	}
}

// ToFile downloads url into dest and verifies the sha256 pin.
// Fail closed on mismatch. Does not leave a mismatched file in place.
func ToFile(ctx context.Context, dest, rawURL, pin string, fetch Fetcher) error {
	want := strings.ToLower(strings.TrimSpace(pin))
	if !strings.HasPrefix(want, "sha256:") || len(want) < len("sha256:")+64 {
		return fmt.Errorf("hypermesh: pinned hash required")
	}
	want = want[len("sha256:"):]
	if fetch == nil {
		fetch = HTTPGet(nil)
	}
	body, err := fetch(ctx, rawURL)
	if err != nil {
		return err
	}
	defer body.Close()

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "lab-pull-*.gguf")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	sum := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, sum), body)
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpName)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return closeErr
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if got != want {
		_ = os.Remove(tmpName)
		return fmt.Errorf("hypermesh: artifact hash mismatch")
	}
	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
