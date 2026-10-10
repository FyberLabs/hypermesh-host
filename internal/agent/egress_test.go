package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/pathb"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

func TestAttachEgressObservationFailClosed(t *testing.T) {
	a := &Agent{log: discardLog()}
	a.dockerRun = nil
	a.egressSample = func(_ context.Context) ([]string, bool) { return nil, false }
	res := jobOutcome{Result: pathb.Result{Passed: true, ImageHash: "sha256:abc"}}
	a.attachEgressObservation(context.Background(), &res)
	if res.EgressOk == nil || *res.EgressOk {
		t.Fatalf("want egress_ok=false, got %+v", res.EgressOk)
	}
	if res.EgressReason != runtime.EgressUnobserved && res.EgressReason != runtime.EgressInspectFailed {
		t.Fatalf("want fail-closed reason, got %q", res.EgressReason)
	}
}

func TestJobResultBodyIncludesEgressForLabSoak(t *testing.T) {
	ok := true
	body := jobResultBody(api.KindPathB, jobOutcome{
		Result:       pathb.Result{Passed: true, ImageHash: "sha256:x"},
		EgressOk:     &ok,
		EgressReason: runtime.EgressOverlayOnly,
	})
	if body.EgressOk == nil || !*body.EgressOk || body.EgressReason != runtime.EgressOverlayOnly {
		t.Fatalf("lab soak body missing egress: %+v", body)
	}
	lease := jobResultBody(api.KindLeaseStart, jobOutcome{
		Result:       pathb.Result{Passed: true, ImageHash: "sha256:x"},
		EgressOk:     &ok,
		EgressReason: runtime.EgressOverlayOnly,
	})
	if lease.EgressOk != nil || lease.EgressReason != "" {
		t.Fatalf("lease_start must not carry egress: %+v", lease)
	}
}

func TestAttachEgressObservationOverlayOnly(t *testing.T) {
	netJSON, _ := json.Marshal([]map[string]any{{
		"Internal": true,
		"Options": map[string]string{
			"com.docker.network.bridge.enable_icc":            "false",
			"com.docker.network.bridge.enable_ip_masquerade": "false",
		},
		"Containers": map[string]any{},
	}})
	a := &Agent{log: discardLog()}
	a.dockerRun = func(_ context.Context, args ...string) ([]byte, error) {
		if !strings.Contains(strings.Join(args, " "), runtime.WorkloadNetworkName) {
			t.Fatalf("unexpected args: %v", args)
		}
		return netJSON, nil
	}
	a.egressSample = func(_ context.Context) ([]string, bool) {
		return []string{}, true
	}
	res := jobOutcome{}
	a.attachEgressObservation(context.Background(), &res)
	if res.EgressOk == nil || !*res.EgressOk || res.EgressReason != runtime.EgressOverlayOnly {
		t.Fatalf("want overlay_publish_only, got ok=%v reason=%s", res.EgressOk, res.EgressReason)
	}
}
