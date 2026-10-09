package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
)

// WorkloadState is the container status the agent needs while it waits
// for llama-server. IP is the IPv4 address on the internal workload
// bridge. It is empty when the container is not running.
type WorkloadState struct {
	Running  bool
	Status   string
	ExitCode int
	IP       string
}

// Exited reports that the container has stopped. A missing container is
// reported as exited by InspectWorkloadState.
func (s WorkloadState) Exited() bool {
	if s.Running {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(s.Status)) {
	case "exited", "dead", "removing":
		return true
	default:
		return false
	}
}

// InspectWorkloadState reads one container. The address is accepted only
// when the container is running on the internal workload network and that
// address is a private IPv4. Host port publish is not consulted: an
// internal network does not create a listener on the host.
func InspectWorkloadState(ctx context.Context, run DockerRunner, container string) (WorkloadState, error) {
	if run == nil || strings.TrimSpace(container) == "" {
		return WorkloadState{}, fmt.Errorf("workload container unavailable")
	}
	out, err := run(ctx, "inspect", "--format", "{{json .}}", container)
	if err != nil {
		if dockerAbsent(out, err) {
			return WorkloadState{Status: "exited", ExitCode: -1}, nil
		}
		return WorkloadState{}, err
	}
	var body struct {
		State struct {
			Running  bool
			Status   string
			ExitCode int
		}
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string }
		}
	}
	if json.Unmarshal(out, &body) != nil {
		return WorkloadState{}, fmt.Errorf("workload inspect invalid")
	}
	state := WorkloadState{
		Running:  body.State.Running,
		Status:   body.State.Status,
		ExitCode: body.State.ExitCode,
	}
	networks := body.NetworkSettings.Networks
	endpoint, ok := networks[WorkloadNetworkName]
	if !state.Running {
		return state, nil
	}
	if !ok || len(networks) != 1 {
		return state, fmt.Errorf("workload container must use only %s", WorkloadNetworkName)
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(endpoint.IPAddress))
	if err != nil || !addr.Is4() || !addr.IsPrivate() || addr.IsLoopback() {
		return state, fmt.Errorf("workload container has no private bridge address")
	}
	state.IP = addr.String()
	return state, nil
}
