# Hypermesh host agent

Host software for certified Jetson boxes. First machine: NVIDIA Jetson AGX Orin 64GB. Product name is **Hypermesh** or **Hyperme.sh**. This repo is not the control plane and does not live in FyberLabs/panopticon.

The agent enrolls, heartbeats, and runs **stub-suite-v1** Path B against Panopticon `products/hypermesh` on port **8010**. It does not invent AIL, a subnet, Connect, public bot rental, or tokens/sec.

## Status

First slice. Tunnel apply is a no-op until the control plane sends a payload (there is no tunnel HTTP yet). NVIDIA-runtime container start is an interface; Start without a workload digest errors instead of faking a lease. Catalog id `llama-3.1-8b-q4` is in-tree and uncertified.

## Build and test

Go 1.22+.

```bash
go test ./...
go build -o hypermesh-agent ./cmd/agent
```

Tests use `net/http/httptest`. They do not need a live Panopticon.

## Run against :8010

Bring up Panopticon Hypermesh (`docker compose` service on **8010**). Then:

```bash
export HYPERMESH_CONTROL_PLANE_URL=http://127.0.0.1:8010
export HYPERMESH_TENANT_ID=<marketplace-tenant-uuid>
export HYPERMESH_ENROLL_TOKEN=<one-time-token>
export HYPERMESH_STATE_DIR=./state

./hypermesh-agent
```

Same values as flags: `-control-plane-url`, `-tenant-id`, `-enroll-token`, `-state-dir`.

Every request sends `X-Tenant-ID`. After enroll the process writes `device.json` (`device_id`, `credential_prefix` only). The enroll token is not stored. The control plane currently returns `credential_prefix` and does not check Bearer on agent routes; `-device-secret-header` is a hook for when that header is documented. Do not send a guessed `Authorization`.

Agent routes used (and only these):

| Call | Path |
|---|---|
| Enroll | `POST /api/v1/hypermesh/agent/enroll` |
| Heartbeat | `POST /api/v1/hypermesh/agent/heartbeat` |
| Job pull | `GET /api/v1/hypermesh/agent/jobs?device_id=` |
| Result | `POST /api/v1/hypermesh/agent/jobs/{id}/result` |

Heartbeat includes image hash, uptime, and thermal when available, plus an nvpmodel reading in the local log. Path B stub collects those and POSTs `passed` + `image_hash`. It is not a real cert suite.

Logs must not contain enroll tokens, device secrets, or renter prompts.

## Install architecture

### Path A — AGX Orin (do now)

Module **P3701-0005**, board `jetson-agx-orin-devkit`. Overlay APP only.

Pin **JetPack 7.2.1 / L4T r39.2.1** (Ubuntu 24.04 sample rootfs, kernel 6.8) unless workload images are still JetPack 6 / CUDA 12, in which case pin **6.2.3 / 36.5.2**. No in-place JP6→JP7 apt upgrade.

On the flash host: `l4t_create_default_user.sh`. On the device: `multi-user.target`. Do not purge `gdm3` or `nvidia-l4t-3d-core`. Do not overwrite `/etc/nv_boot_control.conf`.

Then: systemd unit + this agent + Docker from get.docker.com + JetPack `nvidia-container` + `nvidia-ctk runtime configure --runtime=docker` + Docker `default-runtime` nvidia. GPU path is `--runtime=nvidia`, never x86 `--gpus`.

AGX 64GB nvpmodel default is **30W (id 2)**. Report nvpmodel. Do not default MAXN + `jetson_clocks`.

Details: [deploy/agx/first-boot.md](deploy/agx/first-boot.md). Unit: [deploy/agx/hypermesh-agent.service](deploy/agx/hypermesh-agent.service).

### Path B — Seeed Super J4012 (later)

Orin NX 16GB, not AGX. Seeed mfi **JP6.2 / 36.4.3** or **JP7.2.0 / 39.2.0** (not NVIDIA 39.2.1). Separate image, different nvpmodel table (`MAXN_SUPER`), factory via Seeed massflash. Do not mix flash XML. Placeholder: [deploy/seeed-j4012/README.md](deploy/seeed-j4012/README.md).

## Layout

```
cmd/agent           process
internal/api        four-route HTTP client
internal/pathb      stub-suite-v1
internal/runtime    NVIDIA Container Runtime start/stop
internal/tunnel     WireGuard apply (no-op / honest error)
internal/telem      image hash, nvpmodel, thermals, uptime
internal/agent      enroll / heartbeat / job loop
deploy/agx          systemd + first-boot
deploy/seeed-j4012  placeholder
```

## Out of scope here

Tunnel allocate, lease start, workload pull, a real Path B suite, Seeed factory flash, public bot rental, fake tok/s. Those wait on the control plane or a later slice.
