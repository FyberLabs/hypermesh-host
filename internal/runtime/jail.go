package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// WorkloadNetworkName is the dedicated Docker bridge for lease containers.
// It is created --internal so the workload has no default route to the host
// LAN or public internet. Docker does not publish host ports for a container
// whose only network is internal: there is no docker-proxy and no NAT rule
// for -p <wg-ip>:8080:8080. The agent listens on the WireGuard address and
// forwards TCP to the container address on this bridge. Do not attach a
// second network.
const WorkloadNetworkName = "hypermesh-workload"

// JailArgs are docker run flags for every hosted artifact container
// (lab or renter). The agent stays on the host network. It reaches the
// container at its bridge address, and the gateway reaches the container
// through the agent's listener on the WireGuard address.
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
		"--log-driver", "none",
		"--ulimit", "core=0",
		"--tmpfs", "/tmp",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
	}
}

func workloadNetworkInspectArgs() []string {
	return []string{"network", "inspect", WorkloadNetworkName}
}

func workloadNetworkCreateArgs() []string {
	// --internal: no default route off the overlay to the host LAN / public
	// internet. ICC and masquerade stay off so containers cannot hairpin to
	// each other or SNAT onto a host path. -p does not open a host listener
	// on an internal-only network. The agent forwards from the WireGuard
	// address to the container address on this bridge.
	return []string{
		"network", "create",
		"--driver", "bridge",
		"--internal",
		"--opt", "com.docker.network.bridge.enable_icc=false",
		"--opt", "com.docker.network.bridge.enable_ip_masquerade=false",
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
	// Check every occurrence so a later option cannot override the jail.
	logSeen, coreSeen := false, false
	for i, arg := range args {
		value := ""
		flag := strings.SplitN(arg, "=", 2)[0]
		if flag != "--log-driver" && flag != "--ulimit" {
			continue
		}
		if strings.Contains(arg, "=") {
			value = strings.SplitN(arg, "=", 2)[1]
		} else if i+1 < len(args) {
			value = args[i+1]
		}
		if flag == "--log-driver" {
			logSeen = true
			if value != "none" {
				return fmt.Errorf("hypermesh: workload jail requires log driver none")
			}
		}
		if flag == "--ulimit" && strings.HasPrefix(value, "core=") {
			coreSeen = true
			if value != "core=0" && value != "core=0:0" {
				return fmt.Errorf("hypermesh: workload jail requires core=0")
			}
		}
	}
	if !logSeen || !coreSeen {
		return fmt.Errorf("hypermesh: workload jail requires explicit logging and core dump limits")
	}
	return nil
}

// WorkloadPrivacy is the effective Docker configuration, not the requested argv.
type WorkloadPrivacy struct {
	LogDriver string
	CoreSoft  int64
	CoreHard  int64
}

// InspectWorkloadPrivacy fails closed before an instance probe sends any text.
func InspectWorkloadPrivacy(ctx context.Context, run DockerRunner, container string) (WorkloadPrivacy, error) {
	observation := WorkloadPrivacy{CoreSoft: -1, CoreHard: -1}
	if run == nil || strings.TrimSpace(container) == "" {
		return observation, fmt.Errorf("workload container unavailable")
	}
	out, err := run(ctx, "inspect", "--format", "{{json .HostConfig}}", container)
	if err != nil {
		return observation, fmt.Errorf("workload privacy inspection failed")
	}
	var config struct {
		LogConfig struct{ Type string }
		Ulimits   []struct {
			Name string
			Soft *int64
			Hard *int64
		}
	}
	if err := json.Unmarshal(out, &config); err != nil {
		return observation, fmt.Errorf("workload privacy inspection invalid")
	}
	observation.LogDriver = config.LogConfig.Type
	coreCount := 0
	for _, limit := range config.Ulimits {
		if limit.Name == "core" {
			coreCount++
			if limit.Soft != nil && limit.Hard != nil {
				observation.CoreSoft, observation.CoreHard = *limit.Soft, *limit.Hard
			}
		}
	}
	if coreCount != 1 || observation.LogDriver != "none" || observation.CoreSoft != 0 || observation.CoreHard != 0 {
		return observation, fmt.Errorf("workload privacy configuration unsafe")
	}
	return observation, nil
}
