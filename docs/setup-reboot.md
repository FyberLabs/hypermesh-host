# Prerequisite reboot and resume

This Linux stage is for a new host **before agent installation or enrollment**.
It applies to an OS reboot-required marker or the approved change from a
graphical default target to headless operation while the graphical target is
still active. An already-headless boot does not request another transition reboot. It does not reboot an installed,
registered or leased Hypermesh host.

Finish `apply-setup` first. Retain the original setup plan and the matching
signed installer in durable storage. Confirm that you can reconnect over SSH
(or use a local console) after the reboot; this command does not configure SSH
or networking. The reboot disconnects the current session.

## Review and request

```sh
sudo hypermesh-deploy plan-reboot --setup-plan setup-plan.json > reboot-plan.json
sudo hypermesh-deploy reboot-setup --plan reboot-plan.json \
  --approve-plan REBOOT_PLAN_SHA256 --reboot
```

Review the new reboot plan's `plan_sha256`, embedded setup, reboot reason and
machine/boot binding. The setup plan's digest is not the reboot approval.
Planning inspects state without changing it. If neither reboot reason applies,
it reports that no initial-setup reboot is required and emits no reboot plan. The command revalidates current
prerequisites and absence of Hypermesh installation/enrollment, saves
`reboot-PLAN_SHA256.json` privately in the installer state directory, rechecks
the host, then submits a normal systemd reboot request.

The request calls logind's `RebootWithFlags` through `/usr/bin/busctl`, with
`SD_LOGIND_ROOT_CHECK_INHIBITORS` (flag 1). This lets the operator approve a
reboot from their own sudo/SSH session without systemctl treating that session
as an unrelated logged-in user. Logind still enforces shutdown block inhibitors
for root and handles delay inhibitors. Before submission, the installer also
checks the aggregate BlockInhibited property, covering root-owned shutdown
blockers that older logind versions exclude from the caller's UID check. There is no force, inhibitor bypass,
interactive authorization, or fallback to an unchecked reboot.
See [systemd's reboot authorization](https://github.com/systemd/systemd/blob/v255/src/login/logind-dbus.c)
and [flag definition](https://github.com/systemd/systemd/blob/v255/src/basic/login-util.h).
The local terminal receives bounded command error output and exit/timeout details;
raw child output is not copied into support journals.
A refusal, timeout, disconnect or lost reply is an unconfirmed outcome, not proof
that reboot failed or completed. Keep the plan and checkpoint.

## Reconnect and verify

After reconnecting, use the same installer and plan:

```sh
sudo hypermesh-deploy resume-setup --plan reboot-plan.json \
  --approve-plan REBOOT_PLAN_SHA256
sudo hypermesh-deploy reboot-status --plan-sha REBOOT_PLAN_SHA256
```

Resume never submits a reboot or reinstalls packages. It requires a matching
checkpoint, the same machine identity and a different kernel boot ID. It then
rechecks hardware/backend, RAM and models-filesystem admission, approved package
state, OS/kernel, default target, inactive graphical target when headless was
requested, and a cleared OS reboot-required marker.

Resume records `verifying` before probing and `verified` only after those
checks pass and the boot ID remains unchanged. The command response separates `attempt_succeeded` from the saved historical
`checkpoint`, so a failed new attempt cannot present an older verified record
as current success. A successful resume returns 0;
failed or unknown verification returns 1. Invalid arguments return 2.
Continue with `plan-agent` and `install-agent` only after verification succeeds.
Registration, model acquisition, certification and owner-approved activation
remain separate stages.

The current prerequisite policy preserves the installed vendor driver/kernel
stack. If reboot loads a different kernel, or OS/package/hardware state changes,
resume fails and requires a new reviewed setup plan. It never edits the original
approval to accept that drift. An unchanged boot ID also fails; elapsed time,
a returned systemd command, and a prior successful journal are not substitutes.

## Interrupted request or verification

`requested` means the durable request intent exists; it does not prove the
machine rebooted. If you are still on the original boot, inspect OS inhibitors
and system logs. Only after reviewing the reason, explicitly retry:

```sh
sudo hypermesh-deploy reboot-setup --plan reboot-plan.json \
  --approve-plan REBOOT_PLAN_SHA256 --reboot --retry-request
```

Do not put this command in a retry loop. Once the boot ID changes, the old plan
cannot submit another reboot; use resume. A `verifying` or
`verification-failed` checkpoint can be rechecked with the same resume command
after correcting the cause. Do not delete checkpoints to bypass a blocker.

Export a redacted historical summary with:

```sh
sudo hypermesh-deploy support-bundle --reboot-plan-sha REBOOT_PLAN_SHA256 \
  --output /root/reboot-support-new.json
```

Status/export omit the machine digest and boot IDs. Support export success means
the checkpoint was collected, even if it records failure; inspect its state and
next action. Retain original private files locally for recovery.

No unattended boot-time continuation is installed. This explicit request and
operator-invoked verification complete the pre-enrollment stage; maintenance
draining/reboot of enrolled hosts and physical AGX acceptance remain pending.

### Installation gate and custom state

Agent planning and installation reject an OS reboot requirement or an active graphical target when headless setup was approved. A recorded reboot for that setup must have a verified checkpoint for this machine and current boot; run `resume-setup` again after a later reboot. These checks run before downloading and again before installation writes. A setup with no current reboot requirement and no recorded request can proceed without an unnecessary reboot.

If `apply-setup` used `--state-dir /absolute/private/path`, pass that same option to `plan-reboot`, `reboot-setup`, `resume-setup`, `reboot-status` and reboot `support-bundle` exports. Pass `--setup-state-dir /absolute/private/path` to `plan-agent`; that path is included in the approval digest and used by `install-agent`. Agent/registration journals continue to use the canonical installer directory. Reboot operations also lock and inspect that canonical directory so selecting a custom prerequisite directory cannot bypass an installed or enrolled host.

## Guided recovery from the AGX reboot failure

The default wizard now offers a separately confirmed retry on the original boot,
after validating the recorded checkpoint. It reruns preflight and never retries
automatically. If the boot has changed, it verifies that boot instead of requesting
another reboot.

Before agent installation (wizard stages 0–1), a newer signed installer on the
same software channel can recover an older wizard session. Run the new verifier
and executable together, then approve rechecking prerequisites. The wizard keeps
the chosen network, host name, memory and model path; preserves old plans and
journals; and writes newly reviewed plans under a separate build directory.
It never rewrites old approval hashes or adopts an installed host. Headless
rechecking considers the currently active graphical target even when the default
was already changed to multi-user by the earlier attempt.

Implementation and regression coverage are complete; the signed release still
needs physical AGX verification. Preserve the failing AGX checkpoint as evidence.
