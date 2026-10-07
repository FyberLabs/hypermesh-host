# Initial lab qualification on an inactive host

The `qualify-host` command establishes a first measured baseline for an approved
model and hardware class. Ordinary hosts continue to use guided setup. A program
operator must authorize the lab run in Panopticon first; the run is bound to this
registered device, tenant, model pins and context for 24 hours.

```sh
sudo ./hypermesh-deploy qualify-host --run-id RUN_UUID
```

The command discovers the current guided installation plan, displays the pinned
model and runtime, and asks permission to acquire the model and perform three
independent tests. Each test checks current prerequisites and capacity, verifies
an authenticated WireGuard handshake and overlay echo, loads the pinned runtime,
checks full context fit, measures generation, and cleans up the temporary workload.
Existing AGX/Jetson NVIDIA test machinery supplies the actual measurements.

Use `--model-token-file /private/huggingface-token` for a gated model, or
`--agent-plan /absolute/path/agent.json` for a non-guided inactive installation.
Tokens are not included in evidence. The host's device identity authenticates
reporting; it cannot authorize its own lab run or publish an AIuditor envelope.

Evidence is kept in `/var/lib/hypermesh/installer/qualification-RUN_UUID.json`.
Interrupted tests remain failed attempts. Rerunning the command retries a pending
report without repeating measurements. For automatic qualification, rerunning
guided setup requests a fresh attempt without operator intervention. Three successful samples go to operator review; the program operator
publishes the measured baseline in AIuditor before ordinary certification resumes.
No service is activated and selling remains disabled by this command.

This first pack captures repeatability, model load, full-context completion and
compact-cert decode measurements. It does not claim sustained thermal stability,
peak-memory measurement, or concurrency qualification. Temperature is recorded
only when the existing runner provides it. The host stays subject to separate
intake approval, normal certification, owner approval and activation.

Implementation and automated recovery/protocol tests are complete. Physical AGX
qualification remains pending deployment of the matching Panopticon API and signed
host build. The operator procedure is documented in Panopticon's
`docs/initial-host-qualification.md`. AMD and additional boards need their own
runtime profiles and physical evidence; this change does not certify them.

Each repetition uses its own manifest-bound workload/measurement ID. Replayed
samples are rejected, including during journal recovery. Reporting advances the
durable checkpoint only when the plane confirms the expected terminal state
(`passed` for successful measurements, `failed` for a recorded failure).
Standalone agent plans use the same regular-file reader as other installer
commands; private registration and qualification journals retain their stricter
ownership and permission checks.

## Automatic guided qualification

Running `sudo ./hypermesh-deploy` now checks whether the configured hardware-class
suite model has an authoritative reference. If it has none, the plane creates or
resumes a qualification attempt and the guide offers the pinned workload for
approval. It executes the same three-sample qualification without asking the host
owner to obtain an operator key or enter a run ID.

After submission the guide leaves setup saved at the certification stage and
prints the network-admin review URL (`/operator/benchmarks`). Rerunning while
review is pending does not repeat workloads or registration. Once the admin
publishes a baseline, the same command continues ordinary certification. Services
remain inactive until the separate owner activation flow completes.

Network admins can reject evidence and still authorize retries. Hosts can also
request fresh attempts after failure or expiry; earlier journals and measurements
remain intact. An unavailable AIuditor
or invalid configuration is not treated as an absent reference. Physical AGX
execution remains pending deployment of both the plane and signed host build.

Before an automatic qualification attempt, the guide asks for an optional private
Hugging Face token file. Enter a file path for a gated model, or press Enter for
public/cached models. The file is validated before starting the attempt, so a
missing or invalid local credential file can be corrected without consuming a run.

If the platform lacks reviewed qualification model pins, guided setup now identifies
that configuration issue instead of reporting a host-name/key conflict. Keep the
registered identity and rerun the same installer after the operator deploys the
model configuration. Arbitrary server response bodies remain excluded from logs.

### Full-context runtime compatibility and failed attempts

The context probe reads `GET /props` and requires exactly one slot whose
`default_generation_settings.n_ctx` matches the approved manifest. It does not
require `generation_settings.n_ctx` in the completion response. The isolated
qualification runtime disables context shifting and prompt caching; exact prompt
and generation counts and a limit stop remain required. A `truncated=true` flag
at that exact boundary is accepted: llama.cpp commit `7fe450e` sets the flag when
no-context-shift generation reaches capacity, even after generating every
requested token. Missing metadata, wrong capacity, multiple slots, early stops,
and incomplete token counts still fail.

Reference: https://github.com/ggml-org/llama.cpp/blob/7fe450e/tools/server/server-context.cpp

Safe workload failure details are stored as `failure_detail` in the local
qualification journal and displayed on failure/resume. Raw subprocess output and
model response content are not added to the report. Historical journals containing
only `failure_code` cannot reconstruct the discarded details.

For an already-reported failed automatic attempt, rerun guided setup and approve
the new measurements after the companion plane update is deployed. This uses the existing registration and cached model, retains
the failed attempt, and does not enable selling or auto-approve a baseline.

Implementation verification includes the AGX 16,368+16 token response, malformed
or mismatched runtime metadata, incomplete boundary responses, and durable
failure/replay behavior. Physical qualification on AGX remains to be rerun after
deployment; the manual context response is not a submitted qualification result.

## Host-requested rebenchmarking

Rerun `sudo ./hypermesh-deploy` after resolving a failed measurement. With the
companion plane update, failed or expired qualification attempts receive a fresh
run on the authenticated host's next request. No administrator action is needed
to retry. The installer asks before downloading or running the workload; one
invocation does not loop over failed benchmarks. Previous run evidence and local
journals remain available.

Guided certification archives a failed plan and offers a fresh plan for every
failure category. Unconfirmed evidence uploads keep the original evidence and
retry its delivery. Enrollment, device keys, model cache, scheduling holds,
intake and activation checks remain in effect.

For an established class reference, the plane initially accepts measured decode
throughput inclusively within 90–110% of the reference. Model/context fit and
thermal checks still apply. The first reference for a new class continues to
require network-admin review; retry permission is not baseline publication or
permission to sell.

### Local certification connectivity

Certification resolves the temporary container's current IPv4 address on the
isolated `hypermesh-workload` bridge and sends its local inference checks to
port 8080 there. Docker can record a requested loopback port binding without
actually publishing it on an internal network (`NetworkSettings.Ports` is null).
Certification therefore does not depend on a listener at host port 18080.
The bridge remains internal, with inter-container communication and masquerading
disabled. The endpoint must belong to the exact newly started, running container
on that network only. Resolution failures retain failed evidence and still clean
up the temporary container.


### Continuing after baseline approval

Rerun the same installer after the network admin publishes the reference. The
saved device credential continues onboarding, certification, and initial paused
activation without a second device-code login or owner browser approval. The
completed owner-approved registration is durable; current owner membership,
device binding, certification, model pins, holds and leases are still checked
before a short-lived activation permit is issued. Selling remains disabled.
Deploy the companion plane activation change before this installer release.
Maintenance resume after an operational stop retains its separate owner check.
