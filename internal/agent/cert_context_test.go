package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAssignedCertificationContextUsesLabWorkloadAndCleans(t *testing.T) {
	for _, pass := range []bool{true, false} {
		t.Run(map[bool]string{true: "matching full window", false: "truncated full window"}[pass], func(t *testing.T) {
			a, job, calls := certFixture(t, 200)
			job.CertificationContextTokens = 512
			a.cfg.LlamaCtxSize = "4096" // The assigned context must win over the host default.
			fullWindowCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/props":
					writeJSON(w, 200, map[string]any{"default_generation_settings": map[string]int{"n_ctx": 512}, "total_slots": 1})
				case "/tokenize":
					writeJSON(w, 200, map[string]any{"tokens": []int{123}})
				case "/completion":
					fullWindowCalls++
					time.Sleep(2 * time.Millisecond)
					writeJSON(w, 200, map[string]any{"model": "/models/model.gguf", "tokens_evaluated": 496, "tokens_predicted": map[bool]int{true: 16, false: 15}[pass], "truncated": !pass, "stop": true, "stop_type": "limit", "generation_settings": map[string]int{"n_ctx": 512}})
				default:
					time.Sleep(2 * time.Millisecond)
					writeJSON(w, 200, map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ready"}}}, "usage": map[string]int{"completion_tokens": 4}})
				}
			}))
			defer server.Close()
			a.certEndpoint = server.URL
			got := a.runJob(context.Background(), job)
			if got.Passed != pass || got.WorkloadVerified != pass || got.Measured != pass || fullWindowCalls != 1 {
				t.Fatalf("pass=%v result=%+v full-window calls=%d", pass, got, fullWindowCalls)
			}
			if pass && (got.ContextFit == nil || !got.ContextFit.ValidFor(512)) {
				t.Fatal("assigned full-context evidence missing")
			}
			joined := ""
			for _, args := range *calls {
				joined += strings.Join(args, " ") + "\n"
			}
			for _, part := range []string{"LLAMA_ARG_CTX_SIZE=512", "LLAMA_ARG_N_PARALLEL=1", "stop cert-container", "rm hypermesh-cert-bench-"} {
				if !strings.Contains(joined, part) {
					t.Fatalf("missing %q in %s", part, joined)
				}
			}
			if a.certContextTokens != 0 || a.cfg.LlamaCtxSize != "4096" {
				t.Fatal("assignment changed another job's context")
			}
		})
	}
}
