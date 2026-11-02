package agent

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// DefaultEnvFile is the agent environment file. systemd's
// EnvironmentFile= loads it on Linux. Launchd does not, so the agent
// reads the same path on startup. A variable that is already set is
// left alone.
const DefaultEnvFile = "/etc/hypermesh/agent.env"

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ApplyEnvFile copies KEY=VALUE lines into the process environment when
// that key is unset or empty. A missing file is not an error. Lines that
// are blank, comments, or not an assignment are skipped. Values are not
// shell-expanded. Matching single or double quotes around a value are
// removed.
func ApplyEnvFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("hypermesh: agent env file: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		key, val, ok := parseEnvLine(line)
		if !ok {
			continue
		}
		if os.Getenv(key) != "" {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return fmt.Errorf("hypermesh: agent env file: %w", err)
		}
	}
	return nil
}

func parseEnvLine(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
		return "", "", false
	}
	if strings.HasPrefix(line, "export ") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	}
	key, val, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	if !envKeyRE.MatchString(key) {
		return "", "", false
	}
	val = strings.TrimSpace(val)
	if len(val) >= 2 {
		q := val[0]
		if (q == '"' || q == '\'') && val[len(val)-1] == q {
			val = val[1 : len(val)-1]
		}
	}
	return key, val, true
}
