//go:build !linux

package installer

import (
	"context"
	"fmt"
	"io"
)

func RunQualifyHost(_ context.Context, _ []string, _ io.Reader, _ io.Writer, diagnostic io.Writer) int {
	fmt.Fprintln(diagnostic, "host qualification requires Linux")
	return 1
}
