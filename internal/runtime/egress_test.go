package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestObserveWorkloadEgressInternalClean(t *testing.T) {
	netJSON, _ := json.Marshal([]networkInspectJSON{{
		Internal: true,
		Options: map[string]string{
			"com.docker.network.bridge.enable_icc":            "false",
			"com.docker.network.bridge.enable_ip_masquerade": "false",
		},
		Containers: map[string]struct {
			IPv4Address string `json:"IPv4Address"`
			Name        string `json:"Name"`
		}{
			"c1": {IPv4Address: "172.18.0.2/16", Name: "hypermesh-cert"},
		},
	}})
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "network inspect "+WorkloadNetworkName {
			t.Fatalf("unexpected docker args: %v", args)
		}
		return netJSON, nil
	}
	sample := func(_ context.Context) ([]string, bool) {
		return []string{
			"ESTAB 0 0 172.18.0.2:8080 172.18.0.1:45000",
		}, true
	}
	ok, reason := ObserveWorkloadEgress(context.Background(), run, sample)
	if !ok || reason != EgressOverlayOnly {
		t.Fatalf("want overlay_publish_only, got ok=%v reason=%s", ok, reason)
	}
}

func TestObserveWorkloadEgressFailClosedMissingSample(t *testing.T) {
	netJSON, _ := json.Marshal([]networkInspectJSON{{
		Internal: true,
		Options:  map[string]string{},
	}})
	run := func(_ context.Context, _ ...string) ([]byte, error) { return netJSON, nil }
	sample := func(_ context.Context) ([]string, bool) { return nil, false }
	ok, reason := ObserveWorkloadEgress(context.Background(), run, sample)
	if ok || reason != EgressUnobserved {
		t.Fatalf("want egress_unobserved, got ok=%v reason=%s", ok, reason)
	}
}

func TestObserveWorkloadEgressNotInternal(t *testing.T) {
	netJSON, _ := json.Marshal([]networkInspectJSON{{
		Internal: false,
	}})
	run := func(_ context.Context, _ ...string) ([]byte, error) { return netJSON, nil }
	sample := func(_ context.Context) ([]string, bool) { return []string{}, true }
	ok, reason := ObserveWorkloadEgress(context.Background(), run, sample)
	if ok || reason != EgressNotInternal {
		t.Fatalf("want network_not_internal, got ok=%v reason=%s", ok, reason)
	}
}

func TestObserveWorkloadEgressUnexpectedPublic(t *testing.T) {
	netJSON, _ := json.Marshal([]networkInspectJSON{{
		Internal: true,
		Containers: map[string]struct {
			IPv4Address string `json:"IPv4Address"`
			Name        string `json:"Name"`
		}{
			"c1": {IPv4Address: "172.18.0.2/16"},
		},
	}})
	run := func(_ context.Context, _ ...string) ([]byte, error) { return netJSON, nil }
	sample := func(_ context.Context) ([]string, bool) {
		return []string{"ESTAB 172.18.0.2:443 8.8.8.8:53"}, true
	}
	ok, reason := ObserveWorkloadEgress(context.Background(), run, sample)
	if ok || reason != EgressUnexpected {
		t.Fatalf("want egress_unexpected, got ok=%v reason=%s", ok, reason)
	}
}

func TestObserveWorkloadEgressNilRunner(t *testing.T) {
	ok, reason := ObserveWorkloadEgress(context.Background(), nil, nil)
	if ok || reason != EgressUnobserved {
		t.Fatalf("want egress_unobserved, got ok=%v reason=%s", ok, reason)
	}
}
