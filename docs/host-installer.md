# Pull-based host installation

Status: approved design; implementation started. This is the installation
roadmap, not a claim that a guided installer already exists. The existing
`hypermesh-deploy inspect` detects hardware; the existing `agx` deployment
command retains its legacy preflight and configuration behavior.

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
- [ ] Validate UMA runtime allocation limits and reserve OS/runtime headroom.
- [x] Read-only CLI preflight plan with resource/hardware blockers and HTTPS
  endpoint reachability checks.
- [ ] Authenticated AIuditor identity and protocol compatibility checks.
- [ ] Signed bootstrap release discovery and installer self-update.
- [ ] Versioned, validated hardware profiles and exact mutation plans.
- [ ] Approval capture, unattended plan application, checkpoint/resume.
- [ ] Package/driver/kernel installation, headless setup, systemd and reboot.
- [ ] Recovery commands, redacted support bundle, and status presentation.
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

The installer should offer headless device authorization: display a short code
and verification URL, let the owner sign in through a browser, choose their
tenant and approve the host, then complete enrollment. Generate private keys
locally and register only public keys. Authorization must expire, be single-use
and bind to the intended enrollment. Retry/resume must not create duplicate
devices; intentional retirement and reenrollment must create fresh keys while
archiving the old identity. Do not collect the owner's password in the terminal.

Implementation and acceptance tasks:

- [ ] Add on-demand/warm-model selection to onboarding and host dashboard.
- [ ] Implement on-demand preparation only for active leases and 15-minute slices.
- [ ] Disclose and meter billable setup; test cached/uncached preparation and
  approved refunds for host/platform provisioning failures.
- [ ] Advertise the specific warm model and verified readiness; handle mode
  changes without disrupting active leases.
- [ ] Implement and reconcile one-minute lease charges and token metering
  before advertising those options.
- [ ] Add device authorization URL/code, tenant/host confirmation and installer polling.
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
- [ ] Add latest-release discovery and persisted minimum-build state.
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
