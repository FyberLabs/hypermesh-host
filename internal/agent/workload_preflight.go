package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/pathb"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
	"github.com/FyberLabs/hypermesh-host/internal/telem"
)

// WorkloadFailure contains a code-owned reason, never raw subprocess output.
type WorkloadFailure struct{ Reason string }

func (e *WorkloadFailure) Error() string { return "pinned workload preflight failed: " + e.Reason }

// WorkloadPreflight is an explicit local check, not an enrollment or agent job.
// The caller must obtain approval, serialize installer work and verify inactivity.
type WorkloadPreflight struct {
	PlanSHA256    string `json:"plan_sha256"`
	CatalogID     string `json:"catalog_id"`
	ImageDigest   string `json:"image_digest"`
	ModelPath     string `json:"model_path"`
	FileHash      string `json:"file_hash"`
	ContextTokens int    `json:"context_tokens"`
	FullContext   bool   `json:"full_context,omitempty"`
	SuiteID       string `json:"suite_id,omitempty"`
}

type WorkloadReceipt struct {
	Request         WorkloadPreflight `json:"request"`
	HostImageHash   string            `json:"host_image_hash"`
	SampleMS        int               `json:"sample_ms"`
	TokensPerSecond float64           `json:"tokens_per_second"`
	CleanupComplete bool              `json:"cleanup_complete"`
	ThermalC        *float64          `json:"thermal_c,omitempty"`
	ContextFit      *pathb.ContextFit `json:"context_fit,omitempty"`
}

var workloadCatalogRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
var workloadPlanRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidateWorkloadPreflight(r WorkloadPreflight) error {
	pin, err := normalizeModelID(r.FileHash)
	if !workloadPlanRE.MatchString(r.PlanSHA256) || !workloadCatalogRE.MatchString(r.CatalogID) ||
		!certImagePin.MatchString(r.ImageDigest) || err != nil || pin != r.FileHash ||
		!filepath.IsAbs(r.ModelPath) || filepath.Clean(r.ModelPath) != r.ModelPath ||
		strings.ContainsAny(r.ModelPath, ":\r\n\x00") || r.ContextTokens < 512 || r.ContextTokens > 131072 || (r.SuiteID != "" && r.SuiteID != pathb.SuitePackV1 && r.SuiteID != pathb.SuiteThinV1) {
		return fmt.Errorf("invalid pinned workload request")
	}
	return nil
}

// RunWorkloadPreflight reuses the pinned runner without constructing an API
// client, starting a service, reading a credential or changing enrollment.
func RunWorkloadPreflight(ctx context.Context, r WorkloadPreflight, state string,
	run func(context.Context, string, ...string) ([]byte, error)) (WorkloadReceipt, error) {
	return runLocalWorkloadPreflight(ctx, r, state, run, "")
}
func runLocalWorkloadPreflight(ctx context.Context, r WorkloadPreflight, state string,
	run func(context.Context, string, ...string) ([]byte, error), endpoint string) (WorkloadReceipt, error) {
	if err := ValidateWorkloadPreflight(r); err != nil {
		return WorkloadReceipt{}, err
	}
	if run == nil {
		return WorkloadReceipt{}, fmt.Errorf("an isolated runtime runner is required")
	}
	// Do not reinterpret a corrupt or stale lease journal as permission to run.
	if _, err := os.Lstat(filepath.Join(state, leaseFile)); !os.IsNotExist(err) {
		return WorkloadReceipt{}, fmt.Errorf("existing lease state must be reconciled before workload preflight")
	}
	src := telem.Default()
	src.ImageHashEnv = ""
	src.ImageHashFile = ""
	// This local check needs filesystem telemetry only, never an inherited executable.
	src.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	a := &Agent{
		cfg: Config{StateDir: state, GGUFPath: r.ModelPath, ListenPort: "18080",
			LlamaCtxSize: strconv.Itoa(r.ContextTokens), LlamaTimeout: "120", LlamaJinja: "1"},
		pathb:         pathb.Runner{Collector: src},
		runtime:       &runtime.NVIDIA{Docker: "/usr/bin/docker", Run: run},
		log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		certModelPath: r.ModelPath, certEndpoint: endpoint,
	}
	if r.FullContext {
		a.certContextTokens = r.ContextTokens
	}
	suite := r.SuiteID
	if suite == "" {
		suite = pathb.SuitePackV1
	}
	res := a.runCertWorkload(ctx, api.Job{
		JobID: r.PlanSHA256, Kind: api.KindCompactCert, SuiteID: suite,
		CatalogID: r.CatalogID, ImageDigest: r.ImageDigest, FileHash: r.FileHash,
		Runtime: "nvidia", Backend: "cuda-jetson",
	}, nil)
	if !res.Passed || !res.WorkloadVerified || !res.Measured {
		return WorkloadReceipt{}, &WorkloadFailure{Reason: res.Reason}
	}
	return WorkloadReceipt{Request: r, HostImageHash: res.ImageHash, SampleMS: res.SampleMs, TokensPerSecond: res.DecodeTokS, CleanupComplete: true, ContextFit: res.ContextFit, ThermalC: res.ThermalC}, nil
}
