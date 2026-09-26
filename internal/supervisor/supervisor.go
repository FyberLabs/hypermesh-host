// Package supervisor is the supervisor that restarts a local runtime and
// keeps placement settings. One process, three placements:
//
//   - outside: not on the network
//   - host: enrolls and heartbeats as a supervisor host and refuses lease_start
//   - renter: talks to the chat gateway POST /v1/chat/completions
//
// The supervisor owns the local AI process. The renter API key and the
// one-time enroll token stay in the environment or a systemd credential.
// They are not rewritten into supervisor.json on start.
package supervisor

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
	credFile        = "supervisor.json"
	deviceFile      = "device.json"

	credAPIKey      = "hypermesh-api-key"
	credEnrollToken = "hypermesh-enroll-token"
)

// forbiddenCallerKeyPrefixes are host, router, and site credentials.
// Same prefixes the visor and the CLI reject. They are not org API keys.
var forbiddenCallerKeyPrefixes = []string{"hm_dev_", "hm_rtr_", "hm_site_"}

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
		return "", fmt.Errorf("supervisor: placement must be outside, host, or renter")
	}
}

// Credentials are the in-memory settings for this process.
// supervisor.json is mode 0600 and stores placement, tenant, lease, and chat
// base. APIKey is never written. EnrollToken is kept in memory. A legacy
// enroll_token already in supervisor.json stays until enroll succeeds, then
// it is removed. Device secrets stay in device.json.
type Credentials struct {
	Placement string `json:"placement"`
	TenantID  string `json:"tenant_id,omitempty"`
	// EnrollToken is the one-time host token. Not a device secret.
	// json marshaling of the file on disk clears it after enroll.
	EnrollToken string `json:"enroll_token,omitempty"`
	// APIKey is the renter key from the environment or a systemd
	// credential. It is not stored in supervisor.json.
	APIKey   string `json:"-"`
	LeaseID  string `json:"lease_id,omitempty"`
	ChatBase string `json:"chat_base,omitempty"`
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
}

// SaveCredentials writes supervisor.json at mode 0600 without the renter API
// key. A legacy enroll token stays only until device.json shows enroll
// succeeded. An enroll token that lives only in the environment is not
// written into the file.
func (s *Supervisor) SaveCredentials() error {
	legacy := ""
	if onDisk, err := LoadCredentials(s.StateDir); err == nil {
		legacy = onDisk.EnrollToken
	}
	disk := Credentials{
		Placement: s.Creds.Placement,
		TenantID:  s.Creds.TenantID,
		LeaseID:   s.Creds.LeaseID,
		ChatBase:  s.Creds.ChatBase,
	}
	if !deviceEnrolled(s.StateDir) {
		disk.EnrollToken = strings.TrimSpace(legacy)
	}
	return writeCredentials(s.StateDir, disk)
}

func writeCredentials(stateDir string, creds Credentials) error {
	if strings.TrimSpace(stateDir) == "" {
		return fmt.Errorf("supervisor: state dir is required")
	}
	creds.APIKey = ""
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	path := filepath.Join(stateDir, credFile)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// readCredential returns the env value, or the systemd credential file
// at $CREDENTIALS_DIRECTORY/<name>. Empty is not an error.
func readCredential(envKey, name string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		return v, nil
	}
	dir := strings.TrimSpace(os.Getenv("CREDENTIALS_DIRECTORY"))
	if dir == "" || name == "" {
		return "", nil
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (s *Supervisor) resolveSecrets() error {
	if strings.TrimSpace(s.Creds.APIKey) == "" {
		key, err := readCredential("HYPERMESH_API_KEY", credAPIKey)
		if err != nil {
			return err
		}
		s.Creds.APIKey = key
	}
	if strings.TrimSpace(s.Creds.EnrollToken) == "" {
		tok, err := readCredential("HYPERMESH_ENROLL_TOKEN", credEnrollToken)
		if err != nil {
			return err
		}
		s.Creds.EnrollToken = tok
	}
	if strings.TrimSpace(s.Creds.EnrollToken) == "" {
		if onDisk, err := LoadCredentials(s.StateDir); err == nil {
			s.Creds.EnrollToken = onDisk.EnrollToken
		}
	}
	return nil
}

func deviceEnrolled(stateDir string) bool {
	b, err := os.ReadFile(filepath.Join(stateDir, deviceFile))
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

func (s *Supervisor) watchEnroll(ctx context.Context) {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if deviceEnrolled(s.StateDir) {
			_ = s.SaveCredentials()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// LoadCredentials reads supervisor.json.
func LoadCredentials(stateDir string) (Credentials, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, credFile))
	if err != nil {
		return Credentials{}, err
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return Credentials{}, err
	}
	return c, nil
}

// Run holds credentials, restarts the local runtime, and opens the
// placement's door. Host placement calls the existing agent.
func (s *Supervisor) Run(ctx context.Context) error {
	if s.Log == nil {
		s.Log = logsafe.New()
	}
	place, err := ParsePlacement(s.Placement)
	if err != nil {
		return err
	}
	s.Placement = place
	s.Creds.Placement = place
	if err := s.resolveSecrets(); err != nil {
		return err
	}
	if err := s.SaveCredentials(); err != nil {
		return err
	}
	switch place {
	case PlacementOutside:
		return Supervise(ctx, s.Runtime, s.backoff())
	case PlacementHost:
		return s.runHost(ctx)
	case PlacementRenter:
		return s.runRenter(ctx)
	default:
		return fmt.Errorf("supervisor: placement %q", place)
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
		return fmt.Errorf("supervisor: local runtime is required")
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
		return fmt.Errorf("supervisor: host placement owns the local runtime")
	}
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
	a, err := agent.New(cfg, s.Log)
	if err != nil {
		return err
	}
	go s.watchEnroll(ctx)
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
	if strings.TrimSpace(s.Creds.ChatBase) == "" {
		s.Creds.ChatBase = DefaultChatBase
	}
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
// gateway. The inbound X-Api-Key is the org key on that forward. Lease and
// tenant headers stay on this process's credentials. Other placements do
// not call the control-plane stub.
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
		apiKey, err := callerAPIKey(r.Header.Get("X-Api-Key"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		status, raw, err := s.forward(r.Context(), apiKey, body)
		if err != nil {
			http.Error(w, redactAPIKey(err.Error(), apiKey), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(redactAPIKey(string(raw), apiKey)))
	})
}

// callerAPIKey is the inbound org key. A missing key, a blank key, or a
// forbidden prefix is refused here, before any upstream forward.
func callerAPIKey(header string) (string, error) {
	key := strings.TrimSpace(header)
	if key == "" {
		return "", errors.New("api key is required")
	}
	for _, prefix := range forbiddenCallerKeyPrefixes {
		if strings.HasPrefix(key, prefix) {
			return "", fmt.Errorf("%s is not a renter identity; use an org API key (purpose: renter)", prefix)
		}
	}
	return key, nil
}

func redactAPIKey(text, key string) string {
	if key == "" {
		return text
	}
	return strings.ReplaceAll(text, key, "***")
}

func (s *Supervisor) forward(ctx context.Context, apiKey string, body []byte) (int, []byte, error) {
	return postChat(ctx, s.HTTP, s.Creds.ChatBase, apiKey, s.Creds.TenantID, s.Creds.LeaseID, body)
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
	status, raw, err := postChat(ctx, s.HTTP, s.Creds.ChatBase, s.Creds.APIKey, s.Creds.TenantID, s.Creds.LeaseID, body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("supervisor: chat gateway HTTP %d", status)
	}
	return raw, nil
}

// CompletionsURL is the gateway chat URL. The control-plane stub is refused
// before any request is sent.
func CompletionsURL(chatBase string) (string, error) {
	chatBase = strings.TrimSpace(chatBase)
	if chatBase == "" {
		return "", fmt.Errorf("supervisor: chat base is required")
	}
	if strings.Contains(chatBase, ControlPlaneStubPath) {
		return "", fmt.Errorf("supervisor: refusing control-plane chat stub")
	}
	u, err := url.Parse(chatBase)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("supervisor: chat base must be an http(s) URL")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = ChatCompletionsPath
	}
	if strings.Contains(u.Path, ControlPlaneStubPath) || strings.Contains(u.RequestURI(), ControlPlaneStubPath) {
		return "", fmt.Errorf("supervisor: refusing control-plane chat stub")
	}
	if u.Path != ChatCompletionsPath {
		return "", fmt.Errorf("supervisor: chat path must be %s", ChatCompletionsPath)
	}
	return u.String(), nil
}

func postChat(ctx context.Context, client *http.Client, chatBase, apiKey, tenant, lease string, body []byte) (int, []byte, error) {
	endpoint, err := CompletionsURL(chatBase)
	if err != nil {
		return 0, nil, err
	}
	if strings.TrimSpace(apiKey) == "" {
		return 0, nil, fmt.Errorf("supervisor: renter API key is required")
	}
	if strings.TrimSpace(lease) == "" {
		return 0, nil, fmt.Errorf("supervisor: lease id is required")
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
		return resp.StatusCode, nil, fmt.Errorf("supervisor: refusing control-plane chat stub")
	}
	return resp.StatusCode, raw, nil
}
