package protection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"
)

const ArtifactSchema = "hypermesh-protection-artifact-v1"

// Capture metadata is part of the hashed artifact, not a renewable dossier label.
// It remains producer-reported evidence, not independent attestation.
type Artifact struct {
	Schema     string          `json:"schema"`
	Kind       string          `json:"kind"`
	ID         string          `json:"id"`
	CapturedAt time.Time       `json:"captured_at"`
	Payload    json.RawMessage `json:"payload"`
}

// SamplePayload is the artifact payload that backs one Sample. The measure
// runner writes it, and VerifyArtifacts requires the dossier's sample fields
// and workload to agree with it.
type SamplePayload struct {
	Binding        Binding     `json:"binding"`
	Metrics        Measurement `json:"metrics"`
	PeakBytes      int64       `json:"peak_bytes"`
	PeakSource     string      `json:"peak_source"`
	WallSeconds    float64     `json:"wall_seconds"`
	ProtectedLabel bool        `json:"protected_label"`
}

// Measurement is the inference result of one load. Adapter output covers
// inference only; process high-water RSS is collected independently.
type Measurement struct {
	GeneratedTokens  int64           `json:"generated_tokens"`
	InferenceSeconds float64         `json:"inference_seconds"`
	RawResult        json.RawMessage `json:"raw_result,omitempty"`
}

func verifySamplePayload(s Sample, payload json.RawMessage) (int64, error) {
	var p SamplePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return 0, fmt.Errorf("sample artifact payload is not a measurement: %s", s.ArtifactSHA256)
	}
	if p.Binding != s.Binding || p.ProtectedLabel != s.Protected || p.PeakBytes != s.PeakBytes || p.PeakSource != "linux_process_rusage_maxrss" ||
		p.Metrics.GeneratedTokens <= 0 || !(p.Metrics.InferenceSeconds > 0) || math.IsInf(p.Metrics.InferenceSeconds, 0) ||
		s.TokensPerSecond != float64(p.Metrics.GeneratedTokens)/p.Metrics.InferenceSeconds {
		return 0, fmt.Errorf("sample artifact does not match its dossier entry: %s", s.ArtifactSHA256)
	}
	return p.Metrics.GeneratedTokens, nil
}

// VerifyArtifacts checks content-addressed evidence, never executes it.
func VerifyArtifacts(r Report, directory string) error {
	refs := map[string]Artifact{}
	samples := map[string]Sample{}
	var generated int64
	for _, c := range r.Checks {
		if _, exists := refs[c.ArtifactSHA256]; exists {
			return fmt.Errorf("duplicate artifact reference")
		}
		refs[c.ArtifactSHA256] = Artifact{Kind: "check", ID: c.ID, CapturedAt: c.CapturedAt}
	}
	for _, s := range r.Samples {
		if _, exists := refs[s.ArtifactSHA256]; exists {
			return fmt.Errorf("duplicate artifact reference")
		}
		refs[s.ArtifactSHA256] = Artifact{Kind: "sample", ID: s.ID, CapturedAt: s.CapturedAt}
		samples[s.ArtifactSHA256] = s
	}
	for ref, expected := range refs {
		if !digest(ref) {
			return fmt.Errorf("missing or invalid artifact digest")
		}
		path := filepath.Join(directory, ref)
		before, err := os.Lstat(path)
		if err != nil || !before.Mode().IsRegular() || before.Size() > 16<<20 {
			return fmt.Errorf("artifact %s must be a regular file of at most 16 MiB", ref)
		}
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("cannot open artifact %s", ref)
		}
		after, err := file.Stat()
		if err != nil || !os.SameFile(before, after) {
			file.Close()
			return fmt.Errorf("artifact changed while opening")
		}
		content, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
		file.Close()
		hash := sha256.Sum256(content)
		if err != nil || len(content) == 0 || len(content) > 16<<20 || hex.EncodeToString(hash[:]) != ref {
			return fmt.Errorf("artifact content mismatch: %s", ref)
		}
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.DisallowUnknownFields()
		var artifact Artifact
		if err := decoder.Decode(&artifact); err != nil {
			return fmt.Errorf("invalid artifact metadata: %s", ref)
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF || artifact.Schema != ArtifactSchema || artifact.Kind != expected.Kind || artifact.ID != expected.ID || artifact.CapturedAt.IsZero() || !artifact.CapturedAt.Equal(expected.CapturedAt) || len(artifact.Payload) == 0 || bytes.Equal(bytes.TrimSpace(artifact.Payload), []byte("null")) {
			return fmt.Errorf("artifact capture metadata mismatch: %s", ref)
		}
		if sample, ok := samples[ref]; ok {
			tokens, err := verifySamplePayload(sample, artifact.Payload)
			if err != nil {
				return err
			}
			// Baseline and protected loads must decode the same number of tokens.
			if generated != 0 && tokens != generated {
				return fmt.Errorf("sample artifacts use different generated token counts")
			}
			generated = tokens
		}
	}
	return nil
}
