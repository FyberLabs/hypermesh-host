# Experimental Darwin dogfood (Apple silicon)

This is **not** a certified host and **not** a guided installer. Sale activation is not part of this path. The certified Jetson classes stay first. An Apple silicon Mac mini can enroll on the test control plane, heartbeat, and configure userspace WireGuard. That is dogfood, not a marketplace-ready host.

Host-local experimental. Do not market TOPS as tokens/sec.

The signed test release includes Darwin ARM64 agent and CLI binaries
(`hypermesh-agent_<version>_darwin_arm64` and
`hypermesh-deploy_<version>_darwin_arm64`). Bootstrap verifies the CLI.
This directory is the manual service install. Automatic updates stay off
on Darwin.

## What you need

- An Apple silicon Mac. Darwin AMD64 is not a release target.
- `sudo`. The agent uses the same paths as Linux: `/etc/hypermesh/agent.env`, `/var/lib/hypermesh`, and `/etc/wireguard`. macOS will create `/var/lib` when you do. There is no second config directory.
- GnuPG (`gpg`), for bootstrap.
- `wireguard-go` and `wireguard-tools` (`wg`) from Homebrew, then copied into `/usr/local/libexec/hypermesh`. macOS has no WireGuard kernel module. The agent does not start `wireguard-go`; [`com.fyberlabs.hypermesh-wg.plist`](com.fyberlabs.hypermesh-wg.plist) runs `/usr/local/libexec/hypermesh/wireguard-go` on `utun8`.
- For a Metal lease: copy `llama-server` into that same directory. The agent job's `PATH` lists it ahead of the system directories. If `llama-server` is missing, start fails and says so. Nothing fakes a completion.
- The LaunchDaemons run as root. `/opt/homebrew` is writable by the user who installed Homebrew, so these jobs do not execute or `PATH`-search that prefix. `ifconfig`, `route`, `netstat`, and `sysctl` come from `/sbin` and `/usr/sbin`.

## Install

Run this from a checkout that contains `deploy/mac/`. The signed release
downloads the binaries. It does not include these plist files.

```sh
brew install gnupg wireguard-go wireguard-tools
python3 bootstrap.py --channel test
```

Bootstrap writes `./hypermesh-host`, `./hypermesh-host.pub`, and
`./hypermesh-host.install.json`. It does not enroll the Mac. Inspect is
optional and is not certification:

```sh
./hypermesh-host inspect
```

Create the binary directories before either downloading or building the agent.
A fresh Mac may have `/usr/local` without `/usr/local/bin`; `install` does not
create its destination's parent directories.

```sh
sudo mkdir -p /usr/local/bin /usr/local/libexec/hypermesh
sudo chown root:wheel /usr/local/bin /usr/local/libexec/hypermesh
sudo chmod 0755 /usr/local/bin /usr/local/libexec/hypermesh
```

If any directory command fails, stop and resolve the reported path or permission
error before continuing. Both binary directories must exist and be root-owned
before copying the agent or runtime tools into them.

Download the matching agent and install it:

```sh
REL=$(python3 -c 'import json; print(json.load(open("hypermesh-host.install.json"))["release_url"])')
BUILD=$(python3 -c 'import json; print(json.load(open("hypermesh-host.install.json"))["build"])')
FP=$(python3 -c 'import json; print(json.load(open("hypermesh-host.install.json"))["fingerprint"])')
./hypermesh-host download \
  --artifact agent \
  --release-url "$REL" \
  --channel test \
  --os darwin \
  --arch arm64 \
  --min-build "$BUILD" \
  --trusted-key ./hypermesh-host.pub \
  --fingerprints "$FP" \
  --output ./hypermesh-agent
sudo install -m 0755 ./hypermesh-agent /usr/local/bin/hypermesh-agent
```

A branch that is not in a signed test release is not on that channel.
On the Mac itself you can build the agent from that checkout and install
that binary the same way:

```sh
go build -o hypermesh-agent ./cmd/agent
sudo install -m 0755 hypermesh-agent /usr/local/bin/hypermesh-agent
```

That binary is not the signed release artifact.

Copy `wireguard-go`, `wg`, and (for a Metal lease) `llama-server` out of
the Homebrew prefix into a directory root owns. `install` follows a
symlink and writes a new file, mode `0755`, owner `root:wheel`. The
plists call those copies. Do not point a plist or `PATH` at
`/opt/homebrew`.

```sh
prefix=$(brew --prefix)
sudo install -o root -g wheel -m 0755 "$prefix/bin/wireguard-go" /usr/local/libexec/hypermesh/wireguard-go
sudo install -o root -g wheel -m 0755 "$prefix/bin/wg" /usr/local/libexec/hypermesh/wg
# Skip the next line when this Mac will not run a Metal lease.
sudo install -o root -g wheel -m 0755 "$prefix/bin/llama-server" /usr/local/libexec/hypermesh/llama-server
```

Prepare the directories, the env file, and the two LaunchDaemons. Load
WireGuard first so `utun8` exists before the agent configures it.

```sh
sudo mkdir -p /etc/hypermesh /var/lib/hypermesh /etc/wireguard
sudo chmod 0700 /var/lib/hypermesh /etc/wireguard
sudo cp deploy/mac/agent.env.example /etc/hypermesh/agent.env
sudo chmod 0600 /etc/hypermesh/agent.env
```

Edit `/etc/hypermesh/agent.env`. Set `HYPERMESH_TENANT_ID` to the tenant
UUID and `HYPERMESH_ENROLL_TOKEN` to the one-time token from
[host enroll](https://portal.test.hyperme.sh/dashboard/hypermesh/host/enroll).
Set `HYPERMESH_HOST_NAME` if the Mac's hostname is not the name you want
reported. Leave `HYPERMESH_WG_USERSPACE=1`.

Confirm `utun8` is free (`ifconfig utun8` should fail). If it is in use,
pick another `utunN`, set `HYPERMESH_WG_IFACE` to that name, and change
the last argument of `wireguard-go` in
[`com.fyberlabs.hypermesh-wg.plist`](com.fyberlabs.hypermesh-wg.plist) to
the same name. Leave the program path as
`/usr/local/libexec/hypermesh/wireguard-go`.

```sh
sudo cp deploy/mac/com.fyberlabs.hypermesh-wg.plist /Library/LaunchDaemons/
sudo cp deploy/mac/com.fyberlabs.hypermesh-agent.plist /Library/LaunchDaemons/
sudo chown root:wheel /Library/LaunchDaemons/com.fyberlabs.hypermesh-wg.plist \
  /Library/LaunchDaemons/com.fyberlabs.hypermesh-agent.plist
sudo chmod 0644 /Library/LaunchDaemons/com.fyberlabs.hypermesh-wg.plist \
  /Library/LaunchDaemons/com.fyberlabs.hypermesh-agent.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/com.fyberlabs.hypermesh-wg.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/com.fyberlabs.hypermesh-agent.plist
```

Older macOS uses `sudo launchctl load -w` on those two plist paths
instead of `launchctl bootstrap system`.

The agent enrolls on startup when `/var/lib/hypermesh/device.json` does
not exist yet. There is no separate enroll command. Watch the log:

```sh
sudo launchctl print system/com.fyberlabs.hypermesh-wg
sudo launchctl print system/com.fyberlabs.hypermesh-agent
tail -f /var/log/hypermesh-agent.log
```

A successful enroll logs `enrolled` and a `device_id`. Heartbeats log
`heartbeat accepted`. The device secret stays in
`/var/lib/hypermesh/device.json` (mode 0600). Do not copy it into
`agent.env`. After enroll you can clear `HYPERMESH_ENROLL_TOKEN` in the
env file and reboot the agent with:

```sh
sudo launchctl bootout system /Library/LaunchDaemons/com.fyberlabs.hypermesh-agent.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/com.fyberlabs.hypermesh-agent.plist
```

`wg show utun8` shows the interface once the control plane has allocated
a tunnel. Until then the agent keeps heartbeating and logs that no
tunnel is allocated.

## How the env file is loaded

Linux systemd uses `EnvironmentFile=/etc/hypermesh/agent.env`. Launchd
cannot do that. [`com.fyberlabs.hypermesh-agent.plist`](com.fyberlabs.hypermesh-agent.plist)
runs `/usr/local/bin/hypermesh-agent` directly, with `WorkingDirectory`
`/var/lib/hypermesh` and `KeepAlive`. The only launchd environment
variable is `PATH`: `/usr/local/libexec/hypermesh:/usr/bin:/bin:/usr/sbin:/sbin`.
`wg` and `llama-server` resolve in the root-owned directory.
`ifconfig`, `route`, `netstat`, and `sysctl` resolve under `/sbin` and
`/usr/sbin`. `/opt/homebrew/bin` is not on that `PATH`. On startup the
agent reads `/etc/hypermesh/agent.env` and sets each key that is not
already in the environment. The keys are the same ones Linux uses.

Required for a first enroll:

| Variable | Required | Notes |
|---|---|---|
| `HYPERMESH_TENANT_ID` | yes | UUID. Sent as `X-Tenant-ID` on every request, including enroll and heartbeat. |
| `HYPERMESH_ENROLL_TOKEN` | yes, until `device.json` exists | One-time token. Same field the Linux agent sends. |
| `HYPERMESH_CONTROL_PLANE_URL` | no | Default `https://api.test.hyperme.sh`. |
| `HYPERMESH_HOST_NAME` | no | Empty uses the output of `hostname`. |
| `HYPERMESH_STATE_DIR` | no | Default `/var/lib/hypermesh`. |
| `HYPERMESH_WG_USERSPACE` | yes on a Mac | Set to `1`. `auto` also lands on userspace when no kernel module exists. Do not set `0`. |

`HYPERMESH_DEVICE_SECRET` is a recovery override. Leave it unset.

## WireGuard

`HYPERMESH_WG_USERSPACE=1` tells the agent to configure an existing
interface. [`com.fyberlabs.hypermesh-wg.plist`](com.fyberlabs.hypermesh-wg.plist)
runs `/usr/local/libexec/hypermesh/wireguard-go -f utun8` with a system
`PATH`. The agent then uses `wg`, `ifconfig`, `route`, and `netstat`.
It does not run `wg-quick` and it does not spawn `wireguard-go`. Linux
userspace still uses `ip` and the `hypermesh` interface owned by
`hypermesh-wg.service`.

## Backend names (fixed)

The control-plane enum is unchanged: `cuda-jetson` | `cuda-sm87` | `llama-cpp-cpu` | `llama-cpp-vulkan` | `hip-rocm`.

There is no CUDA on Mac. Do not send `cuda-jetson` and expect it to work on Apple silicon (that path is still `docker --runtime=nvidia`).

| `backend` | Darwin binary | Linux CPU-tagged binary |
|---|---|---|
| `llama-cpp-cpu` | Metal llama.cpp host runner (lab) | isolated CPU container; default untagged builds retain the stub |
| `llama-cpp-vulkan` | stub (same string) | stub |
| `llama-cpp-metal` | **not in the enum** | same |
| `hip-rocm` | stub unless `-tags rocm` | stub unless `-tags rocm` |

The Metal runner is behind the existing name `llama-cpp-cpu` on `//go:build darwin`. Logs use `backend=llama-cpp-cpu` and `runner=metal`. That is not a new control-plane value.

`image_digest` must still be `repo@digest`. This runner does not pull a container registry and does not supply a GGUF. Start execs `llama-server` (`--host` and `--port`). Metal is the llama.cpp default on Apple silicon. No CUDA flags.

```sh
go test ./...
GOOS=darwin go test ./internal/runtime
```

Darwin command-sequence tests for userspace WireGuard run under `go test ./internal/tunnel` on Linux. They stub `ifconfig`, `route`, and `netstat`.

## Out of scope

Guided installation, sale activation, installer signing and notarization, automatic updates, CUDA on Mac, a new `llama-cpp-metal` enum value, and fake completions.
