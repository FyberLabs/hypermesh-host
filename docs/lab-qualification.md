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
report without repeating measurements. An operator creates a fresh run for a
new attempt. Three successful samples go to operator review; the program operator
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

Network admins can reject evidence or authorize a fresh attempt after failure or
expiry; earlier journals and measurements remain intact. An unavailable AIuditor
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
