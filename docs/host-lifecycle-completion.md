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
