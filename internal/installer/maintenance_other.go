//go:build !linux
package installer
import("context";"fmt";"io")
func RunPlanMaintenance(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{fmt.Fprintln(d,"maintenance requires Linux");return 1}
func RunMaintainAgent(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{fmt.Fprintln(d,"maintenance requires Linux");return 1}
func RunResumeAgent(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{fmt.Fprintln(d,"maintenance requires Linux");return 1}
