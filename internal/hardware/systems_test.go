package hardware

import "testing"

func TestSystemMatrixCoverage(t *testing.T) {
	matrix := Systems()
	if matrix.Schema != 1 || matrix.MinimumMemoryGiB != 16 || matrix.MaximumMemoryGiB != nil || len(matrix.Systems) != 8 || len(matrix.Accelerators) != 5 {
		t.Fatalf("bad matrix %+v", matrix)
	}
	seen := map[string]bool{}
	for _, target := range matrix.Systems {
		if seen[target.ID] || target.RuntimeStatus == "" || target.Acceptance == "" {
			t.Fatalf("invalid target %+v", target)
		}
		seen[target.ID] = true
		if target.OS == "darwin" && target.Arch != "arm64" {
			t.Fatal("Intel Mac enabled")
		}
	}
	for _, addon := range matrix.Accelerators {
		if addon.GGUFSupported || addon.Qualification == "" {
			t.Fatal("unqualified adapter advertised", addon)
		}
	}
}

func TestSystemIdentityDoesNotCapMemoryOrGuessGPU(t *testing.T) {
	cases := []struct {
		facts Facts
		id    string
	}{
		{Facts{Arch: "arm64", Model: "Radxa ROCK 5B+"}, "radxa-rock-5"},
		{Facts{Arch: "arm64", Model: "Radxa Orion O6N"}, "radxa-orion-o6n"},
		{Facts{Arch: "arm64", Model: "Renesas RZ/V2H EVK"}, "renesas-rz-v2h"},
		{Facts{Arch: "arm64", Model: "Seeed J4012 Orin NX", Tegra: true}, "seeed-j4012"},
		{Facts{Arch: "amd64", Processor: "AMD Ryzen AI Max+ 395 w/ Radeon 8060S Graphics"}, "amd-ryzen-ai-max-395"},
		{Facts{Arch: "amd64", Processor: "AMD Ryzen AI Max+ PRO 495 w/ Radeon 8065S Graphics"}, "amd-ryzen-ai-max-495"},
		{Facts{Arch: "arm64", Model: "Mac16,10", Metal: true}, "apple-silicon"},
	}
	for _, tc := range cases {
		for _, gib := range []int64{16, 32, 64, 128, 192, 512, 1024} {
			f := tc.facts
			f.MemoryBytes = gib << 30
			p := Recommend(f)
			if p.SystemID != tc.id {
				t.Fatalf("%+v matched %s want %s", f, p.SystemID, tc.id)
			}
			if !f.Metal && !f.CUDA && !f.HIP && p.Backend != "cpu" {
				t.Fatal("marketing name enabled GPU", p)
			}
		}
	}
	for _, f := range []Facts{{Arch: "amd64", Model: "Radxa ROCK 5B"}, {Arch: "amd64", Processor: "AMD Ryzen AI 395"}, {Arch: "arm64", Model: "unknown"}} {
		if SystemID(f) != "" {
			t.Fatal("incorrect system match", f)
		}
	}
}

func TestARMBoardVulkanPresenceDoesNotChangeCPUReleaseBaseline(t *testing.T) {
	for _, model := range []string{"Radxa ROCK 5B", "Radxa Orion O6N", "Renesas RZ/V2H EVK"} {
		p := Recommend(Facts{Arch: "arm64", Model: model, Vulkan: true, AvailableBytes: 28 << 30, MemoryBytes: 32 << 30})
		if p.Backend != "cpu" || p.BenchmarkArgs[5] != "0" || p.ModelBudgetBytes == 0 {
			t.Fatal("unimplemented Vulkan chosen over explicit CPU baseline", p)
		}
	}
}
