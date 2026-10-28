# Host platform release candidates

Requested coverage: Linux ARM64 for ROCK 5, Orion O6N, RZ/V2H and Jetson J4012;
Linux AMD64 for Ryzen AI Max+ 395/PRO 395 and PRO 495; Darwin ARM64 for Apple
silicon Mac mini and Studio. Target every RAM variant from 16 GiB through the
hardware's supported maximum. There is no software 64/128/512 GiB ceiling.
Installed capacity, currently available memory, model/context fit and reserved
OS/runtime memory remain separate checks. The existing measured 16 GiB minimum
is retained; a nominal 16 GB board whose usable capacity is below that gate
requires a reviewed installed-memory policy rather than an invented measurement.

`hypermesh-host platforms` prints the same matrix shipped as signed
`platforms.json`. `inspect` reports an additive `system_id` when observed names
and architecture match. Neither command authorizes enrollment or selling.

| Target | Artifact | Runtime / installation state |
|---|---|---|
| ROCK 5 family, Orion O6N | Linux ARM64 | Explicit `llama-cpp-cpu` isolated OCI runtime; candidate Debian 12/13 and Ubuntu 22.04/24.04 CPU setup policies preserve vendor packages |
| Renesas RZ/V2H | Linux ARM64 | Same CPU runtime; vendor Yocto installation policy still required; never route Yocto through APT |
| Seeed J4012 / Orin NX | Linux ARM64 | Existing `cuda-jetson` runtime; retain the module's exact JetPack/L4T image |
| Ryzen AI Max+ 395/PRO 395, PRO 495 | Linux AMD64 | `rocm,cpu` build tags; HIP requires compatible ROCm, KFD/DRI and pinned workload image; no HSA override |
| Apple silicon Mac mini / Studio | Darwin ARM64 | Signed binaries and inspection; native Metal process/service lifecycle, installation and qualification remain pending |

## Release payload and verification

The publisher builds the agent and operator CLI for all three OS/architecture
pairs, signs all six binaries plus the platform matrix, and includes them in
storage and GitHub release assets. Transport names remain
`hypermesh-agent_<version>_<os>_<arch>` and
`hypermesh-deploy_<version>_<os>_<arch>`. The installed public command remains
`hypermesh-host`. The Linux ARM64 build enables `cpu`; Linux AMD64 enables
`rocm,cpu`; Darwin uses its OS-specific code. Drivers, model files and runtime
libraries are not bundled or downloaded from unreviewed locations.

Bootstrap detects Linux or Apple silicon macOS and verifies the corresponding
artifact under the existing signature, channel, immutable release identity and
build-floor rules. Explicit `--os` supports cross-downloads; Darwin AMD64 is
rejected. No fallback to a Linux executable occurs on Mac. macOS output points
to inspection and does not promise that the Linux guided installer will work.
The Go `download` command supports the same explicit OS selection. Automatic
agent updating is disabled on non-Linux candidates so the existing Linux-only
update/apply path cannot replace a Darwin binary with a Linux payload.

Release version/build allocation and production promotion remain governed by
[release versioning](release-versioning.md). These changes prepare a new test
release after merge; branch builds do not publish a release. A successful build
is not physical host acceptance. The platform matrix does not open placement,
change prices, turn on selling, or grant memory-protection qualifications.

## CPU and HIP workload contract

CPU is an explicit requested backend, not a fallback for a GPU/NPU request.
Its container retains the internal network, read-only model mount/rootfs,
no logging, zero core limit, dropped capabilities and no-new-privileges.
It forces zero GPU layers. Linux CPU containers receive the same effective
Docker privacy inspection as CUDA/HIP; the native Metal exemption is Darwin-only.

HIP now pulls the exact OCI digest and binds the verified model file and catalog
alias rather than starting the old unbound scaffold. CPU/HIP pinned certification
uses the same temporary-container model hash checks, actual completion probes,
measured results and cleanup gate. Vendor images must contain the appropriate
llama-server build. No timing or memory measurement is synthesized.

## Accelerator add-ons

The initial add-on scope is Coral Edge TPU (USB, PCIe and M.2, including dual TPU)
and all four Radxa AICore modules: AX-M1, DX-M1, DX-M1M and SG2300X.
Each has a separate matrix entry and SDK/model/device acceptance. M.2 describes
a connector, not a common inference API. SG2300X uses a board-to-board carrier
and SoC/PCIe endpoint modes; it is not a drop-in M.2 card.

| Add-on | Model contract | Remaining implementation and acceptance |
|---|---|---|
| Coral Edge TPU | Edge-TPU-compiled TensorFlow Lite; no direct GGUF contract | Detect each USB/PCIe device and driver, pin libedgetpu/TFLite, isolate inference adapter, bind compiled model hashes, validate unsupported-op/CPU partition behavior, benchmark exact model/device; dual modules require both PCIe lanes to use both TPUs |
| Radxa AX-M1 | AXera compiled model plus matching runtime/tokenizer assets | Pin AXCL/firmware/compiler versions, enumerate each accelerator, validate model partitions and device memory, isolate adapter, verify model/tokenizer assets, measure real inference and lifecycle cleanup |

| Radxa DX-M1 | DX-COM compiled DXNN with DX-RT and matching firmware/driver | Verify PCIe/M-key carrier, exact driver/firmware/runtime/compiler tuple, per-device model fit and unsupported operations, isolated adapter, cleanup and sustained inference |
| Radxa DX-M1M | Separate DXNN target using DX-RT; do not assume DX-M1 memory/firmware interchangeability | Verify B+M-key host wiring and its own device capacity, compiled model target, firmware and physical acceptance |
| Radxa SG2300X | SOPHON compiled model with matching vendor runtime/assets | Verify carrier and SoC versus PCIe endpoint topology, SDK/firmware, per-device memory, model assets, isolated adapter and physical acceptance |

These adapters are not enabled by this candidate. They need dedicated workload
contracts; no existing GGUF backend is relabeled as TPU/NPU. The host's minimum
RAM policy does not imply an add-on has 16 GiB of accelerator memory.

## Acceptance before sales

For each exact board/chip/RAM/OS/driver/runtime combination record:

1. Verified release, boot identity, service restart and update/rollback behavior.
2. Actual model and runtime allocation limits, OS headroom, full-context fit.
3. Enrollment, tunnel/heartbeat, pinned image/model execution and cleanup.
4. Sustained inference, throttling/thermal behavior, concurrency and storage use.
5. Privacy controls and any separately requested memory-protection tier evidence.
6. Reviewer approval bound to that evidence before catalog placement or selling.

Pending slices: Renesas vendor-Yocto deployment; macOS launchd, pinned native
Metal runtime, private process lifecycle and uninstall/update; Coral adapter;
AX-M1, DX-M1, DX-M1M and SG2300X adapters; physical acceptance and promotion. No new platform is declared
sales-ready merely by adding it to the release matrix.

## Primary platform references

- [Orion O6N](https://radxa.com/products/orion/o6n/) and [Radxa OS support](https://docs.radxa.com/en/software-support-matrix/rockchip).
- [Renesas SDK configuration](https://renesas-rz.github.io/rzv_ai_sdk/latest/v2h-configuration.html) documents its Yocto BSP.
- [AMD ROCm device matrix](https://rocm.docs.amd.com/en/latest/reference/gpu-specs.html); validate the installed release's exact SKU support.
- [Coral inference requirements](https://www.coral.withgoogle.com/docs/edgetpu/inference/) and [dual TPU lane requirements](https://www.coral.withgoogle.com/products/m2-accelerator-dual-edgetpu).
- [Radxa AX-M1 documentation](https://docs.radxa.com/en/aicore/ax-m1).

Additional references: [DEEPX SDK](https://docs.radxa.com/en/aicore/dx-m1/dx-sdk), [DX-RT compiled-model contract](https://docs.radxa.com/en/aicore/dx-m1/dx-sdk/dx-rt), and [SG2300X topology](https://docs.radxa.com/en/aicore/sg2300x).
