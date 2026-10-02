//go:build !linux

package installer

import ("context";"fmt";"io")

func RunGuidedInstall(_ context.Context,_ []string,_ io.Reader,_ io.Writer,diagnostic io.Writer)int {
 fmt.Fprintln(diagnostic,"Guided host installation currently supports Linux only.")
 return 1
}
