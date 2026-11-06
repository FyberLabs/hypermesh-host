# Certification measurements on Linux and native Mac

Measurement collection is independent of certificate issuance. Missing class
references, unverified references and incomplete enrollment must not prevent a
local model measurement. `measure-certification` records a fresh report without
contacting the control plane or changing enrollment, eligibility or selling.

A certificate still requires evaluation of current evidence against the applicable
program. `measurement_state=passed` means the requested samples completed;
`certification_state=not_evaluated` is deliberately separate. Neither status
claims sustained performance, hardware protection or physical acceptance.

## Native Apple silicon

Use a local Metal-enabled llama.cpp `llama-bench`, its complete runtime libraries,
and an explicit GGUF. Resolve and verify upstream artifact provenance before
running; this command verifies the supplied executable/model SHA256 values, not
the trustworthiness of an arbitrary supplied pin or all dynamic libraries.

```sh
hypermesh-host measure-certification \
  --model-file /absolute/path/model.gguf --model-sha256 MODEL_HEX \
  --runtime-file /absolute/path/llama-bench --runtime-sha256 EXECUTABLE_HEX \
  --backend metal --context-tokens 16384 --generated-tokens 32 \
  --samples 3 --artifact-dir /private/path/evidence
```

On Darwin, `certify-agent` accepts these same native measurement arguments;
`plan-certification` explains them. No Linux, CUDA, Docker or enrollment plan is
required. The executable runs natively with Metal GPU offload, never a CUDA
container. The parser verifies the requested model, context depth, decode count,
GPU layer count, backend and measured timing. Upstream `MTL` and `Metal` backend
names are accepted. CPU-only output cannot pass a Metal request.

Each sample starts a fresh process and loads the model independently. The test
prefills the context minus the generated tokens before measuring decode. It is
not an HTTP-serving, tunnel, thermal-soak or memory-protection test. In particular,
a native measurement report does not grant a Mac permission to sell.

## Linux NVIDIA

Use the existing pinned isolated runtime without requiring a class baseline:

```sh
sudo hypermesh-host measure-certification \
  --model-file /var/lib/hypermesh/models/cache/MODEL.gguf \
  --model-sha256 MODEL_HEX \
  --runtime-image REGISTRY/IMAGE@sha256:DIGEST \
  --backend cuda --context-tokens 16384 --samples 3 \
  --artifact-dir /private/path/evidence
```

This path reuses the existing NVIDIA workload runner, including model hashing,
container isolation, timed completion, full-context exercise and cleanup. Its
raw receipt retains the suite's actual decode and context-fit evidence;
`generated_tokens=0` in the outer report means no native llama-bench decode-count
request was made. It never substitutes a locally selected class reference.
Linux CPU, CUDA, HIP and Vulkan llama-bench builds can alternatively use the
explicit executable form with the matching `--backend`.

## Reports and workload preservation

Each invocation creates a private directory with a random run ID, `report.json`
and raw successful-subprocess sample output. A failed measurement retains its
failure code and prior completed samples. Missing input/model/runtime pins,
unsupported backends, insufficient observed memory and active leases are recorded
as failures, rather than disappearing behind program-readiness errors. Invalid
command syntax or unavailable evidence storage cannot produce a report.

The runner checks the configured state directory for `lease.json` before every
sample and never stops host services or interrupts leases. Use an idle, paused
host and preserve the normal scheduler/maintenance exclusion while measuring;
the local lease-file check is not a distributed reservation. Executables run in
a separate process group with bounded output and a per-sample deadline; inherited
host credentials and loader overrides are not passed. Hardware and observed
software-inventory identity must remain unchanged for passing measurements.
The environment hash describes this measuring binary's inventory, not an
assertion that the installed agent has the same build identity.

The plane's first-baseline qualification path also permits candidate collection
when an existing reference is unverified. It retains that reference and all
candidate evidence; it does not silently replace a class reference, certify the
host or activate selling. Normal automated comparison remains available when a
trusted matching reference exists.
