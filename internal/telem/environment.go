package telem

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"

	"github.com/FyberLabs/hypermesh-host/internal/buildinfo"
)

// environmentHash detects changes independently of an operator-pinned image
// hash. Only digests leave the host. Uptime, temperatures and boot IDs are
// deliberately excluded. Missing optional platform files have stable markers;
// unreadable or oversized inventory is unknown, never silently a new baseline.
func (s Source) environmentHash(power string) string {
	paths := s.EnvironmentFiles
	if paths == nil {
		paths = []string{
			s.OSRelease, s.TegraRelease,
			"/proc/sys/kernel/osrelease", "/proc/device-tree/model", "/sys/module/nvidia/version",
			"/sys/module/amdgpu/version", "/sys/class/dmi/id/bios_version",
			"/usr/local/cuda/version.json", "/usr/local/cuda/version.txt",
			"/var/lib/dpkg/status", "/etc/nvpmodel.conf",
			"/System/Library/CoreServices/SystemVersion.plist",
		}
	}
	paths = append([]string(nil), paths...)
	sort.Strings(paths)
	h := sha256.New()
	fmt.Fprintf(h, "hypermesh-environment-v1\x00%s\x00%s\x00%s\x00%s\x00", runtime.GOOS, runtime.GOARCH, buildinfo.Current(), power)
	for _, path := range paths {
		if path == "" {
			continue
		}
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			fmt.Fprintf(h, "%s\x00missing\x00", path)
			continue
		}
		if err != nil {
			return ""
		}
		digest := sha256.New()
		n, err := io.Copy(digest, io.LimitReader(f, (8<<20)+1))
		f.Close()
		if err != nil || n > 8<<20 {
			return ""
		}
		fmt.Fprintf(h, "%s\x00%x\x00", path, digest.Sum(nil))
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}
