//go:build linux

package installer

import (
	"fmt"
	"github.com/FyberLabs/hypermesh-host/internal/agent"
	"github.com/FyberLabs/hypermesh-host/internal/pull"
	"strconv"
)

func guidedCachePolicy(directory string, ui guidedUI, change bool) error {
	existing, err := agent.ReadCachePolicy(directory)
	if err != nil {
		return err
	}
	if existing != nil && !change {
		return nil
	}
	const gib = uint64(1 << 30)
	capacity, err := pull.CapacityBytes(directory)
	if err != nil {
		return fmt.Errorf("cannot inspect model cache filesystem")
	}
	maximum := uint64(0)
	if capacity > int64(2*gib) {
		maximum = uint64(capacity)/gib - 2
	}
	if maximum == 0 {
		return fmt.Errorf("model filesystem has no room after its free-space reserve")
	}
	suggested := maximum / 2
	if suggested > 100 {
		suggested = 100
	}
	if suggested < 1 {
		suggested = 1
	}
	reserve := uint64(2)
	if existing != nil {
		suggested = existing.MaxBytes / gib
		reserve = existing.ReserveBytes / gib
	}
	quotaText, err := ui.ask("Maximum disk space for downloaded models (GiB)", strconv.FormatUint(suggested, 10))
	if err != nil {
		return err
	}
	quota, err := strconv.ParseUint(quotaText, 10, 32)
	if err != nil || quota == 0 || quota > maximum {
		return fmt.Errorf("choose a positive model cache quota within the filesystem capacity")
	}
	reserveText, err := ui.ask("Disk space to keep free on the model filesystem (GiB)", strconv.FormatUint(reserve, 10))
	if err != nil {
		return err
	}
	reserve, err = strconv.ParseUint(reserveText, 10, 32)
	if err != nil || reserve < 2 || quota+reserve > uint64(capacity)/gib {
		return fmt.Errorf("keep at least 2 GiB free and fit quota plus reserve within the filesystem")
	}
	fmt.Fprintln(ui.out, "Models download only for requested work. Unused models are removed least-recently-used first; active, reserved and owner-pinned models are protected.")
	if err = ui.confirm("Save this host's model cache quota and free-space reserve?"); err != nil {
		return err
	}
	policy := agent.CachePolicy{Schema: 1, MaxBytes: quota * gib, ReserveBytes: reserve * gib}
	if existing != nil {
		policy.Pins = existing.Pins
		policy.TokenFile = existing.TokenFile
	}
	return agent.WriteCachePolicy(directory, policy)
}
