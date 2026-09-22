package runtime

import (
	"context"
	"fmt"
	"strings"
)

// WorkloadNetworkName is the dedicated Docker bridge for lease containers.
// It is created --internal so the workload has no default route to the host
// LAN or public internet. Ingress stays host-side docker-proxy on the
// WireGuard client address only (-p <wg-ip>:8080:8080).
const WorkloadNetworkName = "hypermesh-workload"

// JailArgs are docker run flags for every hosted artifact container
// (lab or renter). Agent and witness stay on host network and reach the
// workload via the overlay publish address, same as the gateway door.
//
// Guarantees:
//   - not --network=host (no LAN/host namespace)
//   - no Docker socket mount
//   - all Linux capabilities dropped
//   - read-only rootfs (+ /tmp tmpfs)
//   - no host devices for sensors/cameras/robots (GPU via nvidia runtime
//     or explicit HIP kfd/dri only)
func JailArgs() []string {
	return []string{
		"--network", WorkloadNetworkName,
		"--read-only",
		"--tmpfs", "/tmp",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
	}
}

func workloadNetworkInspectArgs() []string {
	return []string{"network", "inspect", WorkloadNetworkName}
}

func workloadNetworkCreateArgs() []string {
	return []string{
		"network", "create",
		"--driver", "bridge",
		"--internal",
		WorkloadNetworkName,
	}
}

// ensureWorkloadNetwork creates the internal bridge if missing.
// A concurrent create that already succeeded is treated as success.
func ensureWorkloadNetwork(ctx context.Context, run func(ctx context.Context, args ...string) ([]byte, error)) error {
	if _, err := run(ctx, workloadNetworkInspectArgs()...); err == nil {
		return nil
	}
	out, err := run(ctx, workloadNetworkCreateArgs()...)
	if err == nil {
		return nil
	}
	if _, err2 := run(ctx, workloadNetworkInspectArgs()...); err2 == nil {
		return nil
	}
	return wrapDocker("network create", out, err)
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
	}
	return ""
}

// jailForbidden reports a docker run argv that breaks the workload jail.
func jailForbidden(args []string) error {
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "docker.sock") {
		return fmt.Errorf("hypermesh: workload jail forbids docker.sock mount")
	}
	net := flagValue(args, "--network")
	if net == "host" {
		return fmt.Errorf("hypermesh: workload jail forbids host network")
	}
	if net != "" && net != WorkloadNetworkName {
		return fmt.Errorf("hypermesh: workload jail requires network %s, got %s", WorkloadNetworkName, net)
	}
	return nil
}
