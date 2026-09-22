package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func assertWorkloadJail(t *testing.T, args []string) {
	t.Helper()
	if flagValue(args, "--network") == "host" || contains(args, "--network=host") {
		t.Fatalf("must not use host network: %v", args)
	}
	if flagValue(args, "--network") != WorkloadNetworkName {
		t.Fatalf("want network %s: %v", WorkloadNetworkName, args)
	}
	if !contains(args, "--read-only") {
		t.Fatalf("want --read-only: %v", args)
	}
	if !contains(args, "--cap-drop") || !contains(args, "ALL") {
		t.Fatalf("want --cap-drop ALL: %v", args)
	}
	if !contains(args, "--security-opt") || !contains(args, "no-new-privileges:true") {
		t.Fatalf("want no-new-privileges: %v", args)
	}
	if !contains(args, "--tmpfs") || !contains(args, "/tmp") {
		t.Fatalf("want /tmp tmpfs for read-only root: %v", args)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "docker.sock") {
		t.Fatalf("must not mount docker.sock: %v", args)
	}
	// Weights mount is the only host path bind; no /dev/video, /dev/ttyUSB, etc.
	for _, a := range args {
		if strings.HasPrefix(a, "--device") && !strings.Contains(a, "/dev/kfd") && !strings.Contains(a, "/dev/dri") {
			t.Fatalf("unexpected host device: %v", args)
		}
	}
	if err := jailForbidden(args); err != nil {
		t.Fatalf("jailForbidden: %v", err)
	}
}

func TestJailArgs(t *testing.T) {
	assertWorkloadJail(t, append([]string{"run", "-d"}, JailArgs()...))
}

func TestEnsureWorkloadNetworkCreatesWhenMissing(t *testing.T) {
	var seen [][]string
	err := ensureWorkloadNetwork(context.Background(), func(_ context.Context, args ...string) ([]byte, error) {
		seen = append(seen, append([]string{}, args...))
		if len(args) >= 2 && args[0] == "network" && args[1] == "inspect" {
			if len(seen) == 1 {
				return nil, errors.New("no such network")
			}
			return []byte(`[{"Name":"hypermesh-workload"}]`), nil
		}
		if len(args) >= 2 && args[0] == "network" && args[1] == "create" {
			if !contains(args, "--internal") || !contains(args, WorkloadNetworkName) {
				t.Fatalf("create args: %v", args)
			}
			return []byte("netid\n"), nil
		}
		t.Fatalf("unexpected %v", args)
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0][1] != "inspect" || seen[1][1] != "create" {
		t.Fatalf("inspect then create: %+v", seen)
	}
}

func TestEnsureWorkloadNetworkAlreadyExists(t *testing.T) {
	var seen [][]string
	err := ensureWorkloadNetwork(context.Background(), func(_ context.Context, args ...string) ([]byte, error) {
		seen = append(seen, append([]string{}, args...))
		return []byte(`[{"Name":"hypermesh-workload"}]`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0][1] != "inspect" {
		t.Fatalf("inspect only: %+v", seen)
	}
}

func TestJailForbiddenHostNetwork(t *testing.T) {
	if err := jailForbidden([]string{"run", "--network", "host"}); err == nil {
		t.Fatal("expected host network error")
	}
	if err := jailForbidden([]string{"run", "-v", "/var/run/docker.sock:/var/run/docker.sock"}); err == nil {
		t.Fatal("expected docker.sock error")
	}
}
