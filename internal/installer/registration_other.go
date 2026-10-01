//go:build !linux

package installer
import ("context";"fmt";"io")
func RunRegisterAgent(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int{
 fmt.Fprintln(diagnostic,"register-agent currently requires Linux");return 1
}
func RunRegistrationStatus(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int{
 fmt.Fprintln(diagnostic,"registration-status currently requires Linux");return 1
}
