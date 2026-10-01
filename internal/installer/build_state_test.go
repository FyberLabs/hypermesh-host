package installer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildStatePersistsAndLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	floor, commit, unlock, err := lockBuildState(path)
	if err != nil || floor != 0 { t.Fatalf("initial state: %d %v", floor, err) }
	if _, _, _, err := lockBuildState(path); err == nil { t.Fatal("concurrent writer acquired lock") }
	if err := commit(22); err != nil { t.Fatal(err) }
	if err := commit(21); err == nil { t.Fatal("lowered floor") }
	unlock()
	floor, commit, unlock, err = lockBuildState(path)
	if err != nil { t.Fatal(err) }
	defer unlock()
	if floor != 22 { t.Fatalf("lost floor: %d", floor) }
	if err := commit(22); err != nil { t.Fatal("same-build retry blocked", err) }
	if err := commit(23); err != nil { t.Fatal(err) }
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 { t.Fatal("state permissions") }
}

func TestBuildStateFailsClosed(t *testing.T) {
	for _, content := range []string{"", "broken", "{}", "{\"schema\":2,\"build\":22}", "{\"schema\":1,\"build\":-1}"} {
		path := filepath.Join(t.TempDir(), "state")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil { t.Fatal(err) }
		if _, _, _, err := lockBuildState(path); err == nil { t.Fatalf("accepted %q", content) }
		if _, err := os.Stat(path+".lock"); !os.IsNotExist(err) { t.Fatal("failed read left lock") }
	}
}
