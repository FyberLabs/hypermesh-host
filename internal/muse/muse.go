// Package muse is the supervisor that restarts a local runtime.
// One process, three placements:
//
//   - outside: not on the network
//   - host: enrolls and heartbeats as host type supervisor and refuses lease_start
//   - renter: talks to the chat gateway POST /v1/chat/completions
//
// The renter API key and the one-time enroll token are not stored in
// muse.json. The key is read from the environment or a systemd credential.
// The enroll token is dropped after enroll succeeds. Host WireGuard keys
// stay on the host. This package does not certify a host.
package muse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/agent"
	"github.com/FyberLabs/hypermesh-host/internal/catalog"
	"github.com/FyberLabs/hypermesh-host/internal/logsafe"
)

const (
	PlacementOutside = "outside"
	PlacementHost    = "host"
	PlacementRenter  = "renter"

	// ChatCompletionsPath is the renter door on the chat gateway.
	ChatCompletionsPath = "/v1/chat/completions"
	// ControlPlaneStubPath is the always-409 control-plane route.
	// Renter chat must not call it.
	ControlPlaneStubPath = "/api/v1/hypermesh/renter/chat/completions"

	DefaultChatBase = "https://chat.test.hyperme.sh"
	credFile        = "muse.json"
	// RenterCredential is the systemd LoadCredential= name.
	RenterCredential = "renter_api_key"
)

// ParsePlacement accepts outside, host, and renter.
func ParsePlacement(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case PlacementOutside:
		return PlacementOutside, nil
	case PlacementHost:
		return PlacementHost, nil
	case PlacementRenter:
		return PlacementRenter, nil
	default:
		return "", fmt.Errorf("muse: placement must be outside, host, or renter")
	}
}

// Credentials are what this process holds in memory.
// APIKey and EnrollToken are not written to muse.json.
type Credentials struct {
	Placement   string `json:"placement"`
	TenantID    string `json:"tenant_id,omitempty"`
	LeaseID     string `json:"lease_id,omitempty"`
	ChatBase    string `json:"chat_base,omitempty"`
	APIKey      string `json:"-"`
	EnrollToken string `json:"-"`
}

type diskCreds struct {
	Placement   string `json:"placement"`
	TenantID    string `json:"tenant_id,omitempty"`
	LeaseID     string `json:"lease_id,omitempty"`
	ChatBase    string `json:"chat_base,omitempty"`
	EnrollToken string `json:"enroll_token,omitempty"`
	APIKey      string `json:"api_key,omitempty"`
}

// Runtime is one start of the local AI process. The supervisor calls it
// again after it returns, until ctx is cancelled.
type Runtime func(context.Context) error

// Supervisor is the one image. Placement selects the door.
type Supervisor struct {
	Placement string
	StateDir  string
	Creds     Credentials
	Runtime   Runtime
	Backoff   time.Duration
	Agent     agent.Config
	HTTP      *http.Client
	Log       *slog.Logger
	// ListenAddr is the local door (renter and host forward). Empty skips listen.
	ListenAddr string

	mu            sync.Mutex
	tokenFromDisk string
	keepDiskToken bool
}

// LoadRenterKey reads the renter API key from the environment, a file,
// or $CREDENTIALS_DIRECTORY/renter_api_key. It is not taken from argv.
// Set sources must match. An empty result is allowed; renter chat
// checks for a key before it posts.
func LoadRenterKey(envValue, filePath, credentialsDir string) (string, error) {
	return oneLine("renter API key", envValue, filePath, credentialFile(credentialsDir))
}

// LoadEnrollToken reads the one-time token from the environment or a
// file. It is not taken from argv.
func LoadEnrollToken(envValue, filePath string) (string, error) {
	return oneLine("enroll token", envValue, filePath, "")
}

func credentialFile(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, RenterCredential)
}

func oneLine(name, envValue, filePath, extraPath string) (string, error) {
	envValue = strings.TrimSpace(envValue)
	vals := []string{}
	if envValue != "" {
		if strings.Contains(envValue, "\n") || strings.Contains(envValue, "\x00") {
			return "", fmt.Errorf("muse: %s is not a single line", name)
		}
		vals = append(vals, envValue)
	}
	for _, path := range []string{strings.TrimSpace(filePath), strings.TrimSpace(extraPath)} {
		if path == "" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			if extraPath != "" && path == extraPath && os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("muse: %s file: %w", name, err)
		}
		line := strings.TrimSpace(string(b))
		if line == "" {
			continue
		}
		if strings.Contains(line, "\n") || strings.Contains(line, "\x00") {
			return "", fmt.Errorf("muse: %s is not a single line", name)
		}
		vals = append(vals, line)
	}
	if len(vals) == 0 {
		return "", nil
	}
	for _, v := range vals[1:] {
		if v != vals[0] {
			return "", fmt.Errorf("muse: %s values differ", name)
		}
	}
	return vals[0], nil
}

// SaveCredentials writes muse.json at mode 0600 without the renter key.
// A disk enroll token is kept only until enroll succeeds.
func (s *Supervisor) SaveCredentials() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Supervisor) saveLocked() error {
	if strings.TrimSpace(s.StateDir) == "" {
		return fmt.Errorf("muse: state dir is required")
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.StateDir, 0o700); err != nil {
		return err
	}
	disk := diskCreds{
		Placement: s.Creds.Placement,
		TenantID:  s.Creds.TenantID,
		LeaseID:   s.Creds.LeaseID,
		ChatBase:  s.Creds.ChatBase,
	}
	if s.keepDiskToken {
		disk.EnrollToken = s.tokenFromDisk
	}
	body, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	path := filepath.Join(s.StateDir, credFile)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// LoadCredentials reads muse.json. Secrets in an old file are returned
// in memory only.
func LoadCredentials(stateDir string) (Credentials, error) {
	disk, err := readDisk(stateDir)
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{
		Placement:   disk.Placement,
		TenantID:    disk.TenantID,
		LeaseID:     disk.LeaseID,
		ChatBase:    disk.ChatBase,
		APIKey:      disk.APIKey,
		EnrollToken: disk.EnrollToken,
	}, nil
}

func readDisk(stateDir string) (diskCreds, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, credFile))
	if err != nil {
		return diskCreds{}, err
	}
	var disk diskCreds
	if err := json.Unmarshal(b, &disk); err != nil {
		return diskCreds{}, err
	}
	return disk, nil
}

// prepareState merges muse.json into memory and rewrites the file
// without the renter key. A host that still needs a token found on
// disk keeps that token until enroll succeeds.
func (s *Supervisor) prepareState() error {
	place, err := ParsePlacement(s.Placement)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Placement = place
	disk, err := readDisk(s.StateDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if strings.TrimSpace(s.Creds.TenantID) == "" {
			s.Creds.TenantID = disk.TenantID
		}
		if strings.TrimSpace(s.Creds.LeaseID) == "" {
			s.Creds.LeaseID = disk.LeaseID
		}
		if strings.TrimSpace(s.Creds.ChatBase) == "" {
			s.Creds.ChatBase = disk.ChatBase
		}
		if strings.TrimSpace(s.Creds.APIKey) == "" {
			s.Creds.APIKey = disk.APIKey
		}
		if strings.TrimSpace(s.Creds.EnrollToken) == "" && strings.TrimSpace(disk.EnrollToken) != "" {
			s.Creds.EnrollToken = disk.EnrollToken
			s.tokenFromDisk = disk.EnrollToken
		}
	}
	s.Creds.Placement = place
	enrolled := deviceEnrolled(s.StateDir)
	if place != PlacementHost || enrolled {
		s.Creds.EnrollToken = ""
		s.tokenFromDisk = ""
		s.keepDiskToken = false
	} else {
		s.keepDiskToken = s.tokenFromDisk != ""
	}
	return s.saveLocked()
}

func deviceEnrolled(stateDir string) bool {
	b, err := os.ReadFile(filepath.Join(stateDir, "device.json"))
	if err != nil {
		return false
	}
	var st struct {
		DeviceID     string `json:"device_id"`
		DeviceSecret string `json:"device_secret"`
	}
	if json.Unmarshal(b, &st) != nil {
		return false
	}
	return strings.TrimSpace(st.DeviceID) != "" && strings.TrimSpace(st.DeviceSecret) != ""
}

// dropEnrollToken removes the one-time token from memory and from muse.json.
func (s *Supervisor) dropEnrollToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keepDiskToken = false
	s.tokenFromDisk = ""
	s.Creds.EnrollToken = ""
	s.Agent.EnrollToken = ""
	_ = os.Unsetenv("HYPERMESH_ENROLL_TOKEN")
	if strings.TrimSpace(s.StateDir) == "" {
		return
	}
	_ = s.saveLocked()
}

// Run holds non-secret placement config, restarts the local runtime,
// and opens the placement's door. Host placement calls the existing agent.
func (s *Supervisor) Run(ctx context.Context) error {
	if s.Log == nil {
		s.Log = logsafe.New()
	}
	if err := s.prepareState(); err != nil {
		return err
	}
	switch s.Placement {
	case PlacementOutside:
		return Supervise(ctx, s.Runtime, s.backoff())
	case PlacementHost:
		return s.runHost(ctx)
	case PlacementRenter:
		return s.runRenter(ctx)
	default:
		return fmt.Errorf("muse: placement %q", s.Placement)
	}
}

func (s *Supervisor) backoff() time.Duration {
	if s.Backoff > 0 {
		return s.Backoff
	}
	return time.Second
}

// Supervise restarts run until ctx is cancelled.
func Supervise(ctx context.Context, run Runtime, backoff time.Duration) error {
	if run == nil {
		return fmt.Errorf("muse: local runtime is required")
	}
	if backoff <= 0 {
		backoff = time.Second
	}
	for {
		err := run(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = err
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Supervisor) runHost(ctx context.Context) error {
	if s.Runtime == nil {
		return fmt.Errorf("muse: host placement owns the local runtime")
	}
	s.mu.Lock()
	cfg := s.Agent
	cfg.HostType = agent.HostTypeSupervisor
	if strings.TrimSpace(cfg.StateDir) == "" {
		cfg.StateDir = s.StateDir
	}
	if strings.TrimSpace(cfg.TenantID) == "" {
		cfg.TenantID = s.Creds.TenantID
	}
	if strings.TrimSpace(cfg.EnrollToken) == "" {
		cfg.EnrollToken = s.Creds.EnrollToken
	}
	if strings.TrimSpace(cfg.WGKeyDir) == "" && s.StateDir != "" {
		cfg.WGKeyDir = filepath.Join(s.StateDir, "wireguard")
	}
	s.mu.Unlock()
	a, err := agent.New(cfg, s.Log)
	if err != nil {
		return err
	}
	a.OnEnrolled = s.dropEnrollToken
	errCh := make(chan error, 2)
	go func() { errCh <- Supervise(ctx, s.Runtime, s.backoff()) }()
	go func() { errCh <- a.Run(ctx) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		if err == nil || errors.Is(err, context.Canceled) {
			return ctx.Err()
		}
		return err
	}
}

func (s *Supervisor) runRenter(ctx context.Context) error {
	s.mu.Lock()
	if strings.TrimSpace(s.Creds.ChatBase) == "" {
		s.Creds.ChatBase = DefaultChatBase
	}
	s.mu.Unlock()
	if _, err := CompletionsURL(s.Creds.ChatBase); err != nil {
		return err
	}
	errCh := make(chan error, 2)
	if s.Runtime != nil {
		go func() { errCh <- Supervise(ctx, s.Runtime, s.backoff()) }()
	}
	go func() { errCh <- s.serve(ctx) }()
	if strings.TrimSpace(s.ListenAddr) == "" {
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, http.ErrServerClosed) {
			return ctx.Err()
		}
		return err
	}
}

func (s *Supervisor) serve(ctx context.Context) error {
	if strings.TrimSpace(s.ListenAddr) == "" {
		<-ctx.Done()
		return ctx.Err()
	}
	srv := &http.Server{Addr: s.ListenAddr, Handler: s.Handler()}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Handler is the local door. Renter placement forwards to the chat
// gateway. Other placements do not call the control-plane stub.
func (s *Supervisor) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ChatCompletionsPath || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if s.Placement != PlacementRenter {
			http.Error(w, "this placement does not forward renter chat", http.StatusConflict)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		status, raw, err := s.forward(r.Context(), body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
	})
}

func (s *Supervisor) forward(ctx context.Context, body []byte) (int, []byte, error) {
	s.mu.Lock()
	base, key, tenant, lease := s.Creds.ChatBase, s.Creds.APIKey, s.Creds.TenantID, s.Creds.LeaseID
	s.mu.Unlock()
	return postChat(ctx, s.HTTP, base, key, tenant, lease, body)
}

// PostChat sends one completion to the chat gateway.
func (s *Supervisor) PostChat(ctx context.Context, content string) (json.RawMessage, error) {
	model := catalog.Llama318BQ4
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": content},
		},
		"lease_id": s.Creds.LeaseID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	status, raw, err := s.forward(ctx, body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("muse: chat gateway HTTP %d", status)
	}
	return raw, nil
}

// CompletionsURL is the gateway chat URL. The control-plane stub is refused
// before any request is sent.
func CompletionsURL(chatBase string) (string, error) {
	chatBase = strings.TrimSpace(chatBase)
	if chatBase == "" {
		return "", fmt.Errorf("muse: chat base is required")
	}
	if strings.Contains(chatBase, ControlPlaneStubPath) {
		return "", fmt.Errorf("muse: refusing control-plane chat stub")
	}
	u, err := url.Parse(chatBase)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("muse: chat base must be an http(s) URL")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = ChatCompletionsPath
	}
	if strings.Contains(u.Path, ControlPlaneStubPath) || strings.Contains(u.RequestURI(), ControlPlaneStubPath) {
		return "", fmt.Errorf("muse: refusing control-plane chat stub")
	}
	if u.Path != ChatCompletionsPath {
		return "", fmt.Errorf("muse: chat path must be %s", ChatCompletionsPath)
	}
	return u.String(), nil
}

func postChat(ctx context.Context, client *http.Client, chatBase, apiKey, tenant, lease string, body []byte) (int, []byte, error) {
	endpoint, err := CompletionsURL(chatBase)
	if err != nil {
		return 0, nil, err
	}
	if strings.TrimSpace(apiKey) == "" {
		return 0, nil, fmt.Errorf("muse: renter API key is required")
	}
	if strings.TrimSpace(lease) == "" {
		return 0, nil, fmt.Errorf("muse: lease id is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Api-Key", apiKey)
	if strings.TrimSpace(tenant) != "" {
		req.Header.Set("X-Tenant-ID", tenant)
	}
	req.Header.Set("X-Hypermesh-Lease-Id", lease)
	req.Header.Set("X-Lease-Id", lease)
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if strings.Contains(endpoint, ControlPlaneStubPath) {
		return resp.StatusCode, nil, fmt.Errorf("muse: refusing control-plane chat stub")
	}
	return resp.StatusCode, raw, nil
}
