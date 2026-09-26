package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

const (
	stateFile = "device.json"
	leaseFile = "lease.json"
)

// State is persisted after enroll at mode 0600.
// The enroll token is never stored. DeviceSecret is shown once by enroll.
type State struct {
	DeviceID         string `json:"device_id"`
	CredentialPrefix string `json:"credential_prefix,omitempty"`
	DeviceSecret     string `json:"device_secret,omitempty"`
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
	return writeJSON0600(a.statePath(), st)
}

func (a *Agent) saveLease(h *runtime.Handle) error {
	if h == nil {
		_ = os.Remove(filepath.Join(a.cfg.StateDir, leaseFile))
		return nil
	}
	if err := os.MkdirAll(a.cfg.StateDir, 0o700); err != nil {
		return err
	}
	return writeJSON0600(filepath.Join(a.cfg.StateDir, leaseFile), h)
}

func (a *Agent) loadLease() *runtime.Handle {
	b, err := os.ReadFile(filepath.Join(a.cfg.StateDir, leaseFile))
	if err != nil {
		return nil
	}
	var h runtime.Handle
	if err := json.Unmarshal(b, &h); err != nil {
		return nil
	}
	return &h
}

func writeJSON0600(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
