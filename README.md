# Hypermesh host

Host software for certified Jetson boxes. First machine: NVIDIA Jetson AGX Orin 64GB. Product name is **Hypermesh** or **Hyperme.sh**. This repo is not the control plane and does not live in FyberLabs/panopticon.

This box enrolls, heartbeats, applies the allocated WireGuard session, and runs jobs against Panopticon `products/hypermesh` (path prefix `/api/v1/hypermesh`). It does not invent AIL, a subnet, Connect, public bot rental, or tokens/sec.

## Status

HTTP matches the locked agent shapes (Bearer after enroll, tunnel get/ack, lease_start / lease_stop). Catalog id `llama-3.1-8b-q4` is in-tree and uncertified. Empty or bare `image_digest` on lease_start POSTs `passed=false` (not a running instance; CP must not go active).

Default / example control plane: **`https://api.test.hyperme.sh`**. Not live yet. Tests compile against `httptest`.

## Build and test

Go 1.22+.

```bash
go test ./...
go build -o hypermesh-agent ./cmd/agent
```

Tests use `net/http/httptest` fixtures of the locked shapes. They do not need a live Panopticon.

## Run against the test control plane

On the box (after overlay):

```bash
export HYPERMESH_CONTROL_PLANE_URL=https://api.test.hyperme.sh
export HYPERMESH_TENANT_ID=<marketplace-tenant-uuid>
export HYPERMESH_ENROLL_TOKEN=<one-time-token>
export HYPERMESH_STATE_DIR=./state

./hypermesh-agent
```

Same values as flags: `-control-plane-url`, `-tenant-id`, `-enroll-token`, `-state-dir`, `-listen-port`, `-completions-url`.

Enroll (`POST /api/v1/hypermesh/agent/enroll`) is unauthenticated except `X-Tenant-ID`. The 200 body is `device_id`, `credential_prefix`, and `device_secret` (`hm_dev_…`, shown once). This box writes those to `$STATE_DIR/device.json` at mode **0600**. The enroll token is not stored. The secret is never logged and must not be copied into unit command lines or the journal.

After enroll, these routes send `Authorization: Bearer <device_secret>`, `X-Tenant-ID`, and `device_id` in the body or query:

| Call | Path |
|---|---|
| Heartbeat | `POST /api/v1/hypermesh/agent/heartbeat` |
| Job pull | `GET /api/v1/hypermesh/agent/jobs?device_id=` |
| Result | `POST /api/v1/hypermesh/agent/jobs/{job_id}/result?device_id=` |
| Tunnel | `GET /api/v1/hypermesh/agent/tunnel?device_id=` |
| Tunnel ack | `POST /api/v1/hypermesh/agent/tunnel/ack?device_id=` |

401 is missing/wrong secret. 403 is secret does not match `device_id`.

If this box already has an id on disk but lost the secret, it cannot enroll again. Ask for a new enroll-token (`POST /api/v1/hypermesh/devices/{device_id}/enroll-token`, `X-Tenant-ID` only). That helper exists on the client; the loop on this box does not call it.

### Jobs

Pull is one job or 204. Body: `job_id`, `kind`, `device_id`, optional `lease_id`, `catalog_id`, `image_digest`, `runtime`, `backend`, `suite_id`.

| kind | What this box does |
|---|---|
| `path_b` | thin-v1: collect image hash / uptime / thermal. If this box rebooted since the last successful path_b, POST `passed=false` (host log reason: `reboot`). First run records boot identity and does not fail just for being first. If `http://127.0.0.1:8080/v1/chat/completions` is up, POST a pinned probe and require a real choice. If that endpoint is down, telem still passes and inference is recorded as skipped. |
| `instance_check` | thin-v1: must hit that completions endpoint and fail if there is no real `choices[0].message.content`. This is the container-answers gate. |
| `lease_start` | `image_digest` must be `repo@digest`. `docker pull` then `docker --runtime=nvidia -p 8080:8080 --name hypermesh-<lease_id>`. Empty digest or bare `sha256:hex` → `passed=false`. Host port is `HYPERMESH_LISTEN_PORT` (default 8080). Empty/unknown `kind` → `passed=false`. |
| `lease_stop` | Stop only when `job.lease_id` matches the local handle. Mismatch (including empty `lease_id` with a handle) POSTs `passed=false` and does not `docker stop`. No handle is a soft no-op (`passed=true`). Do not bounce WireGuard. The host peer stays up until GET says `revoked` or 204. |

Optional `backend` on `lease_start`: `cuda-jetson` | `cuda-sm87` | `llama-cpp-cpu` | `llama-cpp-vulkan` | `hip-rocm`. Omitted = `cuda-jetson` (alias `cuda-sm87`). This box is AGX first: that path is `docker --runtime=nvidia`. `runtime` stays `nvidia`. Stubs and unknown (including `disk-io`, which is not in the enum) POST `passed=false` on `lease_start` and `path_b` (`This image does not run <backend>.`) until a later image. `instance_check` on a stub does the same and does not probe.

Result body stays `{"passed", "image_hash?"}`. The probe and the completion text are never logged and never placed in the result.

### Tunnel

Control plane allocates on paid→starting. GET 200 is the current session or 204 none. No `listen_port` on the GET.

One WireGuard peer per **host**, not per lease. `lease_id` / `tunnel_id` on the GET are tickets on that same peer.

Allocated: `peer_private_key` is only on that GET. This box writes a 0600 wg-quick config, `wg-quick up`, then POST ack `{"applied":true}`. Apply the fields it is given (Fyber router; planned `wg.test.hyperme.sh:51820`). `wg-quick up` runs only when `endpoint`, `server_public_key`, `peer_private_key`, `client_address`, or `allowed_ips` change. A restart that sees the same fingerprint does not bounce the iface. The key is never logged. Do not invent a lab peer.

Revoked (host peer revoked) or 204 (no host session): `wg-quick down` and shred `/etc/wireguard/hypermesh.conf` so the peer key does not stay on disk. Do not teardown because `lease_id` changed. `lease_start` / `lease_stop` do not `wg-quick` down/up.

The container is reachable at the WG `client_address` on port 8080. The router is the only customer HTTPS path (`http://<client_address>:8080/v1/chat/completions`).

## Install architecture

### Path A — AGX Orin (do now)

Module **P3701-0005**, board `jetson-agx-orin-devkit`. Overlay APP only.

This slice assumes **JetPack 6.2.3 / L4T 36.5.2** (Ubuntu 22.04 sample rootfs, kernel 5.15, CUDA 12).

- If the box is already 6.2.3: overlay only.
- If not: flash `jetson-agx-orin-devkit` to 6.2.3 / 36.5.2, then overlay. Chris reviews.
- JetPack 7.2.1 / L4T r39.2.1 is a later pin, when JP7 images exist. Do not pull a CUDA 13 image on this box.
- If the box is later found on 7.x and the JP6 OCI image will not run: stop. Do not silently run both.

On the flash host: `l4t_create_default_user.sh`. On the device: `multi-user.target`. Do not purge `gdm3` or `nvidia-l4t-3d-core`. Do not overwrite `/etc/nv_boot_control.conf`.

Then: systemd unit + this program + Docker from get.docker.com + JetPack `nvidia-container` + `nvidia-ctk runtime configure --runtime=docker` + Docker `default-runtime` nvidia + `wireguard-tools`. GPU path is `--runtime=nvidia`, never x86 `--gpus`.

AGX 64GB nvpmodel default is **30W (id 2)**. Report nvpmodel. Do not default MAXN + `jetson_clocks`.

Details: [deploy/agx/first-boot.md](deploy/agx/first-boot.md). Unit: [deploy/agx/hypermesh-agent.service](deploy/agx/hypermesh-agent.service). On-box workload image: [deploy/agx/workload/README.md](deploy/agx/workload/README.md) (`ghcr.io/fyberlabs/hypermesh-test`, built on the AGX, pin sha256 after build).

### Path B — Seeed Super J4012 (later)

Orin NX 16GB, not AGX. Separate image, different nvpmodel table (`MAXN_SUPER`), factory via Seeed massflash. Do not mix flash XML. Placeholder: [deploy/seeed-j4012/README.md](deploy/seeed-j4012/README.md).

## Layout

```
cmd/agent           process
internal/api        locked HTTP client (agent routes + optional enroll-token helper)
internal/pathb      thin-v1 (telem + optional local completions; path_b fails on reboot)
internal/runtime    NVIDIA Container Runtime start/stop
internal/tunnel     WireGuard apply / teardown
internal/telem      image hash, nvpmodel, thermals, uptime
internal/agent      enroll / heartbeat / tunnel / job loop
deploy/agx          systemd + first-boot + on-box workload
deploy/seeed-j4012  placeholder
```

## Out of scope here

Payment-service, Kafka, a full host-scorecard, Seeed factory flash, public bot rental, fake tok/s, renter prompts in logs.
