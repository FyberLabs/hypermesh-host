//go:build !linux

package installer

import (
	"context"
	"fmt"
	"io"
)

func RunApplySetup(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int {
	fmt.Fprintln(diagnostic,"apply-setup currently requires Linux")
	return 1
}
func RunSetupStatus(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int {
	fmt.Fprintln(diagnostic,"setup-status currently requires Linux")
	return 1
}
