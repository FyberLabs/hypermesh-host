//go:build !linux

package installer

import ("context";"fmt";"io")
func RunPlanCertification(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int{fmt.Fprintln(diagnostic,"certification onboarding requires Linux");return 1}
func RunCertifyAgent(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int{fmt.Fprintln(diagnostic,"certification onboarding requires Linux");return 1}
func RunActivateAgent(_ context.Context,_ []string,_ io.Writer,diagnostic io.Writer)int{fmt.Fprintln(diagnostic,"certification onboarding requires Linux");return 1}
