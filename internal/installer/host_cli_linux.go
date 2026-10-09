//go:build linux

package installer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const installedHostCLI = "/usr/local/bin/hypermesh-host"

// Called only with the running bytes verified against the signed release by
// installContinuation. Existing files must be this build or the previously
// approved continuation; never replace an unrelated administrator binary.
func installHostCLI(root string, binary []byte, previous string) error {
	path := agentDest(root, installedHostCLI)
	if err := agentDirectory(filepath.Dir(path), true); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0755 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
			return fmt.Errorf("installed hypermesh-host has unsafe ownership, type or permissions")
		}
		f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return e
		}
		opened, e := f.Stat()
		if e != nil || !os.SameFile(info, opened) {
			f.Close()
			return fmt.Errorf("installed CLI changed while opening")
		}
		raw, e := io.ReadAll(io.LimitReader(f, (256<<20)+1))
		f.Close()
		if e != nil || len(raw) > 256<<20 {
			return fmt.Errorf("cannot read installed CLI within size limit")
		}
		sha := digestSetupBytes(raw)
		if sha == digestSetupBytes(binary) {
			return nil
		}
		if previous == "" || sha != previous {
			return fmt.Errorf("existing hypermesh-host is not the previously verified CLI")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".hypermesh-host-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(binary); err != nil {
		file.Close()
		return err
	}
	if err = file.Chmod(0755); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if info == nil {
		// No overwrite on first installation.
		if err = os.Link(file.Name(), path); err != nil {
			return err
		}
	} else {
		current, e := os.Lstat(path)
		if e != nil || !os.SameFile(info, current) {
			return fmt.Errorf("installed hypermesh-host changed during update")
		}
		if err = os.Rename(file.Name(), path); err != nil {
			return err
		}
	}
	// Remove the staging hard link before syncing and subsequent validation.
	if err = os.Remove(file.Name()); err != nil && !os.IsNotExist(err) {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
