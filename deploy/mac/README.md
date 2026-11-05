# Experimental Darwin dogfood (Apple silicon)

This is **not** a certified host and **not** a guided installer. Sale activation is not part of this path. The certified Jetson classes stay first. An Apple silicon Mac mini can enroll on the test control plane, heartbeat, and configure userspace WireGuard. That is dogfood, not a marketplace-ready host.

Host-local experimental. Do not market TOPS as tokens/sec.

The signed test release includes Darwin ARM64 agent and CLI binaries
(`hypermesh-agent_<version>_darwin_arm64` and
`hypermesh-deploy_<version>_darwin_arm64`). Bootstrap verifies the CLI.
This directory is the manual service install. Automatic updates stay off
on Darwin.

## Enrollment modes

**Default owner path:** run the installer, complete device-code sign-in, then
review and approve the installation through the returned portal
`host-registrations` approval link. Sign-in and host approval are separate steps.
For SSH-only hosts, use the printed verification URL and user code on another
device; no browser is needed on the host. See the
[owner registration and recovery procedure](../../docs/host-installer.md#browser-approved-agent-registration-and-recovery).

Darwin `register-agent` now implements this owner flow for the test-only
`darwin-arm64-lab` class. Install the agent, protected configuration and exact
LaunchDaemon files below, then follow [owner device-code registration](#owner-device-code-registration).
Use a signed test release containing this change; earlier CLIs still reject the
Darwin command. Enrollment stays separate from qualification and selling.

**Rare secondary path:** an authorized non-owner or batch operator obtains a
one-time enroll token through **Register a box** or the device-writer API,
configures `HYPERMESH_TENANT_ID` and `HYPERMESH_ENROLL_TOKEN` in the protected
`agent.env`, then starts WireGuard and the agent. The agent enrolls on its first
start. This path stays supported for non-owner/batch setup and explicitly authorized dogfood.
Choose [owner registration](#owner-device-code-registration) or
[secondary token setup](#secondary-token-setup) after installing the files below.
SSH itself does not require token setup.

The [control-plane enrollment contract](https://github.com/FyberLabs/panopticon/blob/main/products/hypermesh/docs/host-registration.md#choose-an-enrollment-mode)
is the shared source of truth for both paths. Neither path certifies a Mac,
qualifies its performance, or enables marketplace selling. This remains
experimental, paused lab setup.

## Terms

A Mac that already has Hypermesh installed still ships with the Terms unaccepted. Enrollment does not run until you set `HYPERMESH_LICENSE_ACCEPT=yes` in `/etc/hypermesh/agent.env`, or run `hypermesh-agent enroll --license accept`. At a terminal, `hypermesh-agent enroll` prints the Terms and continues only if you type `accept`. The LaunchDaemon has no terminal, so it cannot ask. `hypermesh-host --yes` answers install questions only. It does not accept the Terms.

## What you need

- An Apple silicon Mac. Darwin AMD64 is not a release target.
- `sudo`. The agent uses the same paths as Linux: `/etc/hypermesh/agent.env`, `/var/lib/hypermesh`, and `/etc/wireguard`. macOS will create `/var/lib` when you do. There is no second config directory.
- GnuPG (`gpg`), for bootstrap.
- `wireguard-go` and `wireguard-tools` (`wg`) from Homebrew, then copied into `/usr/local/libexec/hypermesh`. macOS has no WireGuard kernel module. The agent does not start `wireguard-go`; [`com.fyberlabs.hypermesh-wg.plist`](com.fyberlabs.hypermesh-wg.plist) runs `/usr/local/libexec/hypermesh/wireguard-go` on `utun8`.
- For a Metal lease: install the [pinned llama-server build](#optional-metal-runner) below into that same directory. Enrollment and heartbeats work without it; Metal leases require it. The agent job's `PATH` lists the directory ahead of the system directories.
- The LaunchDaemons run as root. `/opt/homebrew` is writable by the user who installed Homebrew, so these jobs do not execute or `PATH`-search that prefix. `ifconfig`, `route`, `netstat`, and `sysctl` come from `/sbin` and `/usr/sbin`.

## Command Line Tools over SSH

Prepare Apple's Command Line Tools before Homebrew or a local Go build. Check
whether a developer directory is already selected:

```sh
/usr/bin/xcode-select --print-path
/usr/bin/xcrun --find clang
```

If both commands succeed, keep the existing developer-tool selection. If the
tools are missing, `xcode-select --install` requests a GUI dialog; it does not
complete an unattended SSH installation. From the SSH session, list the updates
Apple offers for this Mac:

```sh
/usr/sbin/softwareupdate --list
```

Find the Command Line Tools entry and copy its exact `Label` value, excluding
`Label:` itself. Replace the placeholder below with that label; do not guess a
version or install all available updates. The install command accepts the
package license, so review it before proceeding.

```sh
sudo /usr/sbin/softwareupdate --install "<exact Command Line Tools label>" --agree-to-license
/usr/bin/xcode-select --print-path
/usr/bin/xcrun --find clang
```

Wait for installation to finish and both checks to succeed before running
`brew install` or `go build`. If no compatible Command Line Tools package is
listed, use [Apple's Command Line Tools installation instructions](https://developer.apple.com/documentation/xcode/installing-the-command-line-tools/)
to obtain the package for this macOS version. A successful download or a queued
GUI request alone does not mean the tools are installed. See also Apple's
[developer-directory selection guide](https://developer.apple.com/documentation/xcode/configuring-command-line-tools-settings/).

Downloading the signed agent below avoids installing Go or compiling the agent;
it does not require Command Line Tools for that binary itself. Homebrew and
building runtime tools can still need them. The manual LaunchDaemon files and
runtime-tool setup below are still required.

## Install

Download the test-channel bootstrap from [Hypermesh hosts](https://hyperme.sh/hosts)
and save it as `bootstrap.py` in an empty working directory. The publisher now
includes the two LaunchDaemon plists and `agent.env.example` as individual files
in the signed release checksum list, alongside the binaries. A source checkout
is not required for this installation path. Use a release built with these
resources and the matching CLI; earlier releases do not contain them.

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

Download the service files from that same immutable release, using the same
trusted key, fingerprint and build floor as the agent. These downloads create
private, non-executable files in the working directory and never overwrite an
existing file:

```sh
for artifact in darwin-agent-plist darwin-wg-plist darwin-env; do
  case "$artifact" in
    darwin-agent-plist) file=com.fyberlabs.hypermesh-agent.plist ;;
    darwin-wg-plist) file=com.fyberlabs.hypermesh-wg.plist ;;
    darwin-env) file=agent.env.example ;;
  esac
  ./hypermesh-host download \
    --artifact "$artifact" \
    --release-url "$REL" \
    --match-installer \
    --channel test --os darwin --arch arm64 \
    --min-build "$BUILD" \
    --trusted-key ./hypermesh-host.pub \
    --fingerprints "$FP" \
    --output "./$file" || exit 1
done
```

Stop if verification or a file download fails. An older release missing these
files cannot satisfy this step; select a newer signed build. Downloads do not
install services or modify `/etc/hypermesh/agent.env`.

A branch that is not in a signed test release is not on that channel.
To build the agent instead, use a separate source checkout on the Mac. From
its repository root, build and install that binary:

```sh
go build -o hypermesh-agent ./cmd/agent
sudo install -m 0755 hypermesh-agent /usr/local/bin/hypermesh-agent
```

That binary is not the signed release artifact. Return to the verified-download
working directory for the service-file installation steps below.

Copy `wireguard-go` and `wg` out of the Homebrew prefix into a directory
root owns. Install the optional Metal runner separately below. `install` follows a
symlink and writes a new file, mode `0755`, owner `root:wheel`. The
plists call those copies. Do not point a plist or `PATH` at
`/opt/homebrew`.

```sh
prefix=$(brew --prefix)
sudo install -o root -g wheel -m 0755 "$prefix/bin/wireguard-go" /usr/local/libexec/hypermesh/wireguard-go
sudo install -o root -g wheel -m 0755 "$prefix/bin/wg" /usr/local/libexec/hypermesh/wg
```

### Optional Metal runner

For enrollment/heartbeat-only dogfood, skip this section and continue with the
LaunchDaemon setup below. Those operations do not need `llama-server`. Keep the
host paused for model use until a runner and model have been qualified. A Metal
`lease_start` without the runner fails with `llama-server not on PATH`.

For Metal experiments, build **llama.cpp v0.5.0**, pinned to commit
`7fe450e19305b828c199d602c23a8337aaa1f03b`. This is the same source revision as
the AGX workload baseline, compiled here for Apple silicon. It is a lab build
recipe; it does not establish Metal model compatibility or physical acceptance.
The agent's signed release does not include this third-party runner or a GGUF.

Complete the Command Line Tools checks above, then run these commands in a fresh
working directory on the Apple silicon Mac. Homebrew supplies CMake; the
llama.cpp source is pinned independently:

```sh
brew install cmake
LLAMA_CPP_COMMIT=7fe450e19305b828c199d602c23a8337aaa1f03b
git clone --depth 1 --branch v0.5.0 https://github.com/ggml-org/llama.cpp.git llama.cpp-v0.5.0
if [ "$(git -C llama.cpp-v0.5.0 rev-parse HEAD)" != "$LLAMA_CPP_COMMIT" ]; then
  printf '%s\n' 'llama.cpp revision mismatch; stop before building or installing.' >&2
  exit 1
fi
cmake -S llama.cpp-v0.5.0 -B llama.cpp-v0.5.0/build \
  -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_OSX_ARCHITECTURES=arm64 \
  -DBUILD_SHARED_LIBS=OFF \
  -DGGML_BACKEND_DL=OFF \
  -DGGML_METAL=ON \
  -DGGML_METAL_EMBED_LIBRARY=ON \
  -DGGML_OPENMP=OFF \
  -DLLAMA_OPENSSL=OFF \
  -DLLAMA_USE_PREBUILT_UI=OFF \
  -DLLAMA_BUILD_TESTS=OFF \
  -DLLAMA_BUILD_SERVER=ON
cmake --build llama.cpp-v0.5.0/build --config Release --target llama-server --parallel 4
file llama.cpp-v0.5.0/build/bin/llama-server
otool -L llama.cpp-v0.5.0/build/bin/llama-server
```

Stop if cloning, the revision check, configuring or building fails. Confirm that
`file` reports `arm64`. The static llama/ggml libraries and embedded Metal sources
let the runner move out of the build directory. Check `otool -L` for macOS system
libraries/frameworks only; a dependency on a build directory or Homebrew prefix
must be resolved before installing into the root-owned runtime directory.
See the [build instructions at the pinned revision](https://github.com/ggml-org/llama.cpp/blob/7fe450e19305b828c199d602c23a8337aaa1f03b/docs/build.md).

After the checks succeed, copy the built runner into the directory prepared
above, then check that it starts with the LaunchDaemon's restricted PATH:

```sh
sudo install -o root -g wheel -m 0755 llama.cpp-v0.5.0/build/bin/llama-server /usr/local/libexec/hypermesh/llama-server
env -i PATH=/usr/local/libexec/hypermesh:/usr/bin:/bin:/usr/sbin:/sbin /usr/local/libexec/hypermesh/llama-server --version
```

Record the commit and version output with the lab report. `--version` checks
binary startup only; it does not load a model or qualify a Metal lease. Updating
the runner later requires a new software inventory and model qualification.

### LaunchDaemon setup

Prepare the directories, the env file, and the two LaunchDaemons. Load
WireGuard first so `utun8` exists before the agent configures it.

```sh
sudo mkdir -p /etc/hypermesh /var/lib/hypermesh /etc/wireguard
sudo chmod 0700 /var/lib/hypermesh /etc/wireguard
sudo cp ./agent.env.example /etc/hypermesh/agent.env
sudo chmod 0600 /etc/hypermesh/agent.env
```

### Owner device-code registration

Prepare `/etc/hypermesh/agent.env` privately with the HTTPS control-plane URL,
matching tenant UUID and explicit host name. Keep `HYPERMESH_STATE_DIR=/var/lib/hypermesh`
and `HYPERMESH_WG_USERSPACE=1`. Leave `HYPERMESH_ENROLL_TOKEN` empty; do not set
`HYPERMESH_DEVICE_SECRET`, `HYPERMESH_REENROLL` or `HYPERMESH_WG_KEY_DIR`.
The owner path uses the reviewed plists unchanged and `utun8`. A customized utun
or plist requires explicit operator recovery; it cannot silently resume an
existing approval. Keep WireGuard and agent LaunchDaemons unloaded until approval.

Copy the verified plists **without bootstrapping them**:

```sh
sudo cp ./com.fyberlabs.hypermesh-wg.plist /Library/LaunchDaemons/
sudo cp ./com.fyberlabs.hypermesh-agent.plist /Library/LaunchDaemons/
sudo chown root:wheel /Library/LaunchDaemons/com.fyberlabs.hypermesh-{wg,agent}.plist
sudo chmod 0644 /Library/LaunchDaemons/com.fyberlabs.hypermesh-{wg,agent}.plist
sudo ./hypermesh-host register-agent \
  --issuer https://AUTH_HOST/realms/REALM \
  --dashboard-url https://portal.test.hyperme.sh \
  --start-services
```

Use the realm issuer configured for your environment; it must be a trusted
operator input. Sign in using the displayed device code, then separately open
the printed host-approval URL, review this host's hardware and approve it in the
portal. The CLI never approves its own request or attaches an existing device
by hostname. The test plane must expose the experimental class and approval API;
a missing class or API is a deployment blocker, not permission to use a Jetson class.

After server-confirmed completion, the CLI publishes `/var/lib/hypermesh/device.json`
and a private WireGuard key under `/var/lib/hypermesh/registration-keys/<request-id>/`.
No enroll token is needed. With `--start-services`, it bootstraps WireGuard,
waits for `utun8`, then bootstraps the agent. Without that flag it leaves both
services unloaded. A pre-existing unowned `utun8` blocks startup. This path never
stops or replaces a running service. The CLI's `registered-inactive` state
means the host stays paused and unqualified even when its local agent is running.

The final JSON includes a `plan_sha256` identifying this installed configuration.
The root-only registration journal uses the same schema and recovery identity
fields as Linux, under `/var/lib/hypermesh/installer/registration-<plan_sha256>.json`.
Its digest binds the installed agent/runtime binaries, plists, env file, stable
hardware, issuer and dashboard. These hashes detect changes after the initial
manual installation; use the signed-download checks above to establish its origin.
The reported model memory is a current conservative estimate, not a qualified
Metal allocation limit, benchmark or certificate.

After an interrupted login, pending approval, lost completion response or startup
failure, rerun the same command with the original configuration and owner account.
The existing request and keys are reused. To inspect local state without sign-in:

```sh
sudo ./hypermesh-host registration-status --plan-sha <plan_sha256>
```

For a denied or expired request, rerun with `--restart-approval`. The CLI requires
a fresh login as the original owner and a server-confirmed terminal decision,
archives the previous private request, and generates new keys requiring new portal
approval. Retirement, changed configuration, an existing unrelated identity and
conflicting key files require explicit recovery; do not delete journals or overwrite
`device.json`. Status and diagnostic output exclude private keys and credentials.

After this owner path, skip the secondary token and manual bootstrap commands
below. Use the [service logs](#service-observation) to verify actual enrollment and
heartbeats; tests and a successful CLI return do not establish live acceptance.

### Secondary token setup

Use this section for authorized non-owner/batch setup or interim Darwin dogfood,
not as a replacement for an available owner device-code approval flow.

1. Sign in to the correct portal and tenant with authority for the host's
   organization. Open [Register a box](https://portal.test.hyperme.sh/dashboard/hypermesh/host/enroll),
   select the experimental Apple silicon class `darwin-arm64-lab`, and use the
   displayed one-time token and tenant UUID. If the class is unavailable, stop
   and have an authorized operator check the deployed test class configuration;
   do not register a Mac as an Orin/Jetson class.
2. For operator/API minting, an authorized device writer calls
   `POST /api/v1/hypermesh/devices` with the matching `X-Tenant-ID`, authorized
   bearer session or scoped API key, and the class/serial/Path A request from the
   [agent HTTP contract](https://github.com/FyberLabs/panopticon/blob/main/products/hypermesh/README.md#agent-contract-http).
   Use truthful Apple hardware fields and the test-only Darwin class. Omit
   `host_org_id` to use the caller's verified host organization; an internal
   service token alone is not token-mint authority. The new-device response's
   `issued_enroll_token` is shown once. For explicitly authorized recovery of an
   existing device, `POST /api/v1/hypermesh/devices/{id}/enroll-token` returns
   `enroll_token`. Normal restarts reuse `device.json` instead of minting again.
3. Edit `/etc/hypermesh/agent.env` privately. Set `HYPERMESH_TENANT_ID` to that
   tenant UUID and `HYPERMESH_ENROLL_TOKEN` to the one-time token, retaining mode
   0600. Set `HYPERMESH_LICENSE_ACCEPT=yes` only after you accept the Terms, or
   enroll from a terminal with `hypermesh-agent enroll --license accept` before
   loading the agent. Do not paste a browser access token or the saved device secret here, or
   put any credentials in shell arguments, tickets or support logs.
4. Set `HYPERMESH_HOST_NAME` if the Mac's hostname is not the intended reported
   name. Leave `HYPERMESH_WG_USERSPACE=1`. Continue below to load WireGuard first,
   then the agent; the agent exchanges the enroll token for its saved device
   identity on first startup.

See the [host identity and token ownership rules](https://github.com/FyberLabs/panopticon/blob/main/products/hypermesh/docs/host-identity-lifecycle.md)
for host-organization authority and explicit recovery boundaries.

Confirm `utun8` is free (`ifconfig utun8` should fail). If it is in use,
pick another `utunN`, set `HYPERMESH_WG_IFACE` to that name, and change
the last argument of `wireguard-go` in
[`com.fyberlabs.hypermesh-wg.plist`](com.fyberlabs.hypermesh-wg.plist) to
the same name. Leave the program path as
`/usr/local/libexec/hypermesh/wireguard-go`.

```sh
sudo cp ./com.fyberlabs.hypermesh-wg.plist /Library/LaunchDaemons/
sudo cp ./com.fyberlabs.hypermesh-agent.plist /Library/LaunchDaemons/
sudo chown root:wheel /Library/LaunchDaemons/com.fyberlabs.hypermesh-wg.plist \
  /Library/LaunchDaemons/com.fyberlabs.hypermesh-agent.plist
sudo chmod 0644 /Library/LaunchDaemons/com.fyberlabs.hypermesh-wg.plist \
  /Library/LaunchDaemons/com.fyberlabs.hypermesh-agent.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/com.fyberlabs.hypermesh-wg.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/com.fyberlabs.hypermesh-agent.plist
```

Older macOS uses `sudo launchctl load -w` on those two plist paths
instead of `launchctl bootstrap system`.

### Service observation

On the secondary token path, the agent enrolls on startup when
`/var/lib/hypermesh/device.json` does not exist yet and the Terms have been
accepted. The owner path already
published that identity after portal approval. Watch the log:

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
| `HYPERMESH_ENROLL_TOKEN` | secondary first enroll only, until `device.json` exists | One-time token. Same field the Linux agent sends. |
| `HYPERMESH_LICENSE_ACCEPT` | before the first enroll | Set to `yes` to accept the Hypermesh Terms. Unset means not accepted. `hypermesh-agent enroll --license accept` is the other way. Install `--yes` does not set this. |
| `HYPERMESH_CONTROL_PLANE_URL` | no | Default `https://api.test.hyperme.sh`. |
| `HYPERMESH_HOST_NAME` | no | Empty uses the output of `hostname`. |
| `HYPERMESH_STATE_DIR` | no | Default `/var/lib/hypermesh`. |
| `HYPERMESH_WG_USERSPACE` | yes on a Mac | Set to `1`. `auto` also lands on userspace when no kernel module exists. Do not set `0`. |

For the secondary token path, a first startup without `device.json` requires
both `HYPERMESH_TENANT_ID` and `HYPERMESH_ENROLL_TOKEN`. Empty or whitespace-only
values stop startup with one error naming the missing keys, before enrollment
requests, telemetry collection or enrollment-key creation. Fix the env file,
then restart the agent. A saved device identity needs no enroll token; it still
needs the tenant ID. Whitespace in an unused token does not force re-enrollment.

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
