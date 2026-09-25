# AGX Orin first-boot

NVIDIA Jetson AGX Orin 64GB, module **P3701-0005**, board `jetson-agx-orin-devkit`. NVIDIA image plus our unit, this host program, and the container runtime. Not a Seeed flash.

Get the tenant id and enroll token from [host enroll](https://portal.test.hyperme.sh/dashboard/hypermesh/host/enroll). Shared steps for both boxes: [host-enroll.md](https://github.com/FyberLabs/hypermesh-docs/blob/main/host-enroll.md). Public page: https://hyperme.sh/hosts.

## Image

**JetPack 7.2.1 / L4T R39.2.1** (Ubuntu 24.04.5 sample rootfs, kernel 6.8, CUDA 13). If the box is already there, skip the flash and install Hypermesh.

Confirm before you build or start a workload:

```bash
cat /etc/nv_tegra_release
```

Expect R39 / REVISION 2.1.

## Flash (only if the box is not already 7.2.1)

Use NVIDIA SDK Manager or `Linux_for_Tegra` for `jetson-agx-orin-devkit`. Super J4012 uses Seeed’s image, not this XML.

Headless, before flash, create the default user so the OEM wizard does not start:

```bash
sudo ./tools/l4t_create_default_user.sh -u hypermesh -p CHANGE_ME -a -n hypermesh-agx
```

After first boot, stay on the text console:

```bash
sudo systemctl set-default multi-user.target
```

Leave `gdm3` and `nvidia-l4t-3d-core` installed. Leave `/etc/nv_boot_control.conf` as NVIDIA wrote it.

## Install Hypermesh

On the device, after the NVIDIA image boots. Go 1.22+. Workloads start as `docker --runtime=nvidia`.

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
sudo apt-get update && sudo apt-get install -y wireguard-tools

# From this repo, on the AGX:
go build -o hypermesh-agent ./cmd/agent
sudo install -m 0755 hypermesh-agent /usr/local/bin/hypermesh-agent

sudo mkdir -p /etc/hypermesh /var/lib/hypermesh
sudo chmod 0700 /var/lib/hypermesh
sudo cp deploy/agx/hypermesh-agent.service /etc/systemd/system/
sudo cp deploy/agx/agent.env.example /etc/hypermesh/agent.env
```

Set tenant id, enroll token, and `HYPERMESH_GGUF_PATH` to a regular file. `chmod 0600` the env file.

```bash
sudo chmod 0600 /etc/hypermesh/agent.env
sudo systemctl daemon-reload
sudo systemctl enable --now hypermesh-agent
sudo systemctl status hypermesh-agent
```

Optional AIuditor witness sidecar (loopback only, hydrate, cannot fulfill): [aiuditor-witness.service](aiuditor-witness.service). Enable it only after parent token and tenant exist. Not this Go agent.

### Control plane

Set `HYPERMESH_CONTROL_PLANE_URL=https://api.test.hyperme.sh`. Path prefix is `/api/v1/hypermesh`.

After enroll, this box persists the secret in `/var/lib/hypermesh/device.json` (0600). Do not put that secret in `agent.env`, the unit command line, or the journal.

### WireGuard

Install `wireguard-tools`. One WireGuard peer per host, not per lease. This box generates the host private key at `/etc/wireguard/hypermesh.key` (0600, directory 0700), reuses it on restart, replaces it only when absent or invalid, and POSTs only the public key (`POST /agent/tunnel/peer`, no `device_id`) on boot and whenever that key does not match an allocated session, then GETs. POST 404 means the device key was saved and there is no tunnel. POST 409 `Tunnel is revoked` leaves WireGuard down. GET 200 `status=allocated` with a null or different `peer_public_key`, or GET 409 whose body `code` is `peer_public_key_not_registered` (`detail` is not the match), POSTs again and re-GETs. GET 200 `status=revoked` tears WireGuard down and does not POST again, even when `peer_public_key` is null or different; that line is logged once until a later allocated session is applied. The current config stays up until a GET shows the new public key, then this box switches and acks again. GET `/agent/tunnel` never includes `peer_private_key`. When a session is allocated, this box applies GET `endpoint`, `server_public_key`, `allowed_ips`, and `client_address` plus that local private key, writes a 0600 wg-quick config, `wg-quick up`, then acks. A later GET with a different `lease_id` / `tunnel_id` but the same peer fields is a ticket on that tunnel — no second `wg-quick up`. `wg-quick` down/up does not run on `lease_start` or `lease_stop`. Teardown only when GET is `revoked` (host peer revoked) or 204 (no host session). Router endpoint is `wg.test.hyperme.sh:51820`. There is no `listen_port` on the GET. Do not invent a lab peer. The host private key is never logged, never sent to the plane, and is shredded on teardown. A person on this box should never need to see that key. Chat stays on `client_address:8080`.

### Workload

`lease_start` with `repo@digest` runs `docker pull` then `docker --runtime=nvidia -p <client_address>:8080:8080` (host port configurable via `HYPERMESH_LISTEN_PORT`) and bind-mounts the host GGUF at `/models/model.gguf:ro`. Set `HYPERMESH_GGUF_PATH` to a regular file (AGX64-1 cottage path is `/home/chris/hypermesh/workload/model.gguf`). Empty, missing, or a directory POSTs `passed=false` — Docker must not create a directory mount. Success POSTs `image_hash` = sha256 of that file (catalog `file_hash`), not the OCI pin. Start without a tunnel address, or with a different lease already running, also POSTs `passed=false`. The container must listen on `0.0.0.0:8080` and serve OpenAI-shaped `POST /v1/chat/completions`. The Fyber router is the only customer HTTPS path; it proxies to `http://<client_address>:8080/v1/chat/completions`.

Empty `image_digest` POSTs `passed=false` (no soak image; not a running instance). Bare `sha256:hex` fails. Do not invent a registry digest. Host will not invent `image_hash` / `repo@digest`. Catalog id stays `llama-3.1-8b-q4`.

Path B suite telem hash (`image_hash` from `HYPERMESH_IMAGE_HASH`, `/etc/hypermesh/image-hash`, or a hash of `/etc/nv_tegra_release` / `/etc/os-release`) is **not** the catalog `file_hash`.

Build the on-box image after overlay: [workload/README.md](workload/README.md). Target name `ghcr.io/fyberlabs/hypermesh-test`. Do not build that image on x86. Docker 29 Buildx does not accept `--runtime=nvidia` at build — the Dockerfile links CUDA devel stubs. Verify the running container on the AGX with `--runtime=nvidia --platform linux/arm64` (not `--gpus`). Pin the sha256 after the on-box build (CI may pin a digest then; Host will not invent one). The FROM tag is Hardware Research / Product lock default `nvcr.io/nvidia/cuda:13.2.1-devel-ubuntu24.04` (compile in-image; matches host CUDA 13.2.1; Orin sm_87) and stays **unverified-until-AGX-build** until someone on this box pulls it and the build succeeds. Runtime alternative `13.2.1-runtime-ubuntu24.04` is for a copy-a-binary recipe only. L4T major MUST match the host (r39). Do not invent `l4t-jetpack:r39.2.1`. Do not use `l4t-jetpack` / `r36.*` / `ubuntu:22.04` / JP6. If 13.2.1 hits the driver-allowlist gap on-box, keep **13.0.0 only** as the documented fallback (`13.0.0-devel-ubuntu24.04` with `-e NVIDIA_DISABLE_REQUIRE=true`) — rescue, not a second default.

**GGUF is still required** before `instance_check` / completions, but it is **not** `COPY`’d into the image (eMMC cannot bake 4.9G). On AGX64-1 set `HYPERMESH_GGUF_PATH` to `/home/chris/hypermesh/workload/model.gguf`. A missing or directory path POSTs `passed=false` before Docker runs. Without that running container, the completions endpoint has nothing to serve.

### thin-v1

`path_b` collects image hash / uptime / thermal. If `http://127.0.0.1:8080/v1/chat/completions` answers, this box POSTs a pinned probe and requires a real choice. If that endpoint is down, `path_b` still passes the hardware telem part so enroll/green is not stuck before `lease_start`, and records that inference was skipped.

`instance_check` must hit that endpoint and fail if there is no real completion. That is the “container answers” gate. It still needs the GGUF-backed image running.

Result body stays `{passed, image_hash?}`. The probe and the completion text are not logged.

Do not quote tokens/sec.

## Power

AGX Orin 64GB nvpmodel default is **30W (id 2)**. This box reports `nvpmodel -q`. Do not default the box to MAXN + `jetson_clocks`. Do not apply the Seeed Super J4012 / NX Super nvpmodel table.

## Catalog

In-tree P2 id: `llama-3.1-8b-q4`. Uncertified until a soak hash exists.

## Update

Leave `agent.env` and `device.json` alone.

```bash
cd hypermesh-host
git pull
go build -o hypermesh-agent ./cmd/agent
sudo install -m 0755 hypermesh-agent /usr/local/bin/hypermesh-agent
sudo systemctl restart hypermesh-agent
```
