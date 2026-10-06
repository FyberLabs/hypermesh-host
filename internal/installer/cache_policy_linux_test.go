//go:build linux

package installer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/FyberLabs/hypermesh-host/internal/agent"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerAcquisitionHonorsSharedQuota(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	os.Mkdir(cache, 0700)
	if err := agent.WriteCachePolicy(dir, agent.CachePolicy{Schema: 1, MaxBytes: 3}); err != nil {
		t.Fatal(err)
	}
	body := []byte("four")
	pin := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	path := filepath.Join(cache, strings.TrimPrefix(pin, "sha256:")+".gguf")
	called := false
	err := acquireModel(context.Background(), path, pin, 4, func(context.Context) (io.ReadCloser, error) {
		called = true
		return io.NopCloser(bytes.NewReader(body)), nil
	})
	if err == nil || called {
		t.Fatalf("quota refusal err=%v fetched=%v", err, called)
	}
}
func TestGuidedCachePolicyRequiresApprovalAndResumes(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	ui := guidedUI{in: bufio.NewReader(strings.NewReader("1\n2\nn\n")), out: &out, ctx: context.Background()}
	if err := guidedCachePolicy(dir, ui, false); err == nil {
		t.Fatal("decline accepted")
	}
	if p, _ := agent.ReadCachePolicy(dir); p != nil {
		t.Fatal("decline wrote policy")
	}
	ui.in = bufio.NewReader(strings.NewReader("1\n2\ny\n"))
	if err := guidedCachePolicy(dir, ui, false); err != nil {
		t.Fatal(err)
	}
	ui.in = bufio.NewReader(strings.NewReader(""))
	if err := guidedCachePolicy(dir, ui, false); err != nil {
		t.Fatal("resume prompted again", err)
	}
	p, err := agent.ReadCachePolicy(dir)
	if err != nil || p.MaxBytes != 1<<30 || p.ReserveBytes != 2<<30 {
		t.Fatalf("%+v %v", p, err)
	}
}
