package installer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// One floor across channels prevents switching channels from rolling back.
// Keep the state and its lock in an owner-controlled local directory.
type buildState struct {
	Schema int `json:"schema"`
	Build int64 `json:"build"`
}

func lockBuildState(path string) (int64, func(int64) error, func(), error) {
	unlock := func() {}
	lock := path + ".lock"
	if err := os.Mkdir(lock, 0o700); err != nil {
		return 0, nil, unlock, fmt.Errorf("cannot lock build state; another download may be running (inspect the .lock directory)")
	}
	unlock = func() { _ = os.Remove(lock) }
	fail := func() (int64, func(int64) error, func(), error) {
		unlock()
		return 0, nil, func() {}, fmt.Errorf("build state is invalid or unreadable; preserve it and recover from a trusted build record")
	}
	state := buildState{Schema: 1}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() & 0o077 != 0 { return fail() }
		file, err := os.Open(path)
		if err != nil { return fail() }
		data, readErr := io.ReadAll(io.LimitReader(file, 16385))
		_ = file.Close()
		if readErr != nil || len(data) > 16384 { return fail() }
		if err := json.Unmarshal(data, &state); err != nil || state.Schema != 1 || state.Build <= 0 { return fail() }
	} else if !os.IsNotExist(err) { return fail() }
	commit := func(build int64) error {
		if build <= 0 || build < state.Build { return fmt.Errorf("refusing to lower the persisted build floor") }
		data, err := json.Marshal(buildState{Schema: 1, Build: build})
		if err != nil { return err }
		file, err := os.CreateTemp(filepath.Dir(path), ".hypermesh-build-state-")
		if err != nil { return fmt.Errorf("cannot stage build state") }
		temp := file.Name()
		defer os.Remove(temp)
		if _, err := file.Write(data); err != nil { _ = file.Close(); return err }
		if err := file.Sync(); err != nil { _ = file.Close(); return err }
		if err := file.Close(); err != nil { return err }
		if err := os.Rename(temp, path); err != nil { return fmt.Errorf("cannot persist build floor") }
		dir, err := os.Open(filepath.Dir(path))
		if err != nil { return err }
		syncErr := dir.Sync()
		_ = dir.Close()
		if syncErr != nil { return syncErr }
		state.Build = build
		return nil
	}
	return state.Build, commit, unlock, nil
}
