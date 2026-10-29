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

The collector did not send a model prompt to the active workload or take over
its lease. The issue's full renter-lease/model-prompt journey remains a separate
controlled Bronze/end-to-end test; the canary establishes the Docker output
persistence behavior on the real lab daemon. No completed inference or hardware
qualification is claimed by this report.
