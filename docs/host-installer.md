# Pull-based host installation

Status: approved design; implementation started. This is the installation
roadmap, not a claim that a guided installer already exists. The existing
`hypermesh-deploy inspect` detects hardware; the existing `agx` deployment
command retains its legacy preflight and configuration behavior.

## Scope before AGX-1 acceptance

Approved on 2026-09-30: finish the Linux onboarding and maintenance work before
the operator attempts the AGX-1 installation. Workload operating modes and
macOS support are deferred; device authorization is **not** deferred with the
workload-mode section where it was originally recorded.

Complete and validate each milestone before starting the next:

1. UMA runtime capacity and OS/runtime reserves.
2. Versioned Linux hardware profiles and exact package/driver/kernel plans.
3. Plan-specific approval, unattended application, checkpoint/reboot resume,
   headless configuration and systemd installation.
4. Verified installer update, recovery/repair/uninstall, status and redacted
   support bundles.
5. Device authorization and fresh-key enrollment with authenticated AIuditor
   compatibility, connectivity and benchmark gates before selling.
6. Independent bootstrap trust and hyperme.sh download integration, then the
   full automated installation/recovery acceptance suite.

Physical AGX-1 installation is the final acceptance run, not something claimed
complete before the machine is available. N5 MAX and other Linux targets need
their own measured acceptance; no profile is certified by its product name.
Website and control-plane dependencies stay on this delivery checklist.

## Approved behavior

An operator downloads a signed host installer. Before enrollment or privileged
changes, it checks internet connectivity to the selected AIuditor, detects
CPU/GPU architecture and memory topology, measures resources, and presents an
installation plan. A familiar model name may select a candidate profile; it
does not prove compatibility or grant certification.

Resource minimums use **GiB (2^30 bytes)** consistently:

- At least 16 GiB installed system memory.
- At least 12 GiB total discrete VRAM, or a proposed UMA allocation of at least
  12 GiB backed by currently available system RAM.
- Free space on the chosen model-cache filesystem at least **2.2 times**
  discrete VRAM or the proposed UMA allocation, rounded up to whole bytes.
  This is disk capacity, not an additional system-RAM requirement.
- UMA allocation must respect measured runtime limits and leave the OS and
  other services adequate headroom. Never infer allocatable memory solely
  from the 64/128 GB product label or double-count firmware-reserved VRAM.
- Aggregate GPU capacity is only an admission gate. Preflight must demonstrate
  that the runtime supports the selected GPUs and model distribution.
  A 12 GiB capacity result alone does not prove an 11–12 GB model fits with
  its context, runtime buffers, and working memory.

Unknown architecture, unknown capacity, inaccessible cache storage, or failed
AIuditor connectivity must produce a named blocker and a concrete next action.
An HTTP response alone is not proof of AIuditor identity: distinguish DNS/TLS
reachability from authenticated protocol compatibility and enrollment.

## Installation stages and approvals

1. Inspect locally without root; choose environment, reported host name,
   model directory, memory allocation, and candidate hardware profile.
2. Verify signed bootstrap metadata and installer checksum with a bundled
   trust root, then update the installer to the selected channel's version.
   Record that version and the immutable profile revision in the plan.
3. Present exact package, library, driver, kernel, boot, service, headless, and
   reboot changes. Default to headless operation only after explicit approval.
   Preserve SSH/network access and unrelated services.
4. Apply the approved plan. Required changes declined by the operator stop
   with an explanation; optional changes can be skipped. Never silently use
   a general “yes” to approve a materially changed plan.
5. Persist progress atomically; resume after interruption or reboot, rechecking
   hardware and completed steps. Repeated execution converges without duplicate
   units, identities, keys, or package sources.
6. Enroll with a locally generated key and one-time credential, verify tunnel
   and heartbeat connectivity, and benchmark the actual runtime/model.
   Keep selling disabled until all required checks pass.

Support interactive confirmation and unattended execution of a previously
reviewed, versioned plan. A changed plan requires fresh approval. Do not put
tokens, private keys, or passwords in process arguments or diagnostic output.

The existing authenticated agent-update endpoint is for enrolled hosts.
Bootstrap discovery before enrollment needs its own defined trust and release
contract; it must not assume that endpoint is anonymously available. Updating
the installer does not mean upgrading every driver to “latest”: profiles pin
compatible OS/kernel/runtime combinations with recorded validation.

## Repeatability, recovery, and operator visibility

Keep a journal containing installer/profile versions, detected facts, approved
changes, completed steps, reboot requirement, and failure/remediation details.
Back up managed configuration, retain a bootable prior kernel, and provide
repair, resume, and uninstall paths. Do not promise automatic rollback of every
driver/kernel transition. Uninstall must explicitly distinguish removing
software from retiring a host identity and deleting cached models.

Status should show current stage, blockers, what changed, and what to do next.
Provide a redacted support bundle and structured diagnostics. Retiring a host
archives its prior identity; reenrollment generates a fresh key/setup.
Hardware fingerprints describe machine capabilities, never durable identity.

For NAS appliances, use an explicitly chosen existing cache filesystem.
Never partition/format disks or alter storage pools, shares, or NAS services
as an incidental installer step. Headless mode means graphical login is
disabled with consent, not that other services are disabled.

## Implementation tracking

- [x] Existing hardware inspection and backend recommendations.
- [x] Resource admission policy in `internal/installer/admission.go`, with
  boundary, UMA, invalid-input, and overflow test cases.
- [x] Connect resource admission to Linux RAM, GPU capacity and filesystem probes
  through the read-only CLI (operator explicitly selects memory topology).
- [x] Query integrated CUDA/HIP runtime capacity and enforce OS/runtime reserves.
- [ ] Validate actual allocation and model/context fit on target hardware.
- [x] Read-only CLI preflight plan with resource/hardware blockers and HTTPS
  endpoint reachability checks.
- [x] Device-authenticated control-plane protocol and registration identity check, with a redacted readiness snapshot.
- [x] Authenticated AIuditor tenant/class program membership check through the plane, without local fallback.
- [x] Explicit, recoverable one-shot device heartbeat with committed acknowledgment.
- [x] Explicit inactive WireGuard handshake and gateway overlay data-path probe.
- [ ] Workload compatibility checks and physical WireGuard acceptance.
- [x] Signed bootstrap verification, channel discovery and persistent build floor.
- [x] Signed agent download for both Linux architectures, with optional exact installer-release binding.
- [ ] Reviewed installer self-update.
- [x] Versioned candidate Linux profiles and exact prerequisite package/headless plans.
- [ ] Hardware validation of profiles and complete agent/service/apply plans.
- [x] Exact-digest approval and journaled apply/resume for prerequisite packages and headless target.
- [x] Approved first installation of verified agent, rollback helper, update trust, configuration and inactive systemd units; journaled retry/status.
- [x] Device login and browser-approved registration CLI, fresh-key persistence, owner-bound recovery and redacted registration status.
- [ ] Generic hardware-class admission for AMD/N5 and additional platform classes.
- [ ] Enrollment-gated service activation, guided approval and automatic reboot/resume.
- [ ] Package/driver/kernel installation, headless setup, systemd and reboot.
- [x] Redacted installer support bundle with stage summaries and recovery guidance.
- [ ] Repair/uninstall recovery commands and unified live status presentation.
- [ ] Enrollment plus benchmark/connectivity gates before selling.
- [ ] Hardware acceptance runs below.

The admission package is wired into `hypermesh-deploy check-resources`.
Its eligible result means only that measured resource capacity passes the
minimums, not that installation or selling is approved.

## Resource-check milestone

Completed: a read-only Linux command emits a schema-versioned JSON report
with measured resources, required disk bytes, eligibility and failure reasons.
Exit codes: 0 capacity passed, 1 failed/unknown measurement, 2 invalid arguments.
It creates no directories, installs nothing, and does not enroll a host.

Build from the reviewed checkout:

```bash
go build -o hypermesh-deploy ./cmd/hypermesh-deploy
./hypermesh-deploy check-resources --memory-mode uma --uma-gib 40 --model-dir /existing/model/cache
```

Use UMA mode on both the AGX and N5 MAX. The 40 GiB allocation above is an
example, not a validated default. Choose an allocation that fits current free
RAM and intended workload; actual GPU allocation still needs runtime preflight.
For example, a proposed 80 GiB allocation requires 176 GiB free cache disk.

For a discrete GPU host:

```bash
./hypermesh-deploy check-resources --memory-mode discrete --model-dir /existing/model/cache
```

RAM comes from Linux MemTotal/MemAvailable, which describe OS-visible memory
rather than the product label. Disk uses filesystem space available to
unprivileged users (Bavail), on the explicit existing directory. Quotas,
write permissions and runtime allocations still need later preflight.
NVIDIA capacity uses nvidia-smi memory.total; otherwise AMD capacity uses
DRM mem_info_vram_total. NVIDIA is selected first on mixed-vendor machines;
capacities from different backends are not combined. Missing drivers or
unreadable capacity fail with remediation. An integrated GPU's reserved
VRAM must not be treated as discrete capacity: select UMA mode explicitly.
Memory topology is operator-selected at this stage, not automatically verified.

Automated coverage includes command arguments/exit codes/JSON, failed probes,
disk boundaries, malformed and overflow measurements, multi-GPU summation,
and a real Linux RAM/filesystem probe that checks no directory was created.
AGX/N5 physical results remain pending. The next milestone is a read-only
installation plan with architecture and AIuditor checks; package installation
and reboot orchestration have not started.

## Required hardware acceptance

Both targets are pending real-machine validation. No device UUID is part of
a profile or test fixture. Select hosts by their reported enrollment name.

| Target | Initial profile investigation | Required evidence |
| --- | --- | --- |
| AGX 64 | Exact module/carrier, JetPack/L4T, kernel, integrated GPU and usable UMA | Inventory, approved plan, free RAM/disk, runtime allocation, benchmark, reboot/resume, heartbeat and workload |
| MINISFORUM N5 MAX, 128 GB, Ryzen AI Max+ 395 | Firmware, Linux support, AMD GPU architecture, ROCm compatibility, UMA limits and NAS service preservation | Same evidence, plus storage/share integrity and GPU allocation under memory pressure |

For each target run: clean install, no-op rerun, interrupted package step,
interrupted enrollment, reboot/resume, supported upgrade, declined required
change, unreachable AIuditor, insufficient RAM/disk, repair, and uninstall.
Verify a real model workload and confirm selling remains blocked on failures.
Record OS/firmware/kernel/driver/runtime versions, installer commit, plan,
redacted logs, and measured results before marking a profile tested.
Certification remains a separate explicit decision.

Consult vendor compatibility documentation when selecting candidate versions:
[AMD Ryzen Linux compatibility](https://rocm.docs.amd.com/projects/radeon-ryzen/en/latest/docs/compatibility/compatibilityryz/native_linux/native_linux_compatibility.html),
[MINISFORUM N5 MAX](https://store.minisforum.com/products/minisforum-n5-max-ai-nas).
Do not copy a version from a moving documentation page into a certified profile
without testing it on the target machine.

## Read-only preflight plan milestone

Implemented: `hypermesh-deploy plan` combines measured resource admission,
hardware inspection/recommendations, and a bounded HTTPS readiness request.
It reports all preliminary blockers together plus the remaining installation
gates. Exit 0 means preliminary checks passed; it does not authorize installation.
Exit 1 means a check failed, and exit 2 means invalid arguments.

```bash
./hypermesh-deploy plan --memory-mode uma --uma-gib 40 \
  --model-dir /existing/model/cache \
  --aiuditor-check-url https://YOUR_AIUDITOR/ITS_READINESS_PATH > preflight.json
```

Replace the URL with the selected deployment's actual readiness endpoint;
there is deliberately no invented default path. Requests require trusted TLS,
time out after ten seconds, reject redirects, and require HTTP 2xx. URLs with
credentials, query parameters or fragments are rejected. Response bodies are
not stored. The command changes no packages, keys, disks or system services.

The JSON includes hardware recommendations, resource evidence, blockers,
`preliminary_checks_passed`, and `install_authorized: false`. A detected
accelerator backend is a preliminary signal; it does not certify the GPU
architecture, driver version or workload compatibility. Missing drivers are
reported as blockers to investigate before creating a mutation plan.
Endpoint reachability is not authenticated AIuditor identity. Those checks,
validated profiles, exact change approval and runtime memory validation remain
explicit pending gates. This plan is diagnostic evidence, not an unattended
installation approval file.

Automated tests cover aggregated failures, pass/blocked exit status, protected
URL rejection, trusted/untrusted TLS, HTTP errors, redirects and cancellation.
Physical AGX 64 and N5 MAX acceptance remains pending. Complete runtime memory
validation and authenticated discovery before starting installation mutations.

## Planned end-to-end acceptance after roadmap completion

The test Panopticon plane has been rebuilt. Once the host installer roadmap is
implemented, the operator will download the ARM host package from hyperme.sh,
run it on the AGX, and enroll it against the test plane as `AGX-1`.
This requires a website download path, signed ARM installer packaging,
bootstrap verification/update, approved preflight changes and enrollment to
work as one flow. The current agent release and diagnostic CLI alone do not
satisfy this acceptance target.

Use the newly reported name for discovery. Update the test smoke host setting
to `AGX-1` when that host is enrolled; do not retain `agx64-1` as an identity
assumption or seed a device UUID. Physical enrollment and smoke acceptance
remain pending that operator run. The N5 MAX is the second hardware target.

## Approved operating modes, billing and device authorization

Status: approved requirements; implementation pending.

| Mode | Model state | Billing |
| --- | --- | --- |
| On-demand | No model kept running while idle; set up the requested model for an active lease | 15-minute rental slices, including the time to download, load and prepare a hostable model. |
| Warm model | Owner chooses a specific model to keep running and ready | One-minute leases or token-based charges once pricing and metering support them end to end. |

Additional setup time is the intended cost differential for custom models,
rather than an arbitrary custom-model surcharge. Cached files are not a live
model; caching may reduce actual setup time. Show renters the setup estimate,
billing start point and minimum charge before acceptance. Track setup time
separately from ready-to-serve time. Define refunds for host/platform-caused
provisioning failures before enabling setup billing. Exact rounding, overruns,
refund and token-metering rules remain to be specified.

### Approved automatic registration through device authorization

The owner does not pre-create a device in the dashboard. After local admission
and approved preflight changes, the installer displays a short device code and
verification URL. The owner opens the URL on another device, signs in, confirms
the intended tenant and the enrollment request, and approves registration.
The installer then registers the actual box and it appears in the dashboard
automatically. No copying a dashboard UUID into the installer or comparing the
machine with a placeholder device is part of onboarding.

The approval must identify the pending installation and its reported host name
and hardware summary. A standard identity-provider device login establishes
the user's identity; it does not by itself approve hardware claims or grant
ownership of an arbitrary organization. Panopticon must validate tenant
membership and derive the authorized host organization on the server before
creating the dashboard entry. If the identity-provider approval page cannot
show the enrollment details, the application must provide that confirmation
within the browser flow.

Use the existing public `hypermesh-native` device-authorization client.
Do not introduce a client secret, collect the owner's password in the terminal,
or require copying a user access token. Authorization expires and binds to the
pending enrollment. Keep device codes and temporary user tokens out of logs,
plans and support bundles; display only the verification URL and user code.

Generate the host's private key locally and register only its public key.
The server assigns the device UUID. Reported host names and hardware
fingerprints are labels and compatibility evidence, not proof of ownership or
identifiers used to adopt an existing device. A matching name must never
silently attach an installation to another host's credentials.

Registration creates an uncertified, unavailable-for-rental dashboard entry.
Show its current onboarding stage, hardware observations, outstanding checks,
failure reason and the action needed to proceed. Registration alone does not
enable selling, mark a hardware class certified or bypass authenticated
AIuditor, tunnel, benchmark and service-activation gates.

Persist enough private local enrollment state to recover an interrupted
attempt. A lost registration response, process restart or retry must recover
the same authorized device without duplicating it or rotating its working
credentials. Enforce public-key uniqueness across tenants, including concurrent
registrations. Reject attempts to reuse another device's identity with a clear
recovery action. An owner-requested retirement archives the old device and
revokes its access; re-adding the box creates fresh keys and a new server-assigned
identity.

This is the approved interaction and acceptance contract. The bounded device-flow
protocol client is implemented in `internal/deviceauth`; the automatic
registration endpoint, dashboard confirmation/status and installer integration
remain implementation work. The inactive installed agent must not be advertised
as having this end-to-end flow yet.

Implementation and acceptance tasks:

- [ ] Add on-demand/warm-model selection to onboarding and host dashboard.
- [ ] Implement on-demand preparation only for active leases and 15-minute slices.
- [ ] Disclose and meter billable setup; test cached/uncached preparation and
  approved refunds for host/platform provisioning failures.
- [ ] Advertise the specific warm model and verified readiness; handle mode
  changes without disrupting active leases.
- [ ] Implement and reconcile one-minute lease charges and token metering
  before advertising those options.
- [x] Approve automatic dashboard registration through device authorization, without pre-created devices or name/UUID matching.
- [x] Implement the bounded device-authorization protocol client, safe URL/code display and pending/slow-down polling.
- [ ] Connect browser tenant/host confirmation and device authorization to the installer registration flow.
- [ ] Register the host automatically under the server-authorized organization and display its onboarding state in the dashboard.
- [ ] Make registration retry-safe across lost responses, restarts and concurrent requests; test tenant and public-key ownership checks.
- [ ] Test pending, denied, expired, cancelled and replayed authorization.
- [ ] Test restart/resume without duplicate enrollment and keep credentials out of logs.
- [ ] Complete ARM download from hyperme.sh through browser approval to enroll
  AGX-1 against the rebuilt test plane; repeat acceptance on the N5 MAX.

These tasks span the host installer/agent, Panopticon authorization, lease and
metering services, pricing, and hyperme.sh interfaces. An installer flag alone
does not complete an operating mode. Physical acceptance remains pending.

## Signed operator CLI release payload

Implemented in the release publisher: alongside the agent, publish
`hypermesh-deploy_<version>_linux_arm64` and
`hypermesh-deploy_<version>_linux_amd64`. Both binaries are included in the same
GPG-signed checksums, blob release directory, channel artifact manifest, and
GitHub release. Existing production releases retain their original signed
artifact set when retried; adding the CLI requires a new version.

- [x] Build and checksum operator CLI binaries for Linux ARM64 and AMD64.
- [x] Include the CLI in new release signing, upload and integrity verification.
- [x] Verify the first post-merge release contains both CLI assets: test build
  21-dc11f597a21e, release run 36802906760; merged CI and release passed.
- [ ] Wire the hyperme.sh package/download experience to verified artifacts.
- [ ] Provide signed bootstrap discovery/self-update and complete guided install.

This completes the binary payload stage, not the full installer. The CLI
currently provides inspection/preflight and the legacy AGX deployment command;
it must not be advertised as the finished guided onboarding flow.
Do not run a downloaded binary until its checksum manifest signature is checked
against an independently trusted release key. A public key downloaded alongside
the binary alone does not establish trust.

CI builds all four Linux binaries using the same helper as publication,
checks their ELF architecture and checksum membership, and proves tampering
with the ARM deploy binary fails checksum verification. Actual AGX/N5 execution
and the website-to-enrollment acceptance flow remain pending.

## Verified CLI download milestone

Implemented: `hypermesh-deploy download` fetches a selected release's deploy
binary without enrollment, using a separately supplied trusted public key and
fingerprint allowlist. This is the verified download primitive, not automatic
latest-release discovery or self-replacement.

```bash
./hypermesh-deploy download \
  --release-url https://YOUR_RELEASE_SERVER/IMMUTABLE_RELEASE_DIRECTORY \
  --channel test --min-build 21 --arch arm64 \
  --trusted-key /path/to/independently-trusted-release.pub \
  --fingerprints YOUR_TRUSTED_FINGERPRINT \
  --output ./hypermesh-deploy.verified
```

Use the actual release directory, which must serve checksums.txt, its binary
detached signature, release.json and the deploy binary. No endpoint is assumed
to be public or anonymously accessible. HTTPS requests have size/time limits;
the original URL may not carry credentials or query parameters. HTTPS redirects
are allowed for asset delivery, with a bounded count. Error messages exclude
URLs and server response bodies.

The signature is checked with the existing GPG verifier before signed metadata
is used. The downloader checks release.json's hash, channel, build floor, commit,
version and release ID, then verifies the chosen architecture's binary hash.
The caller supplies the build floor: zero permits any positive build and
does not establish freshness. Persisted rollback protection and authenticated
latest-release discovery remain separate tasks.

Only verified bytes are published, with owner-executable permissions, through
an atomic no-overwrite link. Existing files and symlinks are not replaced.
The output directory must exist and support hard links. Downloads never run
the new CLI, install packages, alter services or enroll a device.

- [x] Verified, bounded CLI download with channel/build constraints and no-overwrite output.
- [ ] Ship trusted bootstrap key material through an independently trusted channel.
- [x] Add latest-release discovery and persisted minimum-build state.
- [ ] Add reviewed self-update/restart and rollback behavior.
- [ ] Integrate website package delivery and full device authorization enrollment.

Tests cover signature rejection, metadata/binary tampering, wrong channel,
older builds, existing destinations, staging cleanup, size limits and
cancellation. The existing GPG verifier suite supplies cryptographic coverage.
Physical AGX/N5 validation remains pending.

## Persistent accepted-build tracking

Implemented: add `--state-file /owner-controlled/directory/build-state.json`
to verified CLI downloads. The effective minimum is the larger of the explicit
`--min-build` and the persisted highest accepted build. One floor spans test
and production, so switching channels cannot lower it. Same-build retries are
allowed; older builds are rejected before publishing an executable.

The state is optional for one-off downloads, but required for the future
automatic bootstrap/update flow. Omitting it provides only the explicit build
floor. Keep using the same state path; deleting or changing it removes this
local rollback protection. It is not a defense against a privileged local user.

A directory lock serializes downloads sharing the state file. State writes use
a private temporary file, fsync and atomic rename. State is committed after
signature/hash checks and before exposing executable bytes. If output creation
then fails, the floor may advance without a new binary; retry that same build
or a newer one. No automatic lowering is performed.

An interrupted process can leave the `.lock` directory. Confirm no downloader
is running before removing that empty lock directory. Never remove the state
file as lock recovery. Invalid, unreadable, symlinked or broadly readable state
files fail closed; restore a trusted record rather than silently resetting.
Use an owner-controlled local directory that supports rename, links and fsync.

- [x] Persist the accepted build floor across restarts and channels.
- [x] Test concurrent-lock rejection, restart persistence, same-build retry,
  older-build rejection, invalid state and private file permissions.
- [ ] Authenticate latest-release discovery and integrate the persisted floor
  into automatic installer updates.

This completes local build tracking; automatic discovery, self-replacement and
full guided onboarding remain pending.

## Automatic channel discovery milestone

Implemented: `download --channel-root https://YOUR_RELEASE_ROOT` reads
`channels/test.json` or `channels/prod.json`, derives the immutable release
directory from validated channel/release identity, and feeds it through the
existing signature and checksum verifier. Use it instead of `--release-url`.
Discovery requires `--state-file`, independently trusted key material, an
explicit channel and an initial `--min-build`.

The channel pointer is a locator, not a signed authority. All five identity
fields (channel, version, build, commit, release ID) must match signed
release.json before a binary is published or build state changes. Artifact
URLs in the pointer are never followed. Old heads below the stored floor,
malformed metadata, unsafe release IDs and mismatched signed metadata fail
closed. A replay at or above the known floor can still be an older channel
head: this mechanism does not prove global freshness without a trusted newer
build record.

- [x] Discover channel candidates and bind them to verified release metadata.
- [x] Require persistent build tracking for automatic channel discovery.
- [ ] Expose the bootstrap release root through the website/control-plane
  delivery contract with the required access policy.
- [ ] Provision independent bootstrap trust, self-update and reviewed install.

No public storage access or invented service endpoint is assumed. The configured
HTTPS root must serve the existing publisher layout. Discovery fetches files
only and does not execute the CLI, enroll a host or modify system services.

## Expanded local hardware acceptance matrix

These are planned test-box targets supplied by the owner, not claims that the
product names, OS/runtime combinations, or configurations are certified.
Record the actual CPU/GPU identifiers, firmware, OS and usable memory when each
box arrives; refine profile names from measured hardware.

| Planned target | Intended package family | Validation focus |
| --- | --- | --- |
| AGX 64 / AGX-1 | Linux arm64 | JetPack/L4T compatibility, UMA allocation, ARM bootstrap and reenrollment |
| Seeed J4012 Jetson | Linux arm64 | Actual module/RAM, admission minimums, power/thermal constraints and vendor stack |
| MINISFORUM N5 MAX, Ryzen AI Max+ 395, 128 GB | Linux amd64 (x86-64) | AMD GPU/runtime, UMA limits, preserve NAS storage and services |
| Planned 495 system, 192 GB | Linux amd64 if detected x86-64 | Confirm exact processor/GPU and vendor-supported runtime before profile selection |
| Intel x86-64 hosts | Linux amd64 | Actual GPU/backend and memory topology; CPU vendor alone does not establish accelerator support |
| Planned Mac mini M5 Pro, 64 GB | macOS arm64 | Confirm hardware/OS, Metal allocation limits and headless operation |
| Planned Mac mini M6, 32 GB | macOS arm64 | Confirm hardware/OS, OS/runtime memory reserve and practical model fit |
| Larger Apple Silicon Mac Studios | macOS arm64 | Record exact model/RAM, Metal compatibility and sustained workload behavior |

AMD and Intel x86-64 use the same `amd64` instruction-set package name;
RAM capacity belongs in measured profiles, not separate binary names. Linux
Jetsons and Apple Silicon both use ARM64 but require different OS binaries.

Mac support needs its own complete task sequence:

- [ ] Build and publish Darwin arm64 agent and deploy artifacts with signatures.
- [ ] Add macOS resource admission and Metal allocation/headroom checks.
- [ ] Define supported macOS versions, launchd service management, key storage,
  headless behavior and approval/recovery steps without Linux systemd assumptions.
- [ ] Implement installer signing/notarization and website delivery for macOS.
- [ ] Validate enrollment, connectivity, benchmark, lease lifecycle and restart
  recovery on each available Mac profile before advertising support.

Current signed deploy downloads select Linux binaries only. Existing Mac
inspection code is not a complete supported installer. New AMD/Intel and Jetson
profiles also remain pending measured runtime and end-to-end acceptance.

## First-install bootstrap without an existing CLI

Implemented: `packaging/bootstrap.py` uses Python 3 standard libraries and
GPG to download and verify the Linux deploy CLI. The publisher includes this
script in new releases and their signed checksum manifests. No existing
Hypermesh CLI or third-party Python packages are needed.

Obtain the bootstrap script through a trusted distribution channel. Supply
an independently approved public key and full fingerprint; the script never
trusts a key merely because it accompanies a downloaded release. It checks
the checksum signature, signed release identity/channel/build floor and binary
hash, then atomically creates a new owner-executable output without overwriting
files or symlinks. It never executes the binary or enrolls the host.

```bash
python3 bootstrap.py --release-url https://YOUR_RELEASE_SERVER/RELEASE_DIRECTORY \
  --channel test --min-build 24 --arch arm64 \
  --trusted-key /path/to/approved-release.pub --fingerprint APPROVED_FINGERPRINT \
  --output ./hypermesh-deploy
```

Use `--arch amd64` for Linux AMD/Intel x86-64. This is a Linux CLI download,
not the Mac installer. Python and GPG must already be installed; missing tools
fail with a message and are not installed without approval. Initial downloads
use the explicit minimum build; subsequent CLI downloads should use the
persistent state file and channel discovery. The initial trusted script, key,
fingerprint and release location still need to be delivered by the website.

- [x] Standalone first-install verification without an existing CLI.
- [x] Include bootstrap script in release checksums and assets.
- [x] Real-GPG tests for trusted signatures, wrong keys, altered data and
  signature failures, plus rollback/channel rejection and output preservation.
- [ ] Provision the approved public key/fingerprint into bootstrap distribution.
- [ ] Complete the hyperme.sh download page and release delivery endpoint.
- [ ] Verify the first release containing bootstrap.py and perform AGX acceptance.

At implementation time, the committed infra update-key and fingerprint lists
are empty. Their approved independent source is pending operator input.
Do not describe the website bootstrap as ready until those trust and delivery
items are complete. Do not add the private signing key to a host package.

Bootstrap artifact transfers have a 120-second end-to-end deadline covering
connection setup, redirects, response headers and the entire body, in addition
to the 30-second socket timeout and size limits. A Unix main-thread interval
timer interrupts trickling responses; the timer and handler are restored after
each transfer. Unsupported timer contexts fail closed. Regression tests cover
a continuously trickling local server and delayed response setup.

## Linux UMA capacity and headroom

Implemented in both `check-resources` and `plan`: query a single integrated
CUDA or HIP device through the installed driver, then resample Linux
MemAvailable after runtime initialization. Python 3 with its standard-library
ctypes module is required; the probe is embedded in the CLI and needs no
separate script installation. The child runs in Python isolated mode with a
ten-second deadline and bounded output. It creates only a temporary driver
context; it does not allocate the proposed model budget or change system
configuration.

The preliminary UMA budget is the smaller of:

- MemAvailable minus ceil(10% of OS-visible system RAM), with a 2 GiB minimum
  host reserve.
- Runtime-reported free device memory minus 1 GiB runtime reserve.

These are conservative installer policy reserves, not vendor guarantees.
The report includes the runtime/backend/device evidence, both reserves and the
resulting maximum allocation. Disk still requires 2.2 times the proposed
allocation. Firmware carve-outs are not added back to either memory reading.
Missing drivers, query errors, discrete devices in UMA mode, ambiguous multiple
integrated devices and insufficient capacity fail with remediation.

CUDA uses the integrated-device attribute and driver memory query; HIP uses
its own integrated-device attribute and memory query (the enum values differ).
See the [CUDA device API](https://docs.nvidia.com/cuda/archive/12.0.1/cuda-driver-api/group__CUDA__DEVICE.html)
and [HIP runtime API definitions](https://github.com/ROCm/HIP/blob/rocm-6.4.3/include/hip/hip_runtime_api.h).
Driver initialization may consume transient memory. Queries are capacity
snapshots, not a promise that a single allocation or a model with its context
will fit. Actual allocation, benchmark and compatibility verification remain
mandatory before enabling selling; Vulkan-only UMA is not yet measured by
this probe.

Automated coverage includes CUDA/HIP selection and errors, unknown/ambiguous
hardware, context cleanup, reserve boundaries, CLI failure without runtime
evidence, cancellation and output bounds. Physical AGX/N5 results are pending.

## Versioned Linux prerequisite plans

Implemented: `hypermesh-deploy plan-setup` runs the existing resource/hardware
and readiness checks, then builds a reviewable JSON proposal for prerequisite
packages and optional headless boot configuration. It makes no system changes
and never sets `install_authorized`. Exit 0 means the proposal is reviewable;
exit 1 means blocked and exit 2 means invalid arguments.

```bash
./hypermesh-deploy plan-setup --list-profiles
./hypermesh-deploy plan-setup --memory-mode uma --uma-gib 40 \
  --model-dir /existing/model/cache \
  --aiuditor-check-url https://YOUR_AIUDITOR/ITS_READINESS_PATH \
  --profile auto --headless > setup-plan.json
```

The revision-3 policies cover Ubuntu 22.04/24.04 with measured Jetson ARM64
CUDA UMA, AMD x86-64 HIP UMA, or discrete CUDA on Linux ARM64/x86-64.
These are **candidate policies**, not validated driver compatibility matrices.
Names and installed RAM labels cannot override architecture/runtime matching.
Manual profile selection must match the same measured facts as automatic
selection. Other OS/runtime combinations stop with a blocker.

The planner records the running kernel, detected hardware, installer commit/
version/build, profile revision, package-state digest and exact installed
protected package versions. A SHA-256 digest binds those facts, resource
evidence, proposed changes and blockers. It is an integrity identifier for
future approval handling, not a signature or authorization. Regeneration can
change it when resource measurements or system state change.

The first policy preserves the package-managed vendor stack: Jetson requires
an installed nvidia-l4t-core package, AMD requires hip-runtime-amd, and discrete
CUDA requires a nvidia-driver package. Docker must already be supplied by
docker.io or docker-ce. Manual/vendor installations outside those packages
need an explicit future policy; the planner does not replace them.
No OS, kernel, driver, power-mode or GPU-clock upgrade is proposed. The old
AGX command's fixed version pins are not reused as certification evidence.

Shared prerequisites are ca-certificates, gnupg, python3, wireguard-tools,
iproute2 and iputils-ping.
The explicit \`--wireguard-mode kernel\` default matches the agent\'s kernel path.
Only \`--wireguard-mode userspace\` adds wireguard-go. Mode is recorded in the
plan and its digest; the later apply stage must render the same mode into
agent/service configuration. Profile names do not force userspace mode, so
kernel-capable Jetson/J4012, AMD and discrete hosts do not need wireguard-go.
Kernel module/tunnel readiness still needs verification before enrollment. Installed native packages remain unchanged. Local APT metadata
supplies candidates; the planner does not refresh sources or download packages.
It simulates required versions, pins the complete dependency closure, and
simulates again to confirm that the displayed version-pinned argv reproduces
the same transaction. This uses APT's documented
[simulation operations](https://manpages.debian.org/testing/apt/apt-get.8.en.html).
Removals, changes to already installed packages, partial dpkg state, protected
kernel/driver/network/boot/storage changes and changing dependency resolution
block planning. Fix those separately, then generate a new proposal.
Package versions are pinned here; artifact trust and apply-time revalidation
still belong to the apply stage.

The only proposed headless change is `systemctl set-default multi-user.target`
when `--headless` was requested and the current target is graphical. It is
optional and requires later explicit approval. No target isolation, reboot,
SSH change, storage operation or service shutdown runs during planning.
APT maintainer scripts can restart services when packages are eventually
installed; the apply/recovery stage must account for that effect.

Automated tests cover profile matching, rejecting operator overrides and
unsupported OS/runtime combinations, exact dependency closure, installed/
protected package preservation, malformed metadata, no-op plans, headless
intent, system-state digest changes and CLI output. Actual profile validation
and complete approved application are still pending. Next: bind verified agent,
rollback helper, configuration and systemd payloads, then implement approval
and journaled application/resume against a freshly revalidated plan.

## Approved prerequisite application and resume

Revision-3 profiles bind the constrained noninteractive application arguments.
Implemented: schema-2 setup plans record the readiness URL and headless intent
as well as the exact prerequisite operations. Generate a fresh plan with the
same installer build that will apply it. Older schema-1 plans must be
regenerated; they did not bind all revalidation inputs.

Review the JSON, including package versions, maintainer-script implications,
WireGuard mode and any headless change. Then provide its exact
`plan_sha256` explicitly:

```bash
sudo ./hypermesh-deploy apply-setup --plan setup-plan.json --approve-plan REVIEWED_PLAN_SHA256
sudo ./hypermesh-deploy setup-status --plan-sha REVIEWED_PLAN_SHA256
```

The approval digest is not a secret or signature. Root access plus this exact
reviewed digest authorizes only these prerequisite operations. There is no
blanket yes flag, implicit acceptance of a revised plan, shell evaluation or
arbitrary command execution from JSON. Unknown/duplicate JSON fields, altered
content, stale profile/installer versions and blocked proposals are rejected.
This command does not install the agent, write enrollment credentials, enroll,
enable selling, restart services directly, or reboot.

Before applying, the installer reruns resource/hardware/HTTPS preflight,
checks OS/kernel/boot settings, compares installed package state and simulates
the exact remaining package pins. Free-memory readings may fluctuate but must
still pass admission; hardware/runtime identity and allocation must match.
Unrelated package drift or a changed dependency closure requires a newly
reviewed plan. An approved package already installed at its exact version is
recognized on resume rather than installed again.

APT runs with exact pins, no removals/upgrades/recommends, authenticated
packages, a noninteractive frontend and preservation of existing configuration
files. Root's configured APT repositories remain authoritative. Do not run
other package managers or change APT metadata concurrently: the installer lock
serializes Hypermesh applications, while APT manages its own package locks.
Post-install inventory must contain exactly the approved additions; unexpected
state stops before any headless change. Maintainer scripts can have service
side effects even though the installer itself does not restart those services.

The default private journal directory is
`/var/lib/hypermesh/installer` (0700); each plan has an atomically replaced,
fsynced 0600 JSON journal. The journal records stage, state, original boot
target and remediation on failure. An OS file lock prevents overlapping
applications and releases on process exit, including crashes. Symlinked or
loosely writable state paths are refused. `setup-status` reads without creating
state. Keep the plan and journal together for support/recovery.

Run the same apply command to resume after interruption or an operator-managed
reboot. It checks the actual machine state, including work completed just
before a journal-write failure. A partly configured dpkg database blocks with
instructions to repair it manually; the installer does not run an unrestricted
`dpkg --configure -a` or lower approval requirements. Interrupted commands are
bounded and their process group is stopped. Raw child output is omitted from
the JSON journal; inspect system package logs locally for details.

The original boot target is retained in the journal. A headless proposal only
sets the next default target after explicit plan approval; it never isolates
the target or triggers a reboot. Restoring the original target remains an
operator action until repair/uninstall orchestration is implemented.

Automated tests cover exact approval and command tampering, changed host and
package/dependency state, partial/completed install reconciliation, successful
work followed by interruption, failed journal persistence, postcondition
checks, no-op reruns, concurrent locks, private atomic journals and status.
Physical AGX/N5 acceptance remains pending. Next: bind verified agent and
rollback-helper payloads and install configuration/systemd units while retaining
the selected WireGuard mode, then extend the journal to that complete flow.

## Verified agent acquisition

Completed: `download --artifact agent` uses the same independently trusted GPG
key, signed checksum manifest, metadata validation, bounded HTTPS transfer and
persistent build floor as the deploy CLI. The default artifact remains
`deploy`. Only these two executable names are accepted.

For a release-built installer, add `--match-installer` to require the agent's
signed version, build and commit to match that installer exactly. A development
binary without release identity cannot use this option. Mismatch stops before
fetching the executable or advancing the stored build floor. Prefer the
installer's immutable release URL: a moving channel head can legitimately point
at a newer build and will then be rejected by this constraint.

Example (replace the uppercase placeholders with independently reviewed values;
use `amd64` on x86-64):

```bash
./hypermesh-deploy download --artifact agent --match-installer \
  --release-url https://RELEASE_HOST/test/BUILD-COMMIT_PREFIX \
  --channel test --arch arm64 --min-build BUILD \
  --trusted-key /trusted/release-public.asc --fingerprints TRUSTED_FINGERPRINT \
  --state-file /owner-controlled/release-build.json \
  --output ./hypermesh-agent.verified
```

Reuse the same owner-controlled build-state file for installer and agent
downloads so both honor the highest accepted build. The output is a new private
0700 executable; existing paths are never overwritten. It is not executed,
installed into system directories or registered as a service by this command.

Automated coverage exercises both artifacts and both architectures against
signature/hash tampering, discovery mismatch, rollback and existing-file
preservation, plus exact installer identity and invalid artifact selection.
The remaining installation task must bind this verified agent, the rollback
helper, environment configuration and mode-specific systemd units into the
approved plan and journal. Agent/service installation is still incomplete;
this acquisition step does not make AGX-1 ready for acceptance.

## Agent first installation

Implemented: `plan-agent` and `install-agent` complete the file-installation
stage after approved prerequisites. They do not replace an existing enrolled
host; repair/upgrade orchestration remains separate.

Use a release-built installer and its immutable release URL, independent public
key and fingerprint. Complete `apply-setup` first. The plan contains the public
trust key, public host configuration, signed release identity and every
destination file's SHA-256 and mode; it contains no enrollment credential.

```bash
./hypermesh-deploy plan-agent \
  --setup-plan setup-plan.json \
  --release-url https://RELEASE_HOST/test/BUILD-COMMIT_PREFIX --channel test \
  --trusted-key /trusted/release-public.asc --fingerprints TRUSTED_FINGERPRINT \
  --control-plane-url https://api.test.hyperme.sh \
  --tenant-id TENANT_UUID --host-name REPORTED_HOST_NAME > agent-plan.json

# Review agent-plan.json, including public trust, destinations and configuration.
sudo ./hypermesh-deploy install-agent --plan agent-plan.json --approve-plan AGENT_PLAN_SHA256
sudo ./hypermesh-deploy agent-install-status --plan-sha AGENT_PLAN_SHA256
```

For production, select the immutable production release, `--channel prod` and
the production control-plane URL. The release must match this installer's
version, build and commit. Planning and applying verify its signature, metadata
and agent checksum independently. Applying rechecks approved prerequisites
after downloading, reconstructs all payloads and compares them with the approved
manifest before installation. Rebuilding the installer requires new plans.

Installed files are the agent (0755), embedded rollback helper (0755), public
update key (0644), agent environment (0600), and systemd units (0644).
The helper comes from the signed installer itself. Kernel WireGuard installs
no userspace unit or dependency. Userspace mode installs the existing tunnel
unit and makes the agent depend on it. Both modes explicitly set the matching
agent environment value.

Application uses the prerequisite install lock and a private atomic journal at
`/var/lib/hypermesh/installer/agent-PLAN_SHA256.json`. The agent state directory
must be root-owned 0700. Existing files with different bytes, permissions or
ownership, symlinks, enrolled identity, activation marker, vendor units and
service overrides block first installation. Both compiled and running-manager
systemd search paths are checked, including inherited prefix/type drop-ins.
A failed initial conflict check does not authorize adoption on a second attempt. No existing file is replaced.
Resume with the same command: matching installed files are reconciled even if
their completion was not journaled. File publication is atomic and directories
are fsynced. A crash between publication and temporary-link removal can require
operator cleanup of the leftover `.agent-install-*` link; unsafe multiple-link
files are deliberately not adopted automatically.

Success is `installed-awaiting-enrollment`. Units are not enabled, started or
reloaded. Their activation condition requires
`/var/lib/hypermesh/activation-approved`, which this command never creates.
Do not manually create it to skip onboarding. Device authorization, fresh-key
enrollment, authenticated connectivity/benchmark gating and activation remain
the next task; this stage alone is not an AGX-1 acceptance run.

Tests cover kernel/userspace payloads, exact plan binding, unsafe inputs,
private journal persistence, interruption/retry, changed existing files,
symlink paths, existing host/service conflicts and cancellation before writes.

## Agent update policy and dashboard requests

Approved: support periodic update checks and host-dashboard update requests
through one verified updater. The existing agent checks its configured channel
hourly (`HYPERMESH_UPDATE_EVERY`), verifies signed manifests and checksums,
refuses older builds, defers during a lease or unacknowledged job result, and
uses heartbeat confirmation plus rollback. First-install configuration now
sets the channel, hourly interval, public key and allowed fingerprints.
Checks begin only when the enrolled agent is activated.

Still to implement end to end:

- An authenticated, tenant/host-authorized dashboard request to check and update
  when idle, delivered through the existing outbound agent/control-plane path.
  No inbound management port or arbitrary binary URL.
- One serialized update path for periodic and requested checks, with deduplication,
  retry/backoff and jitter. A dashboard request must not bypass signed trust,
  channel/build rules, bad-build suppression or lease safety.
- Visible current/target build and requested, checking, deferred (with reason),
  installing, confirmed, rolled-back or failed state. Show the next action.
- Drain coordination before restarting so new leases cannot race the final idle
  check. Do not interrupt paid work or treat a dashboard click as force permission.

The dashboard/control-plane delivery and status contract are pending; the
existing hourly checker is not a claim that dashboard push already works.


## Device-authorization protocol client

Implemented in `internal/deviceauth`: `Authorize` discovers the configured HTTPS
issuer, requests an `openid` device grant for the existing public
`hypermesh-native` client, displays the verification URL and user code, and
returns an in-memory access token after approval. There is deliberately no
standalone command that prints an access token or asks an operator to paste it.

Discovery must match the configured issuer exactly; device, token and browser
verification endpoints must use its HTTPS origin. Redirects are refused,
response bodies are capped at 1 MiB, and each request has a 15-second deadline
covering its entire body. Authorization is bounded by both a ten-minute overall
deadline and the returned device-code lifetime. Cancellation interrupts requests
and waits.

The client waits before the first poll, honors the issuer interval (five seconds
when omitted), permanently increases the interval for `slow_down`, and backs
off exponentially on request or response-body timeouts. Denial, expiry, invalid
grants and unexpected responses terminate the attempt. Only URL/code instructions
are displayed; raw server responses, transport errors and credentials are not
included in errors. Tokens have redacted default text/JSON formatting and are
not persisted. The caller must keep the explicitly accessed bearer value private.

TLS-backed tests exercise pending/slow-down approval, denial/expiry, cancellation,
intervals, deadline-bounded trickle responses, timeout backoff, redirects,
untrusted discovery, malformed/oversized responses and credential redaction.
These are protocol tests, not a live Keycloak or physical-host acceptance run.

**Control-plane progress:** identity-bound organization ownership is merged and
deployed in [Panopticon #287](https://github.com/FyberLabs/panopticon/pull/287).
[Panopticon #288](https://github.com/FyberLabs/panopticon/pull/288) implements the
durable registration API and dashboard approval described below and is merged.
The CLI integration below uses that API; deployment verification remains separate.

## Automatic registration integration status

Implemented and merged in Panopticon #288:

- A durable registration request binds the verified owner, selected tenant,
  actual reported host name, hardware report, WireGuard public key and hash of
  the locally generated agent credential. Organization/device IDs cannot be
  supplied to claim another host.
- The dashboard displays this installation and requires explicit approval or
  denial. The native device-flow session cannot approve its own request.
- Completion creates one paused, uncertified device and returns its assigned
  UUID. Repeating the same immutable request recovers that device without
  rotating credentials. Device creation and the enrollment event commit
  together; optional Service Bus delivery has a durable retrying outbox.
- Expired/denied requests cannot complete. Archived devices cannot be revived
  through retry. Intentional reinstallation requires a fresh request and keys.
- PostgreSQL concurrency/recovery tests and dashboard approval tests cover the
  server contract. These are not physical-host acceptance.

The API contract and recovery rules are maintained in
[Panopticon's host registration document](https://github.com/FyberLabs/panopticon/blob/main/products/hypermesh/docs/host-registration.md).
The registration API currently requires a platform-configured hardware class;
a reported hardware profile does not create or certify a new class.

Completed host-package integration: `register-agent` connects device authorization,
the installed inactive service and the registration protocol. The implementation
and recovery contract are described below. Authenticated connectivity,
certification and service activation are still pending.

Repair/uninstall, requested agent updates and independent
website/bootstrap acceptance also remain on the pre-AGX delivery checklist.
Workload modes and macOS remain deferred.

## Browser-approved agent registration and recovery

Implemented: after `install-agent`, use the same release-built installer and
approved agent plan. Obtain the issuer, dashboard origin and tenant from trusted
environment configuration. The issuer is the complete HTTPS Keycloak realm URL,
not the dashboard URL. Production uses the production origins.

```bash
sudo ./hypermesh-deploy register-agent \
  --plan agent-plan.json --approve-plan AGENT_PLAN_SHA256 \
  --issuer https://AUTH_HOST/realms/REALM \
  --dashboard-url https://test.hyperme.sh --class-id agx-large

sudo ./hypermesh-deploy registration-status --plan-sha AGENT_PLAN_SHA256
```

The headless command displays the device-login URL and code on stderr. Sign in
there, then open the separately displayed host approval URL. Review the host,
tenant, measured hardware and key fingerprint and approve the installation.
There is no pasted enrollment token or manual device-UUID comparison. stdout
contains only redacted registration status; session tokens remain in memory.

Before registration the CLI verifies all installed payload hashes/modes, the
original install journal, inactive services, systemd search paths and the
approved hardware fingerprint. New requests rerun the real resource, HTTPS,
package and hardware checks. Completion reruns those checks after browser
approval. A selected class cannot override incompatible measured hardware.

Current class admission is deliberately limited to platform-configured
`agx-large` on an AGX Orin with approximately 64 GiB installed RAM and
`nx-volume` on an Orin NX with approximately 16 GiB. Reserved system memory is
accounted for in those physical-memory ranges. Both require detected Tegra/CUDA.
This identifies a candidate module class, not a certified carrier or throughput.
Generic AMD/N5 class admission and other class mappings remain pending in the
control plane; selecting a Jetson class on those machines fails before login.
No host names, device UUIDs, serial numbers or operator keys are compiled in.

Before any control-plane mutation, fresh WireGuard and agent keys plus the
immutable request are atomically saved under the shared installer lock at
`/var/lib/hypermesh/installer/registration-PLAN_SHA256.json` (0600 in private
0700 state). The verified login subject is durably bound before the host grant.
Retries require the same owner, plan, tenant, issuer, dashboard and class.
Raw API errors and transport bodies are omitted from diagnostics.

The API grants host membership, exchanges the login for a tenant session, and
creates the pending request. Approval polling uses five-second intervals;
individual requests have 15-second whole-response deadlines and 1 MiB limits,
with a 25-minute command bound covering login and approval. Transient API errors
stop with retained state; rerun the exact command to reauthenticate and recover.
Redirects are refused. Every returned request, tenant, hardware report, key
fingerprint, approval path and device identity must match the local request.
Tenant UUIDs are canonicalized to lowercase on the wire, and configured API URL
path prefixes are preserved, matching the installed agent plan.

After approval the server assigns the device UUID. That UUID is journaled before
the CLI publishes private `device.json` and a dedicated
`registration-keys/REQUEST_ID/hypermesh.key`. The agent already consumes those
files. Lost responses or an interrupted publication recover the same request,
device and keys. Existing mismatched, linked or unsafe files are never replaced.
A missing identity after a fully completed local registration requires repair;
it is not silently regenerated. Completed registration recovery does not repeat
resource admission, but still checks installed files, service and hardware
identity. Archived hosts cannot be revived.

Denial or expiry leaves the private journal intact and the service inactive.
To start again, rerun the same command with `--restart-approval`. It verifies
the original owner, confirms a denied/expired decision from the server, reruns
preflight, archives the private request and generates fresh keys and a new
request requiring another browser approval. Pending, completed or retired
requests cannot be reset this way. If interrupted after the new journal was
saved, rerun without that flag to resume it. Prior requests remain in private
`registration-history-REQUEST_ID.json` files.

Intentional retirement/reinstallation needs the separate fresh-key reinstall
workflow still on the roadmap. Do not delete journals as a recovery shortcut.
Preserve private journals locally and never upload them as support logs.

Success is `registered-inactive`: the dashboard device is paused and
uncertified. No activation marker, service enable/start, certification result or
selling state is created. The authenticated control-plane and AIuditor program checks are implemented below.
WireGuard and certification execution still precede service activation. AGX-1 physical acceptance is
still pending.

Automated coverage includes TLS auth/session separation and response binding,
owner mismatch before mutation, deadlines and redaction, measured class checks,
lost completion recovery, durable-before-publication ordering, cancellation,
terminal states, private file safety, interrupted identity publication and
installed-payload tampering. CI results are recorded in the implementation PR.

## Completed: authenticated pre-activation check

After `register-agent` reports `registered-inactive`, run:

```bash
sudo ./hypermesh-deploy check-agent \
  --plan agent-plan.json --approve-plan AGENT_PLAN_SHA256
sudo ./hypermesh-deploy onboarding-status --plan-sha AGENT_PLAN_SHA256
```

This is an identity, protocol, AIuditor program and local preflight check. It requires the same
release-built installer and exact approved agent plan. It reads the saved private
device identity under the shared installer lock, verifies installed payloads,
inactive units and key/state files, then reruns current hardware, resource and
approved prerequisite checks. It never rotates keys or writes enrollment state.

The plane must implement
[the onboarding protocol](https://github.com/FyberLabs/panopticon/pull/289).
The CLI sends a freshly generated 32-byte nonce and the saved registration/device
IDs to `POST /api/v1/hypermesh/agent/onboarding/check`, authenticated with the
saved device secret. No browser login or copied UUID is required for this check.
The response must match protocol version/capabilities, nonce, tenant,
registration, device, reported name, class and WireGuard key fingerprint. It must
also advertise `aiuditor_program_v1` and return a recognized program state.
Missing fields, unsupported versions, replayed/mismatched responses, redirects
and old deployments without the route fail closed. The shared HTTPS transport
caps responses at 1 MiB and applies a 15-second deadline through the whole body;
the command has a three-minute overall limit.

A successful response proves the configured plane accepted this saved device
credential and supports this application protocol. The plane reads fresh SQL
state; inactive tenants, retired hosts, device bans and changed registration
bindings cannot be hidden behind the marketplace read cache. The check does not
publish a heartbeat or make an offline machine appear alive.

Output lists class availability, reported Path A/Path B state, scheduling holds,
the AIuditor program result, and the remaining heartbeat, tunnel, activation and selling
gates, with a next action for each. Current hardware intake or certification
problems remain visible even when the HTTPS check succeeds. A reported green
certificate does not skip remaining gates. The last heartbeat timestamp is
historical evidence, not a fresh liveness claim.

`check-agent` exits 0 when device-authenticated API compatibility, the strict
AIuditor program gate and implemented local checks pass, 1 when a check or persistence fails,
and 2 for invalid arguments. **Exit 0 does not mean installation is complete or
selling is allowed.** `activation_allowed` and `selling_allowed` remain false.
Neither command creates an activation marker, starts/enables a service, runs a
benchmark, queues a job, clears a hold or changes the host's sell posture.

The dated redacted status is stored at
`/var/lib/hypermesh/installer/onboarding-PLAN_SHA256.json`, mode 0600. A new check
replaces prior success with `checking` before probing; interruption therefore
cannot leave an old success presented as the newest check. Failures store a
fixed failure code and recovery action, not raw errors or credentials.
`onboarding-status` reads this historical observation without contacting the
plane. Rerun `check-agent` for current evidence. Registration/key files are
preserved on all check failures.

Tests cover TLS device authentication and fresh nonces, protocol and response
binding, unknown/omitted fields, old-plane/error behavior, local-before-remote
gating, cancellation and failed persistence, private journals, identity-file
preservation and the impossibility of promoting a snapshot into activation.
The companion product change is limited to Hypermesh paths so Panopticon CI
selects the Hypermesh-family suites. Physical AGX/N5 acceptance remains pending.

Next complete task: an approved, recoverable onboarding execution path for
heartbeat/tunnel checks and certification execution while keeping
paid workload admission disabled, followed by service activation. This check
does not stand in for that execution or benchmark evidence.

## Completed: redacted installer support bundle

`support-bundle` exports one JSON file covering the selected prerequisite,
agent installation, registration and onboarding journals. Use it after an
interrupted or failed installation, or to share progress during AGX-1 acceptance.
It is a **historical journal summary**, not a fresh hardware/network/service
check, proof of valid credentials, or authorization to activate/sell.

```bash
sudo ./hypermesh-deploy support-bundle \
  --plan-sha AGENT_PLAN_SHA256 \
  --setup-plan-sha SETUP_PLAN_SHA256 \
  --output /root/hypermesh-support-01.json
```

Use `--setup-plan-sha` alone if agent planning has not happened yet. The two
digests are explicitly selected references, not a verified relationship between
plans. Omit the setup digest to collect only the three agent stages. No plan
file, browser login, network access or operational hardware is needed. An older
installer may collect its known journal schema even when its build differs
from the installed release; unknown future fields/schemas are marked invalid
instead of being copied or misinterpreted. The collector's own build/commit is
recorded separately, not represented as the installed agent version.

The command reads only the named journals in
`/var/lib/hypermesh/installer`. It requires the existing private state directory
and installer lock, acquires a nonblocking shared lock, and refuses collection
while a cooperating installer command is changing state. It does not create
missing state/locks, traverse the state directory, run subprocesses, query the
plane, read container/system logs, or modify installation, enrollment or keys.
Symlinks, multiple hard links, unsafe ownership/permissions, nonregular files,
files larger than 2 MiB, duplicate JSON keys and unknown schemas are not read
as valid journals. Each source has a separate collection status.

The export is built from an allowlist: selected plan digests, collector build
and architecture, collection time, fixed stage/state values, a completed-file
count, onboarding timestamps normalized to UTC, fixed gate IDs/states, and
locally generated next actions. Raw journal errors, gate messages, host names,
tenant/device/request IDs, owner accounts, hardware descriptions, URLs,
environment/configuration, public/private keys, device secrets and their
hashes/prefixes are omitted. No heuristic secret-pattern replacement is used.
Unknown failure codes become `unknown`; unknown states/gates invalidate that
section. The artifact still reveals installation timing, plan digests and
operational state, so review it before sharing.

`--output` must be a new absolute filename in an existing root-controlled
directory. Publication is atomic, uses mode 0600, and never overwrites an
existing file or follows a destination symlink. Choose a new filename for each
export. Only this output artifact is created; private source journals stay
local and must not be uploaded as a substitute.

Exit codes:

- **0:** every requested journal was safely summarized and the artifact saved.
  A journal can record a failed installation/check and collection still succeeds.
- **1:** collection/export failed, or a **partial artifact** was saved because
  a requested journal is missing, unreadable or invalid. Inspect
  `collection_complete` and each stage's `collection` field; missing stages
  can simply mean they have not started. Directory/lock failures produce no
  artifact. Publication/durability failures are reported explicitly.
- **2:** invalid command arguments.

A saved `checked` onboarding result remains dated evidence. Run
`check-agent` for a current check; the bundle cannot turn old evidence into
readiness. Recovery guidance preserves registration/keys and never recommends
deleting journals, creating an activation marker, or bypassing certification.

Automated coverage includes secret canaries throughout free-text/identity
fields, historical blocked/failed summaries, source-file preservation,
partial/missing/corrupt/future journals, size/type/permission/link checks,
writer-lock exclusion, cancellation, private atomic output and refusal to
overwrite. Physical AGX/N5 acceptance remains pending. Repair/uninstall,
tunnel and certification execution, activation, and live
status presentation remain separate roadmap tasks.

## Completed: authenticated AIuditor program gate

`check-agent` now requires `aiuditor_program_v1` from the plane. Update Panopticon
before this host release; older planes without that capability cannot pass.
There are no new installer flags or host-side service credentials. The saved
device credential stays between the host and plane.

The plane uses its configured AIuditor service token to check authoritative
role/writer metadata and read valid core-suite membership for this tenant and
hardware class. Missing/unreachable service, rejected service credentials,
a witness/wrong writer, an empty suite, malformed membership or crossed
tenant/class data cannot be replaced by a locally seeded default.

The existing `aiuditor` readiness gate becomes `passed` only for `ready`.
All other recognized states become `blocked` with a specific next action:

- Configure the plane's `AIUDITOR_SERVICE_URL` and `AIUDITOR_INTERNAL_TOKEN`.
- Check AIuditor availability and the plane-to-service network.
- Correct mismatched service authentication without rotating device keys.
- Select the authoritative writer required by `AIUDITOR_REQUIRED_WRITER`.
- Have a program operator configure the tenant/class certification suite.
- Repair incompatible responses or mismatched program records.

On those failures the command exits 1 and durably records `state=failed`,
`failure_code=aiuditor_program`, the checked timestamp and gates. A verified
device API/local preflight remains recorded accurately, but cannot turn a
blocked program into overall success. The next run performs a new probe;
no previous success is reused. Failed status persistence cannot emit success.
`onboarding-status` shows the saved guidance, and `support-bundle` recognizes
the new fixed failure code while preserving its redaction rules.

Both plane-to-AIuditor requests share a ten-second maximum deadline and each
body is limited to 64 KiB. Redirects and arbitrary host-supplied URLs are not
used. Raw upstream errors, service tokens and suite rows are not returned to
the host. Protocol tests exercise missing/unknown capabilities and states,
and installer tests verify each blocked state persists actionable failure
without activation or selling permission.

A passed gate proves the plane could authenticate and read its configured
program. It does **not** prove the host's direct AIuditor/WireGuard path,
runtime support for that suite, a benchmark result or certification. The
AIuditor may still need program configuration before AGX-1 acceptance.
The one-shot heartbeat and WireGuard stages are documented below. Workload
verification and controlled activation remain subsequent execution tasks.

## Release channels and promotion (approved 2026-10-01)

- [x] Preserve each newly published test build as a GitHub prerelease tagged
  `test-<build>-<12-character-commit>`, with the same signed ARM64/AMD64
  installer, agent, bootstrap and verification files as the channel build.
  Complete that archive before replacing the rolling `test` prerelease.
  Conflicting archive tags or bytes fail publication; matching assets remain.
  Test blob retention does not delete these GitHub releases. Existing historical
  builds are not backfilled by this change.
- [ ] Expose **Production** and **Test** downloads on hyperme.sh. Production is
  the default; selecting Test is explicit. Show each channel's available build,
  architecture, release notes and verification instructions. If Production has
  no published release, say so; never silently substitute Test.
- [ ] Promote a specific verified test release to Production with recorded
  source release, commit and artifact digests. Promotion must be a reviewed
  operation, never an automatic consequence of publishing a test build.
  Define signed promotion metadata and preserve tested payload provenance;
  reconcile the current channel/version-bound metadata and global build floor
  before enabling this flow. The existing vX.Y.Z workflow still rebuilds and
  signs a production release; it is not yet the approved promotion mechanism.
- [ ] Persist the owner's chosen update channel during installation and expose
  an explicit later channel change in the host dashboard/agent. Handle a Test
  build newer than Production without silently downgrading or bypassing trust.
- [ ] Serve downloads without requiring access to the private GitHub repository,
  with independent bootstrap trust and end-to-end website/install tests.

Permanent test tags do not match the production workflow's `vX.Y.Z` trigger.
This milestone adds release history, not a production promotion or website
download implementation. GitHub mirroring follows the verified Azure channel
publication; a GitHub API failure can leave the channel published while the
mirror is incomplete. The rolling alias can be briefly unavailable during its
replacement, but a completed permanent release remains downloadable to users
with repository access. A full test workflow rerun allocates a new build number;
it does not reuse an older test build's signed metadata.

## Completed: recoverable one-shot onboarding heartbeat

After registration, explicitly request one heartbeat with the same installed
agent plan and its approved digest:

```bash
sudo ./hypermesh-deploy check-agent \
  --plan agent-plan.json --approve-plan AGENT_PLAN_SHA256 --heartbeat
sudo ./hypermesh-deploy onboarding-status --plan-sha AGENT_PLAN_SHA256
```

Without `--heartbeat` or `--wireguard`, `check-agent` remains read-only on the plane. With the
flag it still verifies private registration state, exact installed payloads,
inactive services and fresh resource/hardware prerequisites first. It performs
the existing device/AIuditor check and requires `onboarding_heartbeat_v1` before
writing. Upgrade the plane first; an older plane produces an actionable error.

The installer saves heartbeat intent before sending, rechecks local identity
and inactive service configuration, measures Linux `/proc/uptime`, and sends
one authenticated HTTPS observation. No owner-supplied uptime, fabricated
device identity, browser token or enrollment retry is used. The plane repeats
AIuditor readiness and revalidates live SQL authority immediately before commit.
Only paused, never-certified, non-lab hosts with no loaded model or hold can
use this pre-activation path. Resolve holds in the dashboard rather than
deleting state or using this command to force the host online.

A successful receipt must bind the fresh nonce, registered tenant/device/key,
measured uptime, explicit recorded state and matching server timestamps.
The journal records `heartbeat_requested`, `heartbeat_recorded_at` and
`heartbeat_uptime_s` only after that acknowledgment. The heartbeat gate then
shows **one HTTPS observation**, while tunnel, certification and activation
remain pending and selling remains blocked. Support bundles export the dated
heartbeat result without device credentials or raw transport errors.

The existing three-minute command bound and 15-second per-request whole-body
deadline still apply. A denied, interrupted, malformed or lost reply exits 1
and stores fixed recovery guidance. The plane may have committed a heartbeat
whose reply was lost; rerunning the same command with `--heartbeat` and the
same keys safely records another observation. A failed final journal write
never reports success. No background retries or service activation occur.

Reading saved status does not refresh it. A later ordinary `check-agent`
clears the previous local heartbeat acknowledgment; any last-heartbeat time
from the plane is historical. An inconsistent journal cannot show a passed
heartbeat without its committed receipt. This is not proof of a continuously
running agent, a WireGuard handshake, direct AIuditor reachability or completed
GPU/model certification.

Automated tests cover explicit opt-in, durable-before-send ordering, older
planes, local/program failures, lost responses, cancellation, persistence
failures, measured uptime bounds, protocol binding and redaction. Physical
AGX-1 acceptance remains pending. WireGuard handshake/data-path checks are now
implemented below; certification execution and enrollment-gated service
activation remain pending.

## Completed: explicit WireGuard connectivity probe

After deploying the plane's `onboarding_tunnel_v1` capability, run:

```bash
sudo ./hypermesh-deploy check-agent \
  --plan agent-plan.json --approve-plan AGENT_PLAN_SHA256 --wireguard
sudo ./hypermesh-deploy onboarding-status --plan-sha AGENT_PLAN_SHA256
```

`--heartbeat` may be combined with this flag. All installation, private identity,
inactive-unit, resource and program checks still run first. Intent is durably
recorded before allocating an idle peer. The plane refuses holds, active/selling
or already-certified hosts, and reuses the registered public key and peer on
retries. Revision-3 prerequisite plans add iproute2 and iputils-ping; userspace
WireGuard additionally requires wireguard-go. Missing tools require reviewing
and approving a new setup plan, not bypassing package approval.

The probe creates a temporary interface named `hm<first-12-plan-digest>`, using
the approved kernel/userspace mode. Only a route to the gateway overlay address
10.77.0.1/32 is installed. The default/LAN route and existing hypermesh interface
are not replaced. The userspace process runs as a bounded child, not a service.
Success requires a fresh handshake with the allocated server key and an ICMP
echo through that interface. HTTPS success alone cannot pass this gate.

The interface, probe route, child process and staged key file are cleaned up
before success is recorded as `wireguard_verified_at`. Cleanup failures block
success. A retry can recover a leftover interface only when both its ownership
alias and public key match this plan/registration; an unknown interface or a
pre-existing exact probe route is a conflict requiring local inspection.
An interruption before the ownership/key markers are complete also requires
inspection. Preserve registration credentials; do not re-enroll to fix networking.

The gateway must synchronize the idle peer, expose the configured UDP endpoint,
and answer ICMP on 10.77.0.1. A timeout calls out those checks; inconsistent gateway
keys or an exhausted pool require platform repair. The command is bounded by
the existing three-minute deadline with a 90-second local probe maximum.
Neither success nor failure starts agent services, grants certification or enables
selling. The idle plane peer persists for later activation and is revoked by host
retirement. A later ordinary check clears the saved connectivity success.

Protocol, allocation/retry, failure, cleanup, timestamp and journal checks are
automated. Physical AGX-1/N5 validation, AIuditor inference and workload
compatibility, certification execution, and service activation remain pending.
