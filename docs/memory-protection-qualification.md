# Memory protection qualification pack v1

This pack prepares evidence for two independent, optional host-selection
requirements: `physical_memory` and `confidential_execution`. A host may eventually
qualify for either or both. Qualification of one never substitutes for the other.
Existing host/model certification and admission still apply.

The local framework is an evidence template and completeness validator. It does
not run a physical attack, enable firmware, attest a guest, or publish a
qualification. Even a complete dossier returns `qualified: false` and requires
independent program review. Production protected placement remains unavailable
until approval decisions are connected to the live verifier and all execution boundaries.

## Target hardware and firmware

| Target | Candidate requirement | Configuration and acceptance evidence |
| --- | --- | --- |
| Ryzen AI Max+ 395, non-PRO | Physical memory, unverified candidate | Obtain OEM confirmation for the exact system and BIOS. Do not infer support from the Ryzen name or SME flag. If firmware exposes TSME, validate its actual operation before review. |
| Ryzen AI Max+ PRO 495 | Physical memory candidate | AMD lists Memory Guard. Record OEM system, BIOS/AGESA version and supported setting; enable Memory Guard/TSME using OEM instructions, cold boot and collect current enablement evidence. |
| Jetson Orin T234 | Physical memory candidate | Record module, board, Jetson Linux release, boot firmware and MB1 BCT digest. Review `enable_nsdram_encryption`, `enable_blanket_nsdram_carveout` and GSC38 coverage using release-matched NVIDIA documentation. Capture the bootloader encryption message and effective configuration. |
| Jetson Thor T264 | Physical memory candidate | Review the release-matched non-secure DRAM encryption configuration and bootloader evidence. Do not copy T234 carveout settings blindly. |
| Apple silicon | Standard execution until a qualifying boundary is established | Secure Enclave memory encryption is not proof for ordinary CPU/Metal model allocations. |
| Rockchip RK3588 / Renesas RZ/V2H | Standard execution; TEE research | TrustZone/OP-TEE and crypto engines are not proof of full DRAM encryption or a protected GPU/NPU model path. |
| Documented SEV-SNP / Arm CCA platform | Confidential execution candidate | Qualify the exact CPU, firmware, hypervisor, guest and verifier. Ryzen Memory Guard support is not proof of SEV-SNP support. Start with CPU-only execution. |

Sources: [AMD PRO 495 specifications](https://www.amd.com/en/products/processors/laptop/ryzen-pro/ai-max-pro-400-series/amd-ryzen-ai-max-plus-pro-495.html),
[AMD protection boundaries](https://www.amd.com/en/developer/sev.html),
[NVIDIA T234 memory encryption](https://docs.nvidia.com/jetson/archives/r36.4.4/DeveloperGuide/SD/Security/MemoryEncryption.html),
[NVIDIA T264 memory encryption](https://docs.nvidia.com/jetson/archives/r38.2.1/DeveloperGuide/SD/Security/MemoryEncryption.html).

Firmware configuration is an operator procedure. Save the original configuration
and recovery method, stop model leases, use a vendor-supported update/configuration
path, reboot and requalify. This command does not flash firmware, change fuses,
write MSRs or reboot a host. Board-specific names and recovery procedures must
come from that OEM; no universal BIOS switch is assumed.

For UMA, record the actual GPU allocations, memory-controller encryption scope
and DMA/IOMMU path. For discrete GPUs, record device memory and transfers
separately. `accelerator_memory_io_boundary` is mandatory for both accelerated
backends; a CPU result cannot qualify them.

## Prepare the evidence dossier

Run on the target Linux host. Use the exact model and runtime content hashes,
not a catalog label, mutable container tag or CPU marketing name.

```sh
hypermesh-host protection \
  --modes physical_memory,confidential_execution \
  --backend cpu \
  --model-sha256 MODEL_CONTENT_SHA256 \
  --runtime-sha256 RUNTIME_CONTENT_SHA256 \
  --context-tokens 4096 > protection-evidence.json
```

Choose only the modes actually under test; for the PRO 495 physical-memory lab,
use `--modes physical_memory --backend uma` when exercising GPU allocations.
`cpu`, `uma` and `discrete_gpu` are evidence memory domains, not runtime names.
The template captures current Linux boot ID and DMI board/firmware identity.
For device-tree systems without DMI, supply `--firmware-id` from the recorded
vendor firmware/build version. This is operator-reported provenance, not proof.
Missing hardware, firmware or boot identity prevents review readiness.

The report lists every required check with `status: unknown`. Populate each
check only after running it and retaining its evidence. `passed` means the test
observed its expected outcome; a negative test passes only when the attempted
unsafe operation was rejected. Failed, skipped or unknown checks block readiness.
Never populate the template from simulated results for a physical qualification.

Store evidence files in a private directory under their lowercase SHA-256 content
hashes. Set `artifact_sha256` to that hash. Keep prompts, credentials, device keys
and proprietary weights out of uploaded artifacts; use public test weights and
synthetic inputs. The validator hashes actual regular files, rejects symlinks and
tampering, and caps each artifact at 16 MiB.

Each artifact is a JSON envelope whose metadata is included in its content hash:

```json
{"schema":"hypermesh-protection-artifact-v1","kind":"check","id":"firmware_identity","captured_at":"2026-10-09T12:00:00Z","payload":{"vendor_log":"captured test evidence"}}
```

Use `kind: sample` with the sample ID for measurement artifacts. A sample
artifact's payload must be the measurement written by `protection measure`; the
validator requires its binding, phase label, peak bytes and throughput to match the
dossier entry, and every sample to use the same generated token count. The capture
producer records `captured_at` when collecting the observation; copy that exact
instant into the corresponding check/sample entry. The validator compares the
hashed metadata with the dossier and rejects missing or relabeled capture times.
All observations must be less than 24 hours old and no later than validation.
Set dossier expiry no later than the earliest observation's capture time plus
24 hours. A newly prepared dossier cannot extend the life of an old artifact.
Hash-bound metadata remains producer-reported; independently trusted capture or
attestation is still required to establish the truth of its contents.

## Physical memory tests

1. Confirm vendor support for the exact SKU, board and installed firmware.
2. Record effective encryption enablement for the current boot using a qualified
   platform probe and vendor evidence. An SME flag, configuration intent or a
   photograph alone is insufficient proof of effective encryption.
3. Establish coverage for weights, input buffers and KV cache in the tested
   memory domain. Qualify UMA/device paths separately.
4. On an isolated lab host, test the disabled/unknown state and confirm that a
   protection-required verifier rejects it. No proprietary model delivery is
   permitted during this test.
5. Reboot and confirm previous boot-bound evidence cannot authorize a new lease.
   Change the firmware revision/configuration and confirm requalification is
   required. Restore the approved configuration and collect fresh evidence.
6. Review encrypted storage and swap, hibernation, core/kernel dumps and log
   controls. Full DRAM encryption does not protect plaintext written to disk.

Steps 4 and 5 require the eventual production verifier. Until it exists, keep
these checks unknown; unit tests of the dossier validator do not substitute for
the physical/integration checks. A complete dossier is therefore not yet
obtainable from current production components alone.

## Confidential execution tests

Use an independently operated verifier and key broker. Require a trusted AMD
certificate chain/TCB or the platform's approved Arm CCA attestation chain, pinned
guest measurement, debug-disabled policy, fresh challenge and a guest-generated
ephemeral key bound into the attestation. Bind key release to tenant, exact model
and runtime digests, guest identity and lease expiry.

Positive tests must execute the pinned workload inside that measured boundary.
Negative tests must reject replayed attestations, altered guest measurements,
wrong guest keys, unauthorized model requests and verifier outages. Attempt host
administrator reads of protected guest memory; verify they cannot retrieve test
weights or secrets. Demonstrate key expiry and cleanup after lease termination.
With an accelerator, prove the device and I/O boundary as well. Until this stack
exists, leave these checks unknown and offer no confidential qualification.

## Repeated workload measurements

Run three independent baseline loads and three independent protected loads using
the same model hash, runtime hash, backend and context size. For firmware toggles,
baseline samples can carry a different boot ID; protected samples must carry the
current protected boot ID. Firmware version and all other bindings must match.
Use a public model in the unprotected baseline.

Use the existing pinned `plan-workload` / `check-workload` or lab qualification
runner for supported profiles; unsupported AMD/ARM runtime profiles remain a
separate prerequisite. Retain unique raw measurement artifacts for every sample.
Populate `samples` with unique `id`, full `binding`, `protected` boolean,
`captured_at`, `tokens_per_second`, measured `peak_bytes` and `artifact_sha256`.
Record peak resident/accelerator memory with the platform's measurement tool;
model file size is not peak memory. Confirm full context fit and run a sustained
stability test under the class's reviewed duration and thermal limits.

The validator reports the protected/baseline mean throughput ratio. It does not
invent a universal performance threshold. The program reviewer defines acceptable
throughput, latency, memory and stability limits for each class/workload and
evaluates raw results. A completeness pass does not imply those limits passed.

## Validate for review

```sh
hypermesh-host protection \
  --modes physical_memory \
  --backend uma \
  --model-sha256 MODEL_CONTENT_SHA256 \
  --runtime-sha256 RUNTIME_CONTENT_SHA256 \
  --context-tokens 4096 \
  --report protection-evidence.json \
  --artifact-dir /private/protection-artifacts
```

Exit 0 means evidence is complete for review, 1 means blockers, and 2 means invalid
arguments/input. Evidence windows are at most 24 hours. Validation rejects future
or expired windows, changed local bindings, missing/duplicate checks, fewer than
three measurements per side, repeated sample artifacts, invalid measurements and
mode downgrades. JSON unknown fields and trailing data are rejected.
The output always sets `qualified: false`. Archive the dossier and artifacts for
program review; never copy `review_ready` into a host certification field.

## Optional host selection

The control plane accepts `protection_requirements` on lease requests and as repeated
query parameters on authenticated `GET /api/v1/hypermesh/renter/hosts`:

```json
{"kind":"p1_hardware","class_id":"agx-large","protection_requirements":["physical_memory"]}
```

Omit the field or use `[]` for ordinary selection. Require both modes by including
both names. Unknown or duplicate values are invalid. The chooser exposes
`qualified_protection_modes`, currently `[]`; protection-filtered lists are empty
and protected checkout returns 409 `protection_qualification_unavailable` before
payment, contract creation or workload enqueue. Explicitly choosing a device does
not bypass the requirement. No host self-report populates the qualified list.

The renter Hosts, Full Model checkout and Your Model pages offer separate
optional checkboxes for the two requirements. Changing them clears the selected
host and uses a distinct query cache entry. Checkout includes the chosen modes;
the options lock once a lease request is pending or an offer exists. The client
also hides rows lacking the required qualification fields if an older server
ignores the query filter. Qualified rows display the independently approved
modes, when those become available.

The approval registry now stores independently reviewed decisions and revocation.
The next production slice needs live current-boot observations and lease binding. Preserve requirements
durably and recheck them before delivery, enqueue, host load and lease activation;
confidential key release must occur inside the attested guest. Do not enable a
positive protected placement merely by removing the current rejection.

## Platform probe and CPU measurement runner

`hypermesh-host protection probe` emits a `firmware_identity` artifact envelope
with capture time, boot/DMI identity, CPU capability, architecture and platform
adapter revision. AMD x86 and device-tree NVIDIA T234/T264 candidates are
recognized; Jetson release metadata is collected where present. Every adapter
currently reports effective encryption as `unknown`: these are local collection
adapters, not qualified enablement probes. No BIOS writes, flash, fuse changes or
MSR access occur. Save the exact output under its SHA-256 content hash. It can
support identity review; it does not pass `encryption_enabled_current_boot` or
prove a memory domain's coverage. The exact OEM board/firmware and hardware
on/off trials are still needed to approve an enablement adapter.

The explicit `measure` subcommand runs trusted, pinned local workloads on Linux:

```sh
hypermesh-host protection measure \
  --model-file /lab/public-model.gguf --model-sha256 MODEL_SHA256 \
  --runtime-file /lab/llama-bench --runtime-sha256 EXECUTABLE_SHA256 \
  --protocol llama-bench --phase baseline \
  --context-tokens 4096 --generated-tokens 128 \
  --samples 3 --timeout 10m --artifact-dir /lab/private-artifacts \
  > baseline-samples.json
```

After the operator changes firmware, cold boots and collects effective enablement
evidence, repeat with `--phase protected` and the same pins/context. Merge the two
sample arrays into the dossier's `samples`; preserve each baseline boot ID and
use the current protected boot for protected samples. Set expiry from the oldest
capture. The phase is only an operator label, never an enablement decision.
On device-tree hosts without DMI, pass the same operator-observed `--firmware-id`
that the main `protection` command accepts; it is a label, not verification.

Use a reviewed, statically linked llama-bench build supporting JSON, context-depth
and CPU-only options. Each sample starts a fresh process with `-p 0 -n N -d
(context-N) -ngl 0 -nkvo 1 -nopo 1 -r 1 -o json`; decode happens at the requested prefilled
context depth. The parser checks the returned model path, depth, token count,
CPU offload settings (including operation offload, so the build must report `no_op_offload`) and single timing sample. It derives decode throughput from
the reported nanoseconds and retains raw benchmark JSON in the hashed artifact.
This benchmark excludes tokenization and sampling time; wall time is also saved.
See the [upstream llama-bench contract](https://github.com/ggml-org/llama.cpp/blob/master/tools/llama-bench/README.md).

Executable and model files must be regular, non-symlink files matching their
SHA-256 pins. Open descriptors are passed to the child, pins are rechecked before
and after every load, and platform identity is rechecked afterwards. The runner
uses a bounded output buffer and per-sample deadline, kills the process group on
completion/timeout, and writes exclusive 0600 content-addressed artifacts.
The executable pin is not a full OS/library/container measurement: qualify a
reviewed static build and platform image separately. The artifact directory must
be private and operator-controlled. The executable is trusted lab code, not an
untrusted program sandbox. Use public test models without proprietary prompts.

Peak bytes come from Linux process `getrusage` high-water RSS, not model file
size. This is not aggregate simultaneous child memory, GPU memory, a thermal or
sustained-stability test, or proof of memory-controller encryption. Only the CPU
backend is supported by this runner; UMA/discrete GPU need separate adapters.

For an existing independently reviewed lab adapter, `--protocol adapter` sends
one JSON object on stdin with schema `hypermesh-measure-request-v1`, `model_path`
(`/proc/self/fd/4`), the requested `generated_tokens` and the full `binding`. The
adapter must load that public model, exercise the requested context on CPU, and
emit a single JSON object whose `generated_tokens` equals the requested count and
whose `inference_seconds` is positive. Its timing is producer
reported, while the runner collects process peak RSS. Pin the adapter and review
all delegated runtime dependencies; an adapter pin does not bind those dependencies.
Unknown output fields, trailing data, a different token count, impossible timing and nonzero exit fail.
This protocol is for trusted lab integration, not automatic qualification.

The control-plane approval registry and durable lease-policy contract are
specified in [protection registry v1](https://github.com/FyberLabs/panopticon/blob/main/products/hypermesh/docs/protection-registry-v1.md).
Review authority remains separate from collection. Registry decisions do not
open protected checkout until live delivery/load enforcement is implemented.
