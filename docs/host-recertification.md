# Software drift and recurring host certification

Host compatibility is an eligibility check. Certification is a time-limited
control-plane decision backed by a new timed model run. Passing preflight never
renews certification or grants a hardware protection tier.

The agent sends an additive `environment_hash` on every heartbeat, independently
of `HYPERMESH_IMAGE_HASH`. The v1 digest covers the agent build, OS and architecture,
running kernel, installed NVIDIA/AMD module versions, Tegra release, CUDA toolkit
metadata, Debian package inventory, BIOS version, and Jetson power configuration.
Apple's system version file is included when present. Optional files have explicit
missing markers; unreadable or oversized files make the observation unknown.
Inventory bytes, host identifiers, credentials and prompts are not sent.

This is drift telemetry, not remote attestation. The initial collector covers
Debian/Ubuntu package changes and the listed platform files. Other package
managers, manually replaced firmware and arbitrary external accelerators need
additional version probes before claiming complete inventory coverage. Exact
model and workload image digests remain separate requirements.

Certification jobs carry the expected `environment_hash`. The agent samples
inventory before and after execution, requires an actual verified timed workload
for a bound job, and sends the environment and `certification_job_id` with its
scorecard. Drift during the run discards the sample. A late result for a different
environment cannot certify the changed host.

The matching Panopticon implementation renews compact certification every 24 hours
and after inventory changes. It blocks new placements and new payment/start
eligibility while evidence is stale, leaves existing lease commitments to finish,
and queues renewal once the host is idle. A renewal updates the host measurement
against the program's existing performance envelope; it does not recalibrate that
envelope to its own sample. Firmware and memory-protection qualifications retain
their separate approval and evidence rules.

Deploy the control-plane migration and API first, then publish the agent through
the existing signed test channel. A fleet that requires this policy for every
host sets `HYPERMESH_REQUIRE_ENVIRONMENT_CERTIFICATION=1` on the plane after its
agents are updated. Without that rollout flag, old agents retain the legacy policy
until their first inventory heartbeat. Once adopted, omission never returns a
host to the legacy policy. No extra binary bundle is involved.

## AGX eligibility policy

`agx-host-eligibility/v2` accepts CUDA toolkit `13.2.<patch>` with patch at least 1
on the existing reviewed AGX BSP configuration. CUDA 13.2.2 therefore qualifies
for a fresh measurement rather than failing only because it differs from 13.2.1.
The workload OCI pin and its CUDA 13.2.1 build base stay reproducible. Unknown,
prerelease, rescue, older and different minor/major toolkit versions stay outside
this policy until their platform profile is reviewed.

This bounded policy is a project eligibility rule, not a vendor guarantee for
all Jetson combinations. NVIDIA documents driver and feature limitations for
[CUDA minor-version compatibility](https://docs.nvidia.com/deploy/cuda-compatibility/minor-version-compatibility.html)
and platform-specific upgrade requirements in
[CUDA for Tegra](https://docs.nvidia.com/cuda/cuda-for-tegra-appnote/index.html).
A timed workload and the program's acceptance gate remain required.

On 2026-10-29 the updated read-only Python probe ran on AGX64-1 and observed CUDA
13.2.2 with all eligibility checks passing. Its report explicitly says
`certification_required: true`. No driver, agent or running lease was changed;
this observation does not claim renewed certification or performance acceptance.
