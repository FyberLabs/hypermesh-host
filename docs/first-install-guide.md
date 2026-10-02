# Install a Hypermesh host on Linux

Start from the Linux download section on hyperme.sh. Choose your processor and
software channel. ARM64 is for Jetson/AGX; AMD64 is for AMD/Intel Linux hosts.
Availability of a package does not mean every accelerator is supported.

## Download and verify

The first download, `hypermesh-bootstrap.py`, is a **verifier**, not the host
service. Python 3 runs it. GnuPG (`gpg`) checks the publisher's signature, and
the verifier checks the downloaded installer's checksum and selected build.

On Ubuntu, install these tools if missing:

```sh
sudo apt-get update
sudo apt-get install python3 gnupg
```

Save the verifier in a new directory and run the release-specific command
displayed on the website. It saves the verified `hypermesh-deploy` executable,
a signing public key, and `hypermesh-deploy.install.json` (the download receipt).
It does not install or start the host service.

## Start setup

In that directory, run:

```sh
sudo ./hypermesh-deploy
```

Follow the prompts. The installer offers your machine's hostname, the existing
`/var/lib/hypermesh/models` directory and a measured memory budget as defaults.
Use the models directory on the disk intended for model storage; setup never
formats disks or changes storage pools. Account network and software update
channel are separate choices.

The guide checks hardware, memory and disk capacity; shows required software
and headless changes for approval; installs the signed host service; opens a
browser device sign-in and host approval; checks WireGuard and heartbeat; then
guides model acquisition, certification and paused activation. You can sign in
using a browser on a different computer or phone.

Required changes declined, unsupported hardware or unavailable platform
configuration stop the process with an explanation. Existing vendor drivers
and kernels are preserved. The guide currently completes registration and
certification only for the supported measured AGX Orin 64 GiB and Orin NX
classes; other hardware needs its platform admission/certification work first.

## Resume

If interrupted or asked to reboot, reconnect and run the same command:

```sh
sudo ./hypermesh-deploy
```

Plans and progress are retained privately in
`/var/lib/hypermesh/installer/guided`. The installer resumes the unfinished
operation and uses the existing journals and checks. Do not delete these files
or download a different executable midway through setup. Reboot requests are
never repeated automatically.

Activation starts the host paused. Enable selling in the dashboard after
checking readiness. A completed installation can be rerun to check its live
status without repeating enrollment or creating another identity.

## Advanced operations

Individual planning, recovery, support and unattended commands remain
available through `./hypermesh-deploy advanced-help`. They are not required
for the normal guided installation. Manual installations made without a
guided session retain their original plans; the guide does not adopt them
or discard an existing identity.

### Recovering a prerequisite reboot

If a reboot request fails, the guide now displays the local logind error. Running
the installer again offers a fresh confirmation to retry on the same boot;
after a reboot it verifies and continues automatically. No reboot is retried
without confirmation.

A newer verified installer can restart prerequisite checks before agent
installation. It asks for approval, retains your selections and old evidence,
and creates fresh plans instead of reusing approvals from the older executable.
Keep the new executable and its verifier receipt together. Model files are
preserved. See [reboot recovery](setup-reboot.md#guided-recovery-from-the-agx-reboot-failure).
