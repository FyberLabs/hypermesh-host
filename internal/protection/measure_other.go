//go:build !linux

package protection

import (
	"fmt"
	"io"
)

func RunMeasure(args []string, out, diagnostic io.Writer) int {
	fmt.Fprintln(diagnostic, "measurement runner requires Linux")
	return 2
}
