package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
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
)

const (
	ownerTokenFile   = "owner.token"
	ownerSockFile    = "owner.sock"
	ownerEffectsFile = "owner-effects.jsonl"
	ownerTokenBytes  = 32
	toolSellPause    = "hypermesh.sell_pause"
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

type ownerLeaseStopRequest struct {
	LeaseID string `json:"lease_id"`
}

type ownerLeaseStopResponse struct {
	Passed    bool   `json:"passed"`
	ImageHash string `json:"image_hash,omitempty"`
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
	mux.HandleFunc("/v0/sell_pause", a.handleOwnerSellPause)
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
		return fmt.Errorf("hypermesh: usage: hypermesh-agent owner sell-pause [--until=RFC3339] | lease-stop --lease-id=ID")
	}
	switch args[0] {
	case "sell-pause":
		fs := flag.NewFlagSet("sell-pause", flag.ContinueOnError)
		until := fs.String("until", "", "optional RFC3339 pause end")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := ownerSellPauseRequest{Until: strings.TrimSpace(*until)}
		return ownerCLIRequest(stateDir, "/v0/sell_pause", body, out)
	case "lease-stop":
		fs := flag.NewFlagSet("lease-stop", flag.ContinueOnError)
		leaseID := fs.String("lease-id", "", "lease id to stop")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*leaseID) == "" {
			return fmt.Errorf("hypermesh: --lease-id is required")
		}
		return ownerCLIRequest(stateDir, "/v0/lease_stop", ownerLeaseStopRequest{LeaseID: *leaseID}, out)
	default:
		return fmt.Errorf("hypermesh: unknown owner command %q", args[0])
	}
}

func ownerCLIRequest(stateDir, path string, body any, out io.Writer) error {
	token, err := readOwnerToken(stateDir)
	if err != nil {
		return fmt.Errorf("hypermesh: owner token: %w", err)
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return err
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
	req, err := http.NewRequest(http.MethodPost, "http://owner"+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
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
