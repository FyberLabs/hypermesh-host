# AGX Orin first-boot

NVIDIA Jetson AGX Orin 64GB, module **P3701-0005**, board `jetson-agx-orin-devkit`. NVIDIA image plus our unit, this host program, and the container runtime. Not a Seeed flash.

Get the tenant id and enroll token from [host enroll](https://portal.test.hyperme.sh/dashboard/hypermesh/host/enroll). Shared steps for both boxes: https://hyperme.sh/hosts.

## Image

**JetPack 7.2.1 / L4T R39.2.1** (Ubuntu 24.04.5 sample rootfs, kernel 6.8, CUDA 13). If the box is already there, skip the flash and install Hypermesh.

Confirm before you build or start a workload:

```bash
cat /etc/nv_tegra_release
```

Expect R39 / REVISION 2.1.

## Soak preflight

This repository owns the AGX soak pins and the portable check migrated from
infra. After applying the overlay and before building or running the soak:

```bash
sudo python3 deploy/agx/agx-preflight.py --write /var/lib/hypermesh/agx-preflight.json
```

Run it from a checkout of the reviewed hypermesh-host release on the AGX itself.
It prints JSON and exits 0 only when every required fact matches; exit 3 means
missing, mismatched, or unproven facts. It reads local files and commands and
does not change firmware, power modes, packages, or Docker configuration.
The pins match `hypermesh-host agx`: L4T R39.2.1, CUDA 13.2.1, kernel 6.8,
Ubuntu 24.04.5, aarch64, nvpmodel 2 / 30W, headless boot, and stopped clocks.
Both preflight paths require `/proc/meminfo` `MemTotal` within the documented
56–66 GiB range for the 64 GB AGX; matching software on a 32 GB module cannot
pass. Docker's configured `runtimes.nvidia.path` must resolve to the installed
executable found as `nvidia-container-runtime`, including valid symlink aliases.
An installed wrapper with Docker pointing at `runc` or a missing executable
does not pass. Both paths query `docker info` through the local
`unix:///var/run/docker.sock` socket and validate the daemon's effective runtime
map. A stopped daemon, an inaccessible socket, or editing `daemon.json` without
applying the configuration cannot produce a passing report.
CUDA is read from toolkit `version.json` or `version.txt`; NVCC component build
numbers do not identify the toolkit maintenance release. The clock check requires
every online CPU and the GPU to have distinct minimum/maximum scaling limits,
with EMC `mrq_rate_locked` and `bwmgr_halt` both 0. It reads the documented
Orin sysfs/debugfs controls; missing files or permission failures remain unknown
and fail the preflight. `jetson_clocks --show` displays settings, not a running
service status, and instantaneous frequency at maximum does not imply locking.
See NVIDIA's [clock control documentation](https://docs.nvidia.com/jetson/archives/r39.2/DeveloperGuide/SD/PlatformPowerAndPerformance/JetsonOrinNanoSeriesJetsonOrinNxSeriesAndJetsonAgxOrinSeries.html#maximizing-jetson-orin-series-performance).
CUDA 13.0.0 is a rescue option and does not pass the soak check. The report
does not certify model execution, memory protection, or a completed soak.

The host-owned script from source revision
`604f3f63a1aee9dfafa93b122ad450fe253cca8a` is deployed on pan-test under the
root-owned `/opt/hypermesh-host/preflight/<source-revision>/agx-preflight.py`,
with `/opt/hypermesh-host/preflight/current` selecting that directory. Its SHA-256
is `74d7a57b89982d74e7fdb2ee1452f32fda5eeb1af07e1bd9c95b3bb15a6a7194`.
The deployed bytes were read back and verified, `--help` passed, and execution
on the x86 test VM correctly exited 3. Those same bytes were then executed on
the AGX. All observed checks passed except CUDA: actual 13.2.2 versus reviewed
13.2.1. The AGX report remains not-ready; ownership handoff does not approve
a new toolkit pin or certify the hardware. See the
[redacted acceptance evidence](../../docs/host-issues-40-133-evidence.json).

The old infra inventory/enrollment consumer is already removed. The obsolete
infra preflight source can now be removed in its separate cleanup PR, referencing
this live deployment and retaining the product-owned path. Future preflight
changes belong here, with versioned deployment/hash verification before updating
the selected product path. No host service or firmware was changed by the handoff.

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
sudo cp deploy/agx/hypermesh-wg.service /etc/systemd/system/
sudo cp deploy/agx/agent.env.example /etc/hypermesh/agent.env
```

Set tenant id and enroll token. `agent.env.example` already sets `HYPERMESH_GGUF_PATH=/var/lib/hypermesh/models/model.gguf` (the binary default when the variable is omitted). Replace that line only when this box should keep a different file. The agent creates `models/` on start (root:root, mode 0755). `/var/lib/hypermesh` stays 0700, and downloaded weight files are 0600 root, so 0755 on `models/` does not make the weights readable to other users. Rootful Docker bind-mounts them as root. On the AGX set `HYPERMESH_WG_USERSPACE=1` (there is no wireguard kernel module). `chmod 0600` the env file. Install `wireguard-go` so `/usr/bin/wireguard-go` exists, plus `wireguard-tools` (`wg`, and `ip` from iproute2).

```bash
sudo chmod 0600 /etc/hypermesh/agent.env
sudo systemctl daemon-reload
sudo systemctl enable --now hypermesh-wg
sudo systemctl enable --now hypermesh-agent
sudo systemctl status hypermesh-wg hypermesh-agent
```

Optional safety-screening witness sidecar (loopback only, hydrate, cannot fulfill requests): [aiuditor-witness.service](aiuditor-witness.service). Enable it only after the parent token and tenant exist. Not this Go agent.

### Control plane

Set `HYPERMESH_CONTROL_PLANE_URL=https://api.test.hyperme.sh`. Path prefix is `/api/v1/hypermesh`.

After enroll, this box persists the secret in `/var/lib/hypermesh/device.json` (0600). Do not put that secret in `agent.env`, the unit command line, or the journal.

### WireGuard

Install `wireguard-tools`. the AGX also needs `/usr/bin/wireguard-go` and [hypermesh-wg.service](hypermesh-wg.service). One WireGuard peer per host, not per lease. This box generates the host private key at `/etc/wireguard/hypermesh.key` (0600, directory 0700), reuses it on restart, replaces it only when absent or invalid, and POSTs only the public key (`POST /agent/tunnel/peer`, no `device_id`) on boot and whenever that key does not match an allocated session, then GETs. POST 404 means the device key was saved and there is no tunnel. POST 409 `Tunnel is revoked` leaves WireGuard down. GET 200 `status=allocated` with a null or different `peer_public_key`, or GET 409 whose body `code` is `peer_public_key_not_registered` (`detail` is not the match), POSTs again and re-GETs. GET 200 `status=revoked` tears WireGuard down and does not POST again, even when `peer_public_key` is null or different; that line is logged once until a later allocated session is applied. The current config stays up until a GET shows the new public key, then this box switches and acks again. GET `/agent/tunnel` never includes `peer_private_key`. When a session is allocated, this box applies GET `endpoint`, `server_public_key`, `allowed_ips`, and `client_address` plus that local private key, writes a 0600 wg-quick config, `wg-quick up`, then acks. A later GET with a different `lease_id` / `tunnel_id` but the same peer fields is a ticket on that tunnel — no second `wg-quick up`. `wg-quick` down/up does not run on `lease_start` or `lease_stop`. Teardown only when GET is `revoked` (host peer revoked) or 204 (no host session). Router endpoint is `wg.test.hyperme.sh:51820`. There is no `listen_port` on the GET. No other lab peer is configured. The host private key is never logged, never sent to the plane, and is shredded on teardown. A person on this box should never need to see that key. Chat stays on `client_address:8080`.

#### Userspace interface (the AGX)

L4T R39.2.1 has TUN and no wireguard kernel module. `hypermesh-wg.service` runs `/usr/bin/wireguard-go -f hypermesh` in its own cgroup (`Restart=on-failure`). The agent does not spawn `wireguard-go` and does not set `KillMode=process` or `KillMode=mixed` (the agent unit stays `KillMode=control-group`). With `HYPERMESH_WG_USERSPACE=1`, or `auto` when the module is missing, the agent configures the existing `hypermesh` interface with `wg syncconf`, `ip addr`, `ip link`, and `ip route`. It does not call `wg-quick`, which would fork `wireguard-go` inside `hypermesh-agent.service`.

`wg show dump` does not echo the config file. The endpoint is the resolved numeric address (`wg.test.hyperme.sh:51820` shows up as an IP and port), AllowedIPs are comma-joined with no spaces, and keys are standard base64. WireGuard also clears host bits (`<tunnel-host>/24` is stored as `<tunnel-network>/24`) and drops duplicate prefixes. `ip -o addr` can print a bare local address (`inet <tunnel-address> peer <gateway-address>/32`) and more than one `inet` line; a match on any of them counts. The agent treats that as the same peer when the public key, resolved endpoint, masked AllowedIPs set, and keepalive match, and the private key and address match. Endpoint comparison uses the Go resolver, then `getent ahosts -- <host>` when that result does not contain the address `wg` already stored. The host must be a plain DNS name (LDH labels, no leading hyphen, at most 253 characters). Anything else, including a token getent would read as an option, fails the compare and is not resolved. IP literals are compared as addresses. A `CGO_ENABLED=0` agent otherwise misses systemd-resolved, disagrees with the glibc lookup `wg syncconf` used, and applies the same hostname on every start. An omitted listen port (the control plane does not send one) is not compared and is not written as `listen_port=0`. An agent restart with those fields unchanged does not call `wg syncconf` and does not change the listen port. The handshake age does not matter for that skip (a handshake about 122s old is still the same peer). A failed plane sync (HTTP 401 or 503) does not tear the link down when a handshake is newer than 180s. `PersistentKeepalive = 25` stays.

`wg syncconf` with no `ListenPort` line is not a no-op here. `wireguard-tools` marks the device as having a listen port even when the file omits it, then sends `listen_port=0`. `wireguard-go` reads 0 as "bind a new port", so the source port changes (for example 33469 to 35800) and the next packet is a new handshake. When a real change is applied, the syncconf file pins `ListenPort` to the port `wg show` already reports.

After syncconf the agent runs `ip addr replace <client_address> dev hypermesh`, `ip link set dev hypermesh up`, and `ip route replace <cidr> dev hypermesh` for each AllowedIPs prefix. Host bits are cleared first (`<tunnel-host>/24` is installed as `<tunnel-network>/24`; iproute2 rejects the unmasked form). Without those routes, `ip route get <gateway-address>` follows the LAN default (`<lan-gateway> dev <lan-interface>`) and ping of the peer network is lost even though `wg show` has a fresh handshake. `0.0.0.0/0` and `::/0` are not installed. They would replace that LAN default route, which is how this box reaches the control plane and `wg.test.hyperme.sh`. This deployment does not intend a full tunnel. Those two prefixes are installed only when `InstallDefaultRoute` is set on the runner. A later AllowedIPs change deletes agent-managed prefixes that are no longer listed. A `proto kernel` route (the connected route from the interface address, including `proto kernel scope link` and a `metric`) and the IPv6 link-local `fe80::/64` stay; reconcile does not delete them. Route adds and deletes are logged at info with `tunnel_id` and `lease_id`. An unchanged restart logs `host peer unchanged (skip apply)`; a real change logs `host peer applied (syncconf)`.

Revoke (GET 200 `status=revoked`, or POST 409 `Tunnel is revoked`) and GET 204 remove every peer, delete those routes, flush the address, and `ip link set dev hypermesh down`. The unit keeps running, so the device stays, but with no peer and the link down it cannot handshake. The agent sets the link up again only when a later allocated session is applied. It does not `systemctl stop` the unit.

Kernel-module hosts leave `HYPERMESH_WG_USERSPACE` unset or set it to `0`. Those boxes keep `wg-quick`. Do not enable `hypermesh-wg.service` there.

##### Migrate the AGX off an agent-spawned wireguard-go

Today the agent (or `wg-quick` with `WG_QUICK_USERSPACE_IMPLEMENTATION=/usr/bin/wireguard-go`) starts `wireguard-go` as a child of `hypermesh-agent.service`. A restart kills it, the link drops, and the new process brings the tunnel back only after a sync succeeds. Cut over once:

```bash
# Stop the agent first so its child releases the hypermesh interface.
sudo systemctl stop hypermesh-agent
# Drop WG_QUICK_USERSPACE_IMPLEMENTATION from /etc/hypermesh/agent.env if it is set.
# Set HYPERMESH_WG_USERSPACE=1 in that file (mode 0600).

sudo cp deploy/agx/hypermesh-wg.service /etc/systemd/system/
sudo cp deploy/agx/hypermesh-agent.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now hypermesh-wg
sudo systemctl restart hypermesh-agent
sudo systemctl status hypermesh-wg hypermesh-agent
```

Later agent restarts do not stop `hypermesh-wg`. `wg show hypermesh` should keep the latest handshake across `systemctl restart hypermesh-agent`.

### Workload

`lease_start` with `repo@digest` runs `docker pull` then `docker --runtime=nvidia -p <client_address>:8080:8080` (host port configurable via `HYPERMESH_LISTEN_PORT`) and bind-mounts the host GGUF at `/models/model.gguf:ro`. Unset `HYPERMESH_GGUF_PATH` uses `/var/lib/hypermesh/models/model.gguf`. An explicit path is kept (a file in a home directory such as `/home/<user>/hypermesh/workload/model.gguf` still works when set). A missing file or a directory POSTs `passed=false` — Docker must not create a directory mount. Success POSTs `image_hash` = sha256 of that file (catalog `file_hash`), not the OCI pin. Start without a tunnel address, or with a different lease already running, also POSTs `passed=false`. The container must listen on `0.0.0.0:8080` and serve OpenAI-shaped `POST /v1/chat/completions`. The agent overrides the image entrypoint (it hardcodes `--alias hypermesh-test`, which wins over `LLAMA_ARG_ALIAS`) so llama-server runs with `LLAMA_ARG_JINJA=1`, `LLAMA_ARG_CTX_SIZE=16384`, `LLAMA_ARG_TIMEOUT=1800`, and `--alias` equal to the lease catalog id. Missing or empty `/models/model.gguf` still fails inside the container. Empty `catalog_id` POSTs `passed=false`. No image rebuild. Docker does not publish `-p` when the container's only network is the internal `hypermesh-workload` bridge, so nothing listens on `<client_address>:8080` until the agent does. The agent forwards TCP from that address to the container's bridge address on port 8080, and posts `passed=true` only after `GET /health` returns 200 on both. The wait is `HYPERMESH_LEASE_START_READY_TIMEOUT_S` (default 600 seconds). The customer HTTPS path proxies to `http://<client_address>:8080/v1/chat/completions`.

Empty `image_digest` POSTs `passed=false` (no soak image; not a running instance). Bare `sha256:hex` fails. The host never makes up a registry digest or `repo@digest`. It computes `image_hash` from the file it ran and never fabricates one. Catalog id stays `llama-3.1-8b-q4`.

The certification suite's telem hash (`image_hash` from `HYPERMESH_IMAGE_HASH`, `/etc/hypermesh/image-hash`, or a hash of `/etc/nv_tegra_release` / `/etc/os-release`) is **not** the catalog `file_hash`.

Build the on-box image after overlay: [workload/README.md](workload/README.md). Target name `ghcr.io/fyberlabs/hypermesh-test`. Do not build that image on x86. Docker 29 Buildx does not accept `--runtime=nvidia` at build — the Dockerfile links CUDA devel stubs. Verify the running container on the AGX with `--runtime=nvidia --platform linux/arm64` (not `--gpus`). Pin the sha256 after the on-box build (CI may pin a digest then; the host never generates one). The default FROM tag is `nvcr.io/nvidia/cuda:13.2.1-devel-ubuntu24.04` (compile in-image; matches host CUDA 13.2.1; Orin sm_87) and stays **not yet verified by an on-box build** until someone on this box pulls it and the build succeeds. Runtime alternative `13.2.1-runtime-ubuntu24.04` is for a copy-a-binary recipe only. L4T major must match the host (r39). There is no `l4t-jetpack:r39.2.1` image. Do not use `l4t-jetpack` / `r36.*` / `ubuntu:22.04` / JP6. If 13.2.1 hits the driver-allowlist gap on-box, keep **13.0.0 only** as the documented fallback (`13.0.0-devel-ubuntu24.04` with `-e NVIDIA_DISABLE_REQUIRE=true`) — rescue, not a second default.

**GGUF is still required** before `instance_check` / completions, but it is **not** `COPY`’d into the image (eMMC cannot bake 4.9G). The default file is `/var/lib/hypermesh/models/model.gguf` unless `HYPERMESH_GGUF_PATH` is set. A missing or directory path POSTs `passed=false` before Docker runs. Without that running container, the completions endpoint has nothing to serve.

### thin-v1

The host certification job for the plane-brokered lease collects image hash, uptime, and thermal readings. If `http://127.0.0.1:8080/v1/chat/completions` answers, this box POSTs a fixed probe and requires a real choice. If that endpoint is down, certification still passes the hardware telemetry part so enrollment is not blocked before `lease_start`, and records that inference was skipped.

`instance_check` must hit that endpoint and fail if there is no real completion. That is the “container answers” gate. It still needs the GGUF-backed image running.

Result body stays `{passed, image_hash?}`. The probe and the completion text are not logged.

Do not quote tokens/sec.

## Power

AGX Orin 64GB nvpmodel default is **30W (id 2)**. This box reports `nvpmodel -q`. Do not default the box to MAXN + `jetson_clocks`. Do not apply the Seeed Super J4012 / NX Super nvpmodel table.

## Catalog

Catalog id: `llama-3.1-8b-q4`. Uncertified until a soak hash exists.

## Update

Leave `agent.env` and `device.json` alone. Do not restart `hypermesh-agent` while a job is unacked unless heartbeats have been down for good. A restart re-runs that unacked job from scratch.

The first boot that picks up userspace WireGuard follows the migration under WireGuard above (install `hypermesh-wg.service`, `daemon-reload`, `enable --now hypermesh-wg`, then restart the agent). After that unit is enabled, later updates are:

```bash
cd hypermesh-host
git pull
go build -o hypermesh-agent ./cmd/agent
sudo install -m 0755 hypermesh-agent /usr/local/bin/hypermesh-agent
sudo systemctl restart hypermesh-agent
```

the AGX with `HYPERMESH_GGUF_PATH` unset picks up `/var/lib/hypermesh/models/model.gguf` from the new binary on that restart. The models directory is created on start (root:root, 0755). Downloaded files are 0600 root, and `/var/lib/hypermesh` is 0700, so the directory mode does not make the weights readable. Rootful Docker bind-mounts them as root. Do not hand-edit `agent.env` for that default. A host that already set `HYPERMESH_GGUF_PATH` keeps that path.

Check the root directory for leftover `lab-pull-*.gguf` partials from a pull that ran before this fix, and remove any that are still there:

```bash
sudo find / -maxdepth 1 -name 'lab-pull-*.gguf' -print
sudo find / -maxdepth 1 -name 'lab-pull-*.gguf' -delete
```

`lease_start` bind-mounts the loaded file in `/var/lib/hypermesh/models/cache` after a `lab_pull`. With no loaded cache file it still requires `/var/lib/hypermesh/models/model.gguf`. The cache budget is `HYPERMESH_MODEL_CACHE_MAX_BYTES` (filesystem size minus the disk floor when unset). `hypermesh-deploy` writes that measured value.
