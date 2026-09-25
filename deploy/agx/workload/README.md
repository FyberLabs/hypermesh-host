# hypermesh-test (build on the AGX)

OCI wrapper that serves OpenAI-shaped `POST /v1/chat/completions` on `0.0.0.0:8080`. The GGUF is **volume-mounted at run**, not baked into the image (AGX64-1 eMMC cannot hold a 4.9G COPY). The host program publishes that as `docker -p <client_address>:8080:8080` on the internal `hypermesh-workload` network (not host network), plus the GGUF bind-mount. The Fyber router is the only customer HTTPS path (`http://<client_address>:8080/v1/chat/completions`).

Target name: **`ghcr.io/fyberlabs/hypermesh-test`**.

Catalog id stays **`llama-3.1-8b-q4`**.

## Workload jail

`lease_start` (and cert-sample starts) always jail the artifact container:

| Guarantee | How |
|---|---|
| No host network | `--network hypermesh-workload` (never `--network=host`) |
| No LAN / public egress | network created with `docker network create --internal` |
| Overlay ingress only | `-p <WireGuard client_address>:8080:8080` |
| No Docker socket | not mounted |
| Dropped caps | `--cap-drop ALL`, `--security-opt no-new-privileges:true` |
| Read-only root | `--read-only` + `--tmpfs /tmp` |
| Weights only | `-v $HYPERMESH_GGUF_PATH:/models/model.gguf:ro` — no other host paths |
| No sensors/cameras/robots | no `--device` grants (GPU via `--runtime=nvidia` only) |

Agent and witness stay on host network; they reach completions via the WG publish address like the gateway door.
## Build this on AGX64-1

This recipe is for **AGX Orin 64GB** (P3701-0005, SM 8.7) on **JetPack 7.2.1 / L4T R39.2.1 / Ubuntu 24.04.5 / CUDA 13**, after the overlay. **Run** with `--runtime=nvidia` (not `--gpus`). Docker 29 Buildx does not accept `--runtime=nvidia` at **build**; the Dockerfile links CUDA devel stubs instead.

**Do not build this on x86 and pretend it is a Jetson image.** The NGC CUDA tag is multi-arch; an x86 build is the wrong arch.

**Do not keep or dual-pin a JP6 image for this box.** `ubuntu:22.04` / `l4t-jetpack` / JetPack 6.2.3 / L4T 36.5.2 is not the AGX default (`l4t-jetpack` is gone in 7.x). If a leftover JP6 image will not run here: **stop**.

### FROM (Hardware Research default)

NVIDIA does not publish `l4t-jetpack` for JP7.x / L4T r39.2.1 (NGC catalog latest remains `r36.4.0`). Do **not** invent `l4t-jetpack:r39.2.1`. Do **not** use `r36.*` / `ubuntu:22.04` / JP6. Do **not** use `whitesscott/l4t-jetpack` unless Product explicitly asks.

This recipe compiles llama.cpp in-image, so the default is **devel**:

```text
nvcr.io/nvidia/cuda:13.2.1-devel-ubuntu24.04
```

Runtime alternative if a later recipe only copies a prebuilt binary: `nvcr.io/nvidia/cuda:13.2.1-runtime-ubuntu24.04`. Not this recipe.

That matches JetPack 7.2 host CUDA **13.2.1**, SBSA / aarch64, Ubuntu 24.04. The tag was **registry-listed** (linux/amd64 + linux/arm64) when this recipe was drafted. It is **not** an on-box pull. Treat the FROM as **unverified-until-AGX-build**. Do not invent a digest. Do not quote a Hub listing as `ghcr.io/fyberlabs/hypermesh-test` `repo@digest`. Pin a digest in CI only after a good on-box build.

**L4T major MUST match the host (r39).** Orin is **sm_87** (`CMAKE_CUDA_ARCHITECTURES=87`).

If 13.2.1 hits the driver-allowlist gap on-box, keep **13.0.0 only** as the documented fallback: `nvcr.io/nvidia/cuda:13.0.0-devel-ubuntu24.04` with `-e NVIDIA_DISABLE_REQUIRE=true`. That is an on-box rescue, not a second default FROM.

Plain `cuda:*-ubuntu24.04` does **not** ship Jetson cuDNN / TensorRT. llama.cpp CUDA decode usually does not need TRT. Only add packages from `repo.download.nvidia.com/jetson` r39.2 if a later workload needs them.

```bash
# On AGX64-1, after first-boot overlay.
# Docker 29 Buildx rejects --runtime=nvidia on docker build. Link uses
# CUDA devel stubs (see Dockerfile). Do not COPY model.gguf. Do not commit it.
sudo docker build --platform linux/arm64 \
  -t ghcr.io/fyberlabs/hypermesh-test .

# Pin the sha256 after a good on-box build. The host program will not invent
# image_hash or a repo@digest.
sudo docker image inspect --format '{{.Id}}' ghcr.io/fyberlabs/hypermesh-test
```

## Field lock

| Field | Who sets it | Invent? |
|---|---|---|
| job `image_digest` | control plane (`repo@digest`) | no — empty stays `passed=false` (no soak image; not a running instance). Bare `sha256:hex` fails. |
| result `image_hash` | this box: Path B is telem/host; `lease_start` is sha256 of `HYPERMESH_GGUF_PATH` | no — do not echo the OCI pin. Do not invent a hash. |
| Path B suite telem hash | this box (`HYPERMESH_IMAGE_HASH`, `/etc/hypermesh/image-hash`, else hash of `/etc/nv_tegra_release` or `/etc/os-release`) | no — this is **not** the catalog `file_hash`. |

`lease_start` still needs `repo@digest` from the control plane. Empty digest stays `passed=false`. Catalog id stays `llama-3.1-8b-q4`.

## GGUF is volume-mounted (not COPY)

Do **not** `COPY model.gguf` into the image. On AGX64-1 set **`HYPERMESH_GGUF_PATH=/home/chris/hypermesh/workload/model.gguf`**. `lease_start` bind-mounts that host file at `/models/model.gguf:ro` after `stat` says it is a regular file. Empty, missing, or a directory POSTs `passed=false` so Docker cannot create a directory mount. **`instance_check` and `POST /v1/chat/completions` still need this image running with that mount.**

```bash
# Manual run (same jail the host program adds):
sudo docker network create --driver bridge --internal hypermesh-workload 2>/dev/null || true
sudo docker run --runtime=nvidia --platform linux/arm64 \
  -p 10.77.0.2:8080:8080 \
  --network hypermesh-workload \
  --read-only --tmpfs /tmp \
  --cap-drop ALL --security-opt no-new-privileges:true \
  -v /home/chris/hypermesh/workload/model.gguf:/models/model.gguf:ro \
  ghcr.io/fyberlabs/hypermesh-test
```

## What it serves

- Listen: `0.0.0.0:8080`
- `POST /v1/chat/completions`
- GPU path: `--runtime=nvidia` (not x86 `--gpus`)
- CUDA build target: SM **87** (`CMAKE_CUDA_ARCHITECTURES=87`)

`entrypoint.sh` still checks `/models/model.gguf` and execs `llama-server` with `--alias hypermesh-test`. The host agent does not use that alias: llama.cpp CLI flags beat `LLAMA_ARG_*`, so `lease_start` overrides the entrypoint and serves the lease catalog id (for example `llama-3.1-8b-q4`) with `LLAMA_ARG_JINJA=1`, `LLAMA_ARG_CTX_SIZE=16384`, and `LLAMA_ARG_TIMEOUT=1800`. A later image that keeps this entrypoint contract does not need a rebuild for those settings.
