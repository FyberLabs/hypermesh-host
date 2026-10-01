//go:build !linux

package installer

import (
	"context"
	"fmt"
	"io"
)
func RunPlanAgent(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int {
	fmt.Fprintln(diagnostic,"plan-agent currently requires Linux")
	return 1
}
func RunInstallAgent(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int {
	fmt.Fprintln(diagnostic,"install-agent currently requires Linux")
	return 1
}

func RunAgentInstallStatus(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int {
	fmt.Fprintln(diagnostic,"agent-install-status currently requires Linux")
	return 1
}
