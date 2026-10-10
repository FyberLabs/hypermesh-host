package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/pathb"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

//go:embed console.html
var ownerConsoleFS embed.FS

const ownerStatusPlaceholder = "__OWNER_STATUS__"

const (
	ownerTokenFile   = "owner.token"
	ownerSockFile    = "owner.sock"
	ownerEffectsFile = "owner-effects.jsonl"
	ownerTokenBytes  = 32
	toolSellPause    = "hypermesh.sell_pause"
	toolSellResume   = "hypermesh.sell_resume"
	toolLeaseStop    = "hypermesh.lease_stop"
)

// ownerEffect is one plane-down receipt line. Not fyber.receipt/v0.
type ownerEffect struct {
	TS        string         `json:"ts"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args"`
	Passed    bool           `json:"passed"`
	SellState api.SellState  `json:"sell_state,omitempty"`
	Until     string         `json:"until,omitempty"`
	LeaseID   string         `json:"lease_id,omitempty"`
	DeviceID  string         `json:"device_id,omitempty"`
	ImageHash string         `json:"image_hash,omitempty"`
}

type ownerSellPauseRequest struct {
	Until    string `json:"until"`
	DeviceID string `json:"device_id"`
}

type ownerSellPauseResponse struct {
	Passed    bool          `json:"passed"`
	SellState api.SellState `json:"sell_state"`
	Until     string        `json:"until,omitempty"`
	DeviceID  string        `json:"device_id,omitempty"`
}

type ownerSellResumeRequest struct {
	DeviceID string `json:"device_id"`
}

type ownerSellResumeResponse struct {
	Passed    bool          `json:"passed"`
	SellState api.SellState `json:"sell_state"`
	Until     string        `json:"until,omitempty"`
	DeviceID  string        `json:"device_id,omitempty"`
	Reason    string        `json:"reason,omitempty"`
}

type ownerLeaseStopRequest struct {
	LeaseID string `json:"lease_id"`
}

type ownerLeaseStopResponse struct {
	Passed    bool   `json:"passed"`
	ImageHash string `json:"image_hash,omitempty"`
}

// ownerStatusResponse is GET /v0/status. JSON only. No secrets.
type ownerStatusResponse struct {
 Maintenance bool `json:"maintenance"`
 WorkPending bool `json:"work_pending"`
	DeviceID  string            `json:"device_id"`
	SellState api.SellState     `json:"sell_state"`
	Until     string            `json:"until,omitempty"`
	// PauseSource is "owner" or "activation" while paused; empty otherwise.
	PauseSource string            `json:"pause_source,omitempty"`
	Lease     *ownerLeaseStatus `json:"lease"`
	PathB     ownerPathBStatus  `json:"path_b"`
	Backend   string            `json:"backend"`
}

type ownerLeaseStatus struct {
	LeaseID     string `json:"lease_id"`
	CatalogID   string `json:"catalog_id,omitempty"`
	ImageDigest string `json:"image_digest,omitempty"`
}

// ownerPathBStatus is what this box actually stores or can probe.
// Control-plane heartbeat certification state is logged only. Status does not store it.
type ownerPathBStatus struct {
	BootIDPresent bool   `json:"boot_id_present"`
	BootID        string `json:"boot_id,omitempty"`
	CompletionsUp bool   `json:"completions_up"`
}

func ownerTokenPath(dir string) string   { return filepath.Join(dir, ownerTokenFile) }
func ownerSockPath(dir string) string    { return filepath.Join(dir, ownerSockFile) }
func ownerEffectsPath(dir string) string { return filepath.Join(dir, ownerEffectsFile) }

func ensureOwnerToken(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := ownerTokenPath(dir)
	if b, err := os.ReadFile(path); err == nil {
		tok := strings.TrimSpace(string(b))
		if tok != "" {
			_ = os.Chmod(path, 0o600)
			return tok, nil
		}
	}
	raw := make([]byte, ownerTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return tok, nil
}

func readOwnerToken(dir string) (string, error) {
	b, err := os.ReadFile(ownerTokenPath(dir))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("hypermesh: owner token is empty")
	}
	return tok, nil
}

func (a *Agent) startOwner(ctx context.Context) error {
	if _, err := ensureOwnerToken(a.cfg.StateDir); err != nil {
		return fmt.Errorf("hypermesh: owner token: %w", err)
	}
	sock := ownerSockPath(a.cfg.StateDir)
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("hypermesh: owner socket: %w", err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(sock)
		return fmt.Errorf("hypermesh: owner socket perms: %w", err)
	}
	srv := &http.Server{
		Handler:           a.ownerHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		_ = os.Remove(sock)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			a.log.Warn("owner socket stopped", "err", err)
		}
	}()
	a.log.Info("owner socket listening", "path", sock)
	return nil
}

func (a *Agent) ownerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/status", a.handleOwnerStatus)
 mux.HandleFunc("/v0/maintenance",a.handleOwnerMaintenance)
	mux.HandleFunc("/v0/console", a.handleOwnerConsole)
	mux.HandleFunc("/", a.handleOwnerConsole)
	mux.HandleFunc("/v0/sell_pause", a.handleOwnerSellPause)
	mux.HandleFunc("/v0/sell_resume", a.handleOwnerSellResume)
	mux.HandleFunc("/v0/activation_release", a.handleActivationRelease)
	mux.HandleFunc("/v0/lease_stop", a.handleOwnerLeaseStop)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.ownerAuthorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *Agent) ownerAuthorized(r *http.Request) bool {
	want, err := readOwnerToken(a.cfg.StateDir)
	if err != nil || want == "" {
		return false
	}
	got := r.Header.Get("Authorization")
	const pfx = "Bearer "
	if !strings.HasPrefix(got, pfx) {
		return false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(got, pfx))
	return subtle.ConstantTimeCompare([]byte(tok), []byte(want)) == 1
}

func (a *Agent) handleOwnerStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	encodeJSON(w, http.StatusOK, a.ownerStatus(r.Context()))
}

func (a *Agent) handleOwnerConsole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	page, err := ownerConsoleFS.ReadFile("console.html")
	if err != nil {
		http.Error(w, "console unavailable", http.StatusInternalServerError)
		return
	}
	raw, err := json.Marshal(a.ownerStatus(r.Context()))
	if err != nil {
		http.Error(w, "status unavailable", http.StatusInternalServerError)
		return
	}
	html := strings.Replace(string(page), ownerStatusPlaceholder, string(raw), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, html)
}

func (a *Agent) ownerStatus(ctx context.Context) ownerStatusResponse {
	sell := a.effectiveSell()
	out := ownerStatusResponse{
 Maintenance:a.maintenanceHeld(), WorkPending:a.pendingResult.Load(),
		DeviceID:  strings.TrimSpace(a.state.DeviceID),
		SellState: sell.SellState,
		Until:     sell.Until,
		Backend:   runtime.NVIDIALogLabel,
		PathB:     a.ownerPathB(ctx),
	}
	if sell.SellState == api.SellPaused && !out.Maintenance {
		out.PauseSource = sell.Source
	}
	if h := a.localLease(); h != nil && strings.TrimSpace(h.LeaseID) != "" {
		out.Lease = &ownerLeaseStatus{
			LeaseID:     strings.TrimSpace(h.LeaseID),
			CatalogID:   strings.TrimSpace(h.CatalogID),
			ImageDigest: strings.TrimSpace(h.ImageDigest),
		}
	}
	return out
}

func (a *Agent) ownerPathB(ctx context.Context) ownerPathBStatus {
	pb := ownerPathBStatus{
		CompletionsUp: a.completionsReachable(ctx),
	}
	rec := a.lastBootRecord()
	id := strings.TrimSpace(rec.BootID)
	if id != "" {
		pb.BootIDPresent = true
		pb.BootID = id
	}
	return pb
}

func (a *Agent) lastBootRecord() pathb.Record {
	if a.pathb.Store == nil {
		return pathb.Record{}
	}
	rec, err := a.pathb.Store.Load()
	if err != nil {
		return pathb.Record{}
	}
	return rec
}

func (a *Agent) completionsReachable(ctx context.Context) bool {
	u := strings.TrimSpace(a.liveCompletionsURL())
	if u == "" {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
	return true
}

func (a *Agent) handleOwnerSellPause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req ownerSellPauseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		deviceID = strings.TrimSpace(a.state.DeviceID)
	}
	out := a.sellPause(api.Job{
		Kind:     api.KindSellPause,
		DeviceID: deviceID,
		Until:    req.Until,
	})
	if out.Passed && out.SellState == api.SellNA {
		out.Passed = false
		out.Reason = "invalid sell_state"
	}
	args := map[string]any{}
	if id := strings.TrimSpace(req.DeviceID); id != "" {
		args["device_id"] = id
	}
	if u := strings.TrimSpace(req.Until); u != "" {
		args["until"] = u
	}
	_ = a.appendOwnerEffect(ownerEffect{
		TS:        a.clock().UTC().Format(time.RFC3339),
		Tool:      toolSellPause,
		Args:      args,
		Passed:    out.Passed,
		SellState: out.SellState,
		Until:     out.Until,
		DeviceID:  out.DeviceID,
	})
	encodeJSON(w, http.StatusOK, ownerSellPauseResponse{
		Passed:    out.Passed,
		SellState: out.SellState,
		Until:     out.Until,
		DeviceID:  out.DeviceID,
	})
}

func (a *Agent) handleOwnerSellResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req ownerSellResumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		deviceID = strings.TrimSpace(a.state.DeviceID)
	}
	out := a.sellResume(deviceID)
	args := map[string]any{}
	if id := strings.TrimSpace(req.DeviceID); id != "" {
		args["device_id"] = id
	}
	_ = a.appendOwnerEffect(ownerEffect{
		TS:        a.clock().UTC().Format(time.RFC3339),
		Tool:      toolSellResume,
		Args:      args,
		Passed:    out.Passed,
		SellState: out.SellState,
		DeviceID:  out.DeviceID,
	})
	resp := ownerSellResumeResponse{
		Passed:    out.Passed,
		SellState: out.SellState,
		Until:     out.Until,
		DeviceID:  out.DeviceID,
	}
	if !out.Passed {
		resp.Reason = out.Reason
	}
	encodeJSON(w, http.StatusOK, resp)
}

// handleActivationRelease is called by the installer after it has verified
// the activated service. It only clears the activation guard.
func (a *Agent) handleActivationRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 4096))
	p, err := a.releaseActivationPause()
	if err != nil {
		http.Error(w, "activation release not saved", http.StatusInternalServerError)
		return
	}
	resp := ownerStatusResponse{
		Maintenance: a.maintenanceHeld(),
		DeviceID:    strings.TrimSpace(a.state.DeviceID),
		SellState:   p.SellState,
		Until:       p.Until,
	}
	if p.SellState == api.SellPaused && !resp.Maintenance {
		resp.PauseSource = p.Source
	}
	encodeJSON(w, http.StatusOK, resp)
}

func (a *Agent) handleOwnerLeaseStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req ownerLeaseStopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.LeaseID) == "" {
		http.Error(w, "lease_id required", http.StatusBadRequest)
		return
	}
	out := a.leaseStop(r.Context(), api.Job{
		Kind:    api.KindLeaseStop,
		LeaseID: req.LeaseID,
	})
	_ = a.appendOwnerEffect(ownerEffect{
		TS:        a.clock().UTC().Format(time.RFC3339),
		Tool:      toolLeaseStop,
		Args:      map[string]any{"lease_id": req.LeaseID},
		Passed:    out.Passed,
		LeaseID:   strings.TrimSpace(req.LeaseID),
		ImageHash: out.ImageHash,
	})
	encodeJSON(w, http.StatusOK, ownerLeaseStopResponse{
		Passed:    out.Passed,
		ImageHash: out.ImageHash,
	})
}

func encodeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *Agent) appendOwnerEffect(e ownerEffect) error {
	if e.Args == nil {
		e.Args = map[string]any{}
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	a.ownerMu.Lock()
	defer a.ownerMu.Unlock()
	if err := os.MkdirAll(a.cfg.StateDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(ownerEffectsPath(a.cfg.StateDir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// RunOwnerCLI talks to $STATE_DIR/owner.sock with the token file.
// It does not enqueue a control-plane job.
func RunOwnerCLI(stateDir string, args []string) error {
	return runOwnerCLI(stateDir, args, os.Stdout)
}

func runOwnerCLI(stateDir string, args []string, out io.Writer) error {
	if strings.TrimSpace(stateDir) == "" {
		return fmt.Errorf("hypermesh: state dir is required")
	}
	if len(args) < 1 {
		return fmt.Errorf("hypermesh: usage: hypermesh-agent owner status | console | sell-pause [--until=RFC3339] | sell-resume | lease-stop --lease-id=ID")
	}
	switch args[0] {
	case "status":
		return ownerCLIDo(stateDir, http.MethodGet, "/v0/status", nil, out)
	case "console":
		return runOwnerConsoleHelp(stateDir, out)
	case "sell-pause":
		fs := flag.NewFlagSet("sell-pause", flag.ContinueOnError)
		until := fs.String("until", "", "optional RFC3339 pause end")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := ownerSellPauseRequest{Until: strings.TrimSpace(*until)}
		return ownerCLIDo(stateDir, http.MethodPost, "/v0/sell_pause", body, out)
	case "sell-resume":
		if len(args) > 1 {
			return fmt.Errorf("hypermesh: sell-resume takes no arguments")
		}
		return ownerCLIDo(stateDir, http.MethodPost, "/v0/sell_resume", ownerSellResumeRequest{}, out)
	case "lease-stop":
		fs := flag.NewFlagSet("lease-stop", flag.ContinueOnError)
		leaseID := fs.String("lease-id", "", "lease id to stop")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*leaseID) == "" {
			return fmt.Errorf("hypermesh: --lease-id is required")
		}
		return ownerCLIDo(stateDir, http.MethodPost, "/v0/lease_stop", ownerLeaseStopRequest{LeaseID: *leaseID}, out)
	default:
		return fmt.Errorf("hypermesh: unknown owner command %q", args[0])
	}
}

func runOwnerConsoleHelp(stateDir string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	sock := ownerSockPath(stateDir)
	tok := ownerTokenPath(stateDir)
	_, _ = fmt.Fprintf(out, "Hypermesh owner console is unix-socket only (not TCP).\n\n")
	_, _ = fmt.Fprintf(out, "  socket: %s\n", sock)
	_, _ = fmt.Fprintf(out, "  token:  %s (mode 0600)\n\n", tok)
	_, _ = fmt.Fprintf(out, "Status:\n  hypermesh-agent -state-dir=%s owner status\n\n", stateDir)
	_, _ = fmt.Fprintf(out, "Page (Bearer required):\n")
	_, _ = fmt.Fprintf(out, "  curl --unix-socket %q \\\n", sock)
	_, _ = fmt.Fprintf(out, "    -H \"Authorization: Bearer $(cat %s)\" \\\n", tok)
	_, _ = fmt.Fprintf(out, "    http://localhost/v0/console\n\n")
	_, _ = fmt.Fprintf(out, "Same page at GET /.\n")
	return nil
}

func ownerCLIDo(stateDir, method, path string, body any, out io.Writer) error {
	token, err := readOwnerToken(stateDir)
	if err != nil {
		return fmt.Errorf("hypermesh: owner token: %w", err)
	}
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", ownerSockPath(stateDir))
			},
		},
		Timeout: 10 * time.Second,
	}
	req, err := http.NewRequest(method, "http://owner"+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("hypermesh: owner socket: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if out != nil {
		_, _ = out.Write(raw)
		if len(raw) == 0 || raw[len(raw)-1] != '\n' {
			_, _ = io.WriteString(out, "\n")
		}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hypermesh: owner %s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}
