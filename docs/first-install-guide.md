# Linux host installation and recovery

Use the channel, minimum build and immutable release URL displayed with your
download. Keep the verified executable and public key together with all plans.
Do not use a mutable "latest" URL in a plan. All commands below run on the host.
Replace uppercase placeholders with the values supplied by your platform
operator/dashboard; do not invent an AIuditor URL, tenant, issuer or model class.

The verifier trusts the public key bundled with this website, independently
of release storage. Fingerprint: 1540ED8CD1EC4BDF72949FC78D741FF786A369FD.
Review that trust before running it. A compromised website remains a trust risk.

## 1. Check and review prerequisites

Create your intended models directory on the filesystem with model capacity.
For a 64 GiB AGX, 40 GiB is an example allocation, not an automatic entitlement:
current free memory must pass admission. Discrete GPU hosts use
`--memory-mode discrete` and omit `--uma-gib`. Keep vendor drivers and Docker
installed through their supported packages. The installer does not silently
replace your kernel or vendor driver stack.

```sh
umask 077
./hypermesh-deploy plan-setup --memory-mode uma --uma-gib 40 \
  --model-dir /YOUR/EXISTING/MODELS_DIRECTORY \
  --aiuditor-check-url https://YOUR_AIUDITOR/READINESS_PATH \
  --profile auto --headless > setup-plan.json
```

Read the entire plan, including blockers, package versions and headless changes.
Do not apply a blocked plan. Use its exact plan_sha256 only after review:

```sh
sudo ./hypermesh-deploy apply-setup --plan setup-plan.json --approve-plan SETUP_SHA256
```

If a reboot is required, follow the release's `plan-reboot --help`,
`reboot-setup --help` and `resume-setup --help`. Reconnect over SSH and
verify resume before agent installation. Never bypass a reboot marker.

## 2. Install the signed service, inactive

Software channel and control-plane network are separate selections. Obtain
the plane URL, tenant ID, issuer and dashboard origin for the network you intend
to join. The test software channel can be chosen independently.

```sh
./hypermesh-deploy plan-agent --setup-plan setup-plan.json \
  --release-url IMMUTABLE_RELEASE_URL --channel CHANNEL \
  --trusted-key ./hypermesh-release.pub \
  --fingerprints 1540ED8CD1EC4BDF72949FC78D741FF786A369FD \
  --control-plane-url https://YOUR_PLANE --tenant-id TENANT_UUID \
  --host-name YOUR_REPORTED_HOST_NAME > agent-plan.json
sudo ./hypermesh-deploy install-agent --plan agent-plan.json --approve-plan AGENT_SHA256
```

Review the agent plan before the second command. Keep this exact executable;
plans bind the installer build. Installation does not start the service.

## 3. Register through browser approval

```sh
sudo ./hypermesh-deploy register-agent --plan agent-plan.json \
  --approve-plan AGENT_SHA256 --issuer https://YOUR_AUTH/realms/YOUR_REALM \
  --dashboard-url https://YOUR_DASHBOARD --class-id YOUR_MEASURED_CLASS
sudo ./hypermesh-deploy registration-status --plan-sha AGENT_SHA256
sudo ./hypermesh-deploy check-agent --plan agent-plan.json --approve-plan AGENT_SHA256 --wireguard --heartbeat
```

Open the displayed device verification URL in a browser, sign in as the host
owner and approve its code. The registration appears in your dashboard.
Do not copy or delete device secrets or WireGuard keys. Class claims must match
measured hardware. Unsupported classes stop; ARM64/AMD64 package availability
alone does not enable a runtime backend.

## 4. Acquire, certify and activate

The operator must provision the class program, immutable catalog source/hash,
runtime digest and performance envelope. Missing configuration is a blocker,
not permission to skip certification. Current controlled runtime certification
supports admitted NVIDIA AGX/J4012 classes; AMD and other backends need their
own implementation and physical acceptance.

```sh
sudo ./hypermesh-deploy plan-certification --agent-plan agent-plan.json > certification-plan.json
sudo ./hypermesh-deploy certify-agent --plan certification-plan.json --approve-plan CERTIFICATION_SHA256 --run
sudo ./hypermesh-deploy activate-agent --plan certification-plan.json --approve-plan CERTIFICATION_SHA256 --start
sudo ./hypermesh-deploy host-status --plan certification-plan.json
```

Review the certification plan before running it. For a gated repository, accept
its license and add `--model-token-file /root/private-model-token` to certify-agent.
The token file must be root-owned and private. Never place tokens in a command
argument, plan, screenshot or support report. Activation requests original-owner
browser approval and starts paused. Enable selling separately in the dashboard
only after successful readiness checks.

## 5. Maintain and recover

```sh
sudo ./hypermesh-deploy plan-maintenance --plan certification-plan.json --operation stop > maintenance-plan.json
sudo ./hypermesh-deploy maintain-agent --plan maintenance-plan.json --approve-plan MAINTENANCE_SHA256 --apply
sudo ./hypermesh-deploy resume-agent --plan maintenance-plan.json --approve-plan MAINTENANCE_SHA256 --start
```

Review before applying. Maintenance drains current work and keeps a persistent
hold on new work. For reboot, plan `--operation reboot` and additionally approve
`--reboot` when applying; resume after reconnecting. A timed-out drain stays
paused and must be investigated, never force-stopped by the installer.

For stopped hosts, plan `plan-recovery --agent-plan agent-plan.json --operation
repair` (or uninstall), review its digest and apply with `recover-agent --plan
recovery-plan.json --approve-plan RECOVERY_SHA256 --apply`. Repair restores the
original approved signed payloads; uninstall removes only managed files.
Both preserve identities, model caches and private backups. Neither retires the
remote host or enables selling. Repair followed by resume needs the retained
maintenance/certification plans. Do not delete journals to make a check pass.

Read `plan-installer-update --help` for a separately reviewed CLI update.
A CLI update does not migrate or reapprove old plans.

## Evidence

```sh
sudo ./hypermesh-deploy support-bundle --plan-sha AGENT_SHA256 \
  --setup-plan-sha SETUP_SHA256 --certification-plan-sha CERTIFICATION_SHA256 \
  --output /root/hypermesh-support-new.json
```

Support export is read-only and redacted. A successfully exported failed stage
is still failed. Retain the original private plans/journals for recovery.
Actual reboot, hardware, network, driver and sustained-load acceptance must
still be tested on your box.
