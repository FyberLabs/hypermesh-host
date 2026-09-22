package pathb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/telem"
)

type fakeCollect struct {
	snap telem.Snapshot
	err  error
}

func (f fakeCollect) Collect(context.Context) (telem.Snapshot, error) {
	return f.snap, f.err
}

func downRunner(t *testing.T) Runner {
	t.Helper()
	return Runner{
		Collector: fakeCollect{snap: telem.Snapshot{ImageHash: "sha256:abc", UptimeS: 9}},
		Endpoint:  "http://127.0.0.1:1/v1/chat/completions",
		HTTP:      &http.Client{Timeout: 150 * time.Millisecond},
	}
}

func TestPathBPassesWithTelemetryWhenEndpointDown(t *testing.T) {
	r := downRunner(t)
	got := r.Run(context.Background(), api.Job{SuiteID: SuiteThinV1, Kind: api.KindPathB})
	if !got.Passed || got.ImageHash != "sha256:abc" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Reason, "skipped") {
		t.Fatalf("path_b should record inference skipped, got %q", got.Reason)
	}
	assertNoProbeLeak(t, got.Reason)
}

func TestInstanceCheckFailsWhenEndpointDown(t *testing.T) {
	r := downRunner(t)
	got := r.Run(context.Background(), api.Job{SuiteID: SuiteThinV1, Kind: api.KindInstanceCheck})
	if got.Passed {
		t.Fatalf("instance_check must fail when the completions endpoint is down: %+v", got)
	}
	if got.ImageHash != "sha256:abc" {
		t.Fatalf("still report telem hash: %+v", got)
	}
	assertNoProbeLeak(t, got.Reason)
}

func TestInstanceCheckPassesOnChoice(t *testing.T) {
	var sawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/" {
			t.Errorf("path %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		sawBody = string(raw)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "ready"}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	r := Runner{
		Collector: fakeCollect{snap: telem.Snapshot{ImageHash: "h"}},
		Endpoint:  srv.URL + "/v1/chat/completions",
	}
	got := r.Run(context.Background(), api.Job{Kind: api.KindInstanceCheck, SuiteID: SuiteThinV1})
	if !got.Passed || got.ImageHash != "h" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(sawBody, probeText) {
		t.Fatal("probe was not POSTed")
	}
	assertNoProbeLeak(t, got.Reason)
	if got.Measured {
		t.Fatal("a choice without usage.completion_tokens must not invent tok/s")
	}
}

func TestPathBMeasuresDecodeOnlyWhenUsageTokensExist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "ready"}},
			},
			"usage": map[string]any{"completion_tokens": 4},
		})
	}))
	t.Cleanup(srv.Close)
	r := Runner{
		Collector: fakeCollect{snap: telem.Snapshot{ImageHash: "sha256:abc"}},
		Endpoint:  srv.URL + "/v1/chat/completions",
	}
	got := r.Run(context.Background(), api.Job{Kind: api.KindPathB, SuiteID: SuiteThinV1})
	if !got.Passed || !got.Measured || got.SampleMs < 1 || got.DecodeTokS <= 0 {
		t.Fatalf("%+v", got)
	}
	assertNoProbeLeak(t, got.Reason)
}

func TestInstanceCheckFailsOnEmptyChoice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{}})
	}))
	t.Cleanup(srv.Close)
	r := Runner{
		Collector: fakeCollect{snap: telem.Snapshot{ImageHash: "h"}},
		Endpoint:  srv.URL,
	}
	got := r.Run(context.Background(), api.Job{Kind: api.KindInstanceCheck})
	if got.Passed {
		t.Fatalf("empty choices must fail instance_check: %+v", got)
	}
	assertNoProbeLeak(t, got.Reason)
}

func TestPathBPassesWhenCompletionsReturnChoice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "ready"}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	r := Runner{
		Collector: fakeCollect{snap: telem.Snapshot{ImageHash: "h"}},
		Endpoint:  srv.URL,
	}
	got := r.Run(context.Background(), api.Job{Kind: api.KindPathB, SuiteID: SuiteThinV1})
	if !got.Passed || got.Reason != "thin-v1" {
		t.Fatalf("%+v", got)
	}
	assertNoProbeLeak(t, got.Reason)
}

func TestUnknownSuiteFails(t *testing.T) {
	r := Runner{Collector: fakeCollect{snap: telem.Snapshot{UptimeS: 1}}}
	got := r.Run(context.Background(), api.Job{SuiteID: "stub-suite-v1", Kind: api.KindPathB})
	if got.Passed {
		t.Fatalf("unknown suite must fail")
	}
}

func TestTelemetryErrorFailsPathB(t *testing.T) {
	r := Runner{Collector: fakeCollect{err: errors.New("no /proc")}}
	got := r.Run(context.Background(), api.Job{SuiteID: SuiteThinV1, Kind: api.KindPathB})
	if got.Passed {
		t.Fatalf("expected fail")
	}
}

func TestEmptySuiteIDIsThinV1(t *testing.T) {
	r := downRunner(t)
	r.Collector = fakeCollect{snap: telem.Snapshot{ImageHash: "h"}}
	got := r.Run(context.Background(), api.Job{Kind: api.KindPathB})
	if !got.Passed || got.ImageHash != "h" {
		t.Fatalf("%+v", got)
	}
}

func TestPathBFailsWhenBootIDChanges(t *testing.T) {
	r := downRunner(t)
	r.Store = FileStore{Dir: t.TempDir()}
	r.Collector = fakeCollect{snap: telem.Snapshot{ImageHash: "sha256:abc", UptimeS: 40, BootID: "boot-aaa"}}

	first := r.Run(context.Background(), api.Job{SuiteID: SuiteThinV1, Kind: api.KindPathB})
	if !first.Passed || first.ImageHash != "sha256:abc" {
		t.Fatalf("first run must record boot_id and not fail: %+v", first)
	}

	r.Collector = fakeCollect{snap: telem.Snapshot{ImageHash: "sha256:abc", UptimeS: 3, BootID: "boot-bbb"}}
	got := r.Run(context.Background(), api.Job{SuiteID: SuiteThinV1, Kind: api.KindPathB})
	if got.Passed {
		t.Fatalf("path_b must fail when boot_id changed: %+v", got)
	}
	if got.Reason != "reboot" {
		t.Fatalf("host log reason must be reboot, got %q", got.Reason)
	}
	if got.ImageHash != "sha256:abc" {
		t.Fatalf("result body still has image_hash: %+v", got)
	}
	assertNoProbeLeak(t, got.Reason)
}

func TestPathBSameBootIDKeepsTelemInference(t *testing.T) {
	r := downRunner(t)
	r.Store = FileStore{Dir: t.TempDir()}
	r.Collector = fakeCollect{snap: telem.Snapshot{ImageHash: "sha256:abc", UptimeS: 9, BootID: "boot-same"}}

	first := r.Run(context.Background(), api.Job{SuiteID: SuiteThinV1, Kind: api.KindPathB})
	if !first.Passed || !strings.Contains(first.Reason, "skipped") {
		t.Fatalf("same boot first run: %+v", first)
	}

	r.Collector = fakeCollect{snap: telem.Snapshot{ImageHash: "sha256:abc", UptimeS: 12, BootID: "boot-same"}}
	got := r.Run(context.Background(), api.Job{SuiteID: SuiteThinV1, Kind: api.KindPathB})
	if !got.Passed || got.ImageHash != "sha256:abc" {
		t.Fatalf("same boot_id keeps telem pass: %+v", got)
	}
	if !strings.Contains(got.Reason, "skipped") {
		t.Fatalf("same boot_id keeps inference skipped: %q", got.Reason)
	}
	assertNoProbeLeak(t, got.Reason)
}

func TestPathBSameBootIDWithCompletions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "ready"}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	r := Runner{
		Collector: fakeCollect{snap: telem.Snapshot{ImageHash: "h", UptimeS: 8, BootID: "boot-same"}},
		Endpoint:  srv.URL,
		Store:     FileStore{Dir: t.TempDir()},
	}
	first := r.Run(context.Background(), api.Job{Kind: api.KindPathB, SuiteID: SuiteThinV1})
	if !first.Passed || first.Reason != "thin-v1" {
		t.Fatalf("%+v", first)
	}

	r.Collector = fakeCollect{snap: telem.Snapshot{ImageHash: "h", UptimeS: 9, BootID: "boot-same"}}
	got := r.Run(context.Background(), api.Job{Kind: api.KindPathB, SuiteID: SuiteThinV1})
	if !got.Passed || got.Reason != "thin-v1" {
		t.Fatalf("same boot_id keeps inference: %+v", got)
	}
	assertNoProbeLeak(t, got.Reason)
}

func TestPathBFirstRunRecordsBootID(t *testing.T) {
	dir := t.TempDir()
	r := downRunner(t)
	r.Store = FileStore{Dir: dir}
	r.Collector = fakeCollect{snap: telem.Snapshot{ImageHash: "h", UptimeS: 7, BootID: "first-boot"}}
	got := r.Run(context.Background(), api.Job{Kind: api.KindPathB, SuiteID: SuiteThinV1})
	if !got.Passed {
		t.Fatalf("first run must not fail: %+v", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, bootFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "first-boot") {
		t.Fatalf("boot identity not recorded: %s", raw)
	}
	fi, err := os.Stat(filepath.Join(dir, bootFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("boot.json is not a secret; got mode %o", fi.Mode().Perm())
	}
}

func TestPathBUptimeBackwardsIsReboot(t *testing.T) {
	now := time.Unix(1_700_000_100, 0)
	r := downRunner(t)
	r.Store = FileStore{Dir: t.TempDir()}
	r.Now = func() time.Time { return now }
	r.Collector = fakeCollect{snap: telem.Snapshot{ImageHash: "h", UptimeS: 80}}

	first := r.Run(context.Background(), api.Job{Kind: api.KindPathB, SuiteID: SuiteThinV1})
	if !first.Passed {
		t.Fatalf("first fallback run: %+v", first)
	}

	r.Collector = fakeCollect{snap: telem.Snapshot{ImageHash: "h", UptimeS: 4}}
	got := r.Run(context.Background(), api.Job{Kind: api.KindPathB, SuiteID: SuiteThinV1})
	if got.Passed || got.Reason != "reboot" {
		t.Fatalf("uptime going backwards is a reboot: %+v", got)
	}
}

func TestInstanceCheckIgnoresBootIDChange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "ready"}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	store := FileStore{Dir: t.TempDir()}
	r := Runner{
		Collector: fakeCollect{snap: telem.Snapshot{ImageHash: "h", UptimeS: 10, BootID: "boot-aaa"}},
		Endpoint:  srv.URL,
		Store:     store,
	}
	_ = r.Run(context.Background(), api.Job{Kind: api.KindPathB, SuiteID: SuiteThinV1})

	r.Collector = fakeCollect{snap: telem.Snapshot{ImageHash: "h", UptimeS: 2, BootID: "boot-bbb"}}
	got := r.Run(context.Background(), api.Job{Kind: api.KindInstanceCheck, SuiteID: SuiteThinV1})
	if !got.Passed || got.ImageHash != "h" {
		t.Fatalf("instance_check still needs a real completion, not a reboot gate: %+v", got)
	}
	assertNoProbeLeak(t, got.Reason)
}

func assertNoProbeLeak(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, probeText) {
		t.Fatalf("probe text leaked: %q", s)
	}
	if strings.Contains(s, "Reply with") {
		t.Fatalf("probe fragment leaked: %q", s)
	}
}
