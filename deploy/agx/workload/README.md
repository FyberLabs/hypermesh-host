# hypermesh-test (build on the AGX)

OCI wrapper that holds a GGUF and serves OpenAI-shaped `POST /v1/chat/completions` on `0.0.0.0:8080`. The host program publishes that as `docker -p 8080:8080`. The Fyber router is the only customer HTTPS path (`http://<client_address>:8080/v1/chat/completions`).

Target name: **`ghcr.io/fyberlabs/hypermesh-test`**.

Catalog id stays **`llama-3.1-8b-q4`**.

## Build this on AGX64-1

This recipe is for **AGX Orin 64GB** (P3701-0005, SM 8.7) on **JetPack 7.2.1 / L4T R39.2.1 / Ubuntu 24.04.5 / CUDA 13**, after the overlay, with `--runtime=nvidia`.

**Do not build this on x86 and pretend it is a Jetson image.** The NGC CUDA tag is multi-arch; an x86 build is the wrong arch.

**Do not keep or dual-pin a JP6 image for this box.** `ubuntu:22.04` / `l4t-jetpack` / JetPack 6.2.3 / L4T 36.5.2 is not the AGX default (`l4t-jetpack` is gone in 7.x). If a leftover JP6 image will not run here: **stop**.

### FROM (documented JP7 path)

NVIDIA does not publish `l4t-jetpack` for L4T r39 / JetPack 7.x (NGC catalog latest remains `r36.4.0`). The honest JP7 container path is NGC CUDA 13 SBSA / aarch64 on Ubuntu 24.04.

Research lock / NVIDIA Orin guide example (this recipe):

```text
nvcr.io/nvidia/cuda:13.0.0-devel-ubuntu24.04
```

Host toolkit is CUDA 13.2. `nvcr.io/nvidia/cuda:13.2.1-devel-ubuntu24.04` is registry-listed. This recipe **keeps 13.0.0-devel** because that is the documented JP 7.2 example. CUDA in the image need not equal the host toolkit minor. **L4T major MUST match the host (r39).**

That 13.0.0 tag was **registry-listed** (linux/amd64 + linux/arm64) when this recipe was drafted. It is **not** an on-box pull. Treat the FROM as **unverified-until-AGX-build**. Do not invent a digest. Do not quote a Hub listing as `ghcr.io/fyberlabs/hypermesh-test` `repo@digest`. Pin a digest in CI only after a good on-box build.

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
