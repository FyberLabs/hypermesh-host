//go:build darwin

package installer

import (
	"context"
	"errors"
	"fmt"
	"github.com/FyberLabs/hypermesh-host/internal/deviceauth"
	"github.com/FyberLabs/hypermesh-host/internal/hardware"
	"github.com/FyberLabs/hypermesh-host/internal/hostregistration"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"
)

func RunRegisterAgent(ctx context.Context, args []string, out, diagnostic io.Writer) int {
	if os.Geteuid() != 0 || runtime.GOARCH != "arm64" {
		fmt.Fprintln(diagnostic, "Darwin owner registration requires root on Apple silicon")
		return 1
	}
	// Hardware detection invokes system tools. Do not search a Homebrew or
	// caller-writable PATH while running as root.
	if err := os.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin"); err != nil {
		fmt.Fprintln(diagnostic, "cannot configure system tool path")
		return 1
	}
	d := macRegistrationDeps{detect: hardware.Detect, remote: func(base, tenant string) (macRegistrationRemote, error) { return hostregistration.New(base, tenant) }, wait: waitRegistration,
		authorize: func(ctx context.Context, issuer, dashboard, requestID string, out io.Writer) (string, error) {
			token, err := deviceauth.AuthorizeRegistration(ctx, issuer, dashboard, requestID, out)
			return token.Bearer(), err
		},
		command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Env = []string{"PATH=/usr/local/libexec/hypermesh:/usr/bin:/bin:/usr/sbin:/sbin", "HOME=/var/root", "LC_ALL=C"}
			return cmd.Output()
		},
		absent: func(err error) bool { var exit *exec.ExitError; return errors.As(err, &exit) && exit.ExitCode() == 113 },
	}
	return runMacRegister(ctx, args, out, diagnostic, d)
}
func RunRegistrationStatus(_ context.Context, args []string, out, diagnostic io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(diagnostic, "registration-status requires root")
		return 1
	}
	return runMacRegistrationStatus(args, out, diagnostic, "")
}
