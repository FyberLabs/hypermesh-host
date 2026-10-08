// Package bench is the certification pack. The host runs it inside the lab soak job (path_b).
// A repo digest is read from the local image. It is never invented.
package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/catalog"
)

// PackIDs are the catalog rows this box can try during certification.
func PackIDs() []string {
	return []string{
		catalog.Llama318BQ4,
		catalog.WhisperSmall,
		catalog.EmbedMiniLM,
		catalog.Qwen2532BQ4,
		catalog.Llama3170BQ4,
	}
}

// LocalImage is the tag this box may already have. Empty means this
// catalog id has no workload image to inspect.
func LocalImage(catalogID string) string {
	switch catalogID {
	case catalog.Llama318BQ4:
		return "ghcr.io/fyberlabs/hypermesh-test"
	default:
		return ""
	}
}

var repoDigest = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*@sha256:[0-9a-f]{64}$`)

// FirstRepoDigest returns the first repo@sha256 value in a docker
// RepoDigests JSON array. Anything else is empty.
func FirstRepoDigest(raw []byte) string {
	var digests []string
	if err := json.Unmarshal(raw, &digests); err != nil {
		return ""
	}
	for _, item := range digests {
		digest := strings.ToLower(strings.TrimSpace(item))
		if strings.Contains(digest, "/") && repoDigest.MatchString(digest) {
			return digest
		}
	}
	return ""
}

// Pull fetches the local workload tag. It is not a catalog pin.
// A repo@sha256 is refused here so a missing inspect cannot be invented.
func Pull(ctx context.Context, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.Contains(ref, "@") {
		return fmt.Errorf("cert pull wants a local tag, not a pin")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "pull", ref).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			return fmt.Errorf("docker pull: %w", err)
		}
		return fmt.Errorf("docker pull: %w: %s", err, detail)
	}
	return nil
}

// Resolve returns the repo digest of the local workload image.
// It pulls the tag only when inspect is empty. An unknown catalog id
// and a failed pull are empty. The tag itself is never returned.
func Resolve(ctx context.Context, catalogID string) string {
	ref := LocalImage(catalogID)
	if ref == "" {
		return ""
	}
	if digest := Inspect(ctx, ref); digest != "" {
		return digest
	}
	if err := Pull(ctx, ref); err != nil {
		return ""
	}
	return Inspect(ctx, ref)
}

// Inspect runs docker image inspect for ref and returns a repo digest.
// A missing image or a missing docker is empty.
func Inspect(ctx context.Context, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", ref).Output()
	if err != nil {
		return ""
	}
	return FirstRepoDigest(out)
}
