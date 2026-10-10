# Authenticated acquisition and controlled activation

This Linux flow supports admitted `agx-large` and `nx-volume` NVIDIA hosts. It
finishes certification while the agent is inactive, then starts the agent paused.
HIP, Metal, Renesas and Rockchip runtime certification are not enabled by this change.

## Operator prerequisites

Deploy the paired Panopticon change and database migration before this host
release. Configure the authenticated certification program with exactly one
`pack-v1` or `thin-v1` suite member for the tenant/class, and a matching envelope:
GGUF file SHA-256, NVIDIA runtime, advertised context, positive decode rate and
optional thermal baseline. Missing or multi-model programs fail closed.

The platform operator must review catalog model size, hash, immutable runtime
image digest and context; these must match the class envelope. Configure the
catalog's immutable source through the internal authenticated
`PUT /api/v1/hypermesh/internal/onboarding-models/{catalog_id}` endpoint with
`{"source_ref":"PUBLISHER/REPOSITORY@40_HEX_COMMIT/path/model.gguf"}`.
Use the existing internal token and tenant headers through the internal service,
not a host or browser credential. A download does not supply a source, hash,
or performance baseline. The runtime image must be public or already cached.

Complete the approved prerequisite/agent installation and original-owner
registration. Resolve intake, scheduling restrictions, leases and pending jobs in the dashboard.
Activation requires accepted intake; certification can run while intake is pending.

## Host commands

Use the installed release's deploy CLI and retain its reviewed agent plan:

```sh
sudo hypermesh-host plan-certification --agent-plan agent-plan.json > certification-plan.json
sudo hypermesh-host certify-agent --plan certification-plan.json \
  --approve-plan CERTIFICATION_PLAN_SHA256 --run
sudo hypermesh-host activate-agent --plan certification-plan.json \
  --approve-plan CERTIFICATION_PLAN_SHA256 --start
```

Review the full certification plan before approving its `plan_sha256`. It binds
the registered device, frozen server run, model source/size/hash, runtime digest,
context exercise, class envelope and destination beneath the approved models
directory. A plan expires after 24 hours. Plan creation records a server run;
it does not download or start a service.

For gated model repositories, supply `--model-token-file /root/private-hf-token`
to `certify-agent`. The root-owned private file contains only the authorized HF
token. Accept any upstream model license before downloading. Tokens are never
stored in plans/journals or emitted in logs; redirects strip the token outside
huggingface.co. Only allowlisted publishers at immutable commits are accepted.

Downloads have a 30-minute total deadline and exact size/hash checks. The
content-addressed cache is on the selected models filesystem; existing files
are verified, never replaced. The download reserves space for the model plus
2 GiB and 64 MiB metadata margin, alongside existing hardware/resource checks.
Temporary files are removed after ordinary failure. Docker image layers use
Docker's own storage; a separate Docker capacity budget remains future work.

The installer uses the pinned temporary runtime, exercises the entire advertised
context and measures a short completion, removes the container, checks fresh
WireGuard connectivity and repeats local preflight. It saves exact evidence
before uploading. Panopticon compares it to the frozen class envelope
and atomically records the result. This is a measured short certification, not
a sustained/soak benchmark or third-party hardware attestation.

Activation requires a fresh browser/device login by the original owner, current
program/catalog pins, accepted intake, no scheduling restrictions, jobs, or leases and a short-lived
server permit. The installer revalidates installed payloads, identity and systemd
unit paths. It saves an indefinite paused sell posture before creating the
activation marker and enabling the agent. It requires 15 seconds of stable running state with the same systemd invocation,
PID and restart count before recording activation, and attempts
bounded stop/disable/marker removal on failure. This is a startup stability
check, not a guarantee of future service uptime. Starting the agent does not
enable selling; that remains a separate owner action under the existing gates.

## Retry and recovery

Keep the same plan and private installer state. The command prints the run ID,
plan digest, stage and fixed failure code. A lost result acknowledgment retries
the exact saved evidence without redownloading or rerunning the workload.
Pre-upload failures can retry the same unexpired plan after correcting the cause.
A class-envelope failure is terminal for that run; review the cause before
preparing a new run. A fresh plan resets only the
suspension belonging to that same failed inactive run; unrelated suspensions
still require operator resolution. Expired plans or changed pins need a
new reviewed run. Never edit journal verdicts or create activation markers by hand.

An interrupted startup remains `activating`; rerun the same approved
`activate-agent` command to reconcile only its matching marker and units.
Unconfirmed cleanup remains visible and never reports success. Preserve
`certification-PLAN_SHA256.json` in the private installer state for recovery;
it contains measured metadata, not model tokens or browser credentials.

Automated tests cover download integrity/deadlines, immutable/idempotent result
upload, owner and state gates, SQL rollback/concurrency, and startup rollback.
Physical AGX installation, real class baseline compatibility, reboot recovery,
and model license/source provisioning still require operator acceptance.

## Read-only certification recovery

Inspect the private journal without contacting the plane, rerunning a workload,
or changing services:

```sh
sudo hypermesh-host certification-status --plan-sha CERTIFICATION_PLAN_SHA256
sudo hypermesh-host support-bundle \
  --certification-plan-sha CERTIFICATION_PLAN_SHA256 \
  --output /root/hypermesh-support-new.json
```

The status command prints the same redacted certification stage that the support
bundle exports. You can combine the certification selector with agent, setup
and workload plan selectors to collect their independent historical records.
A combined bundle does not establish that independently selected plans belong
to the same installation.

Both commands acquire a shared installer lock, read private regular files and
validate state/evidence consistency. They never create missing state or retry an
operation. Missing, unsafe, corrupt or incomplete records produce an explicit
collection status and exit 1; support-bundle still saves a partial redacted
artifact. Exit 0 means collection succeeded, including when the recorded
certification failed. Use the stage's state, failure_code and next_action to
understand the outcome.

The summary distinguishes an unconfirmed upload, a terminal class-envelope
failure, rollback after failed startup, unconfirmed activation/cleanup, and
recorded stable startup. It includes dated context, throughput, cleanup and
tunnel observations, but omits run/device IDs, model hashes, registry/source
addresses, local model paths and credentials. Keep the original plan and journal
locally; the redacted export cannot restore them.

All observations are historical. The journal alone cannot revalidate the frozen
manifest, plan expiry, server verdict, current service state or selling gates.
An active record does not authorize selling or prove that the agent is still
running. Use host-status below for current service and plane observations. Reviewed maintenance reboot/resume is implemented; see [lifecycle completion](host-lifecycle-completion.md).

## Live host status

After creating the certification plan, use its matching signed installer:

```sh
sudo hypermesh-host host-status --plan certification-plan.json
```

This read-only command combines current local observations and an authenticated
plane query. It never starts/stops services, reboots, sends a heartbeat, creates
a WireGuard interface, changes a scheduling restriction or modifies selling state. It uses the
saved device credential; no browser login is needed.

The JSON gates report installed-file and systemd configuration integrity, local
identity, certification history bound to this plan/run/model, activation marker,
reboot requirement, enabled/running service state, current hardware intake,
class, certification, scheduling restriction, certification-program availability and heartbeat age.
Each blocked gate has a fixed next action. The service must retain the same PID,
invocation and restart count across the plane observation. No raw service errors,
keys, device/run IDs, model paths or registry addresses enter the output.

Exit 0 means the implemented diagnostic gates passed during this observation.
Exit 1 means attention is needed or the observation could not be completed;
exit 2 means invalid command arguments. The command takes the installer read
lock and has a 90-second overall deadline for its bounded command/network
operations. As with other filesystem verification, kernel I/O stalls retain
normal OS limitations. No journal is written.

The heartbeat gate uses a diagnostic maximum age of 120 seconds measured against
the plane clock. This is not the scheduler's eligibility policy. A future-dated
heartbeat fails the gate; an absent or stale heartbeat must not be refreshed by
the diagnostic itself. A fresh heartbeat is not proof of the WireGuard data path.

A green report grants neither activation nor selling permission. It does not
rerun memory/model workloads, verify free capacity, probe the active tunnel,
verify package-specific reboot completion, or prove sustained uptime. Inspect
the dashboard's current lease and scheduling-restriction state before taking corrective action.
For a stopped service or interrupted activation, retain the original plan and
use certification-status guidance and the controlled activation flow. Do not
start the agent by creating its activation marker manually.

The installation integrity check compares configuration and units with the original approved payloads. A changed agent binary is accepted only with matching signed update evidence, original trust, channel, architecture and minimum build. Reviewed maintenance resume remains paused and uses fresh owner authority. See [lifecycle completion](host-lifecycle-completion.md) and [AGX acceptance](agx-acceptance.md).

## Host-requested rebenchmarking

Rerun `sudo ./hypermesh-host` after resolving a failed measurement. With the
companion plane update, failed or expired qualification attempts receive a fresh
run on the authenticated host's next request. No administrator action is needed
to retry. The installer asks before downloading or running the workload; one
invocation does not loop over failed benchmarks. Previous run evidence and local
journals remain available.

Guided certification archives a failed plan and offers a fresh plan for every
failure category. Unconfirmed evidence uploads keep the original evidence and
retry its delivery. Enrollment, device keys, model cache, scheduling restrictions,
intake and activation checks remain in effect.

For an established class reference, the plane initially accepts measured decode
throughput inclusively within 90–110% of the reference. Model/context fit and
thermal checks still apply. The first reference for a new class continues to
require network-admin review; retry permission is not baseline publication or
permission to sell.
