// Package runtime — workload jail and host-side egress observation.
//
// ObserveWorkloadEgress records a boolean the plane can trust for admission
// sandbox. It does not scan the network or weaken the jail.
// If we cannot observe honestly, ok is false with reason egress_unobserved.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Egress reason codes posted on lab soak job results.
const (
	EgressOverlayOnly   = "overlay_publish_only"
	EgressUnobserved    = "egress_unobserved"
	EgressNotInternal   = "network_not_internal"
	EgressUnexpected    = "egress_unexpected"
	EgressInspectFailed = "egress_inspect_failed"
)

// DockerRunner runs docker CLI args (tests inject a stub).
type DockerRunner func(ctx context.Context, args ...string) ([]byte, error)

// ConnSampler returns host-visible connection lines for the workload
// (ss / conntrack). Tests inject a stub. Nil fails closed.
type ConnSampler func(ctx context.Context) (lines []string, ok bool)

// networkInspectJSON is the subset of `docker network inspect` we need.
type networkInspectJSON struct {
	Internal  bool              `json:"Internal"`
	Options   map[string]string `json:"Options"`
	Containers map[string]struct {
		IPv4Address string `json:"IPv4Address"`
		Name        string `json:"Name"`
	} `json:"Containers"`
}

// ObserveWorkloadEgress checks the jail bridge is internal and samples
// host-visible connections. Fail closed when inspect or sampling is missing.
//
// Honest claim when ok: no connection except the published overlay path was
// observed from the host namespace. This is not a proof that weights are safe.
func ObserveWorkloadEgress(ctx context.Context, run DockerRunner, sample ConnSampler) (ok bool, reason string) {
	if run == nil {
		return false, EgressUnobserved
	}
	out, err := run(ctx, workloadNetworkInspectArgs()...)
	if err != nil {
		return false, EgressInspectFailed
	}
	var nets []networkInspectJSON
	if err := json.Unmarshal(out, &nets); err != nil || len(nets) == 0 {
		return false, EgressInspectFailed
	}
	net := nets[0]
	if !net.Internal {
		return false, EgressNotInternal
	}
	// ICC / masquerade off are part of the jail create args; treat missing
	// options as fail-closed only when we can read them as enabled.
	if v := strings.ToLower(strings.TrimSpace(net.Options["com.docker.network.bridge.enable_icc"])); v == "true" {
		return false, EgressNotInternal
	}
	if v := strings.ToLower(strings.TrimSpace(net.Options["com.docker.network.bridge.enable_ip_masquerade"])); v == "true" {
		return false, EgressNotInternal
	}

	if sample == nil {
		sample = defaultConnSample
	}
	lines, sampled := sample(ctx)
	if !sampled {
		return false, EgressUnobserved
	}
	ips := containerIPs(net)
	if unexpectedWorkloadConn(lines, ips) {
		return false, EgressUnexpected
	}
	return true, EgressOverlayOnly
}

func containerIPs(net networkInspectJSON) []string {
	var ips []string
	for _, c := range net.Containers {
		ip := strings.TrimSpace(c.IPv4Address)
		if slash := strings.IndexByte(ip, '/'); slash >= 0 {
			ip = ip[:slash]
		}
		if ip != "" {
			ips = append(ips, ip)
		}
	}
	return ips
}

// unexpectedWorkloadConn looks for lines that mention a container IP and a
// non-RFC1918 peer. Keep this conservative: unclear lines without a public
// peer are treated as jail/overlay chatter.
func unexpectedWorkloadConn(lines, containerIPs []string) bool {
	if len(containerIPs) == 0 {
		return false
	}
	for _, line := range lines {
		lower := strings.ToLower(line)
		hit := false
		for _, ip := range containerIPs {
			if strings.Contains(lower, strings.ToLower(ip)) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		if hasPublicPeer(lower) {
			return true
		}
	}
	return false
}

func hasPublicPeer(line string) bool {
	// Tokenize on whitespace and strip ports (addr:port / [addr]:port).
	for _, tok := range strings.Fields(line) {
		host := tok
		if i := strings.LastIndex(host, ":"); i > 0 {
			// Keep IPv6 bracket form; for IPv4 strip :port.
			if strings.Count(host, ":") == 1 || strings.HasPrefix(host, "[") {
				host = host[:i]
			}
		}
		host = strings.Trim(host, "[]")
		if !looksLikeIP(host) {
			continue
		}
		if strings.HasPrefix(host, "127.") || host == "::1" || host == "0.0.0.0" {
			continue
		}
		if strings.HasPrefix(host, "10.") ||
			strings.HasPrefix(host, "192.168.") ||
			strings.HasPrefix(host, "172.") {
			continue
		}
		return true
	}
	return false
}

func looksLikeIP(host string) bool {
	if host == "" || host == "*" {
		return false
	}
	if strings.Contains(host, ".") {
		parts := strings.Split(host, ".")
		if len(parts) != 4 {
			return false
		}
		for _, p := range parts {
			if p == "" {
				return false
			}
			for _, c := range p {
				if c < '0' || c > '9' {
					return false
				}
			}
		}
		return true
	}
	return strings.Contains(host, ":")
}

func defaultConnSample(ctx context.Context) ([]string, bool) {
	// Prefer ss; fall back to conntrack. Missing both → unobserved.
	if lines, ok := runLines(ctx, "ss", "-Htan"); ok {
		return lines, true
	}
	if lines, ok := runLines(ctx, "conntrack", "-L"); ok {
		return lines, true
	}
	// Last resort: host nf_conntrack table if readable.
	raw, err := os.ReadFile("/proc/net/nf_conntrack")
	if err == nil {
		return splitNonEmpty(string(raw)), true
	}
	return nil, false
}

func runLines(ctx context.Context, name string, args ...string) ([]string, bool) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, false
	}
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return splitNonEmpty(string(out)), true
}

func splitNonEmpty(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// FormatEgressLog is a one-line host log helper.
func FormatEgressLog(ok bool, reason string) string {
	return fmt.Sprintf("egress_ok=%v reason=%s", ok, reason)
}
