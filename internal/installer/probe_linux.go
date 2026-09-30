//go:build linux

package installer

import (
	"context"
	"fmt"
	"os"
	"syscall"
)

func probeResources(ctx context.Context, directory, mode string, allocation int64) (Resources, error) {
	r := Resources{UMA: mode == "uma", UMAAllocationBytes: allocation}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return r, fmt.Errorf("model directory must exist and be accessible; choose the intended cache filesystem")
	}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil { return r, fmt.Errorf("cannot read /proc/meminfo") }
	r.SystemBytes, r.AvailableSystemBytes, err = parseMemory(string(data))
	if err != nil { return r, err }
	var fs syscall.Statfs_t
	if err := syscall.Statfs(directory, &fs); err != nil {
		return r, fmt.Errorf("cannot measure free space on model filesystem")
	}
	if fs.Bsize <= 0 || fs.Bavail > uint64(1<<63-1)/uint64(fs.Bsize) {
		return r, fmt.Errorf("invalid filesystem capacity measurement")
	}
	r.DiskFreeBytes = int64(fs.Bavail)*fs.Bsize
	if !r.UMA {
		r.DiscreteVRAMBytes, err = discreteMemory(ctx)
		if err != nil { return r, err }
	}
	if err := ctx.Err(); err != nil { return r, fmt.Errorf("resource check interrupted") }
	return r, nil
}
