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
	"github.com/FyberLabs/hypermesh-host/internal/buildinfo"
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
 TargetOS string
	Artifact string
	MatchInstaller bool
	StateFile string
	ChannelRoot string
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
	flags.StringVar(&opt.Artifact, "artifact", "deploy", "signed executable: deploy or agent")
	flags.BoolVar(&opt.MatchInstaller, "match-installer", false, "require the signed release to match this installer build, version and commit")
	flags.StringVar(&opt.BaseURL, "release-url", "", "HTTPS directory containing release assets")
	flags.StringVar(&opt.ChannelRoot, "channel-root", "", "discover channels/<channel>.json under this HTTPS root; requires --state-file")
	flags.StringVar(&opt.Channel, "channel", "", "expected channel: test or prod")
	flags.StringVar(&opt.Arch, "arch", runtime.GOARCH, "target architecture: arm64 or amd64")
 flags.StringVar(&opt.TargetOS, "os", runtime.GOOS, "target OS: linux or darwin (Apple silicon)")
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
	fmt.Fprintf(out, "Verified %s/%s %s: channel=%s build=%d commit=%s\n", downloadOS(opt), opt.Arch, downloadArtifact(opt), meta.Channel, meta.Build, meta.Commit)
	return 0
}

func downloadArtifact(opt downloadOptions) string {
	if opt.Artifact == "" { return "deploy" } // Preserve existing callers.
	return opt.Artifact
}

func downloadOS(opt downloadOptions) string {
 if opt.TargetOS=="" {return "linux"} // Existing internal Linux installation plans.
 return opt.TargetOS
}

func validateDownload(opt downloadOptions) error {
	if artifact := downloadArtifact(opt); artifact != "deploy" && artifact != "agent" {
		return fmt.Errorf("artifact must be deploy or agent")
	}
	if opt.MatchInstaller && (buildinfo.CurrentBuild() <= 0 ||
		!releaseVersion.MatchString(buildinfo.CurrentVersion()) || !releaseCommit.MatchString(buildinfo.Current())) {
		return fmt.Errorf("match-installer requires a release-built installer with version, build and commit")
	}
	address := opt.BaseURL
	if opt.ChannelRoot != "" {
		if opt.BaseURL != "" || opt.StateFile == "" {
			return fmt.Errorf("channel discovery requires --state-file and cannot be combined with --release-url")
		}
		address = opt.ChannelRoot
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("release URL must be HTTPS without credentials, query or fragment")
	}
	if opt.Channel != "test" && opt.Channel != "prod" { return fmt.Errorf("expected channel must be test or prod") }
	if opt.Arch != "arm64" && opt.Arch != "amd64" { return fmt.Errorf("target architecture must be arm64 or amd64") }
	if osName:=downloadOS(opt); (osName!="linux" && osName!="darwin") || (osName=="darwin" && opt.Arch!="arm64") { return fmt.Errorf("target must be Linux ARM64/AMD64 or Darwin ARM64") }
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
	var advertised *releaseMetadata
	if opt.ChannelRoot != "" {
		var err error
		base, advertised, err = discoverRelease(ctx, opt, fetch)
		if err != nil { return meta, err }
	}
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
	if advertised != nil && meta != *advertised {
		return meta, fmt.Errorf("signed release metadata does not match the discovered channel head")
	}
	if opt.MatchInstaller && (meta.Build != int64(buildinfo.CurrentBuild()) ||
		meta.Version != buildinfo.CurrentVersion() || meta.Commit != buildinfo.Current()) {
		return meta, fmt.Errorf("signed release does not match this installer; select its immutable release URL")
	}
	name := fmt.Sprintf("hypermesh-%s_%s_%s_%s", downloadArtifact(opt), meta.Version, downloadOS(opt), opt.Arch)
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
	if err != nil { return fmt.Errorf("cannot stage verified executable in output directory") }
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

// Channel pointers locate a candidate; only signed metadata authorizes it.
// Derive paths from validated identity fields, never URLs in the pointer.
func discoverRelease(ctx context.Context, opt downloadOptions, fetch artifactFetch) (string, *releaseMetadata, error) {
	root := strings.TrimRight(opt.ChannelRoot, "/") + "/"
	raw, err := fetch(ctx, root+"channels/"+opt.Channel+".json", 1<<20)
	if err != nil { return "", nil, err }
	var candidate releaseMetadata
	if err := json.Unmarshal(raw, &candidate); err != nil {
		return "", nil, fmt.Errorf("invalid channel discovery document")
	}
	if candidate.Channel != opt.Channel || candidate.Build <= 0 || candidate.Build < opt.MinimumBuild ||
		!releaseVersion.MatchString(candidate.Version) || !releaseCommit.MatchString(candidate.Commit) {
		return "", nil, fmt.Errorf("channel head does not meet channel, build or metadata requirements")
	}
	expected := candidate.Version
	if candidate.Channel == "test" {
		expected = fmt.Sprintf("%d-%s", candidate.Build, candidate.Commit[:12])
	}
	if candidate.ReleaseID != expected { return "", nil, fmt.Errorf("invalid channel release ID") }
	return root+opt.Channel+"/"+candidate.ReleaseID+"/", &candidate, nil
}
