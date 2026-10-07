// Package pathb runs thin-v1 (lab Path B) and pack-v1 (enrolled compact-cert).
//
// Both collect image hash / uptime / thermal when available. If a local
// OpenAI-shaped completions endpoint is up, they POST a pinned probe and
// require a real choice. They do not invent a host-scorecard, an inference
// bench, or tokens/sec.
//
// The probe text and the completion text are never logged and never
// placed in the job result body.
package pathb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/telem"
)

// SuiteThinV1 is the lab Path B suite_id. Empty suite_id is treated as thin-v1.
const SuiteThinV1 = "thin-v1"

// SuitePackV1 is the enrolled compact-cert suite_id (plane core suite).
const SuitePackV1 = "pack-v1"

// DefaultCompletionsURL is the local OpenAI-shaped path the lease publishes.
const DefaultCompletionsURL = "http://127.0.0.1:8080/v1/chat/completions"

// probeText is the only user message we POST. Never log it. Never put it
// in JobResultRequest.
const probeText = "Reply with the single word ready."

// Result is what the agent POSTs back for the job. Reason is for host
// logs only; the job HTTP body stays {passed, image_hash?}.
// Measured / SampleMs / DecodeTokS are a separate scorecard report.
// They stay unset unless the completion body included usage.completion_tokens.
type Result struct {
	ContextFit *ContextFit
	// WorkloadVerified is set only by the agent after pinned execution and cleanup.
	WorkloadVerified bool
	Passed           bool
	ImageHash        string
	Reason           string
	Measured         bool
	SampleMs         int
	DecodeTokS       float64
	ThermalC         *float64
}

func knownSuite(suiteID string) bool {
	switch strings.TrimSpace(suiteID) {
	case "", SuiteThinV1, SuitePackV1:
		return true
	default:
		return false
	}
}

func suiteLabel(suiteID string) string {
	if strings.TrimSpace(suiteID) == SuitePackV1 {
		return SuitePackV1
	}
	return SuiteThinV1
}

// Collector is the telemetry thin-v1 needs.
type Collector interface {
	Collect(ctx context.Context) (telem.Snapshot, error)
}

// Runner executes thin-v1 for path_b or instance_check jobs.
type Runner struct {
	// Model is the exact catalog alias served by a certification container.
	Model     string
	Collector Collector
	// Endpoint is the completions URL. Empty uses DefaultCompletionsURL.
	Endpoint string
	// VerifiedContainerEndpoint is set only from inspection of the exact local
	// certification container. It authorizes this URL, not other private hosts.
	VerifiedContainerEndpoint string
	// HTTP is optional. Tests inject a short-timeout client.
	HTTP *http.Client
	// Store holds last-seen boot identity in the state dir. Not a secret.
	// Nil skips the reboot gate (tests that only care about telem/inference).
	Store Store
	// Now is optional. Tests freeze wall time for the uptime fallback.
	Now func() time.Time
}

// Run collects host facts, then optionally probes the local completions
// endpoint.
//
// path_b: telemetry is required. If this box rebooted since the last
// successful path_b, POST passed=false (host log reason: reboot). A down
// endpoint still passes so enroll and green are not deadlocked before
// lease_start; inference is recorded as skipped. First run (no prior
// boot_id) records the id and does not fail just for being first.
//
// instance_check: the container-answers gate. It must receive a real
// completion (non-empty choices[0].message.content) or it fails. Boot
// identity is not a gate here.
//
// Unknown suite ids fail. Empty suite_id is thin-v1. pack-v1 is accepted so
// enrolled compact-cert (plane CORE_SUITE_ID) can run the same probe.
//
// compact_cert requires a timed sample (decode_tok_s). A down endpoint or a
// completion without usage.completion_tokens fails closed — unlike path_b,
// which may still green on telem alone before lease_start.
func (r Runner) Run(ctx context.Context, job api.Job) Result {
	if !knownSuite(job.SuiteID) {
		return Result{Passed: false, Reason: fmt.Sprintf("unknown suite %s", job.SuiteID)}
	}
	label := suiteLabel(job.SuiteID)

	snap, telemOK := r.telem(ctx)
	hash := snap.ImageHash
	if !telemOK && job.Kind != api.KindInstanceCheck {
		if r.Collector == nil {
			return Result{Passed: false, Reason: "no telemetry collector"}
		}
		return Result{Passed: false, Reason: "telemetry unavailable"}
	}

	if job.Kind != api.KindInstanceCheck && telemOK {
		if ok, _ := r.checkBoot(snap); !ok {
			return Result{Passed: false, ImageHash: hash, Reason: "reboot", ThermalC: snap.ThermalC}
		}
	}

	ok, reachErr, sampleMs, tokens := r.complete(ctx)
	if job.Kind == api.KindInstanceCheck {
		if !ok {
			reason := "no completion"
			if reachErr != nil {
				reason = "completions unreachable"
			}
			return Result{Passed: false, ImageHash: hash, Reason: reason, ThermalC: snap.ThermalC}
		}
		return withMeasure(Result{Passed: true, ImageHash: hash, Reason: label, ThermalC: snap.ThermalC}, sampleMs, tokens)
	}

	if job.Kind == api.KindCompactCert {
		if !ok {
			reason := "no completion"
			if reachErr != nil {
				reason = "completions unreachable"
			}
			return Result{Passed: false, ImageHash: hash, Reason: reason, ThermalC: snap.ThermalC}
		}
		res := withMeasure(Result{Passed: true, ImageHash: hash, Reason: label, ThermalC: snap.ThermalC}, sampleMs, tokens)
		if !res.Measured {
			return Result{Passed: false, ImageHash: hash, Reason: "no timed sample", ThermalC: snap.ThermalC}
		}
		return res
	}

	if !ok {
		return Result{Passed: true, ImageHash: hash, Reason: label + " telem; inference skipped", ThermalC: snap.ThermalC}
	}
	return withMeasure(Result{Passed: true, ImageHash: hash, Reason: label, ThermalC: snap.ThermalC}, sampleMs, tokens)
}

func withMeasure(res Result, sampleMs, tokens int) Result {
	if tokens <= 0 || sampleMs <= 0 {
		return res
	}
	res.Measured = true
	res.SampleMs = sampleMs
	res.DecodeTokS = float64(tokens) / (float64(sampleMs) / 1000)
	return res
}

func (r Runner) telem(ctx context.Context) (telem.Snapshot, bool) {
	if r.Collector == nil {
		return telem.Snapshot{}, false
	}
	snap, err := r.Collector.Collect(ctx)
	if err != nil {
		return telem.Snapshot{}, false
	}
	return snap, true
}

func (r Runner) endpoint() string {
	if u := strings.TrimSpace(r.Endpoint); u != "" {
		return u
	}
	return DefaultCompletionsURL
}

func (r Runner) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: 45 * time.Second}
}

type completionRequest struct {
	Model    string              `json:"model"`
	Messages []map[string]string `json:"messages"`
}

type completionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// complete POSTs the pinned probe. It never returns the prompt or the
// completion text (success is a bool). Errors are reachability only.
// Probe reports whether the local completions endpoint answered.
// It does not invent a sample. The job result stays separate.
func (r Runner) Probe(ctx context.Context) bool {
	ok, _, _, _ := r.complete(ctx)
	return ok
}

func (r Runner) complete(ctx context.Context) (bool, error, int, int) {
	body, err := json.Marshal(completionRequest{
		Model: r.model(),
		Messages: []map[string]string{
			{"role": "user", "content": probeText},
		},
	})
	if err != nil {
		return false, err, 0, 0
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint(), bytes.NewReader(body))
	if err != nil {
		return false, err, 0, 0
	}
	req.Header.Set("Content-Type", "application/json")

	started := time.Now()
	resp, err := r.client().Do(req)
	if err != nil {
		return false, err, 0, 0
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	sampleMs := int(time.Since(started) / time.Millisecond)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, fmt.Errorf("completions status %d", resp.StatusCode), 0, 0
	}

	var out completionResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, nil, 0, 0
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return false, nil, 0, 0
	}
	tokens := out.Usage.CompletionTokens
	if tokens < 0 {
		tokens = 0
	}
	return true, nil, sampleMs, tokens
}

func (r Runner) model() string {
	if r.Model != "" {
		return r.Model
	}
	return "local"
}
