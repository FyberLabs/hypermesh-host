# Seeed Super J4012

Orin NX 16GB, Seeed Super J401 / reComputer Super J4012. Flash Seeed’s Super image, then install the same Hypermesh host as the AGX.

Enroll and update: [host-enroll.md](https://github.com/FyberLabs/hypermesh-docs/blob/main/host-enroll.md). Flash: [first-boot.md](first-boot.md). Public page: https://hyperme.sh/hosts.

AGX uses `deploy/agx/` (NVIDIA JetPack 7.2.1 / L4T 39.2.1). This directory does not change that flash.

## Image

Pick **one** Seeed Super package and stay on it.

| Seeed package | L4T | `/etc/nv_tegra_release` |
|---|---|---|
| JetPack 6.2 | 36.4.3 | R36 / REVISION 4.3 |
| JetPack 7.2.0 | 39.2.0 | R39 / REVISION 2.0 |

```bash
cat /etc/nv_tegra_release
```

Package names (dates move; take the Orin NX 16GB Super row on Seeed’s flash wiki):

- JP6.2 / 36.4.3: `mfi_recomputer-super-orin-nx-16g-j401-6.2-36.4.3-*.tar.gz`
- JP7.2.0 / 39.2.0: `mfi_recomputer-super-orin-nx-16g-7.2.0-39.2.0-*.tar.gz`

If the box and the workload later disagree, rebuild the workload on the flashed pin.

## What differs from the AGX

| | AGX Orin 64GB (`deploy/agx`) | Super J4012 (this directory) |
|---|---|---|
| Module | P3701-0005 | Orin NX 16GB |
| Flash | NVIDIA `jetson-agx-orin-devkit` | Seeed Super mfi |
| JetPack | 7.2.1 / 39.2.1 | 6.2 / 36.4.3 **or** 7.2.0 / 39.2.0 |
| nvpmodel | AGX table; default 30W (id 2) | NX Super (`MAXN_SUPER`). Confirm ids on this image. |

Classic (non-Super) J4012 cooling is a different design.

## After flash

Install the host from [first-boot.md](first-boot.md): Docker, NVIDIA runtime, WireGuard, [hypermesh-agent.service](hypermesh-agent.service), [agent.env.example](agent.env.example). Set `HYPERMESH_GGUF_PATH` to a regular file. Tenant id and enroll token come from host enroll.

On-box workload: [workload/README.md](workload/README.md). Build it on this J4012.

Factory massflash that preinstalls the agent is later.
