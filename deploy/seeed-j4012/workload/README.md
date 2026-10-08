# hypermesh-test (build on the Super J4012)

OCI wrapper that holds a GGUF and serves OpenAI-shaped `POST /v1/chat/completions` on `0.0.0.0:8080`. Same target name as the AGX: **`ghcr.io/fyberlabs/hypermesh-test`**. Fyber publishes the digest **later**, after an on-box pin. Until then there is no soak image.

## Build this on the J4012

Build on **this Super J4012**, against the **one** Seeed pin you flashed (JP6.2 / 36.4.3 **or** JP7.2.0 / 39.2.0). `--runtime=nvidia`.

**Do not** `docker pull ghcr.io/fyberlabs/hypermesh-test`. There is no published digest yet.

**Do not** build this on x86 and pretend it is a Jetson image.

**Do not** copy an AGX-built image onto this box. The AGX cottage pin is JP 7.2.1 / 39.2.1 on AGX Orin. That is a different pin and a different module.

If you chose Seeed JP7.2.0 / 39.2.0 and a JP6 recipe will not run: **stop**. Do not silently run a JP7 image next to a JP6 one. Do not silently dual-pin with NVIDIA / AGX 7.2.1 / 39.2.1.

The AGX recipe in `deploy/agx/workload/` is the JP7 starting point (Ubuntu 24.04, CUDA 13 SBSA NGC base, SM 8.7, `llama-server` on :8080). Rebuild it **here**. On Seeed JP6.2 / 36.4.3, treat that Dockerfile as a recipe to adapt on-box — do not assume CUDA 13 / 24.04 still applies.

```bash
# On the Super J4012, after first-boot overlay.
# Place the GGUF next to the Dockerfile as model.gguf (not committed).
ls -l model.gguf

sudo docker build --runtime=nvidia -t ghcr.io/fyberlabs/hypermesh-test .

# Pin the sha256 after a good on-box build. The host program will not invent a digest.
# Fyber publishes that digest later. Until then, lease_start has no soak image.
sudo docker image inspect --format '{{.Id}}' ghcr.io/fyberlabs/hypermesh-test
```

`lease_start` still needs `repo@digest` from the control plane. **Empty digest fails lease_start only** (`passed=false`, no running instance). Bare `sha256:hex` fails. The host never generates a registry digest.

Catalog id stays `llama-3.1-8b-q4`. Suite stays `thin-v1`.

## Field ownership

| Field | Who sets it | Synthesized? |
|---|---|---|
| job `image_digest` | control plane (`repo@digest`) | no |
| result `image_hash` | this box (telem/host, or started handle `ImageDigest`) | no — host-certification pass needs it non-empty |
| heartbeat `loaded_model_hash` | this box (running lease `repo@digest`) | no — omitempty if no handle |

## What it serves

- Listen: `0.0.0.0:8080`
- `POST /v1/chat/completions`
- GPU path: `--runtime=nvidia` (not x86 `--gpus`)

Do not quote tokens/sec. TOPS is not tok/s.
