package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/pathb"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

var certImagePin = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}$`)

// runCertWorkload owns a temporary, pinned workload for this job only.
// Missing pins, an existing lease, failed inference or uncertain cleanup
// cannot be turned into a certification sample. No service activation,
// model download, lease creation or sell-state change happens here.
func (a *Agent) runCertWorkload(ctx context.Context, job api.Job, observe func(context.Context)) (result pathb.Result) {
	job.SuiteID = certSuite(job.SuiteID)
	fail := func(reason string) pathb.Result { return pathb.Result{Reason: reason} }
	if strings.TrimSpace(job.JobID) == "" || (job.SuiteID != pathb.SuitePackV1 && job.SuiteID != pathb.SuiteThinV1) {
		return fail("certification requires job identity and a supported suite")
	}
	if strings.TrimSpace(job.CatalogID) == "" || !certImagePin.MatchString(job.ImageDigest) {
		return fail("certification requires catalog_id and pinned image_digest")
	}
	pin, err := normalizeModelID(job.FileHash)
	if err != nil || job.FileHash == "" {
		return fail("certification requires pinned file_hash")
	}
	backend, _ := runtime.ResolveBackend(job.Backend)
	// HIP and Metal runners do not yet bind the verified GGUF and alias.
	// Do not certify their scaffold as a pinned workload.
	if backend != runtime.NVIDIAContainer || job.Runtime != "nvidia" {
		return fail("pinned certification requires the NVIDIA runtime")
	}
	if a.localLease() != nil {
		return fail("certification refused while a lease is recorded")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	gguf := a.cfg.GGUFHostPath()
	if cached, cacheErr := a.cacheDest(pin); cacheErr == nil {
		if _, statErr := os.Stat(cached); statErr == nil { gguf = cached }
	}
	if err := runtime.RequireGGUFFile(gguf); err != nil {
		return fail("certification model is missing; pull the pinned model first")
	}
	hash, err := certFileHash(ctx, gguf)
	if err != nil || hash != pin {
		return fail("certification model hash mismatch")
	}
	starter := a.starterFor(backend)
	handle, err := starter.Start(ctx, runtime.Spec{
		LeaseID: certLeaseID + "-" + job.JobID, CatalogID: job.CatalogID,
		ImageDigest: job.ImageDigest, Runtime: job.Runtime, Backend: job.Backend,
		HostPort: a.cfg.PublishPort(), GGUFPath: gguf,
		// Certification is local only; it never publishes on the host LAN or tunnel.
		BindAddress: "127.0.0.1",
		LlamaJinja: a.cfg.LlamaJinja, LlamaCtxSize: a.cfg.LlamaCtxSize,
		LlamaTimeout: a.cfg.LlamaTimeout,
	})
	if err != nil { return fail("certification runtime start failed") }
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if starter.Stop(cleanup, handle) != nil {
			result = fail("certification cleanup failed; inspect the temporary container before retrying")
		}
	}()
	runner := a.pathb
	runner.Model = job.CatalogID
	runner.Endpoint = "http://127.0.0.1:" + a.cfg.PublishPort() + "/v1/chat/completions"
	if a.certEndpoint != "" { runner.Endpoint = a.certEndpoint }
	wait := a.certReadyWait
	if wait <= 0 { wait = 3*time.Minute }
	ready, done := context.WithTimeout(ctx, wait)
	defer done()
	for !runner.Probe(ready) {
		if ready.Err() != nil { return fail("certification completions not ready") }
		timer := time.NewTimer(250*time.Millisecond)
		select {
		case <-ready.Done(): timer.Stop(); return fail("certification completions not ready")
		case <-timer.C:
		}
	}
	result = a.requireImageHash(job.Kind, runner.Run(ctx, job))
	if !result.Passed || !result.Measured || ctx.Err() != nil {
		return fail("certification requires a real timed completion")
	}
	// Do not accept a measurement if the mounted bytes changed during execution.
	after, err := certFileHash(ctx, gguf)
	if err != nil || after != pin { return fail("certification model changed during execution") }
	// Admission must observe the workload before deferred cleanup removes it.
	if observe != nil { observe(ctx) }
	if ctx.Err() != nil { return fail("certification observation timed out") }
	result.WorkloadVerified = true
	return result
}

// Hashing the model participates in the whole workload deadline. The regular
// file read itself uses the OS filesystem semantics; no remote fetch occurs.
func certFileHash(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil { return "", err }
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil { return "", err }
		n, err := f.Read(buf)
		if n > 0 { _, _ = h.Write(buf[:n]) }
		if err == io.EOF { break }
		if err != nil { return "", err }
	}
	if err := ctx.Err(); err != nil { return "", err }
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

func certSuite(suite string) string {
	if strings.TrimSpace(suite) == "" { return pathb.SuiteThinV1 }
	return strings.TrimSpace(suite)
}
