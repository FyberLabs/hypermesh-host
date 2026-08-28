package pathb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func assertNoProbeLeak(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, probeText) {
		t.Fatalf("probe text leaked: %q", s)
	}
	if strings.Contains(s, "Reply with") {
		t.Fatalf("probe fragment leaked: %q", s)
	}
}
