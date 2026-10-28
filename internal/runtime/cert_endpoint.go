package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// CertificationEndpoint resolves only the exact temporary container on the
// isolated workload bridge. Internal networks may ignore Docker port publishing;
// the host can still reach their bridge address without adding workload egress.
func (n *NVIDIA) CertificationEndpoint(ctx context.Context, h *Handle) (string, error) {
 return certificationEndpoint(ctx,n.run,h)
}
func (h *HIP) CertificationEndpoint(ctx context.Context, handle *Handle) (string, error) {
 return certificationEndpoint(ctx,h.run,handle)
}
func certificationEndpoint(ctx context.Context, run DockerRunner, h *Handle) (string,error) {
	if h == nil || strings.TrimSpace(h.ContainerID) == "" {
		return "", fmt.Errorf("missing certification container identity")
	}
	out, err := run(ctx, "inspect", "--format", "{{json .}}", h.ContainerID)
	if err != nil {
		return "", fmt.Errorf("cannot inspect certification container")
	}
	var state struct {
		State           struct{ Running bool }
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string }
		}
	}
	if json.Unmarshal(out, &state) != nil || !state.State.Running {
		return "", fmt.Errorf("certification container is not running")
	}
	networks := state.NetworkSettings.Networks
	endpoint, ok := networks[WorkloadNetworkName]
	if !ok || len(networks) != 1 {
		return "", fmt.Errorf("certification container must use only the isolated workload network")
	}
	addr, err := netip.ParseAddr(endpoint.IPAddress)
	if err != nil || !addr.Is4() || !addr.IsPrivate() {
		return "", fmt.Errorf("certification container has no private bridge address")
	}
	return "http://" + net.JoinHostPort(addr.String(), "8080") + "/v1/chat/completions", nil
}
