# Hypermesh host

Host software for certified Jetson boxes. We enroll **NVIDIA AGX Orin 64GB** and **Seeed Super J4012**. Product name is **Hypermesh** or **Hyperme.sh**. This repo is not the control plane and does not live in FyberLabs/panopticon.

This box enrolls, heartbeats, applies the allocated WireGuard session, and runs jobs against Panopticon `products/hypermesh` (path prefix `/api/v1/hypermesh`). It does not invent AIL, a subnet, Connect, public bot rental, or tokens/sec.

## Status

HTTP matches the fixed agent API shapes (Bearer after enroll, tunnel get/ack, lease_start / lease_stop / sell_pause). H4 adds a **plane-down owner** unix socket on this box (not CP enqueue). Catalog id `llama-3.1-8b-q4` is in-tree and uncertified. Empty or bare `image_digest` on lease_start POSTs `passed=false` (not a running instance; CP must not go active). `lease_start` also fails closed without a tunnel address, without a regular-file `HYPERMESH_GGUF_PATH`, or when a different lease is already running. Success `image_hash` is the GGUF sha256 (not the OCI pin). Result POST retries.

Default control plane: **`https://api.test.hyperme.sh`** (deployed test Panopticon). Unit tests compile against `httptest` and do not need that host.

## Build and test

Go 1.22+.

```bash
go test ./...
go build -ldflags "-X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Commit=$(git rev-parse HEAD)" -o hypermesh-agent ./cmd/agent
go build -o hypermesh-mhs-mcp ./cmd/hypermesh-mhs-mcp
go install github.com/FyberLabs/hypermesh-host/cmd/hypermesh-host@latest
```

`build_commit` on the heartbeat prefers that ldflags value. If it is empty, this box uses the VCS revision from `debug.ReadBuildInfo` (`vcs.revision`). If that is empty too, the value is `unknown`.

`hypermesh-host agx` preflights the pinned AGX image, writes the existing agent env and systemd units, then starts `hypermesh-wg` and `hypermesh-agent`. It stops on the first failure. `-serial` is required. The device id comes from the plane device list for that serial. Catalog must be HTTP 200 at `/api/v1/hypermesh/catalog`. The enroll token is `HYPERMESH_ENROLL_TOKEN` or `-enroll-token-file`, not a command-line value. A rerun on a box that already has `device.json` does not need a fresh token.

Release archives for `hypermesh-host` are linux amd64 and arm64 (`.goreleaser.yaml`). The Homebrew formula lives in `packaging/homebrew` until a tap exists.

Agent releases are separate. `HYPERMESH_UPDATE_CHANNEL=test` tracks builds of `main`. `prod` tracks tagged `vX.Y.Z` releases. The host asks the control plane (`GET /api/v1/hypermesh/agent/updates/{channel}`) with its device credential and downloads from the short-lived URL in that response. It verifies a GPG signature over `checksums.txt` and the SHA-256 of the binary before it installs. It defers a restart while a lease is running or a job result is unacked, restarts only `hypermesh-agent`, and rolls back to `hypermesh-agent.pre-<version>` if the new binary does not heartbeat. There is no GitHub token on the box. Details, and what has to exist before the first publish: [deploy/agx/update.md](deploy/agx/update.md).

The publisher logs in with `azure/login` and GitHub OIDC (`id-token: write`). There is no deployment environment. This private repository is on GitHub Free, so it has no environment secrets. Infra #143 uses two app registrations. A push to `main` logs in with repository variable `HYPERMESH_HOST_RELEASE_TEST_CLIENT_ID` (output `hypermesh_host_test_client_id`). A `v*` tag logs in with `HYPERMESH_HOST_RELEASE_PROD_CLIENT_ID` (output `hypermesh_host_prod_client_id`). `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`, `AZURE_STORAGE_ACCOUNT`, and `AZURE_STORAGE_CONTAINER` are the other four names in `hypermesh_host_release_next_steps`. The test subject is `repo:FyberLabs/hypermesh-host:ref:refs/heads/main`. The prod subject is `repo:FyberLabs/hypermesh-host:ref:refs/tags/v*`. The test publisher writes `test/`, `channels/test.json`, and the lease blob `locks/release-publish`. It reads `channels/prod.json` and does not write that blob or anything under `prod/`. It installs `storage-blob-preview` 1.0.0b3 and sets blob tag `superseded=true` on older `test/` releases before the channel pointer moves. A tag failure does not move the pointer. A prod re-run whose `channels/prod.json` already names this release does not write the pointer again, and it reuses the GitHub Release asset when the name and sha256 match. The prod publisher writes `prod/`, `channels/prod.json`, and `locks/release-publish`. It reads `channels/test.json` and does not write that blob or anything under `test/`. Prod does not delete blobs and does not set blob tags. `locks/release-publish` is the only blob either publisher writes outside its channel.

The names come from infra #143. The signing key is read after that login from Key Vault `kv-fyber-hmhost` in `rg-fyber`: `az keyvault secret show --vault-name kv-fyber-hmhost --name hypermesh-host-release-gpg-private-key --query value -o tsv` and the same command for `hypermesh-host-release-gpg-passphrase`. The passphrase is masked. Those three names are the defaults. Repository variables `HYPERMESH_RELEASE_KV_NAME`, `HYPERMESH_RELEASE_GPG_KEY_SECRET`, and `HYPERMESH_RELEASE_GPG_PASSPHRASE_SECRET` override them when set.

`hypermesh-supervisor` is one Linux image (`deploy/supervisor/Dockerfile`, Alpine runtime) and three placements (`HYPERMESH_SUPERVISOR_PLACEMENT`): `outside` (off the network), `host` (enrolls and heartbeats as host type `supervisor`, refuses `lease_start`), and `renter` (`POST /v1/chat/completions` on the chat gateway). Host placement does not certify the box. The renter API key and the enroll token come from the environment or a systemd credential. They are not rewritten into `supervisor.json` on start.

Default `go test ./...` / `go build` is the AGX image: `hip-rocm` and `llama-cpp-vulkan` (and `llama-cpp-cpu` on Linux) stay exact stubs. ROCm / darwin scaffolds are build-tagged; see `deploy/rocm/` and `deploy/mac/`.

Tests use `net/http/httptest` fixtures of those agent API shapes. They do not need a live Panopticon.

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
| Update | `GET /api/v1/hypermesh/agent/updates/{channel}?device_id=` (`test` or `prod`). 204 means nothing published. The 200 JSON is in [deploy/agx/update.md](deploy/agx/update.md). Do not log `artifacts[].url`. |

401 is missing/wrong secret. 403 is secret does not match `device_id`.

If this box already has an id on disk but lost the secret, it cannot enroll again. Ask for a new enroll-token (`POST /api/v1/hypermesh/devices/{device_id}/enroll-token`, `X-Tenant-ID` only). That helper exists on the client; the loop on this box does not call it.

### Jobs

Pull is one job or 204. Body: `job_id`, `kind`, `device_id`, optional `lease_id`, `catalog_id`, `image_digest`, `file_hash`, `runtime`, `backend`, `suite_id`, `until`. `file_hash` on `lease_start` is the catalog-approved GGUF sha256.

| kind | What this box does |
|---|---|
| `path_b` | thin-v1: collect image hash / uptime / thermal. If this box rebooted since the last successful path_b, POST `passed=false` (host log reason: `reboot`). First run records boot identity and does not fail just for being first. If `http://127.0.0.1:8080/v1/chat/completions` is up, POST a pinned probe and require a real choice. If that endpoint is down, telem still passes and inference is recorded as skipped — but `passed=true` still requires a non-empty `image_hash` (telem/host, else the running lease `ImageDigest`). Empty telem + no lease → `passed=false` (host log reason: `no image_hash`). The host never substitutes a hash. |
| `instance_check` | thin-v1: must hit that completions endpoint and fail if there is no real `choices[0].message.content`. This is the container-answers gate. `passed=true` requires a non-empty `image_hash` the same way as `path_b`. |
| `lab_pull` | Download a pinned GGUF (`source_ref` allowlisted, `image_digest` = `sha256:…`) into the model cache under the GGUF data directory (`/var/lib/hypermesh/models/cache` by default). Before any byte is written, preflight free space on that filesystem: object size (`size_bytes` on the job when the plane sends it, otherwise HTTP HEAD `Content-Length`) plus `HYPERMESH_DISK_MARGIN_BYTES` (default 64 MiB) must leave `HYPERMESH_DISK_FLOOR_BYTES` free (default 2 GiB), and the file must fit in `HYPERMESH_MODEL_CACHE_MAX_BYTES` (default: filesystem size minus the floor). If it does not, least-recently-used models are evicted until it fits. The loaded model, any model backing an active lease, and any model recorded for a catalog that has not yet had its `lease_start` are never evicted. An explicit `HYPERMESH_MODEL_CACHE_MAX_BYTES` is clamped to the filesystem size minus the free-space floor. Eviction happens only after that fit is known. The chosen files are removed before the download GET; a failed download does not put them back. If it still will not fit, the job POSTs `passed=false` with `insufficient_disk: need X, free Y, evictable Z` and downloads nothing. An unknown size POSTs `insufficient_disk: size unknown, free Y` and downloads nothing. The copy stops at the approved size; a longer stream POSTs `passed=false` with `size_exceeded: approved X` and removes the partial. A failed copy removes the partial `lab-pull-*.gguf`. Free space is the same `statfs` helper as heartbeat `disk_free_bytes` (Bavail*Bsize, overflow is an error). |
| `lease_start` | `image_digest` must be `repo@digest`. Requires a tunnel `client_address` and `HYPERMESH_GGUF_PATH` pointing at a regular file (unset uses `/var/lib/hypermesh/models/model.gguf`; an explicit path, including a cottage file, is kept; a missing path must not become a Docker directory). `docker pull` then `docker --runtime=nvidia -p <client_address>:8080:8080 --network hypermesh-workload --read-only --cap-drop ALL --name hypermesh-<lease_id>` and `-v` the cache file recorded for this `catalog_id` while that pin is inside its TTL, at `/models/model.gguf:ro`. With no pin, a non-empty `file_hash` still mounts the loaded cache file when one is present, otherwise `$HYPERMESH_GGUF_PATH`. With no pin and no `file_hash`, it mounts the loaded cache file only when `index.json` records that file for this `catalog_id`. If the cache has no models, it mounts `$HYPERMESH_GGUF_PATH`. If the loaded file belongs to another catalog, the job POSTs `model not verified for catalog` and does not start a container. The mounted file is hashed. A non-empty `file_hash` must match that sha256, and a different value POSTs `artifact hash mismatch`. An empty `file_hash` with a pending pin must match the pin's recorded sha256; a mismatch POSTs `artifact hash mismatch`. An empty `file_hash` with no pin starts the lease only for a same-catalog cache file or an empty cache, and logs `lease_start without file_hash; hash not verified`. A later change will require `file_hash` once the plane sends it. A pending pin whose file is gone is cleared and that start POSTs `pending model missing` without mounting another model; a later `lab_pull` for the catalog can record a new pin. The container is not started in any of those cases. The mount is weights only; no Docker socket; not host network. The image entrypoint hardcodes `llama-server --alias hypermesh-test`, and llama.cpp CLI flags beat `LLAMA_ARG_*`, so this box overrides `--entrypoint /bin/sh`. That command fails if `/models/model.gguf` is missing or empty, then `exec llama-server --host 0.0.0.0 --port 8080 --alias <catalog_id>`. Container env: `LLAMA_ARG_JINJA` (default `1`), `LLAMA_ARG_CTX_SIZE` (default `16384`), `LLAMA_ARG_TIMEOUT` (default `1800`); override with `HYPERMESH_LLAMA_JINJA`, `HYPERMESH_LLAMA_CTX_SIZE`, `HYPERMESH_LLAMA_TIMEOUT`. No image rebuild. Empty `catalog_id` → `passed=false`. The agent creates `hypermesh-workload` as an `--internal` bridge if missing so the container has no LAN/public egress; publish stays on the WireGuard IP. Empty digest or bare `sha256:hex` → `passed=false`. A different local handle → `passed=false` (`lease already running`). Same `lease_id` already running is a remint: no second container, POST the stored GGUF hash. Success POSTs `image_hash` = sha256 of that GGUF (catalog `file_hash`), not the OCI pin. Empty after start → `passed=false` (the host never substitutes a hash). Host port is `HYPERMESH_LISTEN_PORT` (default 8080). Result POST retries. Empty/unknown `kind` → `passed=false`. |
| `lease_stop` | Stop only when `job.lease_id` matches the local handle. Mismatch (including empty `lease_id` with a handle) POSTs `passed=false` and does not `docker stop` / `docker rm`. After a matching stop, `docker rm` the named container (`hypermesh-<lease_id>`) so the next `lease_start` is not blocked on an Exited leftover. No handle is a soft no-op (`passed=true`) and may omit `image_hash`; prefer the handle digest or an existing host hash when available. An already-gone container is also a soft success. A matching stop, and a stop with no local handle, clears the pending model pin for `catalog_id` (or the handle's catalog). The host never substitutes a hash and does not bounce WireGuard. The host peer stays up until GET says `revoked` or 204. |
| `lease_cancel` | Clear the pending pin for `catalog_id`. When `lease_id` is the running lease, stop that container the same way as `lease_stop`, then POST `passed=true` with reason `lease_cancel`. A cancel that does not match a running lease still clears the pin and passes. The plane does not send `lease_cancel` yet, so a cancelled lease clears its pin only when the pending TTL (default 30m) passes. |
| `lease_expiry` | Same pin clear as `lease_cancel`. A matching running lease is stopped. Reason on success is `lease_expiry`. The plane does not send `lease_expiry` yet, so an expired lease clears its pin only when the pending TTL (default 30m) passes. |
| `sell_pause` | Persist local sell posture in `$STATE_DIR/sell.json`. Matching `device_id` sets `sell_state=paused` (optional `until` RFC3339; omit = indefinite). Mismatch POSTs `passed=false` and the current real `sell_state` (usually `selling`) without flipping. Already paused is a no-op `passed=true`. While paused, new `lease_start` is refused (`passed=false`); the current lease is left running. When `until` is reached, posture returns to `selling`. The stored posture records `"source":"owner"`. An `until` at or before the host clock is a resume: the pause is cleared, `sell_state=selling` is persisted and POSTed (the plane resumes selling this way; an earlier agent stores the ended pause and returns to `selling` on its next posture check). During an installer maintenance hold a resume POSTs `passed=false` (`host maintenance hold`) and `sell_state=paused`. Not `path_b`. |

Selling starts as soon as certification passes. Activation writes a temporary `{"sell_state":"paused","source":"activation"}` guard before it starts the service, so a failed start can be retried while the plane still reports the host paused. Once startup is verified, the installer calls `POST /v0/activation_release` on the owner socket, which clears only that guard; an owner pause stays. A maintenance hold writes `{"sell_state":"paused"}` and keeps the posture to restore in `sell-owner.json`: the owner pause if there is one, otherwise selling. When the hold is removed that posture is restored. Outside a maintenance hold, a stored pause with no `source` and no `until` comes from before sources existed. If the file is exactly the compact `{"sell_state":"paused"}` that earlier installers wrote at activation, the agent clears it and the host sells. The agent itself always writes indented JSON, so any other sourceless pause (an owner command, a control-plane `sell_pause` job, or a maintenance hold under an earlier agent) is kept and labeled as an owner pause; `hypermesh-host sell resume` clears it. A pause with an `until` is always an owner pause.

Certification (`path_b`) is also the catalog bench. On that job this box inspects a local workload image and, when the probe returns `usage.completion_tokens`, POSTs `/agent/scorecard` for `pack-v1`. The control plane tags the catalog row from that report. No usage field or no local `repo@sha256` means that row stays untagged. The prompt stays off the result body.

Optional `backend` on `lease_start`: `cuda-jetson` | `cuda-sm87` | `llama-cpp-cpu` | `llama-cpp-vulkan` | `hip-rocm`. Omitted = `cuda-jetson` (alias `cuda-sm87`). This box is AGX first: that path is `docker --runtime=nvidia`. `runtime` stays `nvidia`. On the default AGX / Jetson image, stubs and unknown (including `disk-io`, which is not in the enum) POST `passed=false` on `lease_start` and `path_b` (`This image does not run <backend>.`). `instance_check` on a stub does the same and does not probe.

A later **ROCm** host image (`go build -tags rocm`, not this AGX overlay) may take `hip-rocm`; see [deploy/rocm/README.md](deploy/rocm/README.md). A **darwin** binary may take `llama-cpp-cpu` as the Metal lab runner; Jetson Linux still stubs it. See [deploy/mac/README.md](deploy/mac/README.md). `llama-cpp-vulkan` stays a stub on every image. `llama-cpp-metal` is not in the enum (unknown → the same fail string). `HSA_OVERRIDE` is never set and gfx names are never guessed. TOPS is not tok/s. Cert still means Fyber-published hashes. Path A/B stay Jetson-first.

Result body for `path_b` stays `{"passed", "image_hash?", "egress_ok?", "egress_reason?"}`. Lab soak results include a host-namespace egress observation (`egress_ok` + reason such as `overlay_publish_only`); missing tools fail closed with `egress_unobserved`. Result body for `instance_check` / `lease_start` / `lease_stop` stays `{"passed", "image_hash?"}`. `path_b` / `instance_check` / `lease_start` with `passed=true` must include a non-empty `image_hash` (the digest Panopticon stamps). Empty `image_hash` fails that job only. `sell_pause` POSTs `{"passed", "sell_state", "until?", "device_id?"}`. `sell_state` is required on that result (`selling` \| `paused` \| `draining` \| `off` \| `n/a`). This box never POSTs `passed=true` with `sell_state=n/a`. Heartbeat includes current `sell_state` (default after the actuator exists: `selling`) and `loaded_model_hash` when a lease is running. Path B (`path_b_state`) is not sell posture.

The probe and the completion text are never logged and never placed in the result.

### Owner console (plane-down)

H4 v0. Local owner actuators when the control plane is unreachable. **Not** CP job enqueue, **not** Path B, **not** SociACL / WebAuthn / OIDC / privilege_grant.

Owner console auth: unix socket `$STATE_DIR/owner.sock` (not loopback TCP) + bearer file `$STATE_DIR/owner.token` mode **0600**.

On agent start (after enroll / stored identity): create `owner.token` if missing (32 random bytes, hex, 0600), remove a stale `owner.sock`, listen HTTP on that unix socket (socket file 0600). Every route requires `Authorization: Bearer <token>` matching the file; otherwise **401**.

| Call | Path | Body | Response |
|---|---|---|---|
| Status | `GET /v0/status` | none | JSON only: `{device_id, sell_state, until?, pause_source?, maintenance, work_pending, lease, path_b, backend}`. `pause_source` is `owner` or `activation` while paused outside maintenance. `lease` is `null` or `{lease_id, catalog_id?, image_digest?}` from the local handle. `path_b` is host facts (`boot_id_present`, `boot_id?`, `completions_up`). This box logs CP `path_b_state` on heartbeat and does not persist it — status does not invent that field. `backend` is the log label `cuda-jetson`. No `device_secret`, no WireGuard keys. Does not append `owner-effects.jsonl`. |
| Console | `GET /` and `GET /v0/console` | none | One HTML page on the same unix socket. Same Bearer. Embeds the current status JSON; the page fetches `/v0/status` and buttons call `sell_pause` / `sell_resume` / `lease_stop` (`lease_id` from status). |
| Pause sell | `POST /v0/sell_pause` | optional `{until?, device_id?}`. Device is this box. `until` is RFC3339. If `device_id` is present it must match the enrolled id. | `{passed, sell_state, until?, device_id?}`. Never `passed=true` with `sell_state=n/a`. Same `sellPause` actuator as the CP job. |
| Resume sell | `POST /v0/sell_resume` | optional `{device_id?}` | `{passed, sell_state, device_id?, reason?}`. Same resume as a CP `sell_pause` whose `until` has passed. |
| Stop lease | `POST /v0/lease_stop` | `{lease_id}` required | `{passed, image_hash?}`. Same H1 identity as the CP `lease_stop` job. |

After each authorized call attempt (pass or fail), this box appends one JSON line to `$STATE_DIR/owner-effects.jsonl`: `ts` (RFC3339), `tool` (`hypermesh.sell_pause`, `hypermesh.sell_resume` or `hypermesh.lease_stop`), `args`, `passed`, and the relevant fields (`sell_state` / `until` / `lease_id`). Plane-down receipts for Brewnix — not a `fyber.receipt` schema.

CLI (talks to the socket + token file; does not contact the control plane):

```bash
hypermesh-agent owner status
hypermesh-agent owner console
hypermesh-agent owner sell-pause [--until=RFC3339]
hypermesh-agent owner sell-resume
hypermesh-agent owner lease-stop --lease-id=<id>
```

On an installed host, `sudo hypermesh-host sell pause [--until=RFC3339]`, `sudo hypermesh-host sell resume` and `sudo hypermesh-host sell status` call the same socket using the default state directory.

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

Allocated: this box generates the host WireGuard private key (`/etc/wireguard/hypermesh.key`, mode 0600, directory 0700, root-owned when the unit runs as root) and reuses that file across restart and redeploy. It replaces the file only when it is absent or not a valid X25519 key. On boot, and whenever the local public key does not match an allocated session, it POSTs only `{"peer_public_key":"..."}` to `/agent/tunnel/peer` (no `device_id`; Bearer `hm_dev_` plus `X-Tenant-ID`) and then GETs. POST 404 `Tunnel not found` still saved the device key: success with no tunnel. POST 409 `Tunnel is revoked` is logged and WireGuard stays down. GET 204 is no tunnel. GET 200 `status=allocated` with `peer_public_key` null or different, or GET 409 whose body `code` is `peer_public_key_not_registered` (`detail` is not the match), means POST again and re-GET. GET 200 `status=revoked` tears WireGuard down and does not POST again, even when `peer_public_key` is null or different; that line is logged once until a later allocated session is applied. The live wg-quick config stays on the previous key until a GET shows the new public key; then this box switches and acks again (`ack_applied` is not cleared on rotation). GET never carries `peer_private_key`. This box writes a 0600 wg-quick config from the local private key plus GET `endpoint`, `server_public_key`, `allowed_ips`, and `client_address`, `wg-quick up`, then POST ack `{"applied":true}`. Apply the fields it is given (Fyber router; `wg.test.hyperme.sh:51820`). `wg-quick up` runs only when those peer fields change. A restart that sees the same fingerprint does not bounce the iface. The private key is never logged and never sent to the plane. No other lab peer is configured.

Revoked (host peer revoked) or 204 (no host session): `wg-quick down` and shred `/etc/wireguard/hypermesh.conf` so the peer key does not stay on disk. Do not teardown because `lease_id` changed. `lease_start` / `lease_stop` do not `wg-quick` down/up.

**Userspace (no kernel module).** AGX64-1 / L4T R39 has no wireguard module. [deploy/agx/hypermesh-wg.service](deploy/agx/hypermesh-wg.service) runs `/usr/bin/wireguard-go -f hypermesh` outside the agent cgroup (`Restart=on-failure`). Set `HYPERMESH_WG_USERSPACE=1` (or leave `auto`, which picks userspace when the module is missing). The agent never spawns `wireguard-go` and does not use `KillMode=process` or `KillMode=mixed`. It programs the existing interface with `wg syncconf`, `ip addr` / `ip link`, and `ip route replace` for each AllowedIPs prefix except `0.0.0.0/0` and `::/0` (those would replace the LAN default route; they are installed only when a full tunnel is explicitly configured). Stale AllowedIPs prefixes are deleted. `proto kernel` routes and the IPv6 link-local `fe80::/64` are not. Route adds and deletes are logged at info with `tunnel_id` and `lease_id`. An unchanged restart logs `host peer unchanged (skip apply)`; a real `wg syncconf` logs `host peer applied (syncconf)`. `PersistentKeepalive = 25` stays. `wg show dump` reports a resolved endpoint, re-encoded keys, and AllowedIPs without spaces; that still counts as the same peer. A restart with those fields unchanged does not call `wg syncconf` and does not rebind the listen port (a real change pins `ListenPort` to the current port, because an omitted port is sent as `listen_port=0` and `wireguard-go` binds a new one). A failed plane sync (401/503) also leaves a handshaking interface up. Revoke (GET 200 `status=revoked` or POST 409 `Tunnel is revoked`) and 204 remove peers, routes, and the address, and set the link down; the unit stays up and the tunnel does not return until a new allocated session is applied. Kernel-module hosts keep `wg-quick` (`HYPERMESH_WG_USERSPACE=0` or `auto` when the module loads). Install and the AGX64-1 migration are in [deploy/agx/first-boot.md](deploy/agx/first-boot.md).

The container is reachable at the WG `client_address` on port 8080. The router is the only customer HTTPS path (`http://<client_address>:8080/v1/chat/completions`).

### Host health

Heartbeats run on their own loop, every `HeartbeatEvery` (30s). A failed beat retries with exponential backoff and ±20% jitter, never sooner than 1s and never later than 25s. The cap is under the 30s cadence and inside the plane's about-35s grace (`HYPERMESH_HEARTBEAT_GRACE_WAIT_S`), so a beat still lands before placement treats the host as stale. The plane drops a host whose heartbeat is older than 180s. Jitter on the exponential wait is applied after the clamp, so a step that has reached 25s still spreads through about 20–25s instead of sitting on the cap. A proxy 503 `Retry-After` (delta-seconds or an HTTP-date) on `POST /agent/heartbeat` replaces the exponential delay. The wait is never earlier than that hint: 0–20% is added on top, and the result still stops at 25s. A hint below 1s uses the 1s floor before that upward jitter. A hint at or above 25s waits exactly 25s, not a shorter spread. The header value is not logged. Panopticon #256's 503 and `Retry-After` go to placement, `lease_start`, and chat callers after a plane restart. They are not sent on heartbeat. The first success after failures waits the normal interval again. While beats are failing, the tunnel moving to `up`, a client address appearing, or a successful job pull or tunnel sync can ask for a beat. That request does not send the beat before the current backoff or Retry-After deadline. A wake queued while the heartbeat request is still in flight is dropped when the hold is set, and a wake that arrives during the wait does not end it before that deadline. The beat goes out when the deadline passes, and the 30s cadence resumes after the next success.

Heartbeat may include a top-level `health` object. Existing fields are unchanged. This box still heartbeats when health collection fails; unknown fields are omitted. `last_sync_error_class` is `""` when there is no class. `last_sync_http_status` is `0` when there is no status. Nothing secret is added: no WireGuard keys, no tokens, no device secret, no raw HTTP body, no renter id, no prompt. Lease containers are a count.

`tunnel_state` is `up` (handshake within 180s), `handshaking` (peer configured, or userspace process up, without a recent handshake), `down` (no peer, or kernel interface absent), or `disabled` (userspace mode, no `wireguard-go` process, no interface). `last_sync_at` is RFC3339 UTC of the last successful tunnel sync. `last_sync_error_class` is `http_5xx`, `http_4xx`, `network`, `timeout`, `decode`, or empty (local failures such as `wg syncconf` stay empty; the sync failure counter still increments). `handshake_age_s` is seconds since the latest handshake. `wg_pid` is the `wireguard-go` pid (omitted on the kernel path). `wg_port` is the listen port. `lease_container_count` is running containers whose names start with `hypermesh-`. `disk_free_bytes` is free bytes for non-root users (`statfs` `Bavail*Bsize`) on the filesystem that holds `HYPERMESH_GGUF_PATH`. If that file is not there yet, this box walks up to the nearest existing directory. If the path is unset or `statfs` fails, the field is omitted. `model_cache_bytes` is the sum of GGUF files in the model cache. `model_cache_evictable_bytes` is that sum minus the loaded model and any model backing an active lease. Both are omitted when the cache cannot be read, and both are `0` when the cache is empty. `build_commit` is the ldflags commit, else VCS revision, else `unknown`.

`GET /metrics` on this process exposes the same signals as Prometheus text gauges and counters (`hypermesh_heartbeat_success_total`, `hypermesh_heartbeat_failure_total{class=...}`, `hypermesh_heartbeat_age_seconds`, `hypermesh_tunnel_sync_success_total`, `hypermesh_tunnel_sync_failure_total{class=...}`, `hypermesh_tunnel_state{state=...}`, `hypermesh_handshake_age_seconds`, `hypermesh_wg_pid`, `hypermesh_wg_port`, `hypermesh_lease_container_count`, `hypermesh_disk_free_bytes`, `hypermesh_disk_size_bytes`, `hypermesh_model_cache_bytes`, `hypermesh_model_cache_evictable_bytes`, `hypermesh_pull_bytes_total`, `hypermesh_pull_duration_seconds_total`, `hypermesh_last_sync_timestamp_seconds`, `hypermesh_last_sync_http_status`, `hypermesh_last_sync_error_class{class=...}`, `hypermesh_build_info{commit=...}`). No keys, tokens, or download URLs are labels or log lines.

`hypermesh_heartbeat_age_seconds` is whole seconds since the last heartbeat the plane accepted, computed when `/metrics` is scraped. If this process has not had a successful heartbeat yet, the gauge is seconds since process start, not omitted, so the 180s staleness alert still fires. `hypermesh_disk_size_bytes` is `statfs` `Blocks*Bsize` on the same filesystem as `hypermesh_disk_free_bytes`. Neither gauge has labels. A failed `statfs` omits both.

`/metrics` binds the address on the `hypermesh` interface when that interface has one, and otherwise the tunnel session client address, at port 9464, for example `10.77.0.3:9464`, so a collector on the tunnel can scrape it. The address is never `0.0.0.0`, `::`, or a public IP. When the tunnel is down, or that WireGuard listen fails, the listener is `127.0.0.1:9464`. The failure is logged when the bind changes, and the next try waits with backoff instead of every 2s. It rebinds when that WireGuard address appears or changes. `HYPERMESH_METRICS_ADDR` (flag `-metrics-addr`) is an explicit override and is accepted only for a loopback address or the current WireGuard address. Anything else is refused and the derived address is used. `HYPERMESH_METRICS_ALLOW_NON_LOOPBACK` does not open a public or unspecified listener. A refused or failed listen does not stop heartbeats.

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

### Model path

`HYPERMESH_GGUF_PATH` unset or empty defaults to `/var/lib/hypermesh/models/model.gguf`. That directory is variable data under the existing state tree (FHS `/var/lib`). `device.json` stays in `/var/lib/hypermesh`, mode **0700**. The agent creates `models/` on start as root:root mode **0755**. Downloaded weight files are mode **0600**, owned by root. The 0700 parent means other users cannot traverse to those files; directory mode 0755 does not make the weights readable. Rootful Docker can still bind-mount them: dockerd resolves the mount as root, and the workload image has no `USER`, so the process is uid 0. Rootless Docker or a user-namespace remap cannot read the file. `hypermesh-host` writes the same path into `agent.env` when `-gguf-path` is empty and creates the directory. The systemd unit sets the same `Environment=` default; a value in `agent.env` overrides it. An explicit `HYPERMESH_GGUF_PATH` is used unchanged.

The model store is `/var/lib/hypermesh/models/cache`. Each pinned GGUF is `<sha256>.gguf`. `HYPERMESH_MODEL_CACHE_MAX_BYTES` is the byte budget. Unset, it is the filesystem size minus `HYPERMESH_DISK_FLOOR_BYTES` (default 2 GiB). An explicit value is clamped to that same ceiling, and the clamp is logged. `hypermesh-host` writes that measured value into `agent.env`. The unit documents the same default and leaves the variable unset so `agent.env` wins. Last-used time is an `index.json` in the cache directory, rewritten with a temp file in that directory, fsync, and rename. atime is not used, because these roots are often mounted `relatime` or `noatime`. Startup drops index entries whose files are gone, adopts `*.gguf` files the index does not list, and deletes leftover `lab-pull-*.gguf` and `index.json.tmp-*` files in the cache directory. A successful `lab_pull` records `catalog_ids` on that model in `index.json` and records its pin for that `catalog_id` with an expiry. A later pull for the same catalog moves `catalog_ids` onto the new file. Eviction removes the model and its catalog ids together. The last-used time stays the LRU key. `HYPERMESH_MODEL_CACHE_PENDING_TTL` sets that lifetime (default 30m). The pin is cleared when the expiry passes, when `lease_start` succeeds, and on `lease_stop`, `lease_cancel`, and `lease_expiry` for that catalog. The plane does not send `lease_cancel` or `lease_expiry` yet, so a cancelled or expired lease clears its pin only when that TTL passes. A later `lab_pull` for the same catalog replaces the pin. `lease_start` bind-mounts the pending file while the pin is live. A non-empty job `file_hash` must match the mounted file. An empty `file_hash` with a live pin must match that pin's sha256. An empty `file_hash` with no pin mounts the loaded cache file only when its `catalog_ids` include the job catalog. An empty cache still mounts `HYPERMESH_GGUF_PATH`. Otherwise the job fails with `model not verified for catalog`. A start that proceeds without `file_hash` logs that the hash was not verified. A later change will require `file_hash` once the plane sends it. A pending pin whose file is gone is cleared, and only that `lease_start` fails with `pending model missing`. `lease_start` and a successful `lab_pull` update last-used. The loaded model, a model backing an active lease, and a model waiting for its `lease_start` are not evicted.

### Update

Do not restart `hypermesh-agent` while a job is unacked unless heartbeats have been down for good. The plane does not checkpoint a job: a restart pulls the unacked job again and runs it from scratch (a `lab_pull` starts the download over).

```bash
cd hypermesh-host
git pull
go build -o hypermesh-agent ./cmd/agent
sudo install -m 0755 hypermesh-agent /usr/local/bin/hypermesh-agent
sudo systemctl restart hypermesh-agent
```

Leave `/etc/hypermesh/agent.env` and `/var/lib/hypermesh/device.json` alone.

A host that does not set `HYPERMESH_GGUF_PATH` (AGX64-1 on 2026-09-26) picks up `/var/lib/hypermesh/models/model.gguf` from the new binary on that restart. No env edit. The models directory is created on start. A host that already set `HYPERMESH_GGUF_PATH` keeps that file. Copying the new unit is optional; the binary default covers an omitted variable. Prefer that restart when no job is unacked.

On an existing host, check the root directory for leftover partials from a pull that ran before this fix, and remove any that are still there:

```bash
sudo find / -maxdepth 1 -name 'lab-pull-*.gguf' -print
sudo find / -maxdepth 1 -name 'lab-pull-*.gguf' -delete
```

On AGX64-1 none were present at 2026-09-26 12:52 ET. Keep the check on every host.

`lease_start` refuses until a `lab_pull` has written a regular file at the GGUF path. With the new default, that file is `/var/lib/hypermesh/models/model.gguf`. A missing file stays `passed=false`.

## Layout

```
cmd/agent             host process (hypermesh-agent)
cmd/hypermesh-host  AGX preflight, configure, connect
cmd/hypermesh-supervisor  supervisor (outside, host, renter)
cmd/hypermesh-mhs-mcp     stdio MCP stub for host / MHS device tools (scaffold)
internal/api        HTTP client for the fixed agent routes ( + optional enroll-token helper)
internal/pathb      thin-v1 (telem + optional local completions; path_b fails on reboot)
internal/runtime    NVIDIA Container Runtime start/stop; HIP / Metal scaffolds (gated)
internal/mhs        host MCP scaffold (list_devices / device_status stubs; no CP)
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

`hypermesh-mhs-mcp` is a line-delimited stdio MCP stub (`tools/list` returns `list_devices` and `device_status`; calls return not-implemented). It does not enroll, lease, or call the control plane. Wire real host device I/O later; this is not the renter-laptop tool MCP path.

## Out of scope here

Payment-service, Kafka, a full host-scorecard, Seeed factory flash, public bot rental, fake tok/s, renter prompts in logs. AIuditor witness is a sidecar (`deploy/agx/aiuditor-witness.service`), not this binary. Owner console does not bind loopback/LAN/public TCP, does not enqueue CP jobs, does not add a drain mega-job, `reason_code`, `preempt_mode=hard`, Path B, SociACL, WebAuthn, OIDC, or privilege_grant.


## Enrollment identity

Set `HYPERMESH_HOST_NAME` to the name this host should report, or let the agent
use its operating-system hostname. Names support discovery; they are not
credentials. The agent submits its locally generated WireGuard public key at
enrollment. The private key stays in its protected state directory.

To replace a box's identity, register it again, set the new
`HYPERMESH_ENROLL_TOKEN` and `HYPERMESH_REENROLL=1`, then restart the agent. Each
new token gets a fresh key and server-issued device ID. Restarts with that same
token reuse the persisted identity and key. Re-enrolling under the same host name
for the same owner retires the old install automatically: the plane archives it,
ends its leases and tunnels, and AIuditor revokes its signing key, credentials and
host connection. A name enrolled by another owner, or by a browser-registered
install, is still refused; retire that box from its owner's dashboard first. Upgrade the plane to the enrollment-name/key
API first.

## Hardware preflight and generic benchmarks

Run `hypermesh-host inspect` for a hardware-class fingerprint, detected
processor/memory, backend recommendations and a conservative model memory budget.
Jetson recognition requires Tegra evidence; AGX, other Jetsons, DGX-branded systems
and Apple silicon retain their class labels. Other machines use detected runtime
capabilities. This fingerprint contains no host name, serial, device UUID or keys.

Run `hypermesh-host inspect --benchmark --model /path/model.gguf` to execute
an installed llama-bench after checking the model against measured available
memory. Missing accelerator-memory measurements fail closed. CPU is the fallback
when no accelerator runtime is detected. Software/model downloads are explicit;
recommendations do not grant certification. Existing AGX deployment pins remain
in the AGX-specific installation path.

Backend build options follow the [llama.cpp build documentation](https://github.com/ggml-org/llama.cpp/blob/master/docs/build.md).

## Pull-based installer roadmap

The approved guided installation flow, resource rules, implementation checklist,
and AGX 64 / MINISFORUM N5 MAX acceptance plan are tracked in
[Host installer](docs/host-installer.md). The first implementation increment is
the resource admission policy; guided installation is not yet available.

### Retrying an interrupted test release

Test publishing preserves existing artifact bytes. On retry, the publisher verifies
stored binary and armored checksum signatures against the freshly built checksum
file and the configured signing keyring, then reuses those signatures. GPG creation
timestamps make newly generated signatures differ even for identical checksums.
Missing signatures are uploaded from the new build; changed checksums, invalid
signatures, and storage errors stop publication before the channel pointer moves.
Release CI exercises retries with real GPG signatures and partial uploads.


### Host CLI name and verifier defaults

The host setup and management command is `hypermesh-host`. Guided setup installs
the verified binary into `/usr/local/bin/hypermesh-host` after enrollment and
setup consent. Existing `hypermesh-deploy` source installs remain compatible.
Release transport filenames and private continuation paths retain their previous
names for installed verifiers and timers.

Run `python3 ./hypermesh-bootstrap.py` for the latest published production build,
or add `--channel test` for test. The verifier detects the local Linux architecture
and discovers the immutable release URL through hyperme.sh. It still verifies
the bundled signing trust, metadata and artifact hashes before publishing files.
Existing downloads require `--overwrite`; unpublished production never selects
test. Explicit pinning flags remain supported. See [the installation guide](docs/first-install-guide.md).

Private uploaded-model delivery is an opt-in capability; see
[the grant, version and runtime configuration contract](deploy/private-model-delivery.md).
It requires the corresponding control-plane implementation and acceptance evidence.

## Workload prompt privacy

Hosted Docker workloads use `--log-driver none` and `--ulimit core=0`.
Docker does not retain their stdout/stderr, and core dumps are disabled. Agent
and witness containers keep their own logging settings. An `instance_check`
inspects the running lease container before sending its completion probe and
fails if the effective log driver is not `none` or the core soft/hard limits
are not both zero. It reports only those settings in the host log.

On a lab host, inspect the lease container:

```bash
docker inspect --format '{{json .HostConfig.LogConfig}} {{json .HostConfig.Ulimits}}' <lease-container>
docker logs <lease-container>
```

Expect log type `none`, a `core` ulimit with `Soft: 0` and `Hard: 0`, and no
workload output (Docker may report that the driver does not support reading).
Use a unique synthetic prompt for lab acceptance and verify it is absent from
`/var/lib/docker/containers` and the journal. Existing containers must be
recreated after this upgrade to receive the new settings.
