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

Save the verifier in a directory, then run:

```sh
python3 ./hypermesh-bootstrap.py
# Or select newer test builds:
python3 ./hypermesh-bootstrap.py --channel test
```

The verifier discovers the latest published release and detects Linux ARM64 or
AMD64 locally. Production never falls back to test when unpublished. Use
`--overwrite` to explicitly replace previous downloads after verification.
Advanced `--release-url`, `--min-build` and `--arch` options remain available for
pinned releases or downloading for another host. Unsupported channels and
architectures fail clearly rather than choosing another build.

It saves the verified `hypermesh-host` executable,
a signing public key, and `hypermesh-host.install.json` (the download receipt).
It does not install or start the host service.

## Start setup

In that directory, run:

```sh
sudo ./hypermesh-host
```

Follow the prompts. The installer offers your machine's hostname and the existing
`/var/lib/hypermesh/models` directory as defaults. Choose the model cache quota
and free-space reserve for that filesystem. Setup never formats disks or changes
storage pools. Account network and software update channel are separate choices.

The guide checks hardware, available memory and disk capacity and shows the
required software and headless changes. Its combined approval covers those
changes, any required reboot, signed host service installation, model tests, and
service startup and selling once the host is certified. Browser sign-in and host enrollment happen
before machine changes or reboot. You can use a browser on another computer or
phone.

After enrollment setup installs the verified CLI at `/usr/local/bin/hypermesh-host`, retains a private continuation copy, and
enables `hypermesh-onboarding.timer`. That service resumes the saved setup after
boot, disconnection or temporary failure. It uses the saved device credential;
it does not need another browser login. The guide continues immediately while
you are connected, with a lock preventing the background service from doing the
same work concurrently.

Existing vendor drivers and kernels are preserved. The guide currently completes
registration and certification only for the supported measured AGX Orin 64 GiB
and Orin NX classes; other hardware needs its platform admission/certification
work first. Changes outside the saved consent remain blocked and visible in the
service journal; they are never approved by a generic automatic “yes.”

## Give your consent once with `--yes`

To avoid the setup questions, give your consent for the whole setup once:

```sh
sudo ./hypermesh-host --yes
```

`--yes` accepts the questions of a first setup, and says so as it goes. Those are
the account network, the host name, model storage, headless mode, the model cache
quota and reserve, creating a missing model directory, the system changes, the
model tests, and continuing automatically, including starting to sell once the
host is certified. The saved approval is the same one a typed "yes" records. Answers come
from what the installer detects on this host: network `production`, this machine's
host name, `/var/lib/hypermesh/models`, headless mode on, and a cache quota and
reserve it suggests for that filesystem.

To use different settings in the same run, add the flag for each one:

| Flag | Setting |
| --- | --- |
| `--network production\|test` | account network |
| `--host-name NAME` | name shown in your dashboard |
| `--model-dir /path` | existing model storage directory |
| `--headless=false` | keep the graphical login |
| `--cache-quota-gib N` | model cache quota |
| `--cache-reserve-gib N` | free space to keep (at least 2) |

A setting flag answers its own question even without `--yes`. In a setup that
has already started, a setting flag that differs from the saved one stops with an
error, before anything is changed, rather than being ignored. The two model cache
flags replace a model cache policy that is already saved, as `--cache-settings`
does. A session approved by an older installer covered paused startup only;
`--yes` gives the consent to start selling for such a session too.

`--yes` still stops with a message when this host's own settings are wrong, do not
fit, or fail a check or benchmark, for example an unsupported processor class, a
model quota larger than the disk, or a failed model test. It does not answer
questions that were not part of that consent: accepting changed platform
packages, a new browser approval, switching to a newer installer, or a WireGuard
repair. Run the installer again without `--yes` to answer those. Browser sign-in
is still done by you, in any browser. `--yes` cannot be combined with `--continue`.

## Automatic continuation

Do not rerun the installer after reboot or while waiting for benchmark review.
It runs the required tests, retains evidence while the network admin reviews it,
and continues certification and activation after approval. A certified host starts
selling automatically; pause or resume it with `sudo hypermesh-host sell pause`
and `sell resume`. The first host of a new hardware class is reviewed by a network
admin, which is the only step after your approval that waits on a person. API/network
failures retry with durable exponential backoff from one minute to one hour.
Waiting for review polls about once a minute, without repeating successful tests.
After successful live status verification the onboarding timer disables itself;
the installed agent remains responsible for normal operation and future boots.
Selling remains a separate dashboard setting.

Plans, credentials and progress are retained privately in
`/var/lib/hypermesh/installer/guided`. Model files and the host identity survive
retries. For troubleshooting only, inspect:

```sh
sudo systemctl status hypermesh-onboarding.timer hypermesh-onboarding.service
sudo journalctl -u hypermesh-onboarding.service --since '1 hour ago'
sudo cat /var/lib/hypermesh/installer/guided/continuation-status.json
```

Existing registered installations made with an older release need one recovery
invocation of the newer verified installer to enable this service. This does not
repeat enrollment or discard approvals. An unfinished older session before
registration still requires its owner enrollment; it is not silently adopted.

Dashboard delivery of continuation status, remote approval of additional machine
changes, and automatic updates to the continuation executable remain follow-up
work. The normal agent's signed updater is separate. This service runs the
verified installer build whose plans were approved.

## Advanced operations

Individual planning, recovery, support and unattended commands remain
available through `./hypermesh-host advanced-help`. They are not required
for the normal guided installation. Manual installations made without a
guided session retain their original plans; the guide does not adopt them
or discard an existing identity.

### Recovering a prerequisite reboot

The continuation service retries an unconfirmed prerequisite reboot using the
saved plan and the original reboot consent. It still enforces host inactivity,
shutdown inhibitors and the original machine/boot binding. Once a new boot is
observed it verifies prerequisites before proceeding; it does not reboot again.

Unexpected platform drift or damaged state remains an error requiring recovery.
Preserve the journals when diagnosing it. Advanced manual reboot commands retain
their explicit approval behavior.

After initial owner approval, setup binds a [durable host signing identity](machine-identity.md)
before reboot. Token renewal and key rotation happen automatically; they do not
require keeping your browser signed in.

## CLI name and compatibility

`hypermesh-host` is the host setup and management CLI. After setup, use
`sudo hypermesh-host sell status`, `sell pause`, or `sell resume` from any
directory. Existing `hypermesh-deploy` commands remain a compatibility entry
point. Signed release artifact filenames and private continuation paths retain
the old name so existing verifiers and timers continue to work. The public CLI
is installed only after signature verification and saved setup consent.
