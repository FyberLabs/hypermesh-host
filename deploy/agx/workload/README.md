# hypermesh-test (build on the AGX)

OCI wrapper that holds a GGUF and serves OpenAI-shaped `POST /v1/chat/completions` on `0.0.0.0:8080`. The host program publishes that as `docker -p 8080:8080`. The Fyber router is the only customer HTTPS path (`http://<client_address>:8080/v1/chat/completions`).

Target name: **`ghcr.io/fyberlabs/hypermesh-test`**.

Catalog id stays **`llama-3.1-8b-q4`**.

## Build this on AGX64-1

This recipe is for **AGX Orin 64GB** (P3701-0005, SM 8.7) on **JetPack 7.2.1 / L4T R39.2.1 / Ubuntu 24.04.5 / CUDA 13**, after the overlay, with `--runtime=nvidia`.

**Do not build this on x86 and pretend it is a Jetson image.** The NGC CUDA tag is multi-arch; an x86 build is the wrong arch.

**Do not keep or dual-pin a JP6 image for this box.** `ubuntu:22.04` / `l4t-jetpack` / JetPack 6.2.3 / L4T 36.5.2 is not the AGX default (`l4t-jetpack` is gone in 7.x). If a leftover JP6 image will not run here: **stop**.

### FROM (Hardware Research default)

NVIDIA does not publish `l4t-jetpack` for JP7.x / L4T r39.2.1 (NGC catalog latest remains `r36.4.0`). Do **not** invent `l4t-jetpack:r39.2.1`. Do **not** use `r36.*` / `ubuntu:22.04` / JP6. Do **not** use `whitesscott/l4t-jetpack` unless Product explicitly asks.

This recipe compiles llama.cpp in-image, so the default is **devel** (not the leaner `13.2.1-runtime-ubuntu24.04` copy-a-binary tag):

```text
nvcr.io/nvidia/cuda:13.2.1-devel-ubuntu24.04
```

That matches JetPack 7.2 host CUDA **13.2.1**, SBSA / aarch64, Ubuntu 24.04. The tag was **registry-listed** (linux/amd64 + linux/arm64) when this recipe was drafted. It is **not** an on-box pull. Treat the FROM as **unverified-until-AGX-build**. Do not invent a digest. Do not quote a Hub listing as `ghcr.io/fyberlabs/hypermesh-test` `repo@digest`. Pin a digest in CI only after a good on-box build.

**L4T major MUST match the host (r39).**

If 13.2.1 hits the known r590/r595 driver-allowlist gap on-box, fall back to `nvcr.io/nvidia/cuda:13.0.0-devel-ubuntu24.04` or a `13.3.x-*-ubuntu24.04` tag with `-e NVIDIA_DISABLE_REQUIRE=true`. That is an on-box rescue, not a second default FROM.

Plain `cuda:*-ubuntu24.04` does **not** ship Jetson cuDNN / TensorRT. llama.cpp CUDA decode usually does not need TRT. Only add packages from `repo.download.nvidia.com/jetson` r39.2 if a later workload needs them.

```bash
# On AGX64-1, after first-boot overlay.
# Place llama-3.1-8b-q4 next to this Dockerfile as model.gguf (not committed).
# Missing GGUF fails the docker build honestly. Without that image, instance_check
# and completions have nothing to hit.
ls -l model.gguf

sudo docker build --runtime=nvidia --platform linux/arm64 \
  -t ghcr.io/fyberlabs/hypermesh-test .

# Pin the sha256 after a good on-box build. The host program will not invent
# image_hash or a repo@digest.
sudo docker image inspect --format '{{.Id}}' ghcr.io/fyberlabs/hypermesh-test
```

## Field lock

| Field | Who sets it | Invent? |
|---|---|---|
| job `image_digest` | control plane (`repo@digest`) | no — empty stays `passed=false` (no soak image; not a running instance). Bare `sha256:hex` fails. |
| result `image_hash` | this box (telem/host, or started handle `ImageDigest`) | no — Host will not invent `image_hash` / `repo@digest`. |
| Path B suite telem hash | this box (`HYPERMESH_IMAGE_HASH`, `/etc/hypermesh/image-hash`, else hash of `/etc/nv_tegra_release` or `/etc/os-release`) | no — this is **not** the catalog `file_hash`. |

`lease_start` still needs `repo@digest` from the control plane. Empty digest stays `passed=false`. Catalog id stays `llama-3.1-8b-q4`.

## GGUF still required

`model.gguf` is not in git. Copy **`llama-3.1-8b-q4`** onto this box as `model.gguf` before `docker build`. A missing file fails the build. A built image without that GGUF cannot serve completions; **`instance_check` and `POST /v1/chat/completions` still need this image running with the weights**.

## What it serves

- Listen: `0.0.0.0:8080`
- `POST /v1/chat/completions`
- GPU path: `--runtime=nvidia` (not x86 `--gpus`)
- CUDA build target: SM **87** (`CMAKE_CUDA_ARCHITECTURES=87`)
