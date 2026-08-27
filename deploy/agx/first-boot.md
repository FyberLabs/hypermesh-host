# AGX Orin overlay (this slice)

First certified box: NVIDIA Jetson AGX Orin 64GB, module **P3701-0005**, board `jetson-agx-orin-devkit`. This is overlay APP only: NVIDIA sample rootfs plus our unit, this host program, and the container runtime. It is not a Seeed flash.

## JetPack pin (locked)

This slice assumes **JetPack 6.2.3 / L4T 36.5.2** (Ubuntu 22.04 sample rootfs, kernel 5.15, CUDA 12).

- If the box is already 6.2.3: overlay only (steps below).
- If not: flash `jetson-agx-orin-devkit` to 6.2.3 / 36.5.2, then overlay. Chris reviews the flash.
- JetPack 7.2.1 / L4T r39.2.1 is a later pin, when JP7 images exist. Do not pull a CUDA 13 image on this box.
- If someone later finds the box on 7.x and the JP6 OCI image will not run: **stop**. Do not silently run both.

Confirm what is actually flashed before you build or start a workload:

```bash
cat /etc/nv_tegra_release
```

Expect R36 / REVISION 5.2.

## Flash (only if the box is not already 6.2.3)

Use NVIDIA SDK Manager or `Linux_for_Tegra` for `jetson-agx-orin-devkit`. Do not use Seeed Super J4012 / Orin NX flash XML on this module.

Headless, before flash, create the default user so the OEM wizard does not start:

```bash
sudo ./tools/l4t_create_default_user.sh -u hypermesh -p CHANGE_ME -a -n hypermesh-agx
```

After first boot, stay on the text console:

```bash
sudo systemctl set-default multi-user.target
```

Do **not** purge `gdm3` or `nvidia-l4t-3d-core`. That breaks the GPU stack this overlay still needs.

Do **not** overwrite `/etc/nv_boot_control.conf`. That file is board-specific.

## Overlay APP

On the device, after the NVIDIA image boots:

1. Install Docker from [get.docker.com](https://get.docker.com), not a random distro GPU how-to.
2. Keep JetPack’s `nvidia-container` packages.
3. Point Docker at the NVIDIA runtime and make it the default:

   ```bash
   sudo nvidia-ctk runtime configure --runtime=docker
   ```

   `/etc/docker/daemon.json` should use `"default-runtime": "nvidia"` and must not treat x86 `--gpus` as the Jetson GPU path. Workloads start as `docker --runtime=nvidia`.

4. Install `wireguard-tools` so this box can `wg-quick` an allocated session.
5. Install `hypermesh-agent` to `/usr/local/bin/hypermesh-agent`.
6. Install `deploy/agx/hypermesh-agent.service` to `/etc/systemd/system/`.
7. Copy `deploy/agx/agent.env.example` to `/etc/hypermesh/agent.env`, set tenant id and enroll token, `chmod 0600`.
8. `sudo mkdir -p /var/lib/hypermesh && sudo chmod 0700 /var/lib/hypermesh`
9. `sudo systemctl enable --now hypermesh-agent`

### Control plane

Set `HYPERMESH_CONTROL_PLANE_URL=https://api.test.hyperme.sh`. Path prefix is `/api/v1/hypermesh`. That is the deployed test Panopticon, not a localhost httptest. The URL may not be live yet; this repo still compiles and tests against `httptest`.

After enroll, this box persists the secret in `/var/lib/hypermesh/device.json` (0600). Do not put that secret in `agent.env`, the unit command line, or the journal.

### WireGuard

Install `wireguard-tools`. When a session is allocated, this box applies the fields it is given (endpoint, keys, allowed_ips, client_address), writes a 0600 wg-quick config, `wg-quick up`, then acks. Planned router endpoint is `wg.test.hyperme.sh:51820`. There is no `listen_port` on the GET. Do not invent a lab peer. The peer private key is never logged and is shredded on teardown. A person on this box should never need to see that key.

### Workload

`lease_start` with `repo@digest` runs `docker pull` then `docker --runtime=nvidia -p 8080:8080` (host port configurable via `HYPERMESH_LISTEN_PORT`). The container must listen on `0.0.0.0:8080` and serve OpenAI-shaped `POST /v1/chat/completions`. The Fyber router is the only customer HTTPS path; it proxies to `http://<client_address>:8080/v1/chat/completions`.

Empty `image_digest` POSTs `passed=false` (no soak image; not a running instance). Bare `sha256:hex` fails. Do not invent a registry digest. Catalog id stays `llama-3.1-8b-q4`.

Build the on-box image after overlay: [workload/README.md](workload/README.md). Target name `ghcr.io/fyberlabs/hypermesh-test`. Do not build that image on x86. Pin the sha256 after the on-box build.

### thin-v1

`path_b` collects image hash / uptime / thermal. If `http://127.0.0.1:8080/v1/chat/completions` answers, this box POSTs a pinned probe and requires a real choice. If that endpoint is down, `path_b` still passes the hardware telem part so enroll/green is not stuck before `lease_start`, and records that inference was skipped.

`instance_check` must hit that endpoint and fail if there is no real completion. That is the “container answers” gate.

Result body stays `{passed, image_hash?}`. The probe and the completion text are not logged.

Do not quote tokens/sec.

## Power

AGX Orin 64GB nvpmodel default is **30W (id 2)**. This box reports `nvpmodel -q`. Do not default the box to MAXN + `jetson_clocks`.

## Catalog

In-tree P2 id: `llama-3.1-8b-q4`. Uncertified until a soak hash exists. Do not quote tokens/sec.
