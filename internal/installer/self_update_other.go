//go:build !linux
package installer
import("context";"fmt";"io")
func RunPlanInstallerUpdate(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{fmt.Fprintln(d,"installer self-update requires Linux");return 1}
func RunUpdateInstaller(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{fmt.Fprintln(d,"installer self-update requires Linux");return 1}
func RunRollbackInstaller(_ context.Context,_ []string,_ io.Writer,d io.Writer)int{fmt.Fprintln(d,"installer rollback requires Linux");return 1}
