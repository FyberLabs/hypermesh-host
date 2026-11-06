package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/terms"
)

// ErrTermsNotAccepted is returned when enrollment would start without
// the Hypermesh Terms and nobody can answer at a terminal.
var ErrTermsNotAccepted = errors.New("hypermesh: the Hypermesh Terms are not accepted, so this host was not enrolled. Set HYPERMESH_LICENSE_ACCEPT=yes in agent.env, or run hypermesh-agent enroll --license accept")

// TermsAcceptance is the record stored in device.json and sent on enroll.
type TermsAcceptance struct {
	Version    string
	SHA256     string
	AcceptedAt string
}

// ApplyLicenseFlag records --license accept. Any other value is refused.
// An install --yes flag is not an argument here and does not accept the Terms.
func ApplyLicenseFlag(cfg *Config, value string) error {
	switch strings.TrimSpace(value) {
	case "":
		return nil
	case "accept":
		cfg.LicenseAccept = true
		return nil
	default:
		return fmt.Errorf("hypermesh: --license must be accept. Set HYPERMESH_LICENSE_ACCEPT=yes in agent.env, or pass --license accept")
	}
}

// TermsPreaccepted reports flag or agent.env acceptance.
// Only the exact value yes counts. true, 1, and an install yes do not.
func (c Config) TermsPreaccepted() bool {
	if c.LicenseAccept {
		return true
	}
	switch strings.TrimSpace(c.LicenseAcceptEnv) {
	case "yes":
		return true
	case "":
		v, ok := licenseValueFromFile(c.licenseEnvFile())
		return ok && strings.TrimSpace(v) == "yes"
	default:
		return false
	}
}

// EnrollmentTermsCurrent reports whether device.json already records an
// enrolled host that accepted the current Terms. The version and SHA-256
// must both match, and the acceptance time must be present. A missing file,
// a host that is not enrolled, and a different Terms text do not match.
// Install --yes does not write this record. A preinstalled image has none.
func EnrollmentTermsCurrent(stateDir string) bool {
	st, ok := readDeviceState(stateDir)
	if !ok {
		return false
	}
	if strings.TrimSpace(st.DeviceID) == "" || strings.TrimSpace(st.DeviceSecret) == "" {
		return false
	}
	if strings.TrimSpace(st.AcceptedAt) == "" {
		return false
	}
	return st.TermsVersion == terms.Version && st.TermsSHA256 == terms.SHA256()
}

func readDeviceState(stateDir string) (State, bool) {
	if strings.TrimSpace(stateDir) == "" {
		return State{}, false
	}
	b, err := os.ReadFile(filepath.Join(stateDir, stateFile))
	if err != nil {
		return State{}, false
	}
	var st State
	if json.Unmarshal(b, &st) != nil {
		return State{}, false
	}
	return st, true
}

// AcceptTerms returns nil when the Terms are already accepted or the
// operator types accept at a terminal. It does not enroll. A cancelled
// ctx interrupts the prompt; that is how Ctrl-C and SIGTERM stop the wait.
func (c *Config) AcceptTerms(ctx context.Context) error {
	if c.TermsPreaccepted() {
		return nil
	}
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	in, out, tty := c.termsIO()
	if !tty {
		return ErrTermsNotAccepted
	}
	if err := writeTermsPrompt(out); err != nil {
		return err
	}
	line, err := readAcceptLine(ctx, in)
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != "accept" {
		return ErrTermsNotAccepted
	}
	c.LicenseAccept = true
	return nil
}

// AcceptHostTerms records Terms acceptance for this process.
// Acceptance already stored for the current Terms is kept, so a supervisor
// restart does not ask again. The returned config includes acceptance typed
// at the prompt.
func (a *Agent) AcceptHostTerms(ctx context.Context) (Config, error) {
	if EnrollmentTermsCurrent(a.cfg.StateDir) {
		return a.cfg, nil
	}
	if err := (&a.cfg).AcceptTerms(ctx); err != nil {
		return Config{}, err
	}
	return a.cfg, nil
}

// AcceptedTerms is the stamp to store and send for this enrollment.
func (c *Config) AcceptedTerms(ctx context.Context) (TermsAcceptance, error) {
	if err := c.AcceptTerms(ctx); err != nil {
		return TermsAcceptance{}, err
	}
	return TermsAcceptance{
		Version:    terms.Version,
		SHA256:     terms.SHA256(),
		AcceptedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (c Config) licenseEnvFile() string {
	if p := strings.TrimSpace(c.LicenseEnvFile); p != "" {
		return p
	}
	return DefaultEnvFile
}

func licenseValueFromFile(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	found := false
	val := ""
	for _, line := range strings.Split(string(b), "\n") {
		key, v, ok := parseEnvLine(line)
		if !ok || key != "HYPERMESH_LICENSE_ACCEPT" {
			continue
		}
		found = true
		val = v
	}
	return val, found
}

func (c Config) termsIO() (io.Reader, io.Writer, bool) {
	in := c.TermsStdin
	out := c.TermsStdout
	if out == nil {
		out = os.Stdout
	}
	if c.TermsTTY != nil {
		if in == nil {
			in = os.Stdin
		}
		return in, out, *c.TermsTTY
	}
	if in != nil {
		return in, out, false
	}
	return os.Stdin, out, stdinIsTerminal()
}

func writeTermsPrompt(out io.Writer) error {
	if _, err := out.Write(terms.Text); err != nil {
		return err
	}
	_, err := io.WriteString(out, "\nType accept to accept these Terms.\n")
	return err
}

// readAcceptLine waits for one line. A cancelled ctx interrupts the wait.
// signal.NotifyContext cancels on SIGINT and SIGTERM, which is how Ctrl-C
// leaves the prompt. A terminal or pipe is an *os.File; its read deadline
// unblocks the syscall. Any other reader is abandoned when ctx ends.
func readAcceptLine(ctx context.Context, in io.Reader) (string, error) {
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if f, ok := in.(*os.File); ok {
		return readAcceptFile(ctx, f)
	}
	return readAcceptReader(ctx, in)
}

func readAcceptFile(ctx context.Context, f *os.File) (string, error) {
	stop := context.AfterFunc(ctx, func() {
		_ = f.SetReadDeadline(time.Now())
	})
	line, err := bufio.NewReader(f).ReadString('\n')
	stop()
	_ = f.SetReadDeadline(time.Time{})
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return line, nil
}

func readAcceptReader(ctx context.Context, in io.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			ch <- result{"", err}
			return
		}
		ch <- result{line, nil}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		return r.line, r.err
	}
}
