# Super J4012 first-boot (Path B prep)

Certification prep outline for Seeed Super J4012 (Orin NX 16GB). Seeed **mfi / massflash** only. This is not factory automation and does not wait on hardware.

**Does not block AGX cottage.** Do not change `deploy/agx/`. Do not flash this SKU with `jetson-agx-orin-devkit` XML.

## Pin (one Seeed mfi)

Pick **one** Seeed Super J4012 pin and stay on it. Do not silently dual-pin.

| Pin | L4T | `/etc/nv_tegra_release` |
|---|---|---|
| Seeed mfi JetPack **6.2** | **36.4.3** | R36 / REVISION 4.3 |
| Seeed mfi JetPack **7.2.0** | **39.2.0** | R39 / REVISION 2.0 |

**Not** NVIDIA JetPack **7.2.1 / 39.2.1**. **Not** AGX Path A **6.2.3 / 36.5.2**.

If the box is later found on a pin that does not match the workload you built: **stop**.

Chris reviews the flash. Current Seeed Super J4012 / Orin NX 16GB packages:

- [Seeed Super getting started](https://wiki.seeedstudio.com/recomputer_jetson_super_getting_started/)
- [Seeed flash-to-selected-product](https://wiki.seeedstudio.com/flash/jetpack_to_selected_product/)

Package name shape (dates move; take the Orin NX 16GB Super row):

```text
mfi_recomputer-super-orin-nx-16g-j401-6.2-36.4.3-*.tar.gz
mfi_recomputer-super-orin-nx-16g-7.2.0-39.2.0-*.tar.gz
```

Verify the archive checksum Seeed publishes. Do not use classic (non-Super) J4012 mfi.

## Host (flash PC)

Physical Ubuntu host. Seeed’s matrix:

| Seeed pin | Host Ubuntu |
|---|---|
| JP 6.x / 36.4.3 | 20.04 or 22.04 |
| JP 7.2.0 / 39.2.0 | 20.04, 22.04, or 24.04 (24.04: flash + target components only) |

Install Seeed’s flash dependencies on that host (abootimg, qemu-user-static, nfs-kernel-server, lz4, …). Do not use NVIDIA SDK Manager’s AGX Orin flow.

## Recovery (Orin NX 16GB)

Seeed Super J4012: REC switch, power, USB Type-C data to the host. Confirm:

```bash
lsusb
```

Expect `0955:7323` (Orin NX 16GB). Other Super SKUs are different ids — this Path B box is 16GB only.

Power for Orin NX Super is Seeed’s 19V / 4.74A 5525 barrel. Do not under-power MAXN_SUPER.

## Massflash (Seeed mfi)

On the Ubuntu host, after the Super J4012 is in recovery:

```bash
sudo tar xpf mfi_recomputer-super-orin-nx-16g-*.tar.gz
cd mfi_*   # Seeed tree; example names include mfi_recomputer-orin-super-j401
sudo ./tools/kernel_flash/l4t_initrd_flash.sh --flash-only --massflash 1 --network usb0 --showlogs
```

That writes the Seeed image to the NVMe. It is not an AGX overlay and not a factory Hypermesh preinstall.

If the extracted tree has `tools/l4t_create_default_user.sh` and you need a headless first boot, create the user **before** flash the same way cottage does — on this Seeed tree, not the AGX tree:

```bash
sudo ./tools/l4t_create_default_user.sh -u hypermesh -p CHANGE_ME -a -n hypermesh-j4012
```

If the mfi image still starts the OEM wizard, finish it on the box (HDMI). Then stay on the text console:

```bash
sudo systemctl set-default multi-user.target
```

Do **not** purge `gdm3` or `nvidia-l4t-3d-core`. Do **not** overwrite `/etc/nv_boot_control.conf`.

## After first boot (overlay APP)

Same host program as Path A. On the J4012, after the Seeed image boots:

1. Confirm the pin (`cat /etc/nv_tegra_release`) matches the mfi you flashed.
2. Install Docker from [get.docker.com](https://get.docker.com). Keep JetPack `nvidia-container` packages.
3. `sudo nvidia-ctk runtime configure --runtime=docker` so `/etc/docker/daemon.json` has `"default-runtime": "nvidia"`. Workloads start as `docker --runtime=nvidia`. Not x86 `--gpus`.
4. Install `wireguard-tools`.
5. Install `hypermesh-agent` to `/usr/local/bin/hypermesh-agent`.
6. Install [hypermesh-agent.service](hypermesh-agent.service) to `/etc/systemd/system/`.
7. Copy [agent.env.example](agent.env.example) to `/etc/hypermesh/agent.env`, set tenant id and enroll token, `chmod 0600`.
8. `sudo mkdir -p /var/lib/hypermesh && sudo chmod 0700 /var/lib/hypermesh`
9. `sudo systemctl enable --now hypermesh-agent`

Control plane example: `HYPERMESH_CONTROL_PLANE_URL=https://api.test.hyperme.sh`. Path prefix `/api/v1/hypermesh`. Do not invent a public network or a lab WireGuard peer. After enroll, `device_secret` lives in `/var/lib/hypermesh/device.json` (0600).

Tunnel, lease, and owner-console behavior are the same agent as Path A. See the repo README. One WireGuard peer per host. Planned router endpoint is `wg.test.hyperme.sh:51820`. Chat stays on `client_address:8080`.

## nvpmodel (do not share the AGX table)

This box uses the **Orin NX Super** table. Super J4012 is the SKU that may run `MAXN_SUPER`. Classic J4012 must not.

```bash
sudo nvpmodel -q
grep -i "< POWER_MODEL" /etc/nvpmodel.conf
```

Expect Super names **10W / 15W / 25W / 40W / MAXN_SUPER**. NVIDIA Super-mode docs use `nvpmodel -m 0` for MAXN SUPER on Orin NX — confirm the id on **this** image. AGX default 30W (id 2) is the wrong table.

This box reports `nvpmodel -q`. Do not default a shared id from Path A. Do not quote TOPS as tokens/sec.

## thin-v1 / Path B pass

Suite is `thin-v1`. `path_b` collects image hash / uptime / thermal. If `http://127.0.0.1:8080/v1/chat/completions` answers, this box POSTs a pinned probe and requires a real choice. If that endpoint is down, telem can still pass so enroll/green is not stuck before `lease_start`, and records that inference was skipped.

`passed=true` still needs a **non-empty `image_hash`** (telem/host, else the running lease `ImageDigest`). Empty telem + no lease → `passed=false`. Do not invent a hash.

Empty `image_digest` on `lease_start` POSTs `passed=false` (no soak image). That failure is **lease_start only** — it does not fail Path B telem.

Result body stays `{passed, image_hash?}`. Do not quote tokens/sec.

Build the on-box image after overlay: [workload/README.md](workload/README.md).

## Factory (later)

A factory first-boot that preinstalls this agent, enrolls with a factory token, and leaves the box Path B ready is a **later** slice. This outline is cottage-style prep on a Seeed-flashed Super J4012.
