# Seeed Super J4012 (Path B — document only)

This is **not** the AGX image. Super J4012 is Orin NX 16GB.

Do not block the AGX overlay on this SKU. Do not mix flash XML with `jetson-agx-orin-devkit`.

## What differs

| | AGX Orin 64GB (Path A, now) | Super J4012 (later) |
|---|---|---|
| Module | P3701-0005 | Orin NX 16GB |
| Flash | NVIDIA `jetson-agx-orin-devkit` | Seeed mfi / massflash |
| L4T | NVIDIA JetPack 7.2.1 / r39.2.1 (or 6.2.3 / 36.5.2 if workloads are still JP6) | Seeed mfi **JP6.2 / 36.4.3** or **JP7.2.0 / 39.2.0** — not NVIDIA 39.2.1 |
| nvpmodel | AGX table; default 30W (id 2) | NX Super table (`MAXN_SUPER`). Different ids. |
| Factory | Our overlay on a flashed Dev Kit | Seeed massflash preinstall |

Factory preinstall is a first-boot that installs this agent, enrolls with a factory token, and leaves the box Path A ready. That image is a later slice.

Until then this directory is a placeholder so the AGX work does not grow Seeed-specific flash bits.
