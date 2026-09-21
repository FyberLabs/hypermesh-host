# Hypermesh host

Host software for certified Jetson boxes. First machine: NVIDIA Jetson AGX Orin 64GB. Product name is **Hypermesh** or **Hyperme.sh**. This repo is not the control plane and does not live in FyberLabs/panopticon.

This box enrolls, heartbeats, applies the allocated WireGuard session, and runs jobs against Panopticon `products/hypermesh` (path prefix `/api/v1/hypermesh`). It does not invent AIL, a subnet, Connect, public bot rental, or tokens/sec.

## Status

HTTP matches the locked agent shapes (Bearer after enroll, tunnel get/ack, lease_start / lease_stop / sell_pause). H4 adds a **plane-down owner** unix socket on this box (not CP enqueue). Catalog id `llama-3.1-8b-q4` is in-tree and uncertified. Empty or bare `image_digest` on lease_start POSTs `passed=false` (not a running instance; CP must not go active). `lease_start` also fails closed without a tunnel address, without a regular-file `HYPERMESH_GGUF_PATH`, or when a different lease is already running. Success `image_hash` is the GGUF sha256 (not the OCI pin). Result POST retries.

Default control plane: **`https://api.test.hyperme.sh`** (deployed test Panopticon). Unit tests compile against `httptest` and do not need that host.

## Build and test

Go 1.22+.

```bash
go test ./...
go build -o hypermesh-agent ./cmd/agent
```

Default `go test ./...` / `go build` is the AGX image: `hip-rocm` and `llama-cpp-vulkan` (and `llama-cpp-cpu` on Linux) stay exact stubs. ROCm / darwin scaffolds are build-tagged; see `deploy/rocm/` and `deploy/mac/`.

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
| Heartbeat | `POST /api/v1/hypermesh/agent/heartbeat` (includes `loaded_model_hash` = running lease `repo@digest` when a handle exists; omitempty otherwise) |
| Job pull | `GET /api/v1/hypermesh/agent/jobs?device_id=` |
| Result | `POST /api/v1/hypermesh/agent/jobs/{job_id}/result?device_id=` |
| Tunnel | `GET /api/v1/hypermesh/agent/tunnel?device_id=` |
| Tunnel peer | `POST /api/v1/hypermesh/agent/tunnel/peer?device_id=` (host public key only) |
| Tunnel ack | `POST /api/v1/hypermesh/agent/tunnel/ack?device_id=` |

401 is missing/wrong secret. 403 is secret does not match `device_id`.

If this box already has an id on disk but lost the secret, it cannot enroll again. Ask for a new enroll-token (`POST /api/v1/hypermesh/devices/{device_id}/enroll-token`, `X-Tenant-ID` only). That helper exists on the client; the loop on this box does not call it.

### Jobs

Pull is one job or 204. Body: `job_id`, `kind`, `device_id`, optional `lease_id`, `catalog_id`, `image_digest`, `runtime`, `backend`, `suite_id`, `until`.

| kind | What this box does |
|---|---|
| `path_b` | thin-v1: collect image hash / uptime / thermal. If this box rebooted since the last successful path_b, POST `passed=false` (host log reason: `reboot`). First run records boot identity and does not fail just for being first. If `http://127.0.0.1:8080/v1/chat/completions` is up, POST a pinned probe and require a real choice. If that endpoint is down, telem still passes and inference is recorded as skipped — but `passed=true` still requires a non-empty `image_hash` (telem/host, else the running lease `ImageDigest`). Empty telem + no lease → `passed=false` (host log reason: `no image_hash`). Do not invent a hash. |
| `instance_check` | thin-v1: must hit that completions endpoint and fail if there is no real `choices[0].message.content`. This is the container-answers gate. `passed=true` requires a non-empty `image_hash` the same way as `path_b`. |
| `lease_start` | `image_digest` must be `repo@digest`. Requires a tunnel `client_address` and `HYPERMESH_GGUF_PATH` pointing at a regular file (no cottage default; a missing path must not become a Docker directory). `docker pull` then `docker --runtime=nvidia -p <client_address>:8080:8080 --name hypermesh-<lease_id>` and `-v $HYPERMESH_GGUF_PATH:/models/model.gguf:ro`. Empty digest or bare `sha256:hex` → `passed=false`. A different local handle → `passed=false` (`lease already running`). Same `lease_id` already running is a remint: no second container, POST the stored GGUF hash. Success POSTs `image_hash` = sha256 of that GGUF (catalog `file_hash`), not the OCI pin. Empty after start → `passed=false` (do not invent). Host port is `HYPERMESH_LISTEN_PORT` (default 8080). Result POST retries. Empty/unknown `kind` → `passed=false`. |
| `lease_stop` | Stop only when `job.lease_id` matches the local handle. Mismatch (including empty `lease_id` with a handle) POSTs `passed=false` and does not `docker stop` / `docker rm`. After a matching stop, `docker rm` the named container (`hypermesh-<lease_id>`) so the next `lease_start` is not blocked on an Exited leftover. No handle is a soft no-op (`passed=true`) and may omit `image_hash`; prefer the handle digest or an existing host hash when available. An already-gone container is also a soft success. Do not invent a hash. Do not bounce WireGuard. The host peer stays up until GET says `revoked` or 204. |
| `sell_pause` | Persist local sell posture in `$STATE_DIR/sell.json`. Matching `device_id` sets `sell_state=paused` (optional `until` RFC3339; omit = indefinite). Mismatch POSTs `passed=false` and the current real `sell_state` (usually `selling`) without flipping. Already paused is a no-op `passed=true`. While paused, new `lease_start` is refused (`passed=false`); the current lease is left running. When `until` is reached, posture returns to `selling`. Not `path_b`. |

Optional `backend` on `lease_start`: `cuda-jetson` | `cuda-sm87` | `llama-cpp-cpu` | `llama-cpp-vulkan` | `hip-rocm`. Omitted = `cuda-jetson` (alias `cuda-sm87`). This box is AGX first: that path is `docker --runtime=nvidia`. `runtime` stays `nvidia`. On the default AGX / Jetson image, stubs and unknown (including `disk-io`, which is not in the enum) POST `passed=false` on `lease_start` and `path_b` (`This image does not run <backend>.`). `instance_check` on a stub does the same and does not probe.

A later **ROCm** host image (`go build -tags rocm`, not this AGX overlay) may take `hip-rocm`; see [deploy/rocm/README.md](deploy/rocm/README.md). A **darwin** binary may take `llama-cpp-cpu` as the Metal lab runner; Jetson Linux still stubs it. See [deploy/mac/README.md](deploy/mac/README.md). `llama-cpp-vulkan` stays a stub on every image. `llama-cpp-metal` is not in the enum (unknown → the same fail string). Do not set `HSA_OVERRIDE`. Do not invent gfx names. TOPS is not tok/s. Cert still means Fyber-published hashes. Path A/B stay Jetson-first.

Result body for `path_b` / `instance_check` / `lease_start` / `lease_stop` stays `{"passed", "image_hash?"}`. `path_b` / `instance_check` / `lease_start` with `passed=true` must include a non-empty `image_hash` (the digest Panopticon stamps). Empty `image_hash` fails that job only. `sell_pause` POSTs `{"passed", "sell_state", "until?", "device_id?"}`. `sell_state` is required on that result (`selling` \| `paused` \| `draining` \| `off` \| `n/a`). This box never POSTs `passed=true` with `sell_state=n/a`. Heartbeat includes current `sell_state` (default after the actuator exists: `selling`) and `loaded_model_hash` when a lease is running. Path B (`path_b_state`) is not sell posture.

The probe and the completion text are never logged and never placed in the result.

### Owner console (plane-down)

H4 v0. Local owner actuators when the control plane is unreachable. **Not** CP job enqueue, **not** Path B, **not** SociACL / WebAuthn / OIDC / privilege_grant.

Auth lock (Chris via First Principles, 2026-09-07): unix socket `$STATE_DIR/owner.sock` (not loopback TCP) + bearer file `$STATE_DIR/owner.token` mode **0600**.

On agent start (after enroll / stored identity): create `owner.token` if missing (32 random bytes, hex, 0600), remove a stale `owner.sock`, listen HTTP on that unix socket (socket file 0600). Every route requires `Authorization: Bearer <token>` matching the file; otherwise **401**.

| Call | Path | Body | Response |
|---|---|---|---|
| Status | `GET /v0/status` | none | JSON only: `{device_id, sell_state, until?, lease, path_b, backend}`. `lease` is `null` or `{lease_id, catalog_id?, image_digest?}` from the local handle. `path_b` is host facts (`boot_id_present`, `boot_id?`, `completions_up`). This box logs CP `path_b_state` on heartbeat and does not persist it — status does not invent that field. `backend` is the log label `cuda-jetson`. No `device_secret`, no WireGuard keys. Does not append `owner-effects.jsonl`. |
| Console | `GET /` and `GET /v0/console` | none | One HTML page on the same unix socket. Same Bearer. Embeds the current status JSON; the page fetches `/v0/status` and buttons call `sell_pause` / `lease_stop` (`lease_id` from status). |
| Pause sell | `POST /v0/sell_pause` | optional `{until?, device_id?}`. Device is this box. `until` is RFC3339. If `device_id` is present it must match the enrolled id. | `{passed, sell_state, until?, device_id?}`. Never `passed=true` with `sell_state=n/a`. Same `sellPause` actuator as the CP job. |
| Stop lease | `POST /v0/lease_stop` | `{lease_id}` required | `{passed, image_hash?}`. Same H1 identity as the CP `lease_stop` job. |

After each authorized call attempt (pass or fail), this box appends one JSON line to `$STATE_DIR/owner-effects.jsonl`: `ts` (RFC3339), `tool` (`hypermesh.sell_pause` or `hypermesh.lease_stop`), `args`, `passed`, and the relevant fields (`sell_state` / `until` / `lease_id`). Plane-down receipts for Brewnix — not a `fyber.receipt` schema.

CLI (talks to the socket + token file; does not contact the control plane):

```bash
hypermesh-agent owner status
hypermesh-agent owner console
hypermesh-agent owner sell-pause [--until=RFC3339]
hypermesh-agent owner lease-stop --lease-id=<id>
```

`status` prints `GET /v0/status`. `console` prints unix-socket instructions (Bearer via curl / CLI bridge; this box does not bind TCP). `-state-dir` / `HYPERMESH_STATE_DIR` must point at the running agent's state dir.

Equivalent curl:

```bash
curl --unix-socket "$STATE_DIR/owner.sock" \
  -H "Authorization: Bearer $(cat "$STATE_DIR/owner.token")" \
  http://localhost/v0/status

curl --unix-socket "$STATE_DIR/owner.sock" \
  -H "Authorization: Bearer $(cat "$STATE_DIR/owner.token")" \
  http://localhost/v0/console

curl --unix-socket "$STATE_DIR/owner.sock" \
  -H "Authorization: Bearer $(cat "$STATE_DIR/owner.token")" \
  -H "Content-Type: application/json" \
  -d '{"until":"2026-09-08T00:00:00Z"}' \
  http://localhost/v0/sell_pause
```

### Tunnel

Control plane allocates on paid→starting. GET 200 is the current session or 204 none. No `listen_port` on the GET.

One WireGuard peer per **host**, not per lease. `lease_id` / `tunnel_id` on the GET are tickets on that same peer.

Allocated: this box generates the host WireGuard private key (`/etc/wireguard/hypermesh.key`, 0600) and POSTs only the public key to `/agent/tunnel/peer`. GET never carries `peer_private_key`. This box writes a 0600 wg-quick config from the local private key plus GET `endpoint`, `server_public_key`, `allowed_ips`, and `client_address`, `wg-quick up`, then POST ack `{"applied":true}`. Apply the fields it is given (Fyber router; `wg.test.hyperme.sh:51820`). `wg-quick up` runs only when those peer fields change. A restart that sees the same fingerprint does not bounce the iface. The private key is never logged and never sent to the plane. Do not invent a lab peer.

Revoked (host peer revoked) or 204 (no host session): `wg-quick down` and shred `/etc/wireguard/hypermesh.conf` so the peer key does not stay on disk. Do not teardown because `lease_id` changed. `lease_start` / `lease_stop` do not `wg-quick` down/up.

The container is reachable at the WG `client_address` on port 8080. The router is the only customer HTTPS path (`http://<client_address>:8080/v1/chat/completions`).

## Install architecture

### Path A — AGX Orin (do now)

Module **P3701-0005**, board `jetson-agx-orin-devkit`. Overlay APP only.

This slice assumes **JetPack 7.2.1 / L4T R39.2.1** (Ubuntu 24.04.5 sample rootfs, kernel 6.8, CUDA 13). AGX64-1 stays on this pin.

- If the box is already 7.2.1 / R39.2.1: overlay only.
- If not: flash `jetson-agx-orin-devkit` to 7.2.1 / 39.2.1, then overlay. Chris reviews.
- Do not keep or dual-pin a JetPack 6.2.3 / L4T 36.5.2 image for this box.
- If a leftover JP6 OCI image will not run: stop. Do not silently run both.

On the flash host: `l4t_create_default_user.sh`. On the device: `multi-user.target`. Do not purge `gdm3` or `nvidia-l4t-3d-core`. Do not overwrite `/etc/nv_boot_control.conf`.

Then: systemd unit + this program + Docker from get.docker.com + JetPack `nvidia-container` + `nvidia-ctk runtime configure --runtime=docker` + Docker `default-runtime` nvidia + `wireguard-tools`. GPU path is `--runtime=nvidia`, never x86 `--gpus`.

AGX 64GB nvpmodel default is **30W (id 2)**. Report nvpmodel. Do not default MAXN + `jetson_clocks`.

Details: [deploy/agx/first-boot.md](deploy/agx/first-boot.md). Unit: [deploy/agx/hypermesh-agent.service](deploy/agx/hypermesh-agent.service). On-box workload image: [deploy/agx/workload/README.md](deploy/agx/workload/README.md) (`ghcr.io/fyberlabs/hypermesh-test`, built on the AGX, pin sha256 after build; GGUF is volume-mounted at run, not COPY’d).

### Path B — Seeed Super J4012 (first marketed NX class)

Orin NX 16GB, Seeed Super J4012. **Does not block AGX cottage.** Separate image, Seeed mfi massflash, NX Super nvpmodel (`MAXN_SUPER`). Do not mix AGX flash XML or the AGX nvpmodel table.

Seeed pin is **one** of: mfi **JP6.2 / L4T 36.4.3** or **JP7.2.0 / L4T 39.2.0**. Not NVIDIA / AGX Path A **7.2.1 / 39.2.1**. Do not silently dual-pin.

Prep path (docs + overlay stubs, not factory automation): [deploy/seeed-j4012/README.md](deploy/seeed-j4012/README.md). Flash outline: [deploy/seeed-j4012/first-boot.md](deploy/seeed-j4012/first-boot.md). On-box workload: [deploy/seeed-j4012/workload/README.md](deploy/seeed-j4012/workload/README.md) (build on the J4012; target name `ghcr.io/fyberlabs/hypermesh-test`; Fyber publishes the digest later; do not GHCR pull; empty `image_digest` fails `lease_start` only). Suite stays `thin-v1`. Path B `passed=true` needs a non-empty `image_hash`.

## Layout

```
cmd/agent           process
internal/api        locked HTTP client (agent routes + optional enroll-token helper)
internal/pathb      thin-v1 (telem + optional local completions; path_b fails on reboot)
internal/runtime    NVIDIA Container Runtime start/stop; HIP / Metal scaffolds (gated)
internal/tunnel     WireGuard apply / teardown
internal/telem      image hash, nvpmodel, thermals, uptime
internal/agent      enroll / heartbeat / tunnel / job loop + owner unix socket
deploy/agx          systemd + first-boot + on-box workload (Path A, cottage now)
deploy/seeed-j4012  Path B prep (docs + overlay stubs; does not block AGX)
deploy/rocm         AMD ROCm host-image notes (scaffold; not Path A/B)
deploy/mac          Apple Silicon Metal notes (lab; llama-cpp-cpu on darwin)
```

## Out of scope here

Payment-service, Kafka, a full host-scorecard, Seeed factory flash, public bot rental, fake tok/s, renter prompts in logs. Owner console does not bind loopback/LAN/public TCP, does not enqueue CP jobs, does not add `sell_resume`, a drain mega-job, `reason_code`, `preempt_mode=hard`, Path B, SociACL, WebAuthn, OIDC, or privilege_grant.
