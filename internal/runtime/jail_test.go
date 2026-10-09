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
	if flagValue(args, "--log-driver") != "none" || flagValue(args, "--ulimit") != "core=0" {
		t.Fatalf("workload output/core dumps must not persist: %v", args)
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
	args := append([]string{"run", "-d"}, JailArgs()...)
	assertWorkloadJail(t, args)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--network=host") || strings.Contains(joined, "--network host") {
		t.Fatalf("must not use host network: %v", args)
	}
	// Overlay-only: no host docker.sock, no host net — default route off
	// overlay is denied by --internal + no masquerade on create.
	if strings.Contains(joined, "docker.sock") {
		t.Fatalf("must not mount docker.sock: %v", args)
	}
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
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "enable_icc=false") {
				t.Fatalf("want ICC off: %v", args)
			}
			if !strings.Contains(joined, "enable_ip_masquerade=false") {
				t.Fatalf("want no masquerade / no default route off overlay: %v", args)
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

func TestJailRejectsLoggingAndCoreOverrides(t *testing.T) {
	for _, extra := range [][]string{{"--log-driver", "json-file"}, {"--log-driver=local"}, {"--ulimit", "core=1"}, {"--ulimit=core=0:1"}} {
		if err := jailForbidden(append(JailArgs(), extra...)); err == nil {
			t.Fatalf("accepted override %v", extra)
		}
	}
	if jailForbidden([]string{"run"}) == nil {
		t.Fatal("accepted daemon defaults")
	}
}

func TestInspectWorkloadPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		pass       bool
	}{
		{"safe", `{"LogConfig":{"Type":"none"},"Ulimits":[{"Name":"core","Soft":0,"Hard":0}]}`, true},
		{"logs", `{"LogConfig":{"Type":"json-file"},"Ulimits":[{"Name":"core","Soft":0,"Hard":0}]}`, false},
		{"default core", `{"LogConfig":{"Type":"none"}}`, false},
		{"hard core", `{"LogConfig":{"Type":"none"},"Ulimits":[{"Name":"core","Soft":0,"Hard":1}]}`, false},
		{"invalid", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := InspectWorkloadPrivacy(context.Background(), func(_ context.Context, args ...string) ([]byte, error) {
				if strings.Join(args, " ") != "inspect --format {{json .HostConfig}} fixture" {
					t.Fatalf("wrong inspect: %v", args)
				}
				return []byte(tc.body), nil
			}, "fixture")
			if (err == nil) != tc.pass {
				t.Fatalf("pass=%v err=%v", tc.pass, err)
			}
		})
	}
}
