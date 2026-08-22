# AGX Orin overlay (Path A — do this)

First certified box: NVIDIA Jetson AGX Orin 64GB, module **P3701-0005**, board `jetson-agx-orin-devkit`. This is overlay APP only: NVIDIA sample rootfs plus our unit, agent, and container runtime. It is not a Seeed flash.

## JetPack pin

Pin **JetPack 7.2.1 / L4T r39.2.1** (Ubuntu 24.04 sample rootfs, kernel 6.8) unless the workload images you will load are still JetPack 6 / CUDA 12. In that case pin **JetPack 6.2.3 / L4T 36.5.2** instead.

Do not apt-upgrade JetPack 6 to 7 in place. Flash the matching L4T release.

## Flash (host side)

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

4. Install `wireguard-tools` so the agent can `wg-quick` an allocated session.
5. Install `hypermesh-agent` to `/usr/local/bin/hypermesh-agent`.
6. Install `deploy/agx/hypermesh-agent.service` to `/etc/systemd/system/`.
7. Copy `deploy/agx/agent.env.example` to `/etc/hypermesh/agent.env`, set tenant id and enroll token, `chmod 0600`.
8. `sudo mkdir -p /var/lib/hypermesh && sudo chmod 0700 /var/lib/hypermesh`
9. `sudo systemctl enable --now hypermesh-agent`

The control plane the agent expects is Panopticon `products/hypermesh` on **:8010**. After enroll it persists `device_secret` in `/var/lib/hypermesh/device.json` (0600). Do not put that secret in `agent.env`, the unit command line, or journald.

Empty `image_digest` on `lease_start` POSTs `passed=false` (no soak image; not a running instance). `image_digest` must be `repo@digest` to start. Do not invent a catalog image or tokens/sec.

## Power

AGX Orin 64GB nvpmodel default is **30W (id 2)**. The agent reports `nvpmodel -q`. Do not default the box to MAXN + `jetson_clocks`.

## Catalog

In-tree P2 id: `llama-3.1-8b-q4`. Uncertified until a soak hash exists. Do not quote tokens/sec.
