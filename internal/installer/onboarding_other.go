//go:build !linux

package installer
import ("context";"fmt";"io")
func RunCheckAgent(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int{
 fmt.Fprintln(diagnostic,"check-agent currently requires Linux");return 1
}
func RunOnboardingStatus(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int{
 fmt.Fprintln(diagnostic,"onboarding-status currently requires Linux");return 1
}
