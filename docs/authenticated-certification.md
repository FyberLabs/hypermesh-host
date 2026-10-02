# Authenticated acquisition and controlled activation

This Linux flow supports admitted `agx-large` and `nx-volume` NVIDIA hosts. It
finishes certification while the agent is inactive, then starts the agent paused.
HIP, Metal, Renesas and Rockchip runtime certification are not enabled by this change.

## Operator prerequisites

Deploy the paired Panopticon change and database migration before this host
release. Configure the authenticated AIuditor program with exactly one
`pack-v1` or `thin-v1` suite member for the tenant/class, and a matching envelope:
GGUF file SHA-256, NVIDIA runtime, advertised context, positive decode rate and
optional thermal baseline. Missing or multi-model programs fail closed.

The platform operator must review catalog model size, hash, immutable runtime
image digest and context; these must match the AIuditor envelope. Configure the
catalog's immutable source through the internal authenticated
`PUT /api/v1/hypermesh/internal/onboarding-models/{catalog_id}` endpoint with
`{"source_ref":"PUBLISHER/REPOSITORY@40_HEX_COMMIT/path/model.gguf"}`.
Use the existing internal token and tenant headers through the internal service,
not a host or browser credential. No source, hash or performance baseline is
invented from a download. The runtime image must be public or already cached.

Complete the approved prerequisite/agent installation and original-owner
registration. Resolve intake, holds, leases and pending jobs in the dashboard.
Activation requires accepted intake; certification can run while intake is pending.

## Host commands

Use the installed release's deploy CLI and retain its reviewed agent plan:

```sh
sudo hypermesh-deploy plan-certification --agent-plan agent-plan.json > certification-plan.json
sudo hypermesh-deploy certify-agent --plan certification-plan.json \
  --approve-plan CERTIFICATION_PLAN_SHA256 --run
sudo hypermesh-deploy activate-agent --plan certification-plan.json \
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
before uploading. Panopticon compares it to the frozen AIuditor class envelope
and atomically records the result. This is a measured short certification, not
a sustained/soak benchmark or third-party hardware attestation.

Activation requires a fresh browser/device login by the original owner, current
program/catalog pins, accepted intake, no holds/jobs/leases and a short-lived
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
Physical AGX-1 installation, real AIuditor baseline compatibility, reboot recovery,
and model license/source provisioning still require operator acceptance.
