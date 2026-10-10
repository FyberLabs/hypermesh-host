# AGX physical acceptance

Implementation and simulated CI are separate from hardware acceptance. Use a
test-channel build containing this lifecycle change, with the control plane's paired
onboarding/certification and public-release APIs deployed. Do not run this on a
leased host. Keep the host paused throughout acceptance.

## First installation

Follow [the verified installation guide](first-install-guide.md) through setup,
browser registration, fresh WireGuard/heartbeat checks, authenticated model
acquisition, certification and controlled activation. Use the host's reported
name; no device UUID is supplied. Save all plans privately with `umask 077`,
retain the exact signed installer, and use the actual model filesystem.

Before changing prerequisites, confirm SSH access from a second session and a
physical/serial recovery path. Record the OS/vendor stack and selected UMA
allocation locally. Required memory, disk and GPU checks must pass as measured.
Use a measured class baseline and the reviewed model and catalog records.

Install the verified CLI at a root-owned absolute path, preserving its bytes:

```sh
sudo install -m 0755 ./hypermesh-host /usr/local/bin/hypermesh-host
sudo python3 scripts/host-acceptance.py \
  --installer /usr/local/bin/hypermesh-host \
  --certification-plan /root/host-plans/certification-plan.json \
  --phase initial-ready --output /root/acceptance-initial.json
```

The script is read-only apart from writing a NEW private report. It requires
all live readiness gates, fresh heartbeat, active certification and paused
selling. It bounds command time/output, exports only allowlisted observations
and does not collect credentials, raw subprocess output or private identities.
It cannot prove sustained GPU stability, disk performance or remote SSH recovery.

## Reboot and resume

Review `plan-maintenance --operation reboot` against the retained certification
plan. Apply its digest with `maintain-agent --apply --reboot`. Confirm the drain,
SSH disconnect and new boot. Reconnect and run the approved `resume-agent --start`.
Never use a forced reboot as a substitute for a failed drain. Check the dashboard
still shows the same host and no selling. Then collect:

```sh
sudo python3 scripts/host-acceptance.py \
  --installer /usr/local/bin/hypermesh-host \
  --certification-plan /root/host-plans/certification-plan.json \
  --maintenance-plan /root/host-plans/maintenance-plan.json \
  --phase after-maintenance --output /root/acceptance-after-maintenance.json
```

This phase requires the matching maintenance journal to have completed resume.
The operator must separately record that the physical reboot and SSH reconnect
occurred. The report never labels those observations as automated evidence.

## Repair

Create a new stop-maintenance plan after resume and drain/stop again. Review a
repair plan for the retained original agent installation, then run recover-agent.
A no-damage repair exercises idempotence without deliberately corrupting a host.
For a damage-recovery experiment, use a spare environment and review the exact
changed file in the recovery plan; never remove keys or model data.

Resume with the matching stop-maintenance plan, confirm the same dashboard host
and model cache remain, then collect `--phase after-repair` with both
`--maintenance-plan` and `--recovery-plan`. The collector requires matching
completed repair and maintenance checkpoints plus live readiness.
Uninstall removes managed binaries/config/units only; it is covered by CI and
can be physically tested last if we intend to take the box offline. It does not
retire the remote host; retirement is a separate dashboard action.

## Finish and record

Export support-bundle using the exact setup, agent and certification digests.
Observe an approved workload long enough to check sustained thermal, memory,
network and disk behavior; record duration and measured results explicitly.
The acceptance collector intentionally reports `physical_soak_completed=false`.
No script enables selling. After reviewing the real evidence, the owner may
separately enable selling and run the short gateway check against the enrolled name.

Until these steps run on AGX, physical acceptance remains pending. AMD/N5,
Renesas, Rockchip, macOS and different Jetson classes require their own evidence;
an AGX pass does not certify them.
