package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type PackageChange struct {
	Name string `json:"name"`
	From string `json:"from"`
	To string `json:"to"`
}

var setupPackageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+(:[a-z0-9][a-z0-9-]*)?$`)
var setupVersionRE = regexp.MustCompile(`^[0-9][A-Za-z0-9.+:~_-]*$`)
var setupInstRE = regexp.MustCompile(`^Inst ([^ ]+)(?: \[([^\]]+)\])? \(([^ ]+) .+\)(?: \[.*\])?$`)
var setupConfRE = regexp.MustCompile(`^Conf ([^ ]+) \(([^ ]+) .+\)$`)

func packageBase(name string) string {
	base, _, _ := strings.Cut(name, ":")
	return base
}
func hasPackage(installed map[string]string, name string) bool {
	for key := range installed { if packageBase(key) == name { return true } }
	return false
}
func hasNativePackage(installed map[string]string, name, arch string) bool {
	_, bare := installed[name]
	_, native := installed[name+":"+arch]
	return bare || native
}
func protectedSetupPackage(name string) bool {
	name = packageBase(name)
	for _, prefix := range []string{"radxa", "rockchip", "rknpu", "libmali", "mali-", "cix-", "axcl", "libaxcl", "axera", "dxrt", "dx-", "sophon", "libsophon", "sophgo", "libedgetpu", "gasket", "apex", "renesas", "rzv", "u-boot", "edk2", "firmware-", "linux-", "nvidia-", "libnvidia-", "cuda-", "libcuda", "rocm", "hip-", "libhip", "hsa-", "libhsa", "amdgpu", "openssh", "systemd", "network-manager", "netplan", "grub", "shim", "zfs", "mdadm", "lvm", "samba", "nfs-"} {
		if strings.HasPrefix(name, prefix) { return true }
	}
	return false
}

func parseInstalledPackages(text string) (map[string]string, error) {
	installed := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if line == "" { continue }
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || !setupPackageRE.MatchString(fields[0]) {
			return nil, fmt.Errorf("invalid package inventory; repair dpkg state and rerun")
		}
		status := fields[2]
		if status == "rc " || status == "un " || status == "pn " { continue }
		// An already-installed package is left unchanged.
		if (status != "ii " && status != "hi ") || !setupVersionRE.MatchString(fields[1]) {
			return nil, fmt.Errorf("package database is not fully configured; repair dpkg state before planning")
		}
		if _, exists := installed[fields[0]]; exists { return nil, fmt.Errorf("duplicate package inventory entry") }
		installed[fields[0]] = fields[1]
	}
	if len(installed) == 0 { return nil, fmt.Errorf("installed package inventory is empty") }
	return installed, nil
}

func candidateVersion(text string) (string, error) {
	value := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Candidate:") {
			if value != "" { return "", fmt.Errorf("ambiguous APT candidate") }
			value = strings.TrimSpace(strings.TrimPrefix(line, "Candidate:"))
		}
	}
	if !setupVersionRE.MatchString(value) { return "", fmt.Errorf("no valid package candidate; refresh trusted APT metadata and rerun planning") }
	return value, nil
}

func parseSetupSimulation(text string, installed map[string]string) ([]PackageChange, error) {
	changes := map[string]PackageChange{}
	configured := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "Remv "), strings.HasPrefix(line, "Purg "):
			return nil, fmt.Errorf("APT proposes package removal; setup policy refuses the transaction")
		case strings.HasPrefix(line, "Inst "):
			m := setupInstRE.FindStringSubmatch(line)
			if m == nil || !setupPackageRE.MatchString(m[1]) || !setupVersionRE.MatchString(m[3]) {
				return nil, fmt.Errorf("unrecognized APT install operation")
			}
			if m[2] != "" || hasPackage(installed, packageBase(m[1])) {
				return nil, fmt.Errorf("APT proposes changing an installed package; update it separately and regenerate the plan")
			}
			if protectedSetupPackage(m[1]) {
				return nil, fmt.Errorf("APT proposes a protected kernel, driver, boot, network or storage package; use a validated vendor transition")
			}
			if _, exists := changes[m[1]]; exists { return nil, fmt.Errorf("duplicate APT operation") }
			changes[m[1]] = PackageChange{m[1], "", m[3]}
		case strings.HasPrefix(line, "Conf "):
			m := setupConfRE.FindStringSubmatch(line)
			if m == nil || !setupPackageRE.MatchString(m[1]) || !setupVersionRE.MatchString(m[2]) {
				return nil, fmt.Errorf("unrecognized APT configure operation")
			}
			if _, exists := configured[m[1]]; exists { return nil, fmt.Errorf("duplicate APT configure operation") }
			configured[m[1]] = m[2]
		case strings.HasPrefix(line, "E:"):
			return nil, fmt.Errorf("APT simulation reported an error")
		}
	}
	for name, version := range configured {
		change, ok := changes[name]
		if !ok || change.To != version { return nil, fmt.Errorf("APT configure operation is outside the planned installs") }
	}
	result := []PackageChange{}
	for _, change := range changes {
		if configured[change.Name] != change.To { return nil, fmt.Errorf("APT install lacks a matching configure operation") }
		result = append(result, change)
	}
	sort.Slice(result, func(i,j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func resolveSetupPackages(ctx context.Context, source setupSource, installed map[string]string, required []string, arch string) ([]PackageChange, error) {
	requested := map[string]string{}
	for _, name := range required {
		if !setupPackageRE.MatchString(name) { return nil, fmt.Errorf("invalid profile package") }
		if hasNativePackage(installed, name, arch) { continue }
		raw, err := source.output(ctx, "apt-cache", "policy", name)
		if err != nil { return nil, fmt.Errorf("cannot resolve required package %s; check trusted APT metadata", name) }
		version, err := candidateVersion(string(raw))
		if err != nil { return nil, fmt.Errorf("%s: %w", name, err) }
		requested[name] = version
	}
	if len(requested) == 0 { return []PackageChange{}, nil }
	simulate := func(packages map[string]string) ([]PackageChange, error) {
		args := []string{"--simulate", "--no-install-recommends", "--no-remove", "install"}
		pins := []string{}
		for name, version := range packages { pins = append(pins, name+"="+version) }
		sort.Strings(pins)
		raw, err := source.output(ctx, "apt-get", append(args, pins...)...)
		if err != nil { return nil, fmt.Errorf("APT simulation failed; review dependencies and trusted package sources") }
		return parseSetupSimulation(string(raw), installed)
	}
	changes, err := simulate(requested)
	if err != nil { return nil, err }
	for name, version := range requested {
		found := false
		for _, change := range changes { if packageBase(change.Name) == name && change.To == version { found = true } }
		if !found { return nil, fmt.Errorf("APT did not resolve a required pinned package") }
	}
	// Resolve again with the whole dependency closure pinned, ensuring the
	// displayed execution arguments reproduce the reviewed transaction.
	closure := map[string]string{}
	for _, change := range changes { closure[change.Name] = change.To }
	confirmed, err := simulate(closure)
	if err != nil { return nil, err }
	before, _ := json.Marshal(changes)
	after, _ := json.Marshal(confirmed)
	if string(before) != string(after) { return nil, fmt.Errorf("APT dependency plan changed while pinning; regenerate the plan") }
	return changes, nil
}
