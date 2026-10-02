//go:build !linux
package installer
import ("context";"fmt";"io")
func RunPlanRecovery(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{fmt.Fprintln(d,"host recovery requires Linux");return 1}
func RunRecoverAgent(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{fmt.Fprintln(d,"host recovery requires Linux");return 1}
