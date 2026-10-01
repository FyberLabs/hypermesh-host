package installer

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/update"
)

type releaseMetadata struct {
	Channel string `json:"channel"`
	Version string `json:"version"`
	Commit string `json:"commit"`
	Build int64 `json:"build"`
	ReleaseID string `json:"release_id"`
}

type downloadOptions struct {
	BaseURL, Channel, Arch, Output string
	StateFile string
	MinimumBuild int64
	Key []byte
	Fingerprints []string
}

type artifactFetch func(context.Context, string, int64) ([]byte, error)
type signatureCheck func([]byte, []byte, []byte, []string) error

var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
var releaseCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

func RunDownload(ctx context.Context, args []string, out, diagnostic io.Writer) int {
	flags := flag.NewFlagSet("download", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	var opt downloadOptions
	var keyPath, fingerprints string
	flags.StringVar(&opt.BaseURL, "release-url", "", "HTTPS directory containing release assets")
	flags.StringVar(&opt.Channel, "channel", "", "expected channel: test or prod")
	flags.StringVar(&opt.Arch, "arch", runtime.GOARCH, "target Linux architecture: arm64 or amd64")
	flags.StringVar(&opt.Output, "output", "", "new output path; existing files are never overwritten")
	flags.StringVar(&opt.StateFile, "state-file", "", "optional persistent cross-channel build floor in an owner-controlled directory")
	flags.Int64Var(&opt.MinimumBuild, "min-build", -1, "required minimum accepted signed build")
	flags.StringVar(&keyPath, "trusted-key", "", "independently trusted armored public key file")
	flags.StringVar(&fingerprints, "fingerprints", "", "trusted signing fingerprints")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp { return 0 }
		return 2
	}
	fps, err := update.ParseFingerprints(fingerprints)
	if err != nil || len(fps) == 0 || flags.NArg() != 0 || keyPath == "" {
		fmt.Fprintln(diagnostic, "a trusted key and valid fingerprints are required; no positional arguments")
		return 2
	}
	opt.Fingerprints = fps
	if err := validateDownload(opt); err != nil {
		fmt.Fprintln(diagnostic, err)
		return 2
	}
	file, err := os.Open(keyPath)
	if err != nil {
		fmt.Fprintln(diagnostic, "cannot read trusted key")
		return 1
	}
	opt.Key, err = io.ReadAll(io.LimitReader(file, (1<<20)+1))
	_ = file.Close()
	if err != nil || len(opt.Key) == 0 || len(opt.Key) > 1<<20 {
		fmt.Fprintln(diagnostic, "trusted key is empty, unreadable or too large")
		return 1
	}
	meta, err := downloadCLI(ctx, opt, fetchArtifact, update.GPGVerify)
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	fmt.Fprintf(out, "Verified Linux/%s CLI: channel=%s build=%d commit=%s\n", opt.Arch, meta.Channel, meta.Build, meta.Commit)
	return 0
}

func validateDownload(opt downloadOptions) error {
	u, err := url.Parse(opt.BaseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("release URL must be HTTPS without credentials, query or fragment")
	}
	if opt.Channel != "test" && opt.Channel != "prod" { return fmt.Errorf("expected channel must be test or prod") }
	if opt.Arch != "arm64" && opt.Arch != "amd64" { return fmt.Errorf("target architecture must be arm64 or amd64") }
	if opt.MinimumBuild < 0 || opt.Output == "" { return fmt.Errorf("explicit --min-build and --output are required") }
	return nil
}

func downloadCLI(ctx context.Context, opt downloadOptions, fetch artifactFetch, verify signatureCheck) (releaseMetadata, error) {
	var meta releaseMetadata
	if err := validateDownload(opt); err != nil { return meta, err }
	var persist func(int64) error
	if opt.StateFile != "" {
		statePath, err := filepath.Abs(opt.StateFile)
		if err != nil { return meta, fmt.Errorf("invalid state path") }
		outputPath, err := filepath.Abs(opt.Output)
		if err != nil || outputPath == statePath || outputPath == statePath+".lock" {
			return meta, fmt.Errorf("output and state paths must be distinct")
		}
		floor, commit, unlock, err := lockBuildState(statePath)
		if err != nil { return meta, err }
		defer unlock()
		persist = commit
		if floor > opt.MinimumBuild { opt.MinimumBuild = floor }
	}
	base := strings.TrimRight(opt.BaseURL, "/") + "/"
	sums, err := fetch(ctx, base+"checksums.txt", 1<<20)
	if err != nil { return meta, err }
	sig, err := fetch(ctx, base+"checksums.txt.sig", 1<<20)
	if err != nil { return meta, err }
	if err := verify(sums, sig, opt.Key, opt.Fingerprints); err != nil {
		return meta, fmt.Errorf("release checksum signature is not trusted")
	}
	raw, err := fetch(ctx, base+"release.json", 64<<10)
	if err != nil { return meta, err }
	if err := update.CheckHash(sums, "release.json", raw); err != nil { return meta, err }
	if err := json.Unmarshal(raw, &meta); err != nil { return meta, fmt.Errorf("invalid signed release metadata") }
	if meta.Channel != opt.Channel || meta.Build <= 0 || meta.Build < opt.MinimumBuild ||
		!releaseVersion.MatchString(meta.Version) || !releaseCommit.MatchString(meta.Commit) {
		return meta, fmt.Errorf("signed release does not match channel, minimum build or metadata requirements")
	}
	expectedID := meta.Version
	if meta.Channel == "test" { expectedID = fmt.Sprintf("%d-%s", meta.Build, meta.Commit[:12]) }
	if meta.ReleaseID != expectedID { return meta, fmt.Errorf("signed release ID is inconsistent") }
	name := fmt.Sprintf("hypermesh-deploy_%s_linux_%s", meta.Version, opt.Arch)
	payload, err := fetch(ctx, base+name, 128<<20)
	if err != nil { return meta, err }
	if err := update.CheckHash(sums, name, payload); err != nil { return meta, err }
	if err := ctx.Err(); err != nil { return meta, fmt.Errorf("download cancelled") }
	// Persist before exposing executable bytes: a crash can advance the floor
	// without an output, but cannot expose a newer binary with an older floor.
	if persist != nil {
		if err := persist(meta.Build); err != nil { return meta, err }
	}
	if err := writeVerifiedCLI(opt.Output, payload); err != nil { return meta, err }
	return meta, nil
}

// Stage privately, then link atomically without replacing any existing path.
func writeVerifiedCLI(destination string, payload []byte) error {
	file, err := os.CreateTemp(filepath.Dir(destination), ".hypermesh-download-")
	if err != nil { return fmt.Errorf("cannot stage verified CLI in output directory") }
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(payload); err != nil { _ = file.Close(); return err }
	if err := file.Chmod(0o700); err != nil { _ = file.Close(); return err }
	if err := file.Sync(); err != nil { _ = file.Close(); return err }
	if err := file.Close(); err != nil { return err }
	if err := os.Link(name, destination); err != nil {
		return fmt.Errorf("cannot create output: path already exists or filesystem does not support atomic publication")
	}
	return nil
}

func fetchArtifact(ctx context.Context, address string, limit int64) ([]byte, error) {
	client := &http.Client{Timeout: 2*time.Minute}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.User != nil || len(via) >= 3 {
			return fmt.Errorf("redirect rejected")
		}
		return nil
	}
	return fetchWithClient(ctx, address, limit, client)
}

func fetchWithClient(ctx context.Context, address string, limit int64, client *http.Client) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil { return nil, fmt.Errorf("invalid artifact request") }
	response, err := client.Do(req)
	if err != nil { return nil, fmt.Errorf("artifact download failed; check HTTPS connectivity") }
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK { return nil, fmt.Errorf("artifact download returned HTTP %d", response.StatusCode) }
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(data) == 0 || int64(len(data)) > limit {
		return nil, fmt.Errorf("artifact empty, unreadable or exceeds size limit")
	}
	return data, nil
}
