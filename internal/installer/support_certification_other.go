//go:build !linux

package installer

import (
 "context"
 "fmt"
 "io"
)

func RunCertificationStatus(_ context.Context, _ []string, _ io.Writer, diagnostic io.Writer) int {
 fmt.Fprintln(diagnostic, "certification-status is supported only on Linux")
 return 1
}
