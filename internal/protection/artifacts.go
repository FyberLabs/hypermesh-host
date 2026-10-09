package protection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

// VerifyArtifacts checks content-addressed evidence, never executes it.
func VerifyArtifacts(r Report, directory string) error {
	refs := map[string]Artifact{}
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
	}
	return nil
}
