package hardware

import (
	"embed"
	"encoding/json"
	"strings"
)

// The same matrix ships inside the CLI and in the signed release payload.
// Matching a system does not grant workload, enrollment or sale authority.
//
//go:embed systems.json
var systemsFS embed.FS

type SystemTarget struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	OS                  string   `json:"os"`
	Arch                string   `json:"arch"`
	Backend             string   `json:"backend"`
	AcceleratorsPending []string `json:"accelerators_pending"`
	Acceptance          string   `json:"acceptance"`
	RuntimeStatus       string   `json:"runtime_status"`
	OSPolicy            string   `json:"os_policy,omitempty"`
}
type AcceleratorTarget struct {
	Connector     string `json:"connector,omitempty"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	HostOS        string `json:"host_os"`
	Runtime       string `json:"runtime"`
	ModelFormat   string `json:"model_format"`
	Qualification string `json:"qualification"`
	GGUFSupported bool   `json:"gguf_supported"`
}
type SystemMatrix struct {
	Schema           int                 `json:"schema"`
	MinimumMemoryGiB int64               `json:"minimum_memory_gib"`
	MaximumMemoryGiB *int64              `json:"maximum_memory_gib"`
	Qualification    string              `json:"qualification"`
	Systems          []SystemTarget      `json:"systems"`
	Accelerators     []AcceleratorTarget `json:"accelerators"`
}

func Systems() SystemMatrix {
	raw, _ := systemsFS.ReadFile("systems.json")
	var matrix SystemMatrix
	if err := json.Unmarshal(raw, &matrix); err != nil {
		panic(err)
	}
	return matrix
}

// SystemID identifies a release family from observed architecture and names.
// Unknown hardware stays unknown; RAM never guesses a processor or SKU.
func SystemID(f Facts) string {
	arch := f.Arch
	if arch == "aarch64" {
		arch = "arm64"
	}
	model := strings.ToLower(f.Model)
	cpu := strings.Join(strings.Fields(strings.ToLower(f.Processor)), " ")
	if arch == "arm64" {
		switch {
		case f.Metal:
			return "apple-silicon"
		case f.Tegra && strings.Contains(model, "agx"):
			return "jetson-agx-orin"
		case f.Tegra && (strings.Contains(model, "orin nx") || strings.Contains(model, "j4012")):
			return "seeed-j4012"
		case strings.Contains(model, "radxa") && strings.Contains(model, "rock 5"):
			return "radxa-rock-5"
		case strings.Contains(model, "orion") && strings.Contains(model, "o6n"):
			return "radxa-orion-o6n"
		case strings.Contains(model, "renesas") && (strings.Contains(model, "v2h") || strings.Contains(model, "r9a09g057")):
			return "renesas-rz-v2h"
		}
	}
	if arch == "amd64" && strings.Contains(cpu, "amd ryzen ai max+") {
		switch {
		case hasCPUWord(cpu, "395"):
			return "amd-ryzen-ai-max-395"
		case hasCPUWord(cpu, "495"):
			return "amd-ryzen-ai-max-495"
		}
	}
	return ""
}

func hasCPUWord(cpu, word string) bool {
	for _, field := range strings.Fields(cpu) {
		if field == word {
			return true
		}
	}
	return false
}
