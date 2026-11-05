package agent

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
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

// AcceptTerms returns nil when the Terms are already accepted or the
// operator types accept at a terminal. It does not enroll.
func (c *Config) AcceptTerms() error {
	if c.TermsPreaccepted() {
		return nil
	}
	in, out, tty := c.termsIO()
	if !tty {
		return ErrTermsNotAccepted
	}
	if err := writeTermsPrompt(out); err != nil {
		return err
	}
	line, err := readAcceptLine(in)
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
// The returned config includes acceptance typed at the prompt.
func (a *Agent) AcceptHostTerms() (Config, error) {
	if err := (&a.cfg).AcceptTerms(); err != nil {
		return Config{}, err
	}
	return a.cfg, nil
}

// AcceptedTerms is the stamp to store and send for this enrollment.
func (c *Config) AcceptedTerms() (TermsAcceptance, error) {
	if err := c.AcceptTerms(); err != nil {
		return TermsAcceptance{}, err
	}
	return TermsAcceptance{
		Version:    terms.Version,
		SHA256:     terms.SHA256(),
		AcceptedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
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

func readAcceptLine(in io.Reader) (string, error) {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return line, nil
}
