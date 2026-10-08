# Mac / Apple Silicon (lab scaffold)

This is **not** a certified host class. The certified Jetson classes (AGX Orin and Seeed Super J4012) stay first.

Host-local experimental. Cert still means **Fyber-published hashes**. Do not market TOPS as tokens/sec. TOPS is not tok/s.

## Backend names (fixed)

The CP enum is unchanged: `cuda-jetson` | `cuda-sm87` | `llama-cpp-cpu` | `llama-cpp-vulkan` | `hip-rocm`.

There is **no** CUDA on Mac. Do not send `cuda-jetson` and expect it to work on Apple Silicon (that path is still `docker --runtime=nvidia`).

| `backend` | Darwin binary | Jetson / Linux binary |
|---|---|---|
| `llama-cpp-cpu` | Metal llama.cpp host runner (lab) | stub: `This image does not run llama-cpp-cpu.` |
| `llama-cpp-vulkan` | stub (same string) | stub |
| `llama-cpp-metal` | **not in the enum** → `This image does not run llama-cpp-metal.` | same |
| `hip-rocm` | stub unless `-tags rocm` | stub unless `-tags rocm` |

The Metal runner is behind the existing name `llama-cpp-cpu` on `//go:build darwin`. Logs use `backend=llama-cpp-cpu` and `runner=metal`. That is not a new CP value.

## Lab / cert limits

- `image_digest` must still be `repo@digest` (Fyber-published pin). This slice does not pull GHCR and does not invent a GGUF.
- Start execs `llama-server` (`--host 0.0.0.0 --port 8080`). Metal is the llama.cpp default on Apple Silicon. No CUDA flags. No invented `-ngl` tok/s.
- If `llama-server` is not on PATH, start fails honestly. `instance_check` still requires a real completion; the scaffold does not fake one.
- `go test ./...` on Linux (the AGX image) still stub-fails `llama-cpp-cpu` and `llama-cpp-vulkan` with the exact strings above.
- Darwin scaffold tests live in `//go:build darwin` files.

```bash
go test ./...
GOOS=darwin go test ./internal/runtime
```

## Out of scope here

CUDA on Mac, a new `llama-cpp-metal` enum value, Jetson overlay changes, fake completions, public Thor, GHCR pull.
