// Package protection prepares local evidence for independent qualification review.
// It grants neither certification nor placement authority.
package protection

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
)

const Schema = "hypermesh-protection-evidence-v1"

type Mode string

const (
	Physical     Mode = "physical_memory"
	Confidential Mode = "confidential_execution"
)

type Binding struct {
	BootID        string `json:"boot_id"`
	Hardware      string `json:"hardware"`
	Firmware      string `json:"firmware"`
	ModelSHA256   string `json:"model_sha256"`
	RuntimeSHA256 string `json:"runtime_sha256"`
	Backend       string `json:"backend"`
	ContextTokens int    `json:"context_tokens"`
}

type Check struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	ArtifactSHA256 string `json:"artifact_sha256"`
}

// Sample records paired measurements with the same model/runtime/backend/context.
// Test artifacts must show how throughput and peak memory were measured.
type Sample struct {
	ID              string  `json:"id"`
	Binding         Binding `json:"binding"`
	Protected       bool    `json:"protected"`
	TokensPerSecond float64 `json:"tokens_per_second"`
	PeakBytes       int64   `json:"peak_bytes"`
	ArtifactSHA256  string  `json:"artifact_sha256"`
}

type Report struct {
	Schema     string    `json:"schema"`
	Modes      []Mode    `json:"modes"`
	Binding    Binding   `json:"binding"`
	ObservedAt time.Time `json:"observed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Checks     []Check   `json:"checks"`
	Samples    []Sample  `json:"samples"`
}

type Result struct {
	ReviewReady     bool     `json:"review_ready"`
	Qualified       bool     `json:"qualified"`
	Blockers        []string `json:"blockers"`
	ThroughputRatio float64  `json:"throughput_ratio"`
	Note            string   `json:"note"`
}

func RequiredChecks(modes []Mode, backend string) ([]string, error) {
	if len(modes) == 0 {
		return nil, fmt.Errorf("at least one protection mode is required")
	}
	checks := []string{"firmware_identity", "runtime_fit", "storage_swap_dump_controls", "reboot_invalidation", "firmware_drift_invalidation", "sustained_stability"}
	seen := map[Mode]bool{}
	for _, mode := range modes {
		if seen[mode] {
			return nil, fmt.Errorf("duplicate protection mode")
		}
		seen[mode] = true
		switch mode {
		case Physical:
			checks = append(checks, "vendor_support", "encryption_enabled_current_boot", "memory_domain_coverage", "disabled_state_rejected")
		case Confidential:
			checks = append(checks, "attestation_chain_tcb", "guest_measurement_debug_policy", "challenge_guest_key_binding", "authorized_model_key_release", "host_read_denied", "replay_rejected", "wrong_measurement_rejected", "verifier_outage_rejected", "lease_key_expiry")
		default:
			return nil, fmt.Errorf("unknown protection mode %q", mode)
		}
	}
	switch backend {
	case "cpu":
	case "uma", "discrete_gpu":
		checks = append(checks, "accelerator_memory_io_boundary")
	default:
		return nil, fmt.Errorf("backend must be cpu, uma or discrete_gpu")
	}
	return checks, nil
}

func Template(modes []Mode, b Binding, now time.Time) (Report, error) {
	ids, err := RequiredChecks(modes, b.Backend)
	if err != nil {
		return Report{}, err
	}
	r := Report{Schema: Schema, Modes: modes, Binding: b, ObservedAt: now.UTC(), ExpiresAt: now.UTC().Add(24 * time.Hour), Checks: []Check{}, Samples: []Sample{}}
	for _, id := range ids {
		r.Checks = append(r.Checks, Check{ID: id, Status: "unknown"})
	}
	return r, nil
}

func digest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}

// Evaluate validates completeness and consistency, not the truth of host evidence.
// Expected binding comes from a fresh local observation, not the report itself.
func Evaluate(r Report, expected Binding, now time.Time) Result {
	out := Result{Blockers: []string{}, Note: "Local evidence completeness only. Independent program review and a production verifier are required; this report cannot qualify a host."}
	block := func(s string) { out.Blockers = append(out.Blockers, s) }
	if r.Schema != Schema {
		block("unsupported schema")
	}
	if r.Binding != expected {
		block("current boot, hardware, firmware or workload binding differs")
	}
	b := r.Binding
	if strings.TrimSpace(b.BootID) == "" || strings.TrimSpace(b.Hardware) == "" || strings.TrimSpace(b.Firmware) == "" || !digest(b.ModelSHA256) || !digest(b.RuntimeSHA256) || b.ContextTokens <= 0 {
		block("incomplete binding")
	}
	if r.ObservedAt.IsZero() || r.ObservedAt.After(now) || !r.ExpiresAt.After(now) || !r.ExpiresAt.After(r.ObservedAt) || r.ExpiresAt.Sub(r.ObservedAt) > 24*time.Hour {
		block("stale or invalid evidence window")
	}
	ids, err := RequiredChecks(r.Modes, b.Backend)
	if err != nil {
		block(err.Error())
	}
	allowed := map[string]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	checks := map[string]Check{}
	for _, c := range r.Checks {
		if _, exists := checks[c.ID]; exists {
			block("duplicate check: " + c.ID)
		}
		if !allowed[c.ID] {
			block("unknown check: " + c.ID)
		}
		checks[c.ID] = c
	}
	for _, id := range ids {
		c, ok := checks[id]
		if !ok || c.Status != "passed" || !digest(c.ArtifactSHA256) {
			block("missing or unconfirmed check evidence: " + id)
		}
	}
	seenSamples := map[string]bool{}
	seenArtifacts := map[string]bool{}
	var baseline, protected float64
	var baseCount, protectedCount int
	for _, s := range r.Samples {
		// Firmware toggles require a reboot. Baseline boot IDs may differ,
		// but protected samples must belong to the current protected boot.
		comparison := s.Binding
		comparison.BootID = b.BootID
		if s.ID == "" || seenSamples[s.ID] || seenArtifacts[s.ArtifactSHA256] || strings.TrimSpace(s.Binding.BootID) == "" || comparison != b || (s.Protected && s.Binding.BootID != b.BootID) || !digest(s.ArtifactSHA256) || s.PeakBytes <= 0 || s.TokensPerSecond <= 0 || math.IsNaN(s.TokensPerSecond) || math.IsInf(s.TokensPerSecond, 0) {
			block("invalid, duplicate or mismatched sample")
			continue
		}
		seenSamples[s.ID] = true
		seenArtifacts[s.ArtifactSHA256] = true
		if s.Protected {
			protected += s.TokensPerSecond
			protectedCount++
		} else {
			baseline += s.TokensPerSecond
			baseCount++
		}
	}
	if baseCount < 3 || protectedCount < 3 {
		block("need at least three baseline and three protected samples")
	}
	if baseCount > 0 && protectedCount > 0 {
		ratio := (protected / float64(protectedCount)) / (baseline / float64(baseCount))
		if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 0 {
			block("invalid aggregate throughput")
		} else {
			out.ThroughputRatio = ratio
		}
	}
	out.ReviewReady = len(out.Blockers) == 0
	return out
}
