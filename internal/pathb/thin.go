// Package pathb runs the only suite this slice knows: thin-v1.
//
// thin-v1 collects image hash / uptime / thermal when available. If a
// local OpenAI-shaped completions endpoint is up, it POSTs a pinned
// probe and requires a real choice. It does not invent a host-scorecard,
// an inference bench, or tokens/sec.
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

// SuiteThinV1 is the platform suite_id for this slice. There is no suite payload.
const SuiteThinV1 = "thin-v1"

// DefaultCompletionsURL is the local OpenAI-shaped path the lease publishes.
const DefaultCompletionsURL = "http://127.0.0.1:8080/v1/chat/completions"

// probeText is the only user message we POST. Never log it. Never put it
// in JobResultRequest.
const probeText = "Reply with the single word ready."

// Result is what the agent POSTs back. No tok/s field. Reason is for
// host logs only; the HTTP body stays {passed, image_hash?}.
type Result struct {
	Passed    bool
	ImageHash string
	Reason    string
}

// Collector is the telemetry thin-v1 needs.
type Collector interface {
	Collect(ctx context.Context) (telem.Snapshot, error)
}

// Runner executes thin-v1 for path_b or instance_check jobs.
type Runner struct {
	Collector Collector
	// Endpoint is the completions URL. Empty uses DefaultCompletionsURL.
	Endpoint string
	// HTTP is optional. Tests inject a short-timeout client.
	HTTP *http.Client
}

// Run collects host facts, then optionally probes the local completions
// endpoint.
//
// path_b: telemetry is required. A down endpoint still passes so enroll
// and green are not deadlocked before lease_start; inference is recorded
// as skipped.
//
// instance_check: the container-answers gate. It must receive a real
// completion (non-empty choices[0].message.content) or it fails.
//
// Unknown suite ids fail. Empty suite_id is thin-v1.
func (r Runner) Run(ctx context.Context, job api.Job) Result {
	if job.SuiteID != "" && job.SuiteID != SuiteThinV1 {
		return Result{Passed: false, Reason: fmt.Sprintf("unknown suite %s", job.SuiteID)}
	}

	hash, telemOK := r.telem(ctx)
	if !telemOK && job.Kind != api.KindInstanceCheck {
		if r.Collector == nil {
			return Result{Passed: false, Reason: "no telemetry collector"}
		}
		return Result{Passed: false, Reason: "telemetry unavailable"}
	}

	ok, reachErr := r.complete(ctx)
	if job.Kind == api.KindInstanceCheck {
		if !ok {
			reason := "no completion"
			if reachErr != nil {
				reason = "completions unreachable"
			}
			return Result{Passed: false, ImageHash: hash, Reason: reason}
		}
		return Result{Passed: true, ImageHash: hash, Reason: "thin-v1"}
	}

	if !ok {
		return Result{Passed: true, ImageHash: hash, Reason: "thin-v1 telem; inference skipped"}
	}
	return Result{Passed: true, ImageHash: hash, Reason: "thin-v1"}
}

func (r Runner) telem(ctx context.Context) (string, bool) {
	if r.Collector == nil {
		return "", false
	}
	snap, err := r.Collector.Collect(ctx)
	if err != nil {
		return "", false
	}
	return snap.ImageHash, true
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
}

// complete POSTs the pinned probe. It never returns the prompt or the
// completion text (success is a bool). Errors are reachability only.
func (r Runner) complete(ctx context.Context) (bool, error) {
	body, err := json.Marshal(completionRequest{
		Model: "local",
		Messages: []map[string]string{
			{"role": "user", "content": probeText},
		},
	})
	if err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint(), bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client().Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, fmt.Errorf("completions status %d", resp.StatusCode)
	}

	var out completionResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, nil
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return false, nil
	}
	return true, nil
}
