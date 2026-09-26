package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const maxAsset = 64 << 20

// Config is one update check. Token is a GitHub read token for this
// private repo. It is never logged.
type Config struct {
	Channel        string
	CurrentVersion string
	CurrentCommit  string
	Arch           string
	BinaryPath     string
	PubKeyPath     string
	MarkerPath     string
	Token          string
	Busy           bool
	RollbackWindow time.Duration
	Now            time.Time
}

// Hooks replace network, gpg, install, and restart in tests.
// Nil fields use the production implementations.
type Hooks struct {
	Fetch    Fetcher
	Verify   func(checksums, sig, pubkey []byte) error
	Install  func(dest string, payload []byte, previous string) (string, error)
	Restart  func(ctx context.Context) error
	ReadFile func(path string) ([]byte, error)
}

// Fetcher downloads a URL. accept is the Accept header.
type Fetcher interface {
	Get(ctx context.Context, url, accept string) ([]byte, error)
}

// Tick checks the channel. A verification failure returns a reject
// plan and a nil error. Transport failures return an error so the
// agent keeps running and tries again later.
func Tick(ctx context.Context, cfg Config, h Hooks) (Plan, error) {
	ch, err := ParseChannel(cfg.Channel)
	if err != nil {
		return Plan{Action: ActionReject, Reason: "bad channel"}, err
	}
	if ch == "" {
		return Plan{Action: ActionOff, Reason: "channel unset"}, nil
	}
	cfg.Channel = ch
	now := cfg.Now
	if now.IsZero() {
		now = time.Now()
	}
	window := cfg.RollbackWindow
	if window <= 0 {
		window = 2 * time.Minute
	}
	read := h.ReadFile
	if read == nil {
		read = osRead
	}
	pub, err := read(cfg.PubKeyPath)
	if err != nil || len(pub) == 0 {
		return Plan{Action: ActionReject, Reason: "update pubkey missing"}, nil
	}
	fetch := h.Fetch
	if fetch == nil {
		fetch = HTTPFetcher{Token: cfg.Token}
	}
	api, err := ReleaseAPI(cfg.Channel)
	if err != nil {
		return Plan{}, err
	}
	raw, err := fetch.Get(ctx, api, "application/vnd.github+json")
	if err != nil {
		return Plan{}, err
	}
	var rel ghRelease
	if err := json.Unmarshal(raw, &rel); err != nil {
		return Plan{}, fmt.Errorf("hypermesh update: release document")
	}
	sumsURL, err := assetURL(rel, ChecksumsName)
	if err != nil {
		return Plan{Action: ActionReject, Reason: err.Error()}, nil
	}
	sigURL, err := assetURL(rel, SignatureName)
	if err != nil {
		return Plan{Action: ActionReject, Reason: "release has no checksum signature"}, nil
	}
	manURL, err := assetURL(rel, ManifestName)
	if err != nil {
		return Plan{Action: ActionReject, Reason: "release has no release.json"}, nil
	}
	sums, err := fetch.Get(ctx, sumsURL, "application/octet-stream")
	if err != nil {
		return Plan{}, err
	}
	sig, err := fetch.Get(ctx, sigURL, "application/octet-stream")
	if err != nil {
		return Plan{}, err
	}
	verify := h.Verify
	if verify == nil {
		verify = GPGVerify
	}
	if err := verify(sums, sig, pub); err != nil {
		return Plan{Action: ActionReject, Reason: "checksum signature rejected"}, nil
	}
	manRaw, err := fetch.Get(ctx, manURL, "application/octet-stream")
	if err != nil {
		return Plan{}, err
	}
	if err := CheckHash(sums, ManifestName, manRaw); err != nil {
		return Plan{Action: ActionReject, Reason: "release.json checksum rejected"}, nil
	}
	var man Manifest
	if err := json.Unmarshal(manRaw, &man); err != nil {
		return Plan{Action: ActionReject, Reason: "release.json rejected"}, nil
	}
	plan := Decide(cfg.Channel, cfg.CurrentVersion, cfg.CurrentCommit, man, cfg.Busy)
	if plan.Action != ActionInstall {
		return plan, nil
	}
	binName, err := SelectBinary(string(sums), cfg.Arch)
	if err != nil {
		return Plan{Action: ActionReject, Reason: "no binary for this arch"}, nil
	}
	binURL, err := assetURL(rel, binName)
	if err != nil {
		return Plan{Action: ActionReject, Reason: "binary asset missing"}, nil
	}
	bin, err := fetch.Get(ctx, binURL, "application/octet-stream")
	if err != nil {
		return Plan{}, err
	}
	if err := CheckHash(sums, binName, bin); err != nil {
		return Plan{Action: ActionReject, Version: plan.Version, Commit: plan.Commit, Reason: "binary checksum rejected"}, nil
	}
	install := h.Install
	if install == nil {
		install = InstallFile
	}
	previous := cfg.CurrentVersion
	if strings.TrimSpace(previous) == "" {
		previous = cfg.CurrentCommit
	}
	backup, err := install(cfg.BinaryPath, bin, previous)
	if err != nil {
		return Plan{}, err
	}
	if err := WriteMarker(cfg.MarkerPath, Marker{
		Binary:   cfg.BinaryPath,
		Backup:   backup,
		Version:  plan.Version,
		Deadline: now.Add(window),
	}); err != nil {
		return Plan{}, err
	}
	restart := h.Restart
	if restart == nil {
		restart = RestartAgent
	}
	if err := restart(ctx); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func assetURL(rel ghRelease, name string) (string, error) {
	for _, a := range rel.Assets {
		if a.Name == name && strings.HasPrefix(a.URL, "https://") {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("%w: release has no %s", ErrRejected, name)
}

func osRead(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// HTTPFetcher downloads GitHub API URLs. The token is sent only as a
// header and is not included in errors.
type HTTPFetcher struct {
	Token  string
	Client *http.Client
}

func (h HTTPFetcher) Get(ctx context.Context, rawURL, accept string) ([]byte, error) {
	if strings.TrimSpace(h.Token) == "" {
		return nil, fmt.Errorf("hypermesh update: github token is required")
	}
	if !strings.HasPrefix(rawURL, "https://") {
		return nil, fmt.Errorf("hypermesh update: download url must be https")
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("hypermesh update: download")
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+h.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "hypermesh-agent")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hypermesh update: download failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAsset+1))
	if err != nil {
		return nil, fmt.Errorf("hypermesh update: download failed")
	}
	if len(body) > maxAsset {
		return nil, fmt.Errorf("hypermesh update: asset too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("hypermesh update: download status %d", resp.StatusCode)
	}
	return body, nil
}

// RestartAgent restarts hypermesh-agent and nothing else.
func RestartAgent(ctx context.Context) error {
	return restart(ctx, "systemctl", "restart", AgentUnit)
}

func restart(ctx context.Context, name string, args ...string) error {
	if !restartAllowed(name, args) {
		return fmt.Errorf("hypermesh update: refusing to run %s", name)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hypermesh update: systemctl restart %s", AgentUnit)
	}
	return nil
}

func restartAllowed(name string, args []string) bool {
	return name == "systemctl" && len(args) == 2 && args[0] == "restart" && args[1] == AgentUnit
}
