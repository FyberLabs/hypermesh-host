//go:build !linux

package installer

import (
	"context"
	"fmt"
	"io"
)

func RunSell(_ context.Context, _ []string, _ io.Writer, d io.Writer) int {
	fmt.Fprintln(d, "sell requires Linux")
	return 1
}
