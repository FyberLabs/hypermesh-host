package installer

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const renameExchange = 1 << 1 // RENAME_EXCHANGE

// exchangeFiles atomically swaps the files at a and b.
func exchangeFiles(a, b string) error {
	if sysRenameat2 == 0 {
		return syscall.ENOSYS
	}
	pa, err := syscall.BytePtrFromString(a)
	if err != nil {
		return err
	}
	pb, err := syscall.BytePtrFromString(b)
	if err != nil {
		return err
	}
	cwd := -100 // AT_FDCWD
	_, _, errno := syscall.Syscall6(uintptr(sysRenameat2),
		uintptr(cwd), uintptr(unsafe.Pointer(pa)),
		uintptr(cwd), uintptr(unsafe.Pointer(pb)),
		renameExchange, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// beforeSellSwap runs after the replacement is written and before it is
// swapped in. Tests use it to write a pause the way a running agent would.
var beforeSellSwap = func() {}

// replaceSellIfUnchanged replaces path with next only while path still holds
// expected. A running agent replaces sell.json by rename, so the file is
// swapped with renameat2(RENAME_EXCHANGE) and the swapped-out content is
// checked: if it is not expected, the agent wrote in between and its file is
// swapped back. Where the exchange is unavailable, the bytes are compared
// again right before rename. It reports whether path was replaced.
func replaceSellIfUnchanged(path string, expected, next []byte) (bool, error) {
	for attempt := 0; attempt < 3; attempt++ {
		current, err := os.ReadFile(path)
		if err != nil {
			return false, fmt.Errorf("cannot inspect sell posture")
		}
		if !bytes.Equal(bytes.TrimSpace(current), expected) {
			return false, nil
		}
		tmp, err := writeSellTemp(path, next)
		if err != nil {
			return false, err
		}
		beforeSellSwap()
		err = exchangeFiles(tmp, path)
		if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.EPERM) {
			replaced, ferr := renameIfUnchanged(tmp, path, expected)
			_ = os.Remove(tmp)
			return replaced, ferr
		}
		if err != nil {
			_ = os.Remove(tmp)
			return false, fmt.Errorf("cannot release activation sell pause")
		}
		previous, err := os.ReadFile(tmp)
		if err == nil && bytes.Equal(bytes.TrimSpace(previous), expected) {
			_ = os.Remove(tmp)
			return true, syncDir(path)
		}
		// The agent replaced the file after it was inspected: restore its file.
		if err := exchangeFiles(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return false, fmt.Errorf("cannot restore sell posture written during release")
		}
		_ = os.Remove(tmp)
	}
	return false, nil
}

func renameIfUnchanged(tmp, path string, expected []byte) (bool, error) {
	current, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("cannot inspect sell posture")
	}
	if !bytes.Equal(bytes.TrimSpace(current), expected) {
		return false, nil
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, fmt.Errorf("cannot release activation sell pause")
	}
	return true, syncDir(path)
}

func writeSellTemp(path string, data []byte) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".sell-")
	if err != nil {
		return "", fmt.Errorf("cannot release activation sell pause")
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("cannot release activation sell pause")
	}
	return f.Name(), nil
}

func syncDir(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return nil
	}
	defer dir.Close()
	_ = dir.Sync()
	return nil
}
