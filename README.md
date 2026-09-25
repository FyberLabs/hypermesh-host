# Hypermesh host

Host software for certified Jetson boxes. We enroll **NVIDIA AGX Orin 64GB** and **Seeed Super J4012**. Product name is **Hypermesh** or **Hyperme.sh**. This repo is not the control plane and does not live in FyberLabs/panopticon.

This box enrolls, heartbeats, applies the allocated WireGuard session, and runs jobs against Panopticon `products/hypermesh` (path prefix `/api/v1/hypermesh`). It does not invent AIL, a subnet, Connect, public bot rental, or tokens/sec.

## Status

HTTP matches the locked agent shapes (Bearer after enroll, tunnel get/ack, lease_start / lease_stop / sell_pause). H4 adds a **plane-down owner** unix socket on this box (not CP enqueue). Catalog id `llama-3.1-8b-q4` is in-tree and uncertified. Empty or bare `image_digest` on lease_start POSTs `passed=false` (not a running instance; CP must not go active). `lease_start` also fails closed without a tunnel address, without a regular-file `HYPERMESH_GGUF_PATH`, or when a different lease is already running. Success `image_hash` is the GGUF sha256 (not the OCI pin). Result POST retries.

Default control plane: **`https://api.test.hyperme.sh`** (deployed test Panopticon). Unit tests compile against `httptest` and do not need that host.

## Build and test

Go 1.22+.

```bash
go test ./...
go build -ldflags "-X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Commit=$(git rev-parse HEAD)" -o hypermesh-agent ./cmd/agent
go install github.com/FyberLabs/hypermesh-host/cmd/hypermesh-deploy@latest
```

`build_commit` on the heartbeat prefers that ldflags value. If it is empty, this box uses the VCS revision from `debug.ReadBuildInfo` (`vcs.revision`). If that is empty too, the value is `unknown`.

`hypermesh-deploy agx` preflights the pinned AGX image, writes the existing agent env and systemd units, then starts `hypermesh-wg` and `hypermesh-agent`. It stops on the first failure. `-serial` is required. The device id comes from the plane device list for that serial. Catalog must be HTTP 200 at `/api/v1/hypermesh/catalog`. The enroll token is `HYPERMESH_ENROLL_TOKEN` or `-enroll-token-file`, not a command-line value. A rerun on a box that already has `device.json` does not need a fresh token.

Release archives are linux amd64 and arm64 (`.goreleaser.yaml`). The Homebrew formula lives in `packaging/homebrew` until a tap exists.

`hypermesh-supervisor` is one Linux image (`deploy/supervisor/Dockerfile`, Alpine runtime) and three placements (`HYPERMESH_SUPERVISOR_PLACEMENT`): `outside` (off the network), `host` (enrolls and heartbeats as host type `supervisor`, refuses `lease_start`), and `renter` (`POST /v1/chat/completions` on the chat gateway). Host placement does not certify the box. The renter API key and the enroll token come from the environment or a systemd credential. They are not rewritten into `supervisor.json` on start.

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
| Heartbeat | `POST /api/v1/hypermesh/agent/heartbeat` (includes `loaded_model_hash` = running lease `repo@digest` when a handle exists; omitempty otherwise). Optional `health` object; see Host health. Older planes ignore it. |
| Job pull | `GET /api/v1/hypermesh/agent/jobs?device_id=` |
| Result | `POST /api/v1/hypermesh/agent/jobs/{job_id}/result?device_id=` |
| Tunnel | `GET /api/v1/hypermesh/agent/tunnel?device_id=` |
| Tunnel peer | `POST /api/v1/hypermesh/agent/tunnel/peer` (host public key only; no `device_id`) |
| Tunnel ack | `POST /api/v1/hypermesh/agent/tunnel/ack?device_id=` |

401 is missing/wrong secret. 403 is secret does not match `device_id`.

If this box already has an id on disk but lost the secret, it cannot enroll again. Ask for a new enroll-token (`POST /api/v1/hypermesh/devices/{device_id}/enroll-token`, `X-Tenant-ID` only). That helper exists on the client; the loop on this box does not call it.

### Jobs

Pull is one job or 204. Body: `job_id`, `kind`, `device_id`, optional `lease_id`, `catalog_id`, `image_digest`, `runtime`, `backend`, `suite_id`, `until`.

| kind | What this box does |
|---|---|
| `path_b` | thin-v1: collect image hash / uptime / thermal. If this box rebooted since the last successful path_b, POST `passed=false` (host log reason: `reboot`). First run records boot identity and does not fail just for being first. If `http://127.0.0.1:8080/v1/chat/completions` is up, POST a pinned probe and require a real choice. If that endpoint is down, telem still passes and inference is recorded as skipped — but `passed=true` still requires a non-empty `image_hash` (telem/host, else the running lease `ImageDigest`). Empty telem + no lease → `passed=false` (host log reason: `no image_hash`). Do not invent a hash. |
| `instance_check` | thin-v1: must hit that completions endpoint and fail if there is no real `choices[0].message.content`. This is the container-answers gate. `passed=true` requires a non-empty `image_hash` the same way as `path_b`. |
| `lease_start` | `image_digest` must be `repo@digest`. Requires a tunnel `client_address` and `HYPERMESH_GGUF_PATH` pointing at a regular file (no cottage default; a missing path must not become a Docker directory). `docker pull` then `docker --runtime=nvidia -p <client_address>:8080:8080 --network hypermesh-workload --read-only --cap-drop ALL --name hypermesh-<lease_id>` and `-v $HYPERMESH_GGUF_PATH:/models/model.gguf:ro` (weights only; no Docker socket; not host network). The image entrypoint hardcodes `llama-server --alias hypermesh-test`, and llama.cpp CLI flags beat `LLAMA_ARG_*`, so this box overrides `--entrypoint /bin/sh`. That command fails if `/models/model.gguf` is missing or empty, then `exec llama-server --host 0.0.0.0 --port 8080 --alias <catalog_id>`. Container env: `LLAMA_ARG_JINJA` (default `1`), `LLAMA_ARG_CTX_SIZE` (default `16384`), `LLAMA_ARG_TIMEOUT` (default `1800`); override with `HYPERMESH_LLAMA_JINJA`, `HYPERMESH_LLAMA_CTX_SIZE`, `HYPERMESH_LLAMA_TIMEOUT`. No image rebuild. Empty `catalog_id` → `passed=false`. The agent creates `hypermesh-workload` as an `--internal` bridge if missing so the container has no LAN/public egress; publish stays on the WireGuard IP. Empty digest or bare `sha256:hex` → `passed=false`. A different local handle → `passed=false` (`lease already running`). Same `lease_id` already running is a remint: no second container, POST the stored GGUF hash. Success POSTs `image_hash` = sha256 of that GGUF (catalog `file_hash`), not the OCI pin. Empty after start → `passed=false` (do not invent). Host port is `HYPERMESH_LISTEN_PORT` (default 8080). Result POST retries. Empty/unknown `kind` → `passed=false`. |
| `lease_stop` | Stop only when `job.lease_id` matches the local handle. Mismatch (including empty `lease_id` with a handle) POSTs `passed=false` and does not `docker stop` / `docker rm`. After a matching stop, `docker rm` the named container (`hypermesh-<lease_id>`) so the next `lease_start` is not blocked on an Exited leftover. No handle is a soft no-op (`passed=true`) and may omit `image_hash`; prefer the handle digest or an existing host hash when available. An already-gone container is also a soft success. Do not invent a hash. Do not bounce WireGuard. The host peer stays up until GET says `revoked` or 204. |
| `sell_pause` | Persist local sell posture in `$STATE_DIR/sell.json`. Matching `device_id` sets `sell_state=paused` (optional `until` RFC3339; omit = indefinite). Mismatch POSTs `passed=false` and the current real `sell_state` (usually `selling`) without flipping. Already paused is a no-op `passed=true`. While paused, new `lease_start` is refused (`passed=false`); the current lease is left running. When `until` is reached, posture returns to `selling`. Not `path_b`. |

Certification (`path_b`) is also the catalog bench. On that job this box inspects a local workload image and, when the probe returns `usage.completion_tokens`, POSTs `/agent/scorecard` for `pack-v1`. The control plane tags the catalog row from that report. No usage field or no local `repo@sha256` means that row stays untagged. The prompt stays off the result body.

Optional `backend` on `lease_start`: `cuda-jetson` | `cuda-sm87` | `llama-cpp-cpu` | `llama-cpp-vulkan` | `hip-rocm`. Omitted = `cuda-jetson` (alias `cuda-sm87`). This box is AGX first: that path is `docker --runtime=nvidia`. `runtime` stays `nvidia`. On the default AGX / Jetson image, stubs and unknown (including `disk-io`, which is not in the enum) POST `passed=false` on `lease_start` and `path_b` (`This image does not run <backend>.`). `instance_check` on a stub does the same and does not probe.

A later **ROCm** host image (`go build -tags rocm`, not this AGX overlay) may take `hip-rocm`; see [deploy/rocm/README.md](deploy/rocm/README.md). A **darwin** binary may take `llama-cpp-cpu` as the Metal lab runner; Jetson Linux still stubs it. See [deploy/mac/README.md](deploy/mac/README.md). `llama-cpp-vulkan` stays a stub on every image. `llama-cpp-metal` is not in the enum (unknown → the same fail string). Do not set `HSA_OVERRIDE`. Do not invent gfx names. TOPS is not tok/s. Cert still means Fyber-published hashes. Path A/B stay Jetson-first.

Result body for `path_b` stays `{"passed", "image_hash?", "egress_ok?", "egress_reason?"}`. Lab soak results include a host-namespace egress observation (`egress_ok` + reason such as `overlay_publish_only`); missing tools fail closed with `egress_unobserved`. Result body for `instance_check` / `lease_start` / `lease_stop` stays `{"passed", "image_hash?"}`. `path_b` / `instance_check` / `lease_start` with `passed=true` must include a non-empty `image_hash` (the digest Panopticon stamps). Empty `image_hash` fails that job only. `sell_pause` POSTs `{"passed", "sell_state", "until?", "device_id?"}`. `sell_state` is required on that result (`selling` \| `paused` \| `draining` \| `off` \| `n/a`). This box never POSTs `passed=true` with `sell_state=n/a`. Heartbeat includes current `sell_state` (default after the actuator exists: `selling`) and `loaded_model_hash` when a lease is running. Path B (`path_b_state`) is not sell posture.

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

Allocated: this box generates the host WireGuard private key (`/etc/wireguard/hypermesh.key`, mode 0600, directory 0700, root-owned when the unit runs as root) and reuses that file across restart and redeploy. It replaces the file only when it is absent or not a valid X25519 key. On boot, and whenever the local public key does not match an allocated session, it POSTs only `{"peer_public_key":"..."}` to `/agent/tunnel/peer` (no `device_id`; Bearer `hm_dev_` plus `X-Tenant-ID`) and then GETs. POST 404 `Tunnel not found` still saved the device key: success with no tunnel. POST 409 `Tunnel is revoked` is logged and WireGuard stays down. GET 204 is no tunnel. GET 200 `status=allocated` with `peer_public_key` null or different, or GET 409 whose body `code` is `peer_public_key_not_registered` (`detail` is not the match), means POST again and re-GET. GET 200 `status=revoked` tears WireGuard down and does not POST again, even when `peer_public_key` is null or different; that line is logged once until a later allocated session is applied. The live wg-quick config stays on the previous key until a GET shows the new public key; then this box switches and acks again (`ack_applied` is not cleared on rotation). GET never carries `peer_private_key`. This box writes a 0600 wg-quick config from the local private key plus GET `endpoint`, `server_public_key`, `allowed_ips`, and `client_address`, `wg-quick up`, then POST ack `{"applied":true}`. Apply the fields it is given (Fyber router; `wg.test.hyperme.sh:51820`). `wg-quick up` runs only when those peer fields change. A restart that sees the same fingerprint does not bounce the iface. The private key is never logged and never sent to the plane. Do not invent a lab peer.

Revoked (host peer revoked) or 204 (no host session): `wg-quick down` and shred `/etc/wireguard/hypermesh.conf` so the peer key does not stay on disk. Do not teardown because `lease_id` changed. `lease_start` / `lease_stop` do not `wg-quick` down/up.

**Userspace (no kernel module).** AGX64-1 / L4T R39 has no wireguard module. [deploy/agx/hypermesh-wg.service](deploy/agx/hypermesh-wg.service) runs `/usr/bin/wireguard-go -f hypermesh` outside the agent cgroup (`Restart=on-failure`). Set `HYPERMESH_WG_USERSPACE=1` (or leave `auto`, which picks userspace when the module is missing). The agent never spawns `wireguard-go` and does not use `KillMode=process` or `KillMode=mixed`. It programs the existing interface with `wg syncconf`, `ip addr` / `ip link`, and `ip route replace` for each AllowedIPs prefix except `0.0.0.0/0` and `::/0` (those would replace the LAN default route; they are installed only when a full tunnel is explicitly configured). Stale AllowedIPs prefixes are deleted. `proto kernel` routes and the IPv6 link-local `fe80::/64` are not. Route adds and deletes are logged at info with `tunnel_id` and `lease_id`. An unchanged restart logs `host peer unchanged (skip apply)`; a real `wg syncconf` logs `host peer applied (syncconf)`. `PersistentKeepalive = 25` stays. `wg show dump` reports a resolved endpoint, re-encoded keys, and AllowedIPs without spaces; that still counts as the same peer. A restart with those fields unchanged does not call `wg syncconf` and does not rebind the listen port (a real change pins `ListenPort` to the current port, because an omitted port is sent as `listen_port=0` and `wireguard-go` binds a new one). A failed plane sync (401/503) also leaves a handshaking interface up. Revoke (GET 200 `status=revoked` or POST 409 `Tunnel is revoked`) and 204 remove peers, routes, and the address, and set the link down; the unit stays up and the tunnel does not return until a new allocated session is applied. Kernel-module hosts keep `wg-quick` (`HYPERMESH_WG_USERSPACE=0` or `auto` when the module loads). Install and the AGX64-1 migration are in [deploy/agx/first-boot.md](deploy/agx/first-boot.md).

The container is reachable at the WG `client_address` on port 8080. The router is the only customer HTTPS path (`http://<client_address>:8080/v1/chat/completions`).

### Host health

Heartbeat may include a top-level `health` object. Existing fields are unchanged. This box still heartbeats when health collection fails; unknown fields are omitted. `last_sync_error_class` is `""` when there is no class. `last_sync_http_status` is `0` when there is no status. Nothing secret is added: no WireGuard keys, no tokens, no device secret, no raw HTTP body, no renter id, no prompt. Lease containers are a count.

`tunnel_state` is `up` (handshake within 180s), `handshaking` (peer configured, or userspace process up, without a recent handshake), `down` (no peer, or kernel interface absent), or `disabled` (userspace mode, no `wireguard-go` process, no interface). `last_sync_at` is RFC3339 UTC of the last successful tunnel sync. `last_sync_error_class` is `http_5xx`, `http_4xx`, `network`, `timeout`, `decode`, or empty (local failures such as `wg syncconf` stay empty; the sync failure counter still increments). `handshake_age_s` is seconds since the latest handshake. `wg_pid` is the `wireguard-go` pid (omitted on the kernel path). `wg_port` is the listen port. `lease_container_count` is running containers whose names start with `hypermesh-`. `build_commit` is the ldflags commit, else VCS revision, else `unknown`.

`GET /metrics` on this process exposes the same signals as Prometheus text gauges and counters (`hypermesh_heartbeat_success_total`, `hypermesh_heartbeat_failure_total{class=...}`, `hypermesh_tunnel_sync_success_total`, `hypermesh_tunnel_sync_failure_total{class=...}`, `hypermesh_tunnel_state{state=...}`, `hypermesh_handshake_age_seconds`, `hypermesh_wg_pid`, `hypermesh_wg_port`, `hypermesh_lease_container_count`, `hypermesh_last_sync_timestamp_seconds`, `hypermesh_last_sync_http_status`, `hypermesh_last_sync_error_class{class=...}`, `hypermesh_build_info{commit=...}`). There is no scrape path on the tunnel.

The listener defaults to `127.0.0.1:9464` (`HYPERMESH_METRICS_ADDR`, flag `-metrics-addr`). A non-loopback address, including `0.0.0.0`, `::`, a hostname, or the WireGuard address, is refused and nothing is bound. Set `HYPERMESH_METRICS_ALLOW_NON_LOOPBACK=1` (flag `-metrics-allow-non-loopback`) to allow it; this box then logs a warning that `/metrics` is reachable beyond localhost. A refused or failed listen does not stop heartbeats.

### Workload jail

Every hosted artifact container (lab soak or renter `lease_start`) runs on the internal Docker network `hypermesh-workload` (created `--internal` if missing). Publish is only `-p <client_address>:8080:8080` — not the default bridge with LAN egress, not `--network=host`. Caps are dropped (`--cap-drop ALL`), rootfs is read-only (`--read-only` + `/tmp` tmpfs), and the only host bind is the GGUF `:ro`. No Docker socket. No host devices for sensors/cameras/robots (GPU stays via `--runtime=nvidia` or HIP kfd/dri). Agent and witness stay on host network and talk to the workload over the overlay address, same as the door.

## Enroll and update

Tenant id and token from [host enroll](https://portal.test.hyperme.sh/dashboard/hypermesh/host/enroll). Full steps for both boxes: [host-enroll.md](https://github.com/FyberLabs/hypermesh-docs/blob/main/host-enroll.md). Public page: https://hyperme.sh/hosts.

### NVIDIA AGX Orin 64GB

Module **P3701-0005**, board `jetson-agx-orin-devkit`. **JetPack 7.2.1 / L4T R39.2.1**. If the box is already there, skip the flash.

Flash and install: [deploy/agx/first-boot.md](deploy/agx/first-boot.md). Units: [deploy/agx/hypermesh-agent.service](deploy/agx/hypermesh-agent.service), [deploy/agx/hypermesh-wg.service](deploy/agx/hypermesh-wg.service) (userspace WireGuard; no kernel module on this image). Workload: [deploy/agx/workload/README.md](deploy/agx/workload/README.md). Optional witness sidecar: [deploy/agx/aiuditor-witness.service](deploy/agx/aiuditor-witness.service).

AGX 64GB nvpmodel default is **30W (id 2)**. GPU path is `--runtime=nvidia`.

### Seeed Super J4012

Orin NX 16GB. Seeed Super mfi — **JP6.2 / 36.4.3** or **JP7.2.0 / 39.2.0**, pick one. Not the AGX 7.2.1 image.

Flash and install: [deploy/seeed-j4012/first-boot.md](deploy/seeed-j4012/first-boot.md). Super flash notes: [seeed-host-process.md](https://github.com/FyberLabs/hypermesh-docs/blob/main/seeed-host-process.md). Workload: [deploy/seeed-j4012/workload/README.md](deploy/seeed-j4012/workload/README.md) (build on the J4012).

nvpmodel is the NX Super table (`MAXN_SUPER`). Confirm ids on that image.

### Update

```bash
cd hypermesh-host
git pull
go build -o hypermesh-agent ./cmd/agent
sudo install -m 0755 hypermesh-agent /usr/local/bin/hypermesh-agent
sudo systemctl restart hypermesh-agent
```

Leave `/etc/hypermesh/agent.env` and `/var/lib/hypermesh/device.json` alone.

## Layout

```
cmd/agent             host process (hypermesh-agent)
cmd/hypermesh-deploy  AGX preflight, configure, connect
cmd/hypermesh-supervisor  supervisor (outside, host, renter)
internal/api        locked HTTP client (agent routes + optional enroll-token helper)
internal/pathb      thin-v1 (telem + optional local completions; path_b fails on reboot)
internal/runtime    NVIDIA Container Runtime start/stop; HIP / Metal scaffolds (gated)
internal/tunnel     WireGuard apply / teardown
internal/telem      image hash, nvpmodel, thermals, uptime
internal/metrics    loopback Prometheus text /metrics
internal/buildinfo  build commit (ldflags, else VCS revision)
internal/agent      enroll / heartbeat / tunnel / job loop + owner unix socket
internal/supervisor       supervisor placements (outside, host, renter)
deploy/agx          systemd + first-boot + on-box workload + optional AIuditor witness unit
deploy/supervisor         supervisor image (Alpine runtime; placements outside / host / renter)
deploy/seeed-j4012  Super J4012 first-boot and overlay
deploy/rocm         AMD ROCm host-image notes (scaffold)
deploy/mac          Apple Silicon Metal notes (lab; llama-cpp-cpu on darwin)
```

## Out of scope here

Payment-service, Kafka, a full host-scorecard, Seeed factory flash, public bot rental, fake tok/s, renter prompts in logs. AIuditor witness is a sidecar (`deploy/agx/aiuditor-witness.service`), not this binary. Owner console does not bind loopback/LAN/public TCP, does not enqueue CP jobs, does not add `sell_resume`, a drain mega-job, `reason_code`, `preempt_mode=hard`, Path B, SociACL, WebAuthn, OIDC, or privilege_grant.
