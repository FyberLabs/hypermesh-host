package deploy

import (
	"context"
	"os"
	"strings"
	"testing"
)

func dynamicClockFiles(changes map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if value, ok := changes[path]; ok {
			return []byte(value), nil
		}
		if path == "/sys/devices/system/cpu/online" {
			return []byte("0-1,3"), nil
		}
		if strings.HasSuffix(path, "min_freq") {
			return []byte("100"), nil
		}
		if strings.HasSuffix(path, "max_freq") {
			return []byte("1000"), nil
		}
		if strings.HasSuffix(path, "mrq_rate_locked") || strings.HasSuffix(path, "bwmgr_halt") {
			return []byte("0"), nil
		}
		return os.ReadFile(path)
	}
}

func TestClockControls(t *testing.T) {
	for _, tc := range []struct{ name, path, value, want string }{
		{"dynamic", "", "", clocksStopped},
		{"cpu locked", "/sys/devices/system/cpu/cpu1/cpufreq/scaling_min_freq", "1000", clocksRunning},
		{"gpu locked", "/sys/devices/platform/17000000.gpu/devfreq/17000000.gpu/min_freq", "1000", clocksRunning},
		{"emc locked", "/sys/kernel/debug/bpmp/debug/clk/emc/mrq_rate_locked", "1", clocksRunning},
		{"bandwidth halted", "/sys/kernel/debug/bpmp/debug/bwmgr/bwmgr_halt", "1", clocksRunning},
		{"missing emc", "/sys/kernel/debug/bpmp/debug/clk/emc/mrq_rate_locked", "", ""},
		{"invalid emc", "/sys/kernel/debug/bpmp/debug/clk/emc/mrq_rate_locked", "2", ""},
		{"missing cpu", "/sys/devices/system/cpu/cpu1/cpufreq/scaling_min_freq", "", ""},
		{"invalid range", "/sys/devices/system/cpu/online", "3-1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := Source{ReadFile: dynamicClockFiles(map[string]string{tc.path: tc.value}), Output: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("CPU0: Governor=schedutil MinFreq=100 MaxFreq=1000 CurrentFreq=1000"), nil
			}}
			if got := source.clocks(context.Background()); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	source := Source{ReadFile: dynamicClockFiles(nil), Output: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("not running"), os.ErrPermission
	}}
	if source.clocks(context.Background()) != "" {
		t.Fatal("failed command supplied evidence")
	}
}

func TestCUDARequiresToolkitMetadata(t *testing.T) {
	source := Source{ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist }, Output: func(context.Context, string, ...string) ([]byte, error) { return []byte("release 13.2, V13.2.78"), nil }}
	if source.cuda(context.Background()) != "" {
		t.Fatal("NVCC component build treated as toolkit version")
	}
}
