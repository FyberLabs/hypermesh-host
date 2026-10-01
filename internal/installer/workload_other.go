//go:build !linux

package installer

import ("context";"fmt";"io")

func RunPlanWorkload(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{
 fmt.Fprintln(d,"workload preflight currently requires Linux NVIDIA");return 1
}
func RunCheckWorkload(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{
 fmt.Fprintln(d,"workload preflight currently requires Linux NVIDIA");return 1
}
func RunWorkloadStatus(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{
 fmt.Fprintln(d,"workload status currently requires Linux");return 1
}
