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
