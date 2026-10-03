package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestCheckCommand(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		probeErr error
		disk int64
		code int
		called bool
	}{
		{"UMA pass", []string{"--memory-mode", "uma", "--uma-gib", "40", "--model-dir", "."}, nil, 88*GiB, 0, true},
		{"disk fail", []string{"--memory-mode", "uma", "--uma-gib", "40", "--model-dir", "."}, nil, 87*GiB, 1, true},
		{"probe fail", []string{"--memory-mode", "discrete", "--model-dir", "."}, fmt.Errorf("GPU probe unavailable"), 100*GiB, 1, true},
		{"discrete pass", []string{"--memory-mode", "discrete", "--model-dir", "."}, nil, 100*GiB, 0, true},
		{"no mode", []string{"--model-dir", "."}, nil, 0, 2, false},
		{"installation without allocation", []string{"--memory-mode", "uma", "--model-dir", "."}, nil, 27*GiB, 0, true},
		{"negative allocation", []string{"--memory-mode", "uma", "--uma-gib", "-1", "--model-dir", "."}, nil, 0, 2, false},
		{"overflow", []string{"--memory-mode", "uma", "--uma-gib", "9223372036854775807", "--model-dir", "."}, nil, 0, 2, false},
		{"conflicting allocation", []string{"--memory-mode", "discrete", "--uma-gib", "12", "--model-dir", "."}, nil, 0, 2, false},
		{"unexpected positional", []string{"--memory-mode", "discrete", "--model-dir", ".", "extra"}, nil, 0, 2, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			called := false
			probe := func(ctx context.Context, directory, mode string, allocation int64) (Resources, error) {
				called = true
				if directory == "" || directory[0] != '/' { t.Fatal("directory must be absolute") }
				return Resources{SystemBytes: 64*GiB, AvailableSystemBytes: 48*GiB,
					UMA: mode == "uma", UMARuntime: &UMARuntime{Backend: "cuda", FreeBytes: 48*GiB, TotalBytes: 64*GiB}, UMAAllocationBytes: allocation,
					DiscreteVRAMBytes: 12*GiB, DiskFreeBytes: test.disk}, test.probeErr
			}
			code := runResourceCheck(context.Background(), test.args, &out, &diagnostic, probe)
			if code != test.code || called != test.called {
				t.Fatalf("code=%d called=%v diagnostics=%s", code, called, &diagnostic)
			}
			if called {
				var report ResourceReport
				if err := json.Unmarshal(out.Bytes(), &report); err != nil { t.Fatal(err) }
				if report.SchemaVersion != 1 || report.Admission.Eligible != (code == 0) || report.Scope == "" {
					t.Fatalf("invalid report: %+v", report)
				}
				if code == 1 && len(report.Admission.Reasons) == 0 { t.Fatal("missing blockers") }
			}
		})
	}
}

func TestMemoryParsers(t *testing.T) {
	total, available, err := parseMemory("MemTotal: 67108864 kB\nMemAvailable: 50331648 kB\n")
	if err != nil || total != 64*GiB || available != 48*GiB { t.Fatalf("%d %d %v", total, available, err) }
	for _, input := range []string{
		"", "MemTotal: 10 kB", "MemTotal: 10 kB\nMemAvailable: 11 kB",
		"MemTotal: 10 kB\nMemAvailable: 1 MB",
		"MemTotal: 9223372036854775807 kB\nMemAvailable: 1 kB",
		"MemTotal: 10 kB\nMemTotal: 10 kB\nMemAvailable: 1 kB",
	} {
		if _, _, err := parseMemory(input); err == nil { t.Fatalf("accepted %q", input) }
	}
	if n, err := sumMemory("8192\n8192\n", 1<<20); err != nil || n != 16*GiB {
		t.Fatalf("multi GPU capacity: %d %v", n, err)
	}
	for _, input := range []string{"", "N/A", "-1", "0", "8192\nN/A", "9223372036854775807"} {
		if _, err := sumMemory(input, 1<<20); err == nil { t.Fatalf("accepted %q", input) }
	}
}
