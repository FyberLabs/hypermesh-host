# hypermesh-test (build on the AGX)

OCI wrapper that holds a GGUF and serves OpenAI-shaped `POST /v1/chat/completions` on `0.0.0.0:8080`. The host program publishes that as `docker -p 8080:8080`. The Fyber router is the only customer HTTPS path (`http://<client_address>:8080/v1/chat/completions`).

Target name: **`ghcr.io/fyberlabs/hypermesh-test`**.

## Build this on the AGX

JetPack **6.2.3 / L4T 36.5.2** (Ubuntu 22.04, kernel 5.15, CUDA 12), after the overlay, with `--runtime=nvidia`.

**Do not build this on x86 and pretend it is a Jetson image.**

If this box is on JetPack 7.x and this JP6 image will not run: stop. Do not silently run a JP7 image next to this one.

```bash
# On the AGX, after first-boot overlay.
# Place the GGUF next to this Dockerfile as model.gguf (not committed).
ls -l model.gguf

sudo docker build --runtime=nvidia -t ghcr.io/fyberlabs/hypermesh-test .

# Pin the sha256 after a good on-box build. The host program will not invent a digest.
sudo docker image inspect --format '{{.Id}}' ghcr.io/fyberlabs/hypermesh-test
```

`lease_start` still needs `repo@digest` from the control plane. Empty digest stays `passed=false`. Catalog id stays `llama-3.1-8b-q4`.

## What it serves

- Listen: `0.0.0.0:8080`
- `POST /v1/chat/completions`
- GPU path: `--runtime=nvidia` (not x86 `--gpus`)
