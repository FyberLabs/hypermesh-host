//go:build linux || darwin

package certmeasure

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type bounded struct{ bytes.Buffer }

func (b *bounded) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("benchmark output too large")
	}
	return b.Buffer.Write(p)
}
func execute(ctx context.Context, binary string, args []string) ([]byte, time.Duration, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	// Do not pass host credentials, proxy secrets or loader injection variables.
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "HOME=/nonexistent"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	output := &bounded{}
	cmd.Stdout = output
	cmd.Stderr = &bounded{}
	start := time.Now()
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return output.Bytes(), time.Since(start), err
}
