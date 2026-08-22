# Hypermesh host agent

Host software for certified Jetson boxes. First machine: NVIDIA Jetson AGX Orin 64GB. Product name is **Hypermesh** or **Hyperme.sh**. This repo is not the control plane and does not live in FyberLabs/panopticon.

The agent enrolls, heartbeats, applies the allocated WireGuard tunnel, and runs jobs against Panopticon `products/hypermesh` on port **8010**. It does not invent AIL, a subnet, Connect, public bot rental, or tokens/sec.

## Status

Agent HTTP matches the locked :8010 shapes (Bearer after enroll, tunnel get/ack, lease_start / lease_stop). Catalog id `llama-3.1-8b-q4` is in-tree and uncertified. Empty or bare `image_digest` on lease_start POSTs `passed=false` (not a running instance; CP must not go active).

## Build and test

Go 1.22+.

```bash
go test ./...
go build -o hypermesh-agent ./cmd/agent
```

Tests use `net/http/httptest` fixtures of the locked shapes. They do not need a live Panopticon.

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

Enroll (`POST /api/v1/hypermesh/agent/enroll`) is unauthenticated except `X-Tenant-ID`. The 200 body is `device_id`, `credential_prefix`, and `device_secret` (`hm_dev_…`, shown once). The agent writes those to `$STATE_DIR/device.json` at mode **0600**. The enroll token is not stored. The secret is never logged and must not be copied into unit command lines or journald.

After enroll, these routes send `Authorization: Bearer <device_secret>`, `X-Tenant-ID`, and `device_id` in the body or query:

| Call | Path |
|---|---|
| Heartbeat | `POST /api/v1/hypermesh/agent/heartbeat` |
| Job pull | `GET /api/v1/hypermesh/agent/jobs?device_id=` |
| Result | `POST /api/v1/hypermesh/agent/jobs/{job_id}/result?device_id=` |
| Tunnel | `GET /api/v1/hypermesh/agent/tunnel?device_id=` |
| Tunnel ack | `POST /api/v1/hypermesh/agent/tunnel/ack?device_id=` |

401 is missing/wrong secret. 403 is secret does not match `device_id`.

If the seed AGX already has a `device_id` but lost the secret, it cannot re-enroll (409). Platform mints a new token with `POST /api/v1/hypermesh/devices/{device_id}/enroll-token` (`X-Tenant-ID` only). That helper exists on the client; the agent loop does not call it.

### Jobs

Pull is one job or 204. Body: `job_id`, `kind`, `device_id`, optional `lease_id`, `catalog_id`, `image_digest`, `runtime`, `suite_id`.

| kind | What the agent does |
|---|---|
| `path_b` / `instance_check` | stub-suite-v1: collect image hash / uptime / thermal, POST `passed` + `image_hash` |
| `lease_start` | `image_digest` must be `repo@digest`. `docker pull` then `docker --runtime=nvidia --name hypermesh-<lease_id>`. Empty digest or bare `sha256:hex` → `passed=false`. Empty/unknown `kind` → `passed=false`. |
| `lease_stop` | Stop the container named for that lease. Tunnel teardown waits for GET tunnel `status=revoked`. |

Result body stays `{"passed", "image_hash?"}`.

### Tunnel

Control plane allocates on paid→starting. GET 200 is the current session or 204 none.

Allocated: `peer_private_key` is only on that GET. The agent writes a 0600 wg-quick config, `wg-quick up`, then POST ack `{"applied":true}`. Apply is idempotent: if conf + applied marker already match this `tunnel_id`, `wg-quick up` is skipped. The key is never logged.

Revoked or 204: `wg-quick down` and shred `/etc/wireguard/hypermesh.conf` so the peer key does not stay on disk.

## Install architecture

### Path A — AGX Orin (do now)

Module **P3701-0005**, board `jetson-agx-orin-devkit`. Overlay APP only.

Pin **JetPack 7.2.1 / L4T r39.2.1** (Ubuntu 24.04 sample rootfs, kernel 6.8) unless workload images are still JetPack 6 / CUDA 12, in which case pin **6.2.3 / 36.5.2**. No in-place JP6→JP7 apt upgrade.

On the flash host: `l4t_create_default_user.sh`. On the device: `multi-user.target`. Do not purge `gdm3` or `nvidia-l4t-3d-core`. Do not overwrite `/etc/nv_boot_control.conf`.

Then: systemd unit + this agent + Docker from get.docker.com + JetPack `nvidia-container` + `nvidia-ctk runtime configure --runtime=docker` + Docker `default-runtime` nvidia + `wireguard-tools`. GPU path is `--runtime=nvidia`, never x86 `--gpus`.

AGX 64GB nvpmodel default is **30W (id 2)**. Report nvpmodel. Do not default MAXN + `jetson_clocks`.

Details: [deploy/agx/first-boot.md](deploy/agx/first-boot.md). Unit: [deploy/agx/hypermesh-agent.service](deploy/agx/hypermesh-agent.service).

### Path B — Seeed Super J4012 (later)

Orin NX 16GB, not AGX. Seeed mfi **JP6.2 / 36.4.3** or **JP7.2.0 / 39.2.0** (not NVIDIA 39.2.1). Separate image, different nvpmodel table (`MAXN_SUPER`), factory via Seeed massflash. Do not mix flash XML. Placeholder: [deploy/seeed-j4012/README.md](deploy/seeed-j4012/README.md).

## Layout

```
cmd/agent           process
internal/api        locked HTTP client (agent routes + optional enroll-token helper)
internal/pathb      stub-suite-v1
internal/runtime    NVIDIA Container Runtime start/stop
internal/tunnel     WireGuard apply / teardown
internal/telem      image hash, nvpmodel, thermals, uptime
internal/agent      enroll / heartbeat / tunnel / job loop
deploy/agx          systemd + first-boot
deploy/seeed-j4012  placeholder
```

## Out of scope here

Payment-service, Kafka, a real Path B suite, Seeed factory flash, public bot rental, fake tok/s, renter prompts in logs.
