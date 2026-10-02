# Linux host lifecycle completion

Approved scope: finish the five remaining implementation areas before the operator runs the resulting build on AGX-1. Physical checks may use AGX-1 after implementation; simulated CI is not hardware acceptance.

## Delivery checklist

- [ ] Recovery: reviewed repair/uninstall, interruption recovery, preserved identity/models, maintenance integration.
- [ ] Enrolled maintenance: drain, safe stop/reboot, verified resume and explicit return to service.
- [ ] Reviewed installer self-update and signed agent-update reconciliation.
- [ ] Independent bootstrap trust and guided website-to-install flow.
- [ ] Automated installation/recovery acceptance suite and an executable AGX-1 acceptance procedure.

Workload modes and macOS remain deferred. AMD/N5 and Renesas/Rockchip require separate backend admission and physical evidence.

## Recovery implementation in progress

`plan-recovery --agent-plan FILE --operation repair|uninstall` captures the exact current managed-file hashes and modes. `recover-agent --plan FILE --approve-plan SHA256 --apply` requires that review, inactive disabled services, no local lease and no conflicting systemd configuration. Repair downloads and verifies the original signed payload; uninstall only removes paths in the installation manifest. Both retain private backups, registration journals, device keys, activation history and model caches.

A private durable checkpoint precedes file mutations. Retrying the same plan accepts only original or target bytes and requires intact backups. Changed files, unsafe ownership, symlinks, altered manifests, missing backups or active services stop recovery. Neither command starts services or retires the remote identity. Maintenance integration and end-to-end validation remain pending; this section does not yet mark the recovery milestone complete.

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
