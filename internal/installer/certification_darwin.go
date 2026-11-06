//go:build darwin

package installer

import (
	"context"
	"fmt"
	"io"

	"github.com/FyberLabs/hypermesh-host/internal/certmeasure"
)

// Native certification collects Metal measurements without requiring a Jetson
// onboarding plan. Certificate issuance remains a separate policy evaluation.
func RunCertifyAgent(ctx context.Context, args []string, out, diagnostic io.Writer) int {
	return certmeasure.Run(ctx, args, out, diagnostic)
}
func RunPlanCertification(_ context.Context, _ []string, out, diagnostic io.Writer) int {
	fmt.Fprintln(out, "Native Mac: run certify-agent --model-file GGUF --model-sha256 HEX --runtime-file LLAMA_BENCH --runtime-sha256 HEX --backend metal --artifact-dir DIR. Measurements are retained independently of enrollment and class baselines; certificate issuance is evaluated separately.")
	return 0
}
func RunActivateAgent(_ context.Context, _ []string, _ io.Writer, diagnostic io.Writer) int {
	fmt.Fprintln(diagnostic, "Native measurements do not authorize activation; a current control-plane certificate and owner activation permit are required")
	return 1
}
