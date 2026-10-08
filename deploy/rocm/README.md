# AMD ROCm host image (scaffold)

This is **not** the AGX overlay and **not** a certified host class. The certified Jetson classes (AGX Orin and Seeed Super J4012) stay first.

The default `hypermesh-agent` binary is the AGX / Jetson image. On that image `backend=hip-rocm` POST `passed=false` with the locked string:

```
This image does not run hip-rocm.
```

`llama-cpp-vulkan` stays a stub on every image, including a future ROCm one.

## What this scaffold is

`internal/runtime/hip.go` is the start/stop path for `hip-rocm` (16GB+ class first). `ResolveBackend` returns that path **only** when this binary was built with `-tags rocm`. A runtime probe (`/dev/kfd`) is used at start; missing KFD fails honestly. This is not a TOPS or tokens/sec claim. TOPS is not tok/s.

Do **not** set `HSA_OVERRIDE` or `HSA_OVERRIDE_GFX_VERSION`. Do not invent a gfx target.

Cert still means **Fyber-published hashes**. `image_digest` stays `repo@digest`. This slice does not pull GHCR and does not invent a completion.

## How a future ROCm host image enables it

1. Build a **different** host image (not the AGX overlay):

   ```bash
   go test ./...
   go test -tags rocm ./internal/runtime ./internal/agent
   go build -tags rocm -o hypermesh-agent ./cmd/agent
   ```

   `go test ./...` without `-tags rocm` is the AGX/default cert path. Keep it green.

2. Ship that binary on a box that actually has ROCm and a 16GB+ class AMD GPU. The default Jetson image must not be rebuilt with `-tags rocm`.

3. Control plane still sends `runtime=nvidia` (locked field, do not overload) and `backend=hip-rocm`. Loaders stay `oci|gguf`. `disk-io` is not in the enum.

4. Workload listen path is still `0.0.0.0:8080` / `POST /v1/chat/completions`. `instance_check` still requires a real `choices[0].message.content`. The scaffold does not fake that.

5. Pin the host and workload with Fyber-published hashes. Uncertified catalog ids stay uncertified.

## Out of scope here

NVIDIA Jetson certified classes, public Thor, GHCR pull, fake tok/s, marketing TOPS as tokens/sec, `HSA_OVERRIDE`, invented gfx names.
