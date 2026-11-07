//go:build linux

package certmeasure

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/agent"
)

func request(o options, id string, index int) agent.WorkloadPreflight {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", id, index)))
	return agent.WorkloadPreflight{PlanSHA256: hex.EncodeToString(hash[:]), CatalogID: "measurement", ImageDigest: o.image, ModelPath: o.model, FileHash: "sha256:" + o.modelPin, ContextTokens: o.context, FullContext: true, SuiteID: "pack-v1"}
}
func validateContainer(o options) error {
	if o.backend != "cuda" || os.Geteuid() != 0 {
		return fmt.Errorf("NVIDIA container measurement requires Linux root")
	}
	return agent.ValidateWorkloadPreflight(request(o, "validation", 0))
}
func containerSample(ctx context.Context, o options, id string, index int) ([]byte, time.Duration, float64, error) {
	config, err := os.MkdirTemp("", "hypermesh-measure-docker-")
	if err != nil {
		return nil, 0, 0, err
	}
	defer os.RemoveAll(config)
	run := func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "/usr/bin/docker", append([]string{"--config", config, "--host", "unix:///var/run/docker.sock"}, args...)...)
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/nonexistent", "LC_ALL=C"}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		cmd.WaitDelay = time.Second
		output := &bounded{}
		cmd.Stdout = output
		cmd.Stderr = &bounded{}
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("isolated Docker operation failed: %w", err)
		}
		return output.Bytes(), nil
	}
	start := time.Now()
	receipt, err := agent.RunWorkloadPreflight(ctx, request(o, id, index), o.state, run)
	elapsed := time.Since(start)
	if err != nil {
		return nil, elapsed, 0, err
	}
	if !receipt.CleanupComplete || receipt.ContextFit == nil || !receipt.ContextFit.ValidFor(o.context) {
		return nil, elapsed, 0, fmt.Errorf("incomplete workload/context/cleanup evidence")
	}
	raw, err := json.Marshal(receipt)
	return raw, elapsed, receipt.TokensPerSecond, err
}
