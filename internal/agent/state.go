package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const stateFile = "device.json"

// State is persisted after enroll. The enroll token is never stored.
type State struct {
	DeviceID         string `json:"device_id"`
	CredentialPrefix string `json:"credential_prefix,omitempty"`
}

func (a *Agent) statePath() string {
	return filepath.Join(a.cfg.StateDir, stateFile)
}

func (a *Agent) loadState() (State, error) {
	b, err := os.ReadFile(a.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("hypermesh: state file: %w", err)
	}
	return st, nil
}

func (a *Agent) saveState(st State) error {
	if err := os.MkdirAll(a.cfg.StateDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := a.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.statePath())
}
