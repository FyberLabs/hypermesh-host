# Super J4012 first-boot

Seeed Super J4012 (Orin NX 16GB): Seeed’s Super image, then the same Hypermesh host as the AGX. Factory preinstall is later.

Get the tenant id and enroll token from [host enroll](https://portal.test.hyperme.sh/dashboard/hypermesh/host/enroll). Shared steps: [host-enroll.md](https://github.com/FyberLabs/hypermesh-docs/blob/main/host-enroll.md). Flash detail: [seeed-host-process.md](https://github.com/FyberLabs/hypermesh-docs/blob/main/seeed-host-process.md). Public page: https://hyperme.sh/hosts.

Use Seeed Super mfi, not `jetson-agx-orin-devkit` XML.

## Image

Pick **one** Seeed Super J4012 package and stay on it.

| Pin | L4T | `/etc/nv_tegra_release` |
|---|---|---|
| Seeed mfi JetPack **6.2** | **36.4.3** | R36 / REVISION 4.3 |
| Seeed mfi JetPack **7.2.0** | **39.2.0** | R39 / REVISION 2.0 |

NVIDIA AGX uses JetPack **7.2.1 / 39.2.1** — a different module.

If the box and the workload later disagree, rebuild the workload on the flashed pin.

Current Seeed Super J4012 / Orin NX 16GB packages:

- [Seeed Super getting started](https://wiki.seeedstudio.com/recomputer_jetson_super_getting_started/)
- [Seeed flash-to-selected-product](https://wiki.seeedstudio.com/flash/jetpack_to_selected_product/)

Package name shape (dates move; take the Orin NX 16GB Super row):

```text
mfi_recomputer-super-orin-nx-16g-j401-6.2-36.4.3-*.tar.gz
mfi_recomputer-super-orin-nx-16g-7.2.0-39.2.0-*.tar.gz
```

Verify the archive checksum Seeed publishes. Classic (non-Super) J4012 packages are a different cooling design.

## Host (flash PC)

Physical Ubuntu host. Seeed’s matrix:

| Seeed pin | Host Ubuntu |
|---|---|
| JP 6.x / 36.4.3 | 20.04 or 22.04 |
| JP 7.2.0 / 39.2.0 | 20.04, 22.04, or 24.04 (24.04: flash + target components only) |

Install Seeed’s flash dependencies on that host (abootimg, qemu-user-static, nfs-kernel-server, lz4, …). This is not NVIDIA SDK Manager’s AGX Orin flow.

## Recovery (Orin NX 16GB)

Seeed Super J4012: REC switch, power, USB Type-C data to the host. Confirm:

```bash
lsusb
```

Expect `0955:7323` (Orin NX 16GB). Other Super SKUs use different ids.

Power for Orin NX Super is Seeed’s 19V / 4.74A 5525 barrel. MAXN_SUPER needs that supply.

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

Leave `gdm3` and `nvidia-l4t-3d-core` installed. Leave `/etc/nv_boot_control.conf` as Seeed wrote it.

## Install Hypermesh

Same host program as the AGX. On the J4012, after the Seeed image boots. Confirm `cat /etc/nv_tegra_release` matches the package you flashed. Go 1.22+.

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
sudo apt-get update && sudo apt-get install -y wireguard-tools

# From this repo, on the J4012:
go build -o hypermesh-agent ./cmd/agent
sudo install -m 0755 hypermesh-agent /usr/local/bin/hypermesh-agent

sudo mkdir -p /etc/hypermesh /var/lib/hypermesh
sudo chmod 0700 /var/lib/hypermesh
sudo cp deploy/seeed-j4012/hypermesh-agent.service /etc/systemd/system/
sudo cp deploy/seeed-j4012/agent.env.example /etc/hypermesh/agent.env
```

Set tenant id and enroll token from host enroll. `agent.env.example` sets `HYPERMESH_GGUF_PATH=/var/lib/hypermesh/models/model.gguf`. Replace that line only for a different file. The agent creates the directory on start.

```bash
sudo chmod 0600 /etc/hypermesh/agent.env
sudo systemctl daemon-reload
sudo systemctl enable --now hypermesh-agent
sudo systemctl status hypermesh-agent
```

Control plane: `HYPERMESH_CONTROL_PLANE_URL=https://api.test.hyperme.sh`. Path prefix `/api/v1/hypermesh`. After enroll, the secret lives in `/var/lib/hypermesh/device.json` (0600).

`lease_start` bind-mounts that GGUF at `/models/model.gguf:ro`. Docker does not publish host ports for a container that is only on the internal `hypermesh-workload` network. The agent listens on the WireGuard address and forwards TCP to the container, and posts `passed=true` only after `/health` returns 200 on both paths. Unset `HYPERMESH_GGUF_PATH` uses `/var/lib/hypermesh/models/model.gguf`. An explicit path is kept. A missing file or a directory fails closed. Pin `catalog.image_digest` on the plane after the on-box workload build.

Tunnel and lease behave the same as the AGX. One WireGuard peer per host. Router endpoint is `wg.test.hyperme.sh:51820`. Chat stays on `client_address:8080`.

## nvpmodel (do not share the AGX table)

This box uses the **Orin NX Super** table. Super J4012 is the SKU that may run `MAXN_SUPER`. Classic J4012 must not.

```bash
sudo nvpmodel -q
grep -i "< POWER_MODEL" /etc/nvpmodel.conf
```

Expect Super names **10W / 15W / 25W / 40W / MAXN_SUPER**. NVIDIA Super-mode docs use `nvpmodel -m 0` for MAXN SUPER on Orin NX — confirm the id on **this** image. AGX default 30W (id 2) is the wrong table.

This box reports `nvpmodel -q`. Confirm ids on this image. TOPS is not tokens/sec.

## thin-v1

Suite is `thin-v1`. The lab soak job (`path_b` in code, to be renamed `lab_soak`) collects image hash / uptime / thermal. If `http://127.0.0.1:8080/v1/chat/completions` answers, this box POSTs a pinned probe and requires a real choice. If that endpoint is down, telem can still pass so enroll/green is not stuck before `lease_start`, and records that inference was skipped.

`passed=true` still needs a **non-empty `image_hash`** (telem/host, else the running lease `ImageDigest`). Empty telem + no lease → `passed=false`.

Empty `image_digest` on `lease_start` POSTs `passed=false` (no soak image). That failure is **lease_start only** — it does not fail host-certification telem.

Result body stays `{passed, image_hash?}`. Do not quote tokens/sec.

Build the on-box image after overlay: [workload/README.md](workload/README.md).

## Update

Leave `agent.env` and `device.json` alone. Do not restart `hypermesh-agent` while a job is unacked unless heartbeats have been down for good. A restart re-runs that unacked job from scratch. An omitted `HYPERMESH_GGUF_PATH` picks up `/var/lib/hypermesh/models/model.gguf` from the new binary; an explicit path is kept. `lease_start` refuses until a `lab_pull` writes that file. Downloaded weights are 0600 root under the 0700 state directory; `models/` is 0755, which does not make the files readable to other users. Rootful Docker bind-mounts them as root.

Check the root directory for leftover `lab-pull-*.gguf` partials from a pull that ran before this fix, and remove any that are still there (`find / -maxdepth 1 -name 'lab-pull-*.gguf'`).

```bash
cd hypermesh-host
git pull
go build -o hypermesh-agent ./cmd/agent
sudo install -m 0755 hypermesh-agent /usr/local/bin/hypermesh-agent
sudo systemctl restart hypermesh-agent
```

Factory first-boot that preinstalls this agent is later.
