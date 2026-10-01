//go:build !linux

package installer

import (
	"context"
	"fmt"
	"io"
)

func RunSupportBundle(_ context.Context, _ []string, _ io.Writer, diagnostic io.Writer) int {
	fmt.Fprintln(diagnostic, "support-bundle is supported only on Linux")
	return 1
}
