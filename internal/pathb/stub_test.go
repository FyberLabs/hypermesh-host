package pathb

import (
	"context"
	"errors"
	"testing"

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

func TestStubPassesWithTelemetry(t *testing.T) {
	r := Runner{Collector: fakeCollect{snap: telem.Snapshot{ImageHash: "sha256:abc", UptimeS: 9}}}
	got := r.Run(context.Background(), api.Job{SuiteID: SuiteStubV1, Kind: api.KindPathB})
	if !got.Passed || got.ImageHash != "sha256:abc" {
		t.Fatalf("%+v", got)
	}
}

func TestUnknownSuiteFails(t *testing.T) {
	r := Runner{Collector: fakeCollect{snap: telem.Snapshot{UptimeS: 1}}}
	got := r.Run(context.Background(), api.Job{SuiteID: "real-suite-v9"})
	if got.Passed {
		t.Fatalf("unknown suite must fail")
	}
}

func TestTelemetryErrorFails(t *testing.T) {
	r := Runner{Collector: fakeCollect{err: errors.New("no /proc")}}
	got := r.Run(context.Background(), api.Job{SuiteID: SuiteStubV1})
	if got.Passed {
		t.Fatalf("expected fail")
	}
}

func TestEmptySuiteIDIsStub(t *testing.T) {
	r := Runner{Collector: fakeCollect{snap: telem.Snapshot{ImageHash: "h"}}}
	got := r.Run(context.Background(), api.Job{Kind: api.KindInstanceCheck})
	if !got.Passed || got.ImageHash != "h" {
		t.Fatalf("%+v", got)
	}
}
