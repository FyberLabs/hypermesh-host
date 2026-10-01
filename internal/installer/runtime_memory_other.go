//go:build !linux

package installer

import ("context";"fmt";"io")
func RunRuntimeMemory(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int{
 fmt.Fprintln(diagnostic,"runtime-memory checks currently require Linux UMA");return 1
}
func RunRuntimeMemoryStatus(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int{
 fmt.Fprintln(diagnostic,"runtime-memory status currently requires Linux");return 1
}
