package protection

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// VerifyArtifacts checks content-addressed evidence, never executes it.
func VerifyArtifacts(r Report, directory string) error {
	refs := map[string]bool{}
	for _, c := range r.Checks {
		refs[c.ArtifactSHA256] = true
	}
	for _, s := range r.Samples {
		refs[s.ArtifactSHA256] = true
	}
	for ref := range refs {
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
		hash := sha256.New()
		n, err := io.Copy(hash, io.LimitReader(file, (16<<20)+1))
		file.Close()
		if err != nil || n == 0 || n > 16<<20 || hex.EncodeToString(hash.Sum(nil)) != ref {
			return fmt.Errorf("artifact content mismatch: %s", ref)
		}
	}
	return nil
}
