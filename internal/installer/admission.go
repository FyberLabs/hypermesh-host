// Package installer implements policy for the pull-based host installer.
// Admission is a resource check, not certification or permission to sell.
package installer

import "fmt"

const GiB int64 = 1 << 30

// Resources must come from local probes. Zero means unknown and fails closed.
// UMA allocation is a proposed workload budget, not installed physical RAM.
// DiskFreeBytes is available space on the selected model-cache filesystem.
type Resources struct {
	SystemBytes          int64
	AvailableSystemBytes int64
	DiscreteVRAMBytes    int64
	UMA                  bool
	UMAAllocationBytes   int64
	DiskFreeBytes        int64
}

type Admission struct {
	Eligible          bool     `json:"eligible"`
	AcceleratorBytes  int64    `json:"accelerator_bytes"`
	RequiredDiskBytes int64    `json:"required_disk_bytes"`
	Reasons           []string `json:"reasons"`
}

// CheckResources applies the approved minimums using binary GiB.
// Aggregate discrete VRAM passes only this capacity gate; runtime preflight
// must separately prove that the selected GPUs can execute the target model.
func CheckResources(r Resources) Admission {
	a := Admission{Reasons: []string{}}
	if r.SystemBytes < 16*GiB {
		a.Reasons = append(a.Reasons, "at least 16 GiB system memory is required")
	}
	if r.AvailableSystemBytes < 0 || r.AvailableSystemBytes > r.SystemBytes {
		a.Reasons = append(a.Reasons, "available system memory measurement is invalid")
	}
	a.AcceleratorBytes = r.DiscreteVRAMBytes
	if r.UMA {
		a.AcceleratorBytes = r.UMAAllocationBytes
		if r.UMAAllocationBytes > r.AvailableSystemBytes {
			a.Reasons = append(a.Reasons, "proposed UMA allocation exceeds currently available system memory")
		}
	} else if r.UMAAllocationBytes != 0 {
		a.Reasons = append(a.Reasons, "UMA allocation supplied for a discrete-memory system")
	}
	if a.AcceleratorBytes < 12*GiB {
		a.Reasons = append(a.Reasons, "at least 12 GiB discrete VRAM or proposed available UMA memory is required")
	}
	// ceil(bytes * 2.2), split to avoid multiplication overflow.
	const maxInt64 = int64(1<<63 - 1)
	if a.AcceleratorBytes > (maxInt64-10)/11*5 {
		a.Reasons = append(a.Reasons, "accelerator memory measurement is outside the supported range")
	} else if a.AcceleratorBytes > 0 {
		a.RequiredDiskBytes = (a.AcceleratorBytes/5)*11 + (a.AcceleratorBytes%5*11+4)/5
		if r.DiskFreeBytes < a.RequiredDiskBytes {
			a.Reasons = append(a.Reasons, fmt.Sprintf("model-cache filesystem needs %d free bytes (2.2 times accelerator allocation); measured %d", a.RequiredDiskBytes, r.DiskFreeBytes))
		}
	}
	a.Eligible = len(a.Reasons) == 0
	return a
}
