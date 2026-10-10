package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

// matrixDigest is a fake repo@digest. Not GHCR. Not invented at POST time.
const matrixDigest = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// TestJobKindFailPassMatrix is the README fail/pass table for
// path_b (lab soak), instance_check, lease_start, lease_stop, and sell_pause.
// Existing *_test.go coverage stays; this file is the single matrix.
func TestJobKindFailPassMatrix(t *testing.T) {
	const completion = "UNIQUE_MATRIX_COMPLETION"
	type row struct {
		name        string
		kind        api.JobKind
		job         api.Job
		setup       func(*testing.T, *Agent)
		wantPass    bool
		wantReason  string
		wantHash    string // exact image_hash; empty means do not require exact
		requireHash bool   // passed=true must POST a non-empty image_hash
		forbidHash  bool   // must not invent a hash
		wantSell    api.SellState
		dockerFirst []string // expected docker argv[0] sequence (fake runtime)
		noDocker    bool
	}

	cases := []row{
		{
			name: "path_b/pass_non_empty_image_hash",
			kind: api.KindPathB,
			job:  api.Job{JobID: "m-pathb-pass", Kind: api.KindPathB, DeviceID: testDevice, SuiteID: "thin-v1"},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				downCompletions(t, a)
			},
			wantPass:    true,
			wantReason:  "thin-v1 telem; inference skipped",
			wantHash:    "sha256:fixture",
			requireHash: true,
			noDocker:    true,
		},
		{
			name: "path_b/reboot_fail",
			kind: api.KindPathB,
			job:  api.Job{JobID: "m-pathb-reboot", Kind: api.KindPathB, DeviceID: testDevice, SuiteID: "thin-v1"},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				downCompletions(t, a)
				if err := os.WriteFile(filepath.Join(a.cfg.StateDir, "boot.json"), []byte(`{"boot_id":"previous-boot","uptime_s":99}`+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantPass:   false,
			wantReason: "reboot",
			noDocker:   true,
		},
		{
			name: "path_b/stub_backend_exact_reason",
			kind: api.KindPathB,
			job: api.Job{
				JobID: "m-pathb-stub", Kind: api.KindPathB, DeviceID: testDevice,
				SuiteID: "thin-v1", Runtime: "nvidia", Backend: "llama-cpp-vulkan",
			},
			wantPass:   false,
			wantReason: "This image does not run llama-cpp-vulkan.",
			forbidHash: true,
			noDocker:   true,
		},
		{
			name: "path_b/empty_telem_no_lease",
			kind: api.KindPathB,
			job:  api.Job{JobID: "m-pathb-empty", Kind: api.KindPathB, DeviceID: testDevice, SuiteID: "thin-v1"},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				a.cfg.ImageHash = ""
				downCompletions(t, a)
			},
			wantPass:   false,
			wantReason: "no image_hash",
			forbidHash: true,
			noDocker:   true,
		},
		{
			name: "instance_check/needs_real_completion",
			kind: api.KindInstanceCheck,
			job:  api.Job{JobID: "m-check-up", Kind: api.KindInstanceCheck, DeviceID: testDevice, SuiteID: "thin-v1"},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"choices": []map[string]any{
							{"message": map[string]string{"content": completion}},
						},
					})
				}))
				t.Cleanup(srv.Close)
				a.pathb.Endpoint = srv.URL
			},
			wantPass:    true,
			wantReason:  "thin-v1",
			wantHash:    "sha256:fixture",
			requireHash: true,
			noDocker:    true,
		},
		{
			name: "instance_check/fail_when_down",
			kind: api.KindInstanceCheck,
			job:  api.Job{JobID: "m-check-down", Kind: api.KindInstanceCheck, DeviceID: testDevice, SuiteID: "thin-v1"},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				downCompletions(t, a)
			},
			wantPass:   false,
			wantReason: "completions unreachable",
			noDocker:   true,
		},
		{
			name: "lease_start/empty_digest",
			kind: api.KindLeaseStart,
			job: api.Job{
				JobID: "m-start-empty", Kind: api.KindLeaseStart, DeviceID: testDevice,
				LeaseID: "lease-empty", CatalogID: "llama-3.1-8b-q4", Runtime: "nvidia",
			},
			wantPass:   false,
			wantReason: "no image_digest; no soak image",
			forbidHash: true,
			noDocker:   true,
		},
		{
			name: "lease_start/bare_sha256",
			kind: api.KindLeaseStart,
			job: api.Job{
				JobID: "m-start-bare", Kind: api.KindLeaseStart, DeviceID: testDevice,
				LeaseID: "lease-bare", CatalogID: "llama-3.1-8b-q4",
				ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Runtime:     "nvidia",
			},
			wantPass:   false,
			wantReason: "image_digest is not repo@digest",
			forbidHash: true,
			noDocker:   true,
		},
		{
			name: "lease_start/fake_runtime_repo_digest",
			kind: api.KindLeaseStart,
			job: api.Job{
				JobID: "m-start-ok", Kind: api.KindLeaseStart, DeviceID: testDevice,
				LeaseID: "lease-ok", CatalogID: "llama-3.1-8b-q4",
				ImageDigest: matrixDigest, FileHash: fixtureGGUFHash(), Runtime: "nvidia",
			},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				readyLeaseStart(t, a)
			},
			wantPass:    true,
			wantReason:  "lease_start",
			requireHash: true,
			dockerFirst: []string{"pull", "run"},
		},
		{
			name: "lease_start/no_tunnel",
			kind: api.KindLeaseStart,
			job: api.Job{
				JobID: "m-start-notun", Kind: api.KindLeaseStart, DeviceID: testDevice,
				LeaseID: "lease-notun", CatalogID: "llama-3.1-8b-q4",
				ImageDigest: matrixDigest, Runtime: "nvidia",
			},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				path, _ := writeTestGGUF(t, a.cfg.StateDir)
				a.cfg.GGUFPath = path
				a.bindIP = ""
			},
			wantPass:   false,
			wantReason: "no tunnel address",
			forbidHash: true,
			noDocker:   true,
		},
		{
			name: "lease_start/no_gguf",
			kind: api.KindLeaseStart,
			job: api.Job{
				JobID: "m-start-nogguf", Kind: api.KindLeaseStart, DeviceID: testDevice,
				LeaseID: "lease-nogguf", CatalogID: "llama-3.1-8b-q4",
				ImageDigest: matrixDigest, Runtime: "nvidia",
			},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				a.bindIP = "10.77.0.2/32"
				a.cfg.GGUFPath = ""
			},
			wantPass:   false,
			wantReason: "no GGUF path",
			forbidHash: true,
			noDocker:   true,
		},
		{
			name: "lease_start/gguf_directory",
			kind: api.KindLeaseStart,
			job: api.Job{
				JobID: "m-start-ggufdir", Kind: api.KindLeaseStart, DeviceID: testDevice,
				LeaseID: "lease-ggufdir", CatalogID: "llama-3.1-8b-q4",
				ImageDigest: matrixDigest, Runtime: "nvidia",
			},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				a.bindIP = "10.77.0.2/32"
				a.cfg.GGUFPath = a.cfg.StateDir
			},
			wantPass:   false,
			wantReason: "GGUF is not a file",
			forbidHash: true,
			noDocker:   true,
		},
		{
			name: "lease_start/already_running_other",
			kind: api.KindLeaseStart,
			job: api.Job{
				JobID: "m-start-dual", Kind: api.KindLeaseStart, DeviceID: testDevice,
				LeaseID: "lease-new", CatalogID: "llama-3.1-8b-q4",
				ImageDigest: matrixDigest, Runtime: "nvidia",
			},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				readyLeaseStart(t, a)
				a.lease = &runtime.Handle{
					Name:        "hypermesh-lease-running",
					LeaseID:     "lease-running",
					ImageDigest: matrixDigest,
					FileHash:    "sha256:already",
					ContainerID: "cid-local",
				}
			},
			wantPass:   false,
			wantReason: "lease already running",
			forbidHash: true,
			noDocker:   true,
		},
		{
			name: "lease_start/same_lease_retry",
			kind: api.KindLeaseStart,
			job: api.Job{
				JobID: "m-start-retry", Kind: api.KindLeaseStart, DeviceID: testDevice,
				LeaseID: "lease-running", CatalogID: "llama-3.1-8b-q4",
				ImageDigest: matrixDigest, Runtime: "nvidia",
			},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				readyLeaseStart(t, a)
				a.lease = &runtime.Handle{
					Name:        "hypermesh-lease-running",
					LeaseID:     "lease-running",
					ImageDigest: matrixDigest,
					FileHash:    "sha256:already",
					ContainerID: "cid-local",
				}
			},
			wantPass:    true,
			wantReason:  "lease_start",
			wantHash:    "sha256:already",
			requireHash: true,
			noDocker:    true,
		},
		{
			name: "lease_start/sell_pause_blocks",
			kind: api.KindLeaseStart,
			job: api.Job{
				JobID: "m-start-paused", Kind: api.KindLeaseStart, DeviceID: testDevice,
				LeaseID: "lease-paused", CatalogID: "llama-3.1-8b-q4",
				ImageDigest: matrixDigest, Runtime: "nvidia",
			},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				pause := a.runJob(context.Background(), api.Job{
					JobID: "m-pause-first", Kind: api.KindSellPause, DeviceID: testDevice,
				})
				if !pause.Passed {
					t.Fatalf("setup pause %+v", pause)
				}
			},
			wantPass:   false,
			wantReason: "sell paused",
			forbidHash: true,
			noDocker:   true,
		},
		{
			name: "lease_stop/match",
			kind: api.KindLeaseStop,
			job:  api.Job{JobID: "m-stop-match", Kind: api.KindLeaseStop, DeviceID: testDevice, LeaseID: "lease-running"},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				a.lease = &runtime.Handle{
					Name:        "hypermesh-lease-running",
					LeaseID:     "lease-running",
					ImageDigest: matrixDigest,
					ContainerID: "cid-local",
				}
			},
			wantPass:    true,
			wantReason:  "lease_stop",
			wantHash:    matrixDigest,
			dockerFirst: []string{"stop", "rm"},
		},
		{
			name: "lease_stop/mismatch",
			kind: api.KindLeaseStop,
			job:  api.Job{JobID: "m-stop-mis", Kind: api.KindLeaseStop, DeviceID: testDevice, LeaseID: "lease-other"},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				a.lease = &runtime.Handle{
					Name:        "hypermesh-lease-running",
					LeaseID:     "lease-running",
					ImageDigest: matrixDigest,
					ContainerID: "cid-local",
				}
			},
			wantPass:   false,
			wantReason: "lease_id mismatch",
			noDocker:   true,
		},
		{
			name:       "lease_stop/no_handle_soft_noop",
			kind:       api.KindLeaseStop,
			job:        api.Job{JobID: "m-stop-none", Kind: api.KindLeaseStop, DeviceID: testDevice, LeaseID: "lease-anything"},
			wantPass:   true,
			wantReason: "lease_stop",
			wantHash:   "sha256:fixture", // host hash; never generated
			noDocker:   true,
		},
		{
			name:       "sell_pause/match",
			kind:       api.KindSellPause,
			job:        api.Job{JobID: "m-sell-ok", Kind: api.KindSellPause, DeviceID: testDevice},
			wantPass:   true,
			wantReason: "sell_pause",
			wantSell:   api.SellPaused,
			noDocker:   true,
		},
		{
			name: "sell_pause/device_id_mismatch",
			kind: api.KindSellPause,
			job: api.Job{
				JobID: "m-sell-mis", Kind: api.KindSellPause,
				DeviceID: "dddddddd-dddd-dddd-dddd-dddddddddddd",
			},
			wantPass:   false,
			wantReason: "device_id mismatch",
			wantSell:   api.SellSelling,
			noDocker:   true,
		},
		{
			name: "sell_pause/already_paused_noop",
			kind: api.KindSellPause,
			job:  api.Job{JobID: "m-sell-again", Kind: api.KindSellPause, DeviceID: testDevice},
			setup: func(t *testing.T, a *Agent) {
				t.Helper()
				first := a.runJob(context.Background(), api.Job{
					JobID: "m-sell-first", Kind: api.KindSellPause, DeviceID: testDevice,
				})
				if !first.Passed || first.SellState != api.SellPaused {
					t.Fatalf("setup pause %+v", first)
				}
			},
			wantPass:   true,
			wantReason: "sell_pause",
			wantSell:   api.SellPaused,
			noDocker:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			uptime := filepath.Join(dir, "uptime")
			if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var dockerCalls [][]string
			a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
			a.state.DeviceID = testDevice
			a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				dockerCalls = append(dockerCalls, append([]string{}, args...))
				return []byte("cid-matrix\n"), nil
			}}
			if tc.setup != nil {
				tc.setup(t, a)
			}

			if tc.kind == api.KindInstanceCheck {
				setSafeWorkloadInspection(a)
			}
			got := a.runJob(context.Background(), tc.job)
			if got.Passed != tc.wantPass || (tc.wantReason != "" && got.Reason != tc.wantReason) {
				t.Fatalf("result %+v want pass=%v reason=%q", got, tc.wantPass, tc.wantReason)
			}
			if tc.requireHash && strings.TrimSpace(got.ImageHash) == "" {
				t.Fatalf("passed=true requires non-empty image_hash: %+v", got)
			}
			if tc.wantHash != "" && got.ImageHash != tc.wantHash {
				t.Fatalf("image_hash %q want %q", got.ImageHash, tc.wantHash)
			}
			if tc.kind == api.KindLeaseStart && got.Passed && got.ImageHash == tc.job.ImageDigest {
				t.Fatalf("must not echo OCI pin as image_hash: %q", got.ImageHash)
			}
			if tc.forbidHash && got.ImageHash != "" {
				t.Fatalf("must not invent image_hash: %+v", got)
			}
			if strings.Contains(got.ImageHash, "ghcr.io") {
				t.Fatalf("no GHCR: %q", got.ImageHash)
			}
			if tc.kind == api.KindSellPause {
				if got.SellState != tc.wantSell {
					t.Fatalf("sell_state %q want %q", got.SellState, tc.wantSell)
				}
				if got.Passed && got.SellState == api.SellNA {
					t.Fatalf("never passed=true with sell_state=n/a: %+v", got)
				}
			}

			body := jobResultBody(tc.kind, got)
			assertLockedResultShape(t, tc.kind, body)
			if tc.noDocker && len(dockerCalls) != 0 {
				t.Fatalf("must not invoke docker, calls=%v", dockerCalls)
			}
			if len(tc.dockerFirst) > 0 {
				assertDockerLike(t, dockerCalls, tc.dockerFirst, matrixDigest)
			}
			if strings.Contains(got.Reason, completion) || strings.Contains(got.Reason, "Reply with") {
				t.Fatalf("probe/completion leaked into reason: %q", got.Reason)
			}
		})
	}
}

func TestJobKindStubBackendExactReasons(t *testing.T) {
	backends := []string{"llama-cpp-cpu", "llama-cpp-vulkan", "hip-rocm", "disk-io", "llama-cpp-metal"}
	kinds := []api.JobKind{api.KindPathB, api.KindInstanceCheck, api.KindLeaseStart}
	for _, kind := range kinds {
		for _, backend := range backends {
			if backend == "llama-cpp-cpu" && runtime.MetalImageEnabled() {
				continue
			}
			if backend == "hip-rocm" && runtime.HIPImageEnabled() {
				continue
			}
			t.Run(string(kind)+"/"+backend, func(t *testing.T) {
				dir := t.TempDir()
				uptime := filepath.Join(dir, "uptime")
				if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				var dockerCalls int
				a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
				a.state.DeviceID = testDevice
				a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
					dockerCalls++
					return []byte("cid\n"), nil
				}}
				job := api.Job{
					JobID: "m-stub", Kind: kind, DeviceID: testDevice,
					SuiteID: "thin-v1", Runtime: "nvidia", Backend: backend,
					LeaseID: "lease-stub", CatalogID: "llama-3.1-8b-q4",
					ImageDigest: matrixDigest,
				}
				got := a.runJob(context.Background(), job)
				want := "This image does not run " + backend + "."
				if got.Passed || got.Reason != want {
					t.Fatalf("%+v want %q", got, want)
				}
				if dockerCalls != 0 {
					t.Fatalf("stub must not call docker, calls=%d", dockerCalls)
				}
				if got.ImageHash != "" {
					t.Fatalf("must not invent image_hash: %+v", got)
				}
			})
		}
	}
}

func downCompletions(t *testing.T, a *Agent) {
	t.Helper()
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}
}

func assertLockedResultShape(t *testing.T, kind api.JobKind, body api.JobResultRequest) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	switch kind {
	case api.KindSellPause:
		if !strings.Contains(s, `"sell_state"`) {
			t.Fatalf("sell_pause must POST sell_state: %s", s)
		}
		if body.Passed && body.SellState == api.SellNA {
			t.Fatalf("never passed=true with n/a: %+v", body)
		}
	default:
		if strings.Contains(s, "sell_state") || strings.Contains(s, `"until"`) {
			t.Fatalf("%s result must stay {passed, image_hash?}: %s", kind, s)
		}
		if strings.Contains(s, "reason_code") {
			t.Fatalf("%s result must not include reason_code: %s", kind, s)
		}
		if kind != api.KindPathB {
			if strings.Contains(s, `"reason"`) || strings.Contains(s, "egress_") {
				t.Fatalf("%s result must stay {passed, image_hash?}: %s", kind, s)
			}
		}
	}
	if strings.Contains(s, "ghcr.io") || strings.Contains(s, "peer_private") || strings.Contains(s, "device_secret") {
		t.Fatalf("result leaked forbidden field: %s", s)
	}
}

func assertDockerLike(t *testing.T, calls [][]string, wantFirst []string, ref string) {
	t.Helper()
	filtered := make([][]string, 0, len(calls))
	for _, c := range calls {
		if len(c) > 0 && c[0] == "network" {
			continue
		}
		filtered = append(filtered, c)
	}
	if len(filtered) != len(wantFirst) {
		t.Fatalf("docker calls %v want first tokens %v", filtered, wantFirst)
	}
	for i, w := range wantFirst {
		if len(filtered[i]) == 0 || filtered[i][0] != w {
			t.Fatalf("call %d %v want %s", i, filtered[i], w)
		}
		joined := strings.Join(filtered[i], " ")
		if strings.Contains(joined, "--gpus") {
			t.Fatalf("GPU path is --runtime=nvidia, not --gpus: %v", filtered[i])
		}
		if strings.Contains(joined, "ghcr.io") {
			t.Fatalf("no GHCR pull: %v", filtered[i])
		}
		if w == "pull" && (len(filtered[i]) < 2 || filtered[i][1] != ref) {
			t.Fatalf("pull ref %v want %s", filtered[i], ref)
		}
		if w == "run" {
			if !strings.Contains(joined, "--runtime=nvidia") || !strings.Contains(joined, ref) {
				t.Fatalf("run args %v", filtered[i])
			}
			if strings.Contains(joined, "--network=host") {
				t.Fatalf("workload must not use host network: %v", filtered[i])
			}
			if !strings.Contains(joined, runtime.WorkloadNetworkName) {
				t.Fatalf("want workload network: %v", filtered[i])
			}
			if !strings.Contains(joined, "10.77.0.2:8080:8080") {
				t.Fatalf("publish must be WG IP: %v", filtered[i])
			}
		}
	}
}
