package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/terms"
)

func TestEnrollRequestOmitsEmptyTerms(t *testing.T) {
	raw, err := json.Marshal(api.EnrollRequest{EnrollToken: "one-time", ImageHash: "sha256:abc"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "terms_") || strings.Contains(string(raw), "accepted_at") {
		t.Fatalf("empty terms fields were sent: %s", raw)
	}
	raw, err = json.Marshal(api.EnrollRequest{
		EnrollToken:  "one-time",
		TermsVersion: terms.Version,
		TermsSHA256:  terms.SHA256(),
		AcceptedAt:   "2026-11-05T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"terms_version"`, `"terms_sha256"`, `"accepted_at"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("missing %s in %s", key, raw)
		}
	}
}

func TestInstallYesDoesNotAcceptTerms(t *testing.T) {
	cfg := Config{LicenseEnvFile: filepath.Join(t.TempDir(), "missing.env")}
	if cfg.TermsPreaccepted() {
		t.Fatal("unset terms were treated as accepted")
	}
	for _, env := range []string{"true", "1", "YES", "on", "yes please"} {
		cfg.LicenseAcceptEnv = env
		if cfg.TermsPreaccepted() {
			t.Fatalf("%q accepted the Terms", env)
		}
	}
	cfg.LicenseAcceptEnv = ""
	no := false
	cfg.TermsTTY = &no
	if err := cfg.AcceptTerms(context.Background()); err == nil || !strings.Contains(err.Error(), "--license accept") || !strings.Contains(err.Error(), "HYPERMESH_LICENSE_ACCEPT") {
		t.Fatalf("closed failure: %v", err)
	}
}

func TestAgentEnvYesAcceptsTerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.env")
	body := "# comment\nHYPERMESH_LICENSE_ACCEPT=\"yes\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{LicenseEnvFile: path}
	if !cfg.TermsPreaccepted() {
		t.Fatal("agent.env yes was not accepted")
	}
}

func TestFromEnvLicenseYes(t *testing.T) {
	t.Setenv("HYPERMESH_LICENSE_ACCEPT", "yes")
	cfg := FromEnv()
	cfg.LicenseEnvFile = filepath.Join(t.TempDir(), "missing.env")
	if !cfg.TermsPreaccepted() {
		t.Fatal("process env yes was not accepted")
	}
}

func TestEnrollRefusesWithoutTermsAndWithoutTTY(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, "", "one-time", "")
	a.cfg.LicenseAccept = false
	a.cfg.LicenseEnvFile = filepath.Join(dir, "missing.env")
	no := false
	a.cfg.TermsTTY = &no
	err := a.ensureEnrolled(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--license accept") || !strings.Contains(err.Error(), "HYPERMESH_LICENSE_ACCEPT=yes") {
		t.Fatalf("got %v", err)
	}
	if hits != 0 {
		t.Fatalf("enroll request %d", hits)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "enrollment-keys")); !os.IsNotExist(statErr) {
		t.Fatalf("enrollment keys were created: %v", statErr)
	}
}

func TestEnrollPromptsAndStampsTerms(t *testing.T) {
	var body api.EnrollRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	}))
	defer srv.Close()
	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, "", "one-time", "")
	a.cfg.LicenseAccept = false
	a.cfg.LicenseEnvFile = filepath.Join(dir, "missing.env")
	a.cfg.HostName = "terms-box"
	yes := true
	var out bytes.Buffer
	a.cfg.TermsTTY = &yes
	a.cfg.TermsStdin = strings.NewReader("accept\n")
	a.cfg.TermsStdout = &out
	if err := a.ensureEnrolled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), terms.Text) {
		t.Fatal("prompt did not print the Terms")
	}
	if !strings.Contains(out.String(), "Type accept to accept these Terms.") {
		t.Fatal("prompt did not ask for accept")
	}
	if body.TermsVersion != terms.Version || body.TermsSHA256 != terms.SHA256() {
		t.Fatalf("request terms %+v", body)
	}
	if body.TermsSHA256 != "46620be8f650c44e8188b0e6143be0193f70f6bd901a47d3d5169785e66c7045" {
		t.Fatalf("sha256 %s", body.TermsSHA256)
	}
	if _, err := time.Parse(time.RFC3339, body.AcceptedAt); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.TermsVersion != body.TermsVersion || st.TermsSHA256 != body.TermsSHA256 || st.AcceptedAt != body.AcceptedAt {
		t.Fatalf("device.json %+v request accepted_at %s", st, body.AcceptedAt)
	}
	if strings.Contains(string(raw), "one-time") {
		t.Fatal("enroll token written to state")
	}
}

func TestEnrollPromptRejectsOtherAnswers(t *testing.T) {
	for _, answer := range []string{"yes\n", "Yes\n", "accept more\n", "\n", "I accept\n"} {
		dir := t.TempDir()
		a := newTestAgent(t, "http://127.0.0.1:9", dir, "", "one-time", "")
		a.cfg.LicenseAccept = false
		a.cfg.LicenseEnvFile = filepath.Join(dir, "missing.env")
		yes := true
		a.cfg.TermsTTY = &yes
		a.cfg.TermsStdin = strings.NewReader(answer)
		a.cfg.TermsStdout = &bytes.Buffer{}
		err := a.ensureEnrolled(context.Background())
		if err == nil {
			t.Fatalf("%q was accepted", answer)
		}
	}
}

func TestEnrollCLIYesDoesNotAccept(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		ControlPlaneURL: "http://127.0.0.1:9",
		TenantID:        testTenant,
		EnrollToken:     "one-time",
		StateDir:        dir,
		HeartbeatEvery:  time.Second,
		JobEvery:        time.Second,
		LicenseEnvFile:  filepath.Join(dir, "missing.env"),
	}
	no := false
	cfg.TermsTTY = &no
	err := RunEnrollCLI(context.Background(), cfg, []string{"--yes"})
	if err == nil || !strings.Contains(err.Error(), "--license accept") || !strings.Contains(err.Error(), "HYPERMESH_LICENSE_ACCEPT") {
		t.Fatalf("got %v", err)
	}
	err = RunEnrollCLI(context.Background(), cfg, []string{"--license", "yes"})
	if err == nil || !strings.Contains(err.Error(), "--license must be accept") {
		t.Fatalf("got %v", err)
	}
}

func TestEnrollCLIAcceptSendsTerms(t *testing.T) {
	var body api.EnrollRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfg := Config{
		ControlPlaneURL: srv.URL,
		TenantID:        testTenant,
		EnrollToken:     "one-time",
		StateDir:        dir,
		HostName:        "cli-box",
		HeartbeatEvery:  time.Second,
		JobEvery:        time.Second,
		ImageHash:       "sha256:fixture",
		LicenseEnvFile:  filepath.Join(dir, "missing.env"),
	}
	if err := RunEnrollCLI(context.Background(), cfg, []string{"--yes", "--license", "accept"}); err != nil {
		t.Fatal(err)
	}
	if body.TermsVersion != terms.Version || body.TermsSHA256 != terms.SHA256() || body.HostName != "cli-box" {
		t.Fatalf("body %+v", body)
	}
}

func TestEnrollmentTermsCurrent(t *testing.T) {
	if terms.SHA256() != "46620be8f650c44e8188b0e6143be0193f70f6bd901a47d3d5169785e66c7045" {
		t.Fatalf("sha256 %s", terms.SHA256())
	}
	current := State{
		DeviceID:     "dev-1",
		DeviceSecret: "sec-1",
		TermsVersion: terms.Version,
		TermsSHA256:  terms.SHA256(),
		AcceptedAt:   "2026-11-05T00:00:00Z",
	}
	dir := t.TempDir()
	writeTermsState(t, dir, current)
	if !EnrollmentTermsCurrent(dir) {
		t.Fatal("current enrollment acceptance was ignored")
	}
	stale := []State{
		{DeviceID: "dev-1", DeviceSecret: "sec-1", TermsVersion: terms.Version, TermsSHA256: "0000000000000000000000000000000000000000000000000000000000000000", AcceptedAt: current.AcceptedAt},
		{DeviceID: "dev-1", DeviceSecret: "sec-1", TermsVersion: "other-terms", TermsSHA256: terms.SHA256(), AcceptedAt: current.AcceptedAt},
		{DeviceID: "dev-1", DeviceSecret: "sec-1", TermsVersion: terms.Version, TermsSHA256: terms.SHA256()},
		{DeviceID: "dev-1", TermsVersion: terms.Version, TermsSHA256: terms.SHA256(), AcceptedAt: current.AcceptedAt},
		{DeviceSecret: "sec-1", TermsVersion: terms.Version, TermsSHA256: terms.SHA256(), AcceptedAt: current.AcceptedAt},
		{DeviceID: "dev-1", DeviceSecret: "sec-1"},
	}
	for i, st := range stale {
		d := t.TempDir()
		writeTermsState(t, d, st)
		if EnrollmentTermsCurrent(d) {
			t.Fatalf("case %d counted as current: %+v", i, st)
		}
	}
	if EnrollmentTermsCurrent(t.TempDir()) || EnrollmentTermsCurrent("") || EnrollmentTermsCurrent(filepath.Join(dir, "missing")) {
		t.Fatal("missing device.json counted as accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, stateFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if EnrollmentTermsCurrent(dir) {
		t.Fatal("corrupt device.json counted as accepted")
	}
}

func TestAcceptHostTermsKeepsStoredAcceptance(t *testing.T) {
	dir := t.TempDir()
	writeTermsState(t, dir, State{
		DeviceID:     "dev-1",
		DeviceSecret: "sec-1",
		TermsVersion: terms.Version,
		TermsSHA256:  terms.SHA256(),
		AcceptedAt:   "2026-11-05T00:00:00Z",
	})
	a := newTestAgent(t, "http://127.0.0.1:9", dir, "", "", "")
	a.cfg.LicenseAccept = false
	a.cfg.LicenseAcceptEnv = ""
	a.cfg.LicenseEnvFile = filepath.Join(dir, "missing.env")
	no := false
	a.cfg.TermsTTY = &no
	cfg, err := a.AcceptHostTerms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LicenseAccept {
		t.Fatal("stored acceptance was recorded as a new flag")
	}
}

func TestAcceptHostTermsRefusesStaleStoredTerms(t *testing.T) {
	dir := t.TempDir()
	writeTermsState(t, dir, State{
		DeviceID:     "dev-1",
		DeviceSecret: "sec-1",
		TermsVersion: terms.Version,
		TermsSHA256:  "0000000000000000000000000000000000000000000000000000000000000000",
		AcceptedAt:   "2026-11-05T00:00:00Z",
	})
	a := newTestAgent(t, "http://127.0.0.1:9", dir, "", "one-time", "")
	a.cfg.LicenseAccept = false
	a.cfg.LicenseEnvFile = filepath.Join(dir, "missing.env")
	no := false
	a.cfg.TermsTTY = &no
	_, err := a.AcceptHostTerms(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--license accept") || !strings.Contains(err.Error(), "HYPERMESH_LICENSE_ACCEPT") {
		t.Fatalf("got %v", err)
	}
}

func TestEnrollPromptCancelled(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, "", "one-time", "")
	a.cfg.LicenseAccept = false
	a.cfg.LicenseEnvFile = filepath.Join(dir, "missing.env")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	in := &blockingReader{started: make(chan struct{}), release: release}
	yes := true
	a.cfg.TermsTTY = &yes
	a.cfg.TermsStdin = in
	a.cfg.TermsStdout = &bytes.Buffer{}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- a.ensureEnrolled(ctx) }()
	select {
	case <-in.started:
	case <-time.After(3 * time.Second):
		t.Fatal("prompt did not wait for accept")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled enrollment context did not interrupt the prompt")
	}
	if hits != 0 {
		t.Fatalf("enroll request %d", hits)
	}
}

func TestAcceptTermsFileReadAcceptsLine(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})
	yes := true
	cfg := Config{
		LicenseEnvFile: filepath.Join(t.TempDir(), "missing.env"),
		TermsTTY:       &yes,
		TermsStdin:     r,
		TermsStdout:    &bytes.Buffer{},
	}
	go func() {
		_, _ = w.Write([]byte("accept\n"))
	}()
	if err := cfg.AcceptTerms(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !cfg.LicenseAccept {
		t.Fatal("typing accept on a terminal did not stick")
	}
}

func TestAcceptTermsFileReadCancelled(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})
	yes := true
	out := &promptBuf{ready: make(chan struct{})}
	cfg := Config{
		LicenseEnvFile: filepath.Join(t.TempDir(), "missing.env"),
		TermsTTY:       &yes,
		TermsStdin:     r,
		TermsStdout:    out,
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- cfg.AcceptTerms(ctx) }()
	select {
	case <-out.ready:
	case err := <-errCh:
		t.Fatalf("prompt returned before the read: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("prompt did not start")
	}
	// The prompt is written before the read. Wait until that read is blocked
	// so cancellation has to interrupt the file, not the check before it.
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled context did not interrupt the terminal read")
	}
}

// promptBuf does not embed bytes.Buffer. io.WriteString would otherwise
// call the promoted WriteString and skip the prompt signal.
type promptBuf struct {
	buf   bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (b *promptBuf) Write(p []byte) (int, error) {
	n, err := b.buf.Write(p)
	if bytes.Contains(b.buf.Bytes(), []byte("Type accept to accept these Terms.")) {
		b.once.Do(func() { close(b.ready) })
	}
	return n, err
}

type blockingReader struct {
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (b *blockingReader) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return 0, io.EOF
}

func writeTermsState(t *testing.T, dir string, st State) {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFile), append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
