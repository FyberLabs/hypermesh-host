//go:build !linux

package installer

import ("context";"fmt";"io")

func RunPlanReboot(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{fmt.Fprintln(d,"setup reboot requires Linux");return 1}
func RunRebootSetup(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{fmt.Fprintln(d,"setup reboot requires Linux");return 1}
func RunResumeSetup(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{fmt.Fprintln(d,"setup reboot requires Linux");return 1}
func RunRebootStatus(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{fmt.Fprintln(d,"setup reboot requires Linux");return 1}
