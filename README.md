# Hypermesh host

Host software for [Hypermesh](https://hyperme.sh), a network that rents AI inference on edge hardware. This repository contains the software that runs on a host machine: the installer (`hypermesh-host`) and the agent (`hypermesh-agent`). It is not the control plane.

A host enrolls with the control plane, sends heartbeats, brings up a WireGuard tunnel, and runs the jobs the control plane assigns: certification runs, model downloads and inference leases. Inference runs in a locked-down Docker container that is reachable only through the host's WireGuard address.

Maintained by Fyber Labs TN LLC (Tennessee).

## Status

- Only **test** builds are published. Production builds are not released yet. On the test network, hosts and prices are for testing and host payouts are not paid; see [hyperme.sh/hosts](https://hyperme.sh/hosts).
- Installation and production hosting are **Linux only**. macOS installation is not available. The agent also builds on macOS, where an experimental Metal runner exists as a lab scaffold. It is not a certified or supported host class; see [deploy/mac/README.md](deploy/mac/README.md).
- The code is under active development. Interfaces and file formats can change between builds.

## Supported hardware

Guided setup completes enrollment and certification only for these Jetson classes:

| Hardware | Memory | Notes |
|---|---|---|
| NVIDIA Jetson AGX Orin 64GB | 56 to 66 GiB | JetPack 7.2.1 / L4T R39.2.1. See [deploy/agx/first-boot.md](deploy/agx/first-boot.md). |
| NVIDIA Jetson Orin NX 16GB (documented for the Seeed reComputer Super J4012) | 14 to 17 GiB | JetPack 6.2 / L4T 36.4.3 or JetPack 7.2.0 / L4T 39.2.0. See [deploy/seeed-j4012/README.md](deploy/seeed-j4012/README.md). |

Other machines are not supported for hosting yet:

- `hypermesh-host inspect` detects other hardware (AMD ROCm, NVIDIA discrete GPUs, DGX-branded systems, Apple silicon, CPU-only) and prints a hardware fingerprint, backend suggestions and a conservative model memory budget. It does not enroll or certify anything.
- The guided setup has Ubuntu profiles for Jetson, AMD HIP and discrete CUDA hosts. Each is marked "candidate; physical acceptance pending" in the code.
- ROCm and macOS (Metal) runtime paths exist as build-tagged scaffolds. See [deploy/rocm/README.md](deploy/rocm/README.md) and [deploy/mac/README.md](deploy/mac/README.md).

## Requirements

- A supported host from the table above, running Ubuntu with systemd. The manual `hypermesh-host agx` path requires Ubuntu 24.04.5, kernel 6.8, CUDA 13.2.1 and L4T R39.2.1, and refuses to run otherwise.
- At least 16 GiB of system memory, with more than 12 GiB of it currently free, and more than 12 GiB currently free in the GPU runtime. The model filesystem needs at least 26.4 GiB free (2.2 times 12 GiB) for the minimum install; larger models are checked again when they are used (see [docs/host-installer.md](docs/host-installer.md) for how the checks work).
- Docker Engine (`docker.io` or `docker-ce`) with the NVIDIA container runtime, installed before you start. Guided setup stops if Docker is missing. It proposes other missing packages (WireGuard tools, `wireguard-go` on hosts without the kernel module, and similar) and asks before installing them.
- Python 3 and GnuPG, to run the release verifier.
- Internet access to the control plane.

## Install

Host setup is one guided flow:

1. Download the release verifier from [hyperme.sh/hosts](https://hyperme.sh/hosts). It checks the publisher's signature and the installer's checksum, then saves the verified `hypermesh-host` executable, a public key and a download receipt. It does not install or start anything.

   ```sh
   python3 ./hypermesh-bootstrap.py --channel test
   ```

   The default channel is `prod`, which has no published release yet, so pass `--channel test`. Use `--overwrite` to replace an earlier download.

2. Run the installer from the same directory:

   ```sh
   sudo ./hypermesh-host
   ```

   It checks hardware, memory, disk and network, shows every change it proposes (packages, headless mode, reboot), and asks for approval. You approve the host in a browser on any device. The host stays paused until certification passes. If setup is interrupted or the machine reboots, a systemd timer (`hypermesh-onboarding.timer`) resumes it. Running `sudo ./hypermesh-host` again also resumes.

3. After setup, control selling from the host:

   ```sh
   sudo hypermesh-host sell status
   sudo hypermesh-host sell pause [--until=RFC3339]
   sudo hypermesh-host sell resume
   ```

Full walkthrough: [docs/first-install-guide.md](docs/first-install-guide.md). Hardware-specific flashing and workload notes are under `deploy/`. `sudo ./hypermesh-host advanced-help` lists the individual commands used for automation and recovery, including `hypermesh-host agx` for a manual AGX Orin setup (`-host-name` is required; the enroll token comes from `HYPERMESH_ENROLL_TOKEN` or `-enroll-token-file`, never a command-line value).

## How it works

The agent (`hypermesh-agent`, systemd unit `hypermesh-agent.service`) runs one loop:

1. **Enroll** once with a one-time token (`POST /api/v1/hypermesh/agent/enroll`). The returned device secret is written to `$STATE_DIR/device.json` with mode 0600. The enroll token itself is never stored; only a hash is kept so a new token can be recognized.
2. **Heartbeat** every 30 seconds, including build commit, sell state and host health.
3. **Tunnel.** The agent generates a WireGuard key on the host (mode 0600), registers only the public key, and applies the session the control plane allocates. The private key never leaves the host. A fresh enrollment stores it at `$STATE_DIR/enrollment-keys/<token-hash>/hypermesh.key` (guided installs use `/var/lib/hypermesh/registration-keys/<request-id>/hypermesh.key`), and `device.json` records the directory. Only older state without a recorded directory uses `/etc/wireguard/hypermesh.key`. Hosts without the WireGuard kernel module (the AGX Orin image) use `wireguard-go` through `hypermesh-wg.service`; see `HYPERMESH_WG_USERSPACE`.
4. **Jobs.** Every 15 seconds the agent pulls one job and posts a result. Job kinds are defined in [internal/api/types.go](internal/api/types.go):

   | Job | What the host does |
   |---|---|
   | certification (`compact_cert` and one other kind, see `types.go`) | Collects the image hash, uptime and thermal readings and, if a local completions endpoint is up, sends a fixed probe and requires a real answer. Reports a benchmark scorecard when the probe returns token usage. Fails if the host rebooted since the last successful certification. |
   | `instance_check` | Calls the local completions endpoint and fails unless it returns a real `choices[0].message.content`. |
   | `lab_pull` | Downloads a model file (GGUF) into the model cache after a free-space check. |
   | `model_evict` | Removes a privately delivered model from the cache. |
   | `lease_start` | Starts the inference container, waits for `/health` on the container and through the tunnel listener, then reports the GGUF sha256. Refuses without a tunnel address, a model file or a valid image digest, or if another lease is running. |
   | `lease_stop`, `lease_cancel`, `lease_expiry` | Stop and remove the lease container. |
   | `sell_pause` | Pauses or resumes selling and persists it in `$STATE_DIR/sell.json`. |

5. **Self-update** (optional). With `HYPERMESH_UPDATE_CHANNEL` set to `test` or `prod`, the agent asks the control plane for the newest build, downloads from the short-lived URL it is given, verifies a GPG signature over `checksums.txt` and the binary's SHA-256, and installs it. It waits for any running lease or unacknowledged job, restarts only `hypermesh-agent`, and rolls back if the new binary does not heartbeat. Details: [deploy/agx/update.md](deploy/agx/update.md).

Heartbeat and job calls send `Authorization: Bearer <device secret>` and `X-Tenant-ID`. Other agent routes (all under `/api/v1/hypermesh/agent/`) are `jobs/{job_id}/result`, `tunnel`, `tunnel/peer`, `tunnel/ack`, `scorecard` and `updates/{channel}`.

### Lease container isolation

Each lease container is started with `--runtime=nvidia`, `--read-only`, `--cap-drop ALL`, `--security-opt no-new-privileges:true`, a `/tmp` tmpfs, `--log-driver none` and `--ulimit core=0`. The only host mount is the model file, read-only. It joins an internal Docker network (`hypermesh-workload`) with no LAN or internet route. The agent forwards TCP from the host's WireGuard address (port `HYPERMESH_LISTEN_PORT`, default 8080) to the container's port 8080, binds that listener to the `hypermesh` interface, limits it to 64 connections, and closes idle connections after 5 minutes. Docker keeps no container output, and the agent does not log prompts or completions.

### Model cache

Models are cached in a `cache` directory next to the file named by `HYPERMESH_GGUF_PATH`, `/var/lib/hypermesh/models/cache` by default. If setup selects another model directory, the cache follows it. Downloads are checked against the filesystem's free space, a 2 GiB floor (`HYPERMESH_DISK_FLOOR_BYTES`) and a cache budget (`HYPERMESH_MODEL_CACHE_MAX_BYTES`). The least recently used models are evicted to make room. Models backing an active lease, pending workload reservations and owner-pinned models (`pinned_hashes` in the root-owned policy) are protected. The most recently downloaded model is not protected on its own. See [docs/model-cache.md](docs/model-cache.md).

### Owner controls

While the agent runs it listens on a Unix socket, `$STATE_DIR/owner.sock` (mode 0600), protected by a bearer token in `$STATE_DIR/owner.token`. It works when the control plane is unreachable. It does not enqueue jobs and does not change enrollment.

```sh
hypermesh-agent owner status
hypermesh-agent owner sell-pause [--until=RFC3339]
hypermesh-agent owner sell-resume
hypermesh-agent owner lease-stop --lease-id=<id>
```

Each action is appended to `$STATE_DIR/owner-effects.jsonl`. On an installed host, `hypermesh-host sell` uses the same socket.

### Metrics

`GET /metrics` serves Prometheus text on port 9464 of the host's WireGuard address, or on `127.0.0.1:9464` when the tunnel is down. `HYPERMESH_METRICS_ADDR` can override it, but only with a loopback address or the current WireGuard address. Metrics carry no keys, tokens or prompts.

## Configuration

The agent reads flags and environment variables; `hypermesh-agent -h` lists them all. Installed hosts keep them in `/etc/hypermesh/agent.env` (mode 0600); start from [deploy/agx/agent.env.example](deploy/agx/agent.env.example).

| Variable | Flag | Default |
|---|---|---|
| `HYPERMESH_CONTROL_PLANE_URL` | `-control-plane-url` | `https://api.test.hyperme.sh` |
| `HYPERMESH_TENANT_ID` | `-tenant-id` | none (required, UUID) |
| `HYPERMESH_ENROLL_TOKEN` | `-enroll-token` | none (first enrollment only) |
| `HYPERMESH_STATE_DIR` | `-state-dir` | `/var/lib/hypermesh` |
| `HYPERMESH_HOST_NAME` | none | operating-system hostname |
| `HYPERMESH_REENROLL` | none | unset. Set to `1` with a new enroll token to replace the host's identity. |
| `HYPERMESH_GGUF_PATH` | `-gguf-path` | `/var/lib/hypermesh/models/model.gguf` |
| `HYPERMESH_LISTEN_PORT` | `-listen-port` | `8080` |
| `HYPERMESH_LEASE_START_READY_TIMEOUT_S` | `-lease-ready-timeout` | 600 seconds |
| `HYPERMESH_COMPLETIONS_URL` | `-completions-url` | `http://127.0.0.1:8080/v1/chat/completions` |
| `HYPERMESH_DISK_FLOOR_BYTES`, `HYPERMESH_DISK_MARGIN_BYTES` | `-disk-floor-bytes`, `-disk-margin-bytes` | 2 GiB, 64 MiB |
| `HYPERMESH_MODEL_CACHE_MAX_BYTES` | `-model-cache-max-bytes` | filesystem size minus the disk floor |
| `HYPERMESH_WG_USERSPACE` | `-wg-userspace` | `auto` (`1` = userspace, `0` = kernel `wg-quick`) |
| `HYPERMESH_METRICS_ADDR` | `-metrics-addr` | derived, see Metrics |
| `HYPERMESH_UPDATE_CHANNEL` | `-update-channel` | empty (self-update off) |
| `HYPERMESH_HOST_TYPE` | `-host-type` | `model` (`supervisor` enrolls and heartbeats but refuses `lease_start`) |

Run the agent by hand (for development, as root on a box with Docker and WireGuard):

```sh
export HYPERMESH_TENANT_ID=<tenant-uuid>
export HYPERMESH_ENROLL_TOKEN=<one-time-token>
export HYPERMESH_STATE_DIR=./state
./hypermesh-agent
```

## Hardware inspection

```sh
hypermesh-host inspect
hypermesh-host inspect --benchmark --model /path/to/model.gguf
```

`inspect` prints JSON: a hardware-class fingerprint (no hostname, serial or keys), detected processor and memory, a backend suggestion and a model memory budget. On AMD systems it also reports `memory_encryption` evidence; this is capability information, not proof that memory encryption is enabled. `--benchmark` runs an installed `llama-bench` after checking that the model fits in measured memory. Recommendations are not certification. For backend build options see the [llama.cpp build documentation](https://github.com/ggml-org/llama.cpp/blob/master/docs/build.md).

`hypermesh-host protection` generates and validates a memory-protection evidence template. It does not change firmware or enable anything. Two subcommands support it:

- `hypermesh-host protection probe` collects local platform identity as unverified diagnostic evidence. It does not claim that any protection is enabled.
- `hypermesh-host protection measure` (Linux only) runs 3 to 30 fresh loads of a pinned, CPU-only `llama-bench` (or a trusted adapter) against a public test model. It takes `--model-file`, `--runtime-file`, `--model-sha256`, `--runtime-sha256`, `--phase baseline|protected`, `--artifact-dir`, and optional `--protocol`, `--samples`, `--context-tokens`, `--generated-tokens`, `--timeout` and `--firmware-id` (for device-tree hosts without DMI). It prints samples as JSON and writes content-hashed artifacts. The phase is a label, not proof that protection is enabled.

See [docs/memory-protection-qualification.md](docs/memory-protection-qualification.md) for the full procedure.

## Build from source

Requires Go 1.22 or newer.

```sh
git clone https://github.com/FyberLabs/hypermesh-host.git
cd hypermesh-host
go build -o hypermesh-agent ./cmd/agent
go build -o hypermesh-host ./cmd/hypermesh-host
```

To embed the commit that the agent reports in its heartbeat:

```sh
go build -ldflags "-X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Commit=$(git rev-parse HEAD)" -o hypermesh-agent ./cmd/agent
```

Without the flag the agent uses the VCS revision recorded by Go, or `unknown`. Guided setup expects the signed release build described above, so a binary built from source is for development.

To update an installed agent from source: build it, `sudo install -m 0755 hypermesh-agent /usr/local/bin/hypermesh-agent`, then `sudo systemctl restart hypermesh-agent`. Do not restart while a job is unacknowledged; the control plane re-sends it and it starts over.

## Test

```sh
go vet ./...
go test ./...
```

CI also runs `go test -race ./...` and the Python tests under `packaging/release/`, `scripts/` and `internal/installer/`. Tests use `net/http/httptest` fixtures and do not need a control plane or Jetson hardware.

## Repository layout

```
cmd/agent                  hypermesh-agent
cmd/hypermesh-host         installer and host management CLI
cmd/hypermesh-deploy       same CLI under its earlier name (kept for compatibility)
cmd/hypermesh-supervisor   supervisor process (placements: outside, host, renter)
cmd/hypermesh-mhs-mcp      stub MCP server over stdio (tools return "not implemented")
internal/agent             enroll, heartbeat, tunnel and job loop, owner socket, model cache
internal/api               control-plane client and wire types
internal/installer         guided installer and advanced operations
internal/runtime           container start and stop, isolation settings, backends
internal/tunnel            WireGuard key handling, apply and teardown
internal/update            signed self-update
internal/metrics           Prometheus endpoint
deploy/                    systemd units, env examples and per-hardware notes
docs/                      installer, certification, cache and release documents
packaging/                 release scripts, bootstrap verifier, Homebrew formula
```

## Release publishing

Maintainer notes for the release workflow.

The publisher logs in with `azure/login` and GitHub OIDC (`id-token: write`). There is no deployment environment. Two app registrations are used. A push to `main` logs in with repository variable `HYPERMESH_HOST_RELEASE_TEST_CLIENT_ID`. A `v*` tag logs in with `HYPERMESH_HOST_RELEASE_PROD_CLIENT_ID`. `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`, `AZURE_STORAGE_ACCOUNT`, and `AZURE_STORAGE_CONTAINER` are repository variables. The test subject is `repo:FyberLabs/hypermesh-host:ref:refs/heads/main`. The prod subject is `repo:FyberLabs/hypermesh-host:ref:refs/tags/v*`. The test publisher writes `test/`, `channels/test.json`, and the lease blob `locks/release-publish`. It reads `channels/prod.json` and does not write that blob or anything under `prod/`. It installs `storage-blob-preview` 1.0.0b3 and sets blob tag `superseded=true` on older `test/` releases before the channel pointer moves. A tag failure does not move the pointer. A prod re-run whose `channels/prod.json` already names this release does not write the pointer again, and it reuses the GitHub Release asset when the name and sha256 match. The prod publisher writes `prod/`, `channels/prod.json`, and `locks/release-publish`. It reads `channels/test.json` and does not write that blob or anything under `test/`. Prod does not delete blobs and does not set blob tags. `locks/release-publish` is the only blob either publisher writes outside its channel.

The signing key is read after that login from Key Vault `kv-fyber-hmhost` in resource group `rg-fyber`: `az keyvault secret show --vault-name kv-fyber-hmhost --name hypermesh-host-release-gpg-private-key --query value -o tsv` and the same command for `hypermesh-host-release-gpg-passphrase`. The passphrase is masked. Those three names are the defaults. Repository variables `HYPERMESH_RELEASE_KV_NAME`, `HYPERMESH_RELEASE_GPG_KEY_SECRET`, and `HYPERMESH_RELEASE_GPG_PASSPHRASE_SECRET` override them when set.

## Security

Report vulnerabilities to **security@hyperme.sh**. Please do not open a public issue for a security problem. Report other bugs to **bugs@hyperme.sh** or open a GitHub issue.

## License

Released under the MIT License. See [LICENSE](LICENSE). Copyright (c) 2026 Fyber Labs TN LLC.

The model files and container images that hosts run (for example llama.cpp and the models you download) have their own licenses, which are not covered by this one.
