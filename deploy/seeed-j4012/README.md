# Seeed Super J4012 (Path B — certification prep)

First marketed **NX class** for Hypermesh / Hyperme.sh. Module class: **Jetson Orin NX 16GB**. Carrier: Seeed Super J401 / reComputer Super J4012.

This directory is **docs + overlay stubs**. It is not factory automation and does not wait on J4012 hardware.

## Does not block AGX cottage

Path A (AGX Orin 64GB, P3701-0005, `deploy/agx/`) stays the certified-now cottage path. Nothing here changes AGX flash XML, the AGX JetPack pin, or the AGX nvpmodel table. Do not hold cottage for this SKU. Product reviews this slice; Chris merges.

## What differs

| | AGX Orin 64GB (Path A, cottage now) | Super J4012 (Path B, this directory) |
|---|---|---|
| Module class | P3701-0005 | Orin NX 16GB |
| Board | `jetson-agx-orin-devkit` | Seeed Super J4012 / J401 |
| Flash | NVIDIA `jetson-agx-orin-devkit` | Seeed **mfi / massflash** only |
| JetPack pin | **NVIDIA JP 7.2.1 / L4T 39.2.1** | Seeed mfi **JP6.2 / 36.4.3** *or* **JP7.2.0 / 39.2.0** — pick **one** |
| Not this pin | Do not flash AGX with a Seeed mfi pin | **Not** NVIDIA / AGX **7.2.1 / 39.2.1** |
| nvpmodel | AGX table; default 30W (id 2) | NX Super table (`MAXN_SUPER`). Different ids. Do not share. |
| Image | Overlay APP on a flashed Dev Kit | Separate image. Do not mix flash XML. |
| Factory | Cottage overlay | Seeed massflash preinstall is a **later** slice |

Do **not** use `jetson-agx-orin-devkit` XML, NVIDIA SDK Manager AGX flow, or classic (non-Super) J4012 mfi packages on this box. Classic J4012 cooling is not this SKU.

## JetPack pin (pick one Seeed mfi — do not dual-pin)

Seeed publishes two Super J4012 mfi pins. Choose **one** for the box and the on-box workload. Stay on it.

| Choice | Seeed mfi | L4T | Confirm `/etc/nv_tegra_release` |
|---|---|---|---|
| A | JetPack **6.2** | **36.4.3** | R36 / REVISION 4.3 |
| B | JetPack **7.2.0** | **39.2.0** | R39 / REVISION 2.0 |

**Not** these pins:

- NVIDIA / AGX Path A JetPack **7.2.1 / L4T 39.2.1** (cottage; different module — not the Seeed mfi pin)

If this box is later found on a pin that does not match the image you built: **stop**. Do not silently run a JP6 OCI image on a JP7.2.0 box, or a JP7 image on 36.4.3. Do not silently dual-pin with AGX 7.2.1 / 39.2.1.

```bash
cat /etc/nv_tegra_release
```

Example Seeed package names (dates move; use the current Super J4012 / Orin NX 16GB row on Seeed’s flash wiki):

- JP6.2 / 36.4.3: `mfi_recomputer-super-orin-nx-16g-j401-6.2-36.4.3-*.tar.gz`
- JP7.2.0 / 39.2.0: `mfi_recomputer-super-orin-nx-16g-7.2.0-39.2.0-*.tar.gz`

Flash / first-boot: [first-boot.md](first-boot.md).

## nvpmodel (NX Super table only)

This SKU uses the **Orin NX Super** table. Super J4012 is designed for `MAXN_SUPER`. Do **not** apply the AGX table (30W id 2). Do **not** apply this table to classic J4012.

Orin NX 16GB Super names (NVIDIA Super-mode docs): **10W**, **15W**, **25W**, **40W**, **MAXN_SUPER**. Presence of **40W** and **MAXN_SUPER** is the Super table. Regular NX is not this table.

NVIDIA Super-mode docs set MAXN SUPER on Orin NX with `nvpmodel -m 0`. **Confirm ids on this flashed image** — they are not portable from AGX:

```bash
sudo nvpmodel -q
grep -i "< POWER_MODEL" /etc/nvpmodel.conf
```

This box reports `nvpmodel -q`. Do not invent a shared id map. Do not quote TOPS as tokens/sec.

## Field lock (same agent, same suite)

Suite stays **`thin-v1`**. Empty or unknown `suite_id` is thin-v1.

| Field | Where | Rule |
|---|---|---|
| `image_digest` | job (`lease_start`) | Must be `repo@digest`. Empty digest → `passed=false` on **lease_start only** (no soak image; not a running instance). Bare `sha256:hex` fails. |
| `image_hash` | job result | Path B `passed=true` needs a **non-empty** `image_hash` (telem/host, else running lease `ImageDigest`). Do not invent. Empty hash fails that job only. |
| `loaded_model_hash` | heartbeat | Running lease `repo@digest` when a handle exists; omitempty otherwise. |

Do not invent hashes. Catalog id stays `llama-3.1-8b-q4` until a soak hash exists.

Workload build notes (on the J4012, Fyber-published digest later): [workload/README.md](workload/README.md).

## Overlay stubs (thin)

Same host program as Path A. After a Seeed image boots, install the overlay the same way cottage does — this directory only mirrors the unit layout:

- [hypermesh-agent.service](hypermesh-agent.service)
- [agent.env.example](agent.env.example)

Control plane example stays `https://api.test.hyperme.sh` (path prefix `/api/v1/hypermesh`). Do not invent a public network.

## Out of scope here

AGX flash changes, GHCR pull, Thor SKU, factory massflash automation, code that waits on J4012 hardware, a public bot network, tokens/sec.
