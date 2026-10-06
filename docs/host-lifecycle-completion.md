# Linux host lifecycle completion

Approved scope: finish the five remaining implementation areas before the operator runs the resulting build on AGX-1. Physical checks may use AGX-1 after implementation; simulated CI is not hardware acceptance.

## Delivery checklist

- [x] Recovery: reviewed repair/uninstall, interruption recovery, preserved identity/models, maintenance integration.
- [x] Enrolled maintenance: drain, safe stop/reboot, verified resume and explicit return to service.
- [x] Reviewed installer self-update and signed agent-update reconciliation.
- [x] Independent bootstrap trust and guided website-to-install flow.
- [x] Automated installation/recovery acceptance suite and an executable AGX-1 acceptance procedure.

Workload modes and macOS remain deferred. AMD/N5 and Renesas/Rockchip require separate backend admission and physical evidence.

## Recovery implementation

`plan-recovery --agent-plan FILE --operation repair|uninstall` captures the exact current managed-file hashes and modes. `recover-agent --plan FILE --approve-plan SHA256 --apply` requires that review, inactive disabled services, no local lease and no conflicting systemd configuration. Repair downloads and verifies the original signed payload; uninstall only removes paths in the installation manifest. Both retain private backups, registration journals, device keys, activation history and model caches.

A private durable checkpoint precedes file mutations. Retrying the same plan accepts only original or target bytes and requires intact backups. Changed files, unsafe ownership, symlinks, altered manifests, missing backups or active services stop recovery. Neither command starts services or retires the remote identity. The lifecycle acceptance suite combines real managed files and durable checkpoints with injected systemd/network probes, and exercises install, activation, drain, repair, paused resume and uninstall. Physical behavior remains subject to the AGX procedure.

## Enrolled maintenance procedure

Use the matching signed installer and retain the original agent and certification plans.

1. Review `plan-maintenance --plan certification-plan.json --operation stop` (or `reboot`).
2. Apply with `maintain-agent --plan maintenance-plan.json --approve-plan SHA256 --apply`. Reboot also requires `--reboot`.
3. The local authenticated owner endpoint persists an indefinite pause and a maintenance hold. New workloads and self-updates are refused; lease cleanup remains available. Wait for the current lease/job to finish and for the plane to report paused. The installer also checks for remaining Hypermesh Docker containers and a local lease before disabling and stopping services.
4. If drain times out, the hold remains; correct the blocker and retry the same plan. A durable stopping checkpoint allows recovery if the service stops before its reply or checkpoint arrives. A recorded reboot is never retried implicitly.
5. After maintenance (and a different boot for reboot plans), use `resume-agent --plan maintenance-plan.json --approve-plan SHA256 --start`. It verifies machine identity, installation, credentials, current prerequisites and plane readiness through controlled activation. Startup stays paused. It never turns selling on automatically.

For repair/uninstall, stop first, then review and apply the recovery plan. Backups remain in the private installer recovery directory. Uninstall preserves enrollment and cached models; it does not retire the host. Repair restores the original signed installation and its install journal. Use the retained maintenance plan to resume after repair.

The agent must include the maintenance owner endpoint. Older agents refuse the request; the installer does not substitute a forced stop. Missing connectivity, unexpected units/drop-ins, active workloads, unknown local state or a failed checkpoint prevent progress. Physical SSH reconnect and actual systemd/driver behavior still need AGX-1 acceptance.

## Reviewed installer updates and signed agent evidence

Install the verified CLI as a root-owned executable (for example `sudo install -m 0755 ./hypermesh-deploy /usr/local/bin/hypermesh-deploy`) before using in-place self-update. Keep the original downloaded executable available for recovery.

`plan-installer-update --release-url IMMUTABLE_URL --channel test|prod --trusted-key FILE --fingerprints LIST` verifies a strictly newer signed release and binds the exact existing executable, destination, target release and target hash. Apply the reviewed digest with `update-installer --plan FILE --approve-plan SHA256 --apply`. The current process finishes; invoke the CLI again to use the new version. No child executable is run implicitly and no agent is restarted.

The private `installer-update-PLAN_SHA256/previous` backup survives retries and explicit `rollback-installer --plan FILE --approve-plan SHA256 --apply`. Changed executables or backups are refused. Rollback restores bytes without lowering the persistent download build floor. Old host plans still require their matching signed installer; updating the CLI does not silently approve new prerequisite plans.

The agent updater now retains public signed checksums, detached signature and release metadata under private `update-history` before replacing the binary. Installer verification can accept a changed agent binary only when its actual hash matches that evidence, the original signing trust, expected architecture/channel and minimum installation build. Other managed configuration and unit bytes must still match their original approval. No SAS URL or authentication token is retained.


## Public bootstrap and guided installation

The website's Linux download selector exposes test and production separately and
pins an immutable release ID, minimum build and architecture. Unpublished
production is shown unavailable, never silently replaced by test. Public release
metadata and short-lived read-only artifact redirects come from Panopticon's
`/api/v1/hypermesh/public/host-releases` API. No device credential is required to
obtain the first installer; enrollment and mutation APIs remain authenticated.

The bootstrap embeds the reviewed public release key from infra, independent
of release blob storage. The bundled fingerprint is checked with real GPG in CI.
An explicit key override remains available for a separately reviewed trust
rotation. The public website/source is the initial trust boundary. The script
verifies signatures and hashes and saves a NEW executable and optional public
key; it never runs them. [The installation guide](first-install-guide.md) covers
setup approval, browser registration, model certification, activation and recovery.

## Maintenance permits after the initial plan window

Maintenance resume uses separate original-owner approval and device-authenticated
resume-permit endpoints. A completed certification may outlive the initial
24-hour acquisition/first-activation window, but resume requires the current
green certification and completed evidence, unchanged authoritative program/catalog pins, intact
evidence, accepted intake, no holds/jobs/leases and owner approval within ten
minutes. A permit lasts at most two minutes. First-activation expiry is unchanged.
The host accepts an expired acquisition window only for an explicitly marked
maintenance-resume permit; older planes fail closed.

## Acceptance and deployment boundary

Implementation checklist completion is not physical acceptance. CI covers the
lifecycle with real private files and injected external operations, every
installation/recovery checkpoint, real GPG update reconciliation, tampering,
read-only bounded acceptance collection and channel/download contract tests.
[AGX acceptance](agx-acceptance.md) provides the executable collector and the
manual reboot/reconnect, repair and sustained-workload observations still needed.

Deploy Panopticon's companion API before using maintenance resume or the website
first-install path. Publish the signed host test release before copying its
build into an AGX trial. The website verifier and guide are mirrored from this
repository; future trust/guide changes must update both copies in the same rollout.
No host has been modified, no physical acceptance marked passed, and no selling
enabled by these implementations.

## Demand-driven model cache

Implemented shared installer/agent quota and reserve policy, on-demand lease and
benchmark acquisition, protected LRU eviction, concurrent download reuse, and
cache admission telemetry. See [model storage](model-cache.md) for host controls
and remaining physical acceptance checks. Blob-cache serving remains future work.
