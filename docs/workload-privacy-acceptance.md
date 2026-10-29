# Workload privacy acceptance

Issue [#133](https://github.com/FyberLabs/hypermesh-host/issues/133) requires
workload output and core limits to be explicit. Shared `JailArgs` applies
`--log-driver none --ulimit core=0` to NVIDIA, HIP and Linux CPU workloads.
Instance checks inspect Docker's effective configuration before sending text.
The inspector rejects missing, null or duplicate core limits as well as any
nonzero soft/hard limit or a log driver other than `none`.

## Repeatable local check

From the reviewed host checkout, inspect the existing workload without changing
it. The collector uses the local Docker Unix socket, exports only the logging
and core settings, and creates a new private report without overwriting files:

```sh
sudo python3 scripts/check-workload-privacy.py \
  --container WORKLOAD_CONTAINER \
  --output /root/workload-privacy.json
```

For an explicit synthetic stdout/stderr persistence test, select an already
cached image ID that has `/bin/sh`, then use a new report filename:

```sh
sudo python3 scripts/check-workload-privacy.py \
  --container WORKLOAD_CONTAINER \
  --run-canary --canary-image sha256:EXACT_CACHED_IMAGE_ID \
  --output /root/workload-privacy-canary.json
```

The existing workload is only inspected. A separate temporary CPU container has
32 MiB memory, 0.1 CPU, 16 PIDs, no network, no GPU, no model mount, a read-only
root and a 1 MiB `/tmp` tmpfs. No image is pulled. Its logging/core controls are
inspected before sending a fresh synthetic value on stdin. Image-declared
volumes are rejected by inspecting the cached image before container creation,
and effective mounts are checked again before any payload is sent. The value must reach
both stdout and stderr, while Docker logs, the local Docker container directory
and the journal contain zero matches. The payload never enters argv, an
environment variable, a report or a host file. Only its hash and counts leave
the process. Command output and journal reads are bounded to 8 MiB; the disk
scan is bounded to 64 MiB. An incomplete scan or unexpected Docker response
cannot pass. The disk scan does not follow file symlinks. See the official Docker documentation
for [image-declared volume initialization](https://docs.docker.com/reference/dockerfile/#volume)
and [anonymous-volume removal](https://docs.docker.com/reference/cli/docker/container/rm/#volumes).

Cleanup removes only the container whose random ownership label matches this
run, including its anonymous volumes. A cleanup failure leaves the report failed. Reports are mode 0600 and
contain no container ID, hostname, model path, credentials or raw command output.
Root is required for a complete journal/container-file scan. This is an I/O
persistence check, not an inference benchmark or proof about swap, crash handlers,
privileged administrators or data written outside the workload's configured jail.

## Observed lab result

[The redacted evidence](host-issues-40-133-evidence.json) records the actual run:

- The existing AGX workload had `none` logging and core soft/hard limits 0.
- A separate canary emitted its synthetic value to both stdout and stderr.
- Docker logs were empty; Docker container-file matches and journal matches were 0.
- The canary was removed; the existing workload's start time, restart count and
  running state stayed unchanged.

The initial I/O collector did not send a model prompt or take over a lease.
After explicit owner approval for a synthetic model request, the separate model
probe below completed on the current disposable lab workload. This completes
#133's host-side model-prompt privacy check; public renter checkout, payment,
Bronze qualification and hardware certification are separate acceptance work.

## Explicitly approved model request

Only use a disposable lab lease whose owner approved this test. The command
sends one bounded synthetic completion request; it does not stop, restart or
reconfigure the workload or acquire/settle a lease:

```sh
sudo python3 scripts/check-model-privacy.py \
  --container APPROVED_DISPOSABLE_LAB_WORKLOAD --run \
  --output /root/model-privacy.json
```

The probe resolves the selected container's immutable ID, verifies its effective
logging/core controls, and derives the HTTP endpoint from its sole internal
workload network. It disables environment proxies and rejects redirects. It
reads the server's model alias, then asks for one fresh synthetic output marker
with at most 96 completion tokens. Prompt/output stay in bounded memory and
reports contain only hashes, usage and observations. Unsuccessful inference,
missing usage, a mismatched marker, incomplete scans or changed workload state
cannot pass. No model request is made by the I/O collector or by omitting `--run`.

The recorded real AGX run used the pinned public Q4_K_M artifact (4,920,739,232
bytes, SHA-256 `7b064f5842bf9532c91456deda288a1b672397a54fa729aa665952863033557c`)
and cached image ID
`sha256:2d55427abb5fa0c5013985a7ef27a8821ae9046df165603219187c81923b2674`.
The model bytes were read and verified against the public pin after the probe.
The response consumed 97 prompt tokens and generated 25 completion tokens in
3,631 ms, including the requested marker. Docker logs were empty; input and
output markers had zero matches in container files and the journal. The
workload's running state, start time and PID were unchanged. The original lease
had ended before the first attempt, which sent no request; the successful test
used the subsequently running disposable lab workload. No replacement model
container was created by our attempts.

This is a direct local-container inference/privacy test, not a public gateway,
renter checkout or provider payment journey. It does not claim a sustained
performance baseline or resolve the separate CUDA pin mismatch.
