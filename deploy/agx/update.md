# Agent update channel

The agent checks for a new binary on a channel set in `/etc/hypermesh/agent.env` (mode 0600). An empty `HYPERMESH_UPDATE_CHANNEL` does not check. The check does not restart `hypermesh-wg` and does not reboot. The host has no GitHub token.

`design/test-reliability.md` (hypermesh-docs pull request 54, locked 2026-09-26) says the test channel tracks `main` and the prod channel tracks tagged releases. Hosts do not download those bytes from GitHub. The release workflow puts them in a private Azure Blob container. The control plane mints a short-lived read URL. The agent asks the plane with the same device credential it already uses.

## Channels

| `HYPERMESH_UPDATE_CHANNEL` | What it tracks | Blob prefix |
|---|---|---|
| `test` | Builds of `main`. The commit in `release.json` is the identity. The binary version stays `0.0.0`. | `test/<release-id>/`. release-id is `<build>-<first 12 hex chars of the commit>` (`58-0123456789ab`). The directory is not overwritten. Order is the integer `build`, not the string order of the release-id. |
| `prod` | Tagged semver `vX.Y.Z`. A newer version installs. An older one does not. | `prod/<X.Y.Z>/`. release-id is the semver with no leading `v`. The directory is not overwritten. The publisher also refuses to move the pointer to a semver or a build number that is not strictly newer. |

One workflow, `.github/workflows/release.yml`, publishes both channels: a push to `main` is `test`, and a tag `vX.Y.Z` is `prod`. It runs `packaging/release/publish-agent.sh`. A GitHub Release is also created so a person can download the files. The agent does not read it.

The channel pointer is a different object, outside `test/` and `prod/`:

- `channels/test.json`
- `channels/prod.json`

Each one is exactly `channel`, `version`, `commit`, `release_id`, `build`, and `artifacts` of `name`, `sha256`, and `size`. It has no `url` and no `path`. The plane builds each blob URL as `<channel>/<release_id>/<name>` (`release_id` defaults to `version` only when the index omits it; this publisher always sets it). The publisher writes the index only after every object under `<channel>/<release-id>/` (both binaries, `release.json`, `checksums.txt`, both signature files, and the pubkey) has been uploaded and downloaded back for a SHA-256 match. The upload is one Put Blob of a few hundred bytes, so a reader sees the previous document or the whole new one. A crash before that write leaves the channel on the last complete build. An existing object in a release-id directory is left in place; if its bytes differ, the check fails and the channel manifest is not moved. `channels/*.json` stays outside `test/`, so the test-channel cleanup rule cannot delete the pointer. A missing `channels/<channel>.json` is an empty channel: the plane returns 204 and the host skips.

Infra does not delete `test/` blobs by age. Rule `expireTestChannelArtifacts` deletes a block blob under `test/` only when that blob's index tag is `superseded=true`. This publisher sets that tag on releases older than the newest 20 and never on the release `channels/test.json` names. `channels/` and `prod/` are outside the prefix and are not tagged. Set Blob Tags does not change Last-Modified, and the rule's day count runs from last modification, so a tag on an already-old blob is eligible at the next lifecycle run. `expireTestArtifactVersions` deletes previous versions and snapshots under `test/` after 90 days and does not delete the current blob. If a download 404s, nothing is installed, and the next check tries again.

Objects in `<channel>/<release-id>/`:

- `hypermesh-agent_<version>_linux_arm64`
- `hypermesh-agent_<version>_linux_amd64`
- `release.json` (`channel`, `version`, `commit`, `build`, `release_id`). `build` is a JSON number. This file is signed via `checksums.txt`.
- `checksums.txt` (SHA-256 of the two binaries and `release.json`)
- `checksums.txt.sig` and `checksums.txt.asc` (detached GPG signatures over `checksums.txt`)
- `hypermesh-release.pub` (for an operator to install once; the agent does not trust a pubkey from the container)

`build` is one sequence across channels. While the `release-publish` group is held, the publisher sets it to `max(build in channels/test.json, build in channels/prod.json, GITHUB_RUN_NUMBER) + 1`. A missing index is left out of the max. That number is written into signed `release.json`, the binary ldflags, and the channel index. A host switching channel only moves to a strictly newer build: test build 57 accepts prod build 58 and refuses a replayed build of 57 or 3. ldflags set `buildinfo.Commit`, `buildinfo.Version`, and `buildinfo.Build` to the same number. A development binary has build 0.

The host refuses a candidate whose signed `build` is less than its own, on whichever channel it is checking. An equal build whose commit is the one already running is up to date: the agent logs that at debug and does not install. An equal build with a different commit is refused. Build 0 on a candidate is not newer than a running build of 0, so it is refused. A development binary can install the first positive build. Before replacing `channels/prod.json`, the publisher reads both channel indexes again and fails unless the new build is strictly greater than both. It also refuses a prod semver that is not strictly greater. A missing `channels/prod.json` is the first prod publish.

A re-run of a prod tag whose `prod/<X.Y.Z>/release.json` is already stored verifies that signature and reuses the file. It does not sign again. It uploads only blobs that are missing and whose sha256 matches the signed checksums. A local file that does not match fails with `bump the version`. The build number on that path is the one in the stored `release.json`.

This version does not expire a frozen old head. There is no freshness check on the signed release. A host that never sees a higher build stays on the binary it has.

Binaries are `CGO_ENABLED=0` builds of `./cmd/agent` for `linux/arm64` and `linux/amd64`.

The storage account name and container name are GitHub Environment variables. This repo does not write them into the workflow or the publish script. Upload uses `az storage blob upload --auth-mode login` after `azure/login` (`allow-no-subscriptions: true`, because the release app has no subscription role). There is no storage account key in the workflow.

Test and prod publish through one concurrency group, `release-publish`, with `cancel-in-progress: false`. That still keeps only one pending run: GitHub drops an older pending run when a newer one arrives. The workflow syntax has a `queue` property that would keep the pending run, and current GitHub Actions docs describe it. actionlint 1.7.12 rejects that property, so this repo does not set it. The publish script also acquires a 60-second lease on the blob `locks/publish`, retries while another publisher holds it, and renews the lease until the job exits. `max+1` is computed only after that lease is held. `AZURE_CONFIG_DIR` is set from `$RUNNER_TEMP` in a step (`$GITHUB_ENV`), not in the job `env` block. An always-run step logs `az` out and removes that directory. Third-party actions are pinned by commit SHA. `gpg` and `az` must be installed on the self-hosted runners. Environment `release` gates the job, including the prod path. The tag-on-main check runs only for a tag.

## What the host calls

Path constant: `api.PathUpdates` = `/api/v1/hypermesh/agent/updates/%s`.

```http
GET /api/v1/hypermesh/agent/updates/test?device_id=<device uuid>
Authorization: Bearer <device_secret>
X-Tenant-ID: <tenant uuid>
```

`prod` is the same path with `prod` in place of `test`. No body.

`200` JSON. `artifacts[].url` is a read-only SAS URL, about 10 minutes, for one blob. It is a credential. Do not log it.

```json
{
  "channel": "test",
  "version": "0.0.0",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "release_id": "42-0123456789ab",
  "build": 42,
  "artifacts": [
    {
      "name": "checksums.txt",
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
      "size": 180,
      "url": "https://<account>.blob.core.windows.net/<container>/test/<release-id>/checksums.txt?<sas>"
    },
    {
      "name": "checksums.txt.sig",
      "sha256": "<64 hex>",
      "size": 566,
      "url": "https://<account>.blob.core.windows.net/<container>/test/<release-id>/checksums.txt.sig?<sas>"
    },
    {
      "name": "release.json",
      "sha256": "<64 hex>",
      "size": 96,
      "url": "https://<account>.blob.core.windows.net/<container>/test/<release-id>/release.json?<sas>"
    },
    {
      "name": "hypermesh-agent_0.0.0_linux_arm64",
      "sha256": "<64 hex>",
      "size": 12000000,
      "url": "https://<account>.blob.core.windows.net/<container>/test/<release-id>/hypermesh-agent_0.0.0_linux_arm64?<sas>"
    }
  ]
}
```

`checksums.txt.asc` may be sent instead of, or in addition to, `checksums.txt.sig`. One signature that verifies is enough. Include the binary for the host's arch (`linux_arm64` or `linux_amd64`). `sha256` is 64 hex characters. `size` is the exact byte length. The agent caps the download at `size` and rejects a different length.

The plane reads `channels/<channel>.json` and puts `<channel>/<release_id>/<name>` into the SAS URL. The host accepts `build` and `release_id` on this 200 body. `build` must equal the `build` in the signed `release.json`. A missing index is `204`.

`204` means that channel has no build. The agent skips.

A download that 404s does not install. The agent tries again on the next check. The release named by `channels/test.json` is not tagged `superseded=true`, so the cleanup rule does not delete it.

`401` and `403` match the other agent routes. The agent does not install.

Panopticon owns this route. This repo only calls it.

## Verification

The signature and the SHA-256 in `checksums.txt` are the trust anchor. The plane's `sha256` has to match that signed file. A mismatch is a reject.

1. `GET` the path above with the device credential.
2. Download `checksums.txt` and `checksums.txt.sig` (or `.asc`) from the SAS URLs. The download sends no `Authorization` header. The read stops at `size`.
3. `gpg --status-fd 1 --verify` with the keys in `/etc/hypermesh/update-pubkey.asc`. The status must contain `GOODSIG` and a `VALIDSIG` line whose fingerprint is listed in `HYPERMESH_UPDATE_FINGERPRINTS`. A key that is only in the pubkey file is not enough. `EXPKEYSIG`, `REVKEYSIG`, `EXPSIG`, `BADSIG`, and `ERRSIG` reject the signature, including when `VALIDSIG` is also present. An expired or revoked key is rejected. A zero exit code without `VALIDSIG` rejects.
4. Download `release.json`. Its SHA-256 must be in the signed checksum file, and its channel, version, commit, and `build` must match the plane's 200 body. The host uses that signed `build` for the newer-than-current check.
5. Download the `linux/<arch>` binary. Its SHA-256 must be in the signed checksum file. Failure rejects.
6. A signed `build` lower than the running build is refused. An equal build is up to date when the commit matches, and is refused when it does not. A build this host already rolled back is skipped until a strictly newer build appears. Same test commit on a newer build, or a prod version that is not newer, skips. A lease or an unacked job result defers. Otherwise install.

Query strings are removed from errors. A missing pubkey, a bad signature, a size mismatch, or a checksum mismatch leaves the current binary in place.

## Trusted keys

`/etc/hypermesh/update-pubkey.asc` is one or more ASCII-armored public keys, concatenated. `HYPERMESH_UPDATE_FINGERPRINTS` in `/etc/hypermesh/agent.env` lists the fingerprints that may sign an update, separated by commas or whitespace. The agent accepts a signature only when `VALIDSIG` names one of those fingerprints. It does not accept every key in the file. It does not read a directory, and it does not download the pubkey from the release.

During rotation, enroll or Ansible writes the old block and the new block into that same file and adds the new fingerprint to the list. After every host has the new block, remove the old block and the old fingerprint. A signature from the removed key then fails.

`gpg` must be on `PATH`.

```bash
sudo install -m 0644 hypermesh-release.pub /etc/hypermesh/update-pubkey.asc
sudo install -m 0755 deploy/lib/update-rollback /usr/local/lib/hypermesh/update-rollback
```

Copy the unit that contains `ExecStartPre=/usr/local/lib/hypermesh/update-rollback` only after that script is installed. Without the script, systemd will not start the agent.

## Install and rollback

Install writes `hypermesh-agent.new` in the same directory, fsyncs the file and the directory, renames the current binary to `hypermesh-agent.pre-<version>`, renames the new file onto the destination, and fsyncs the directory again. `<version>` is the version (or commit) being replaced.

The marker `$HYPERMESH_STATE_DIR/update-pending` records `binary`, `backup`, `version`, `deadline`, `build`, and `release_id`. The default window is 2 minutes (`HYPERMESH_UPDATE_ROLLBACK`). Then the agent runs `systemctl restart hypermesh-agent` only.

The new process starts a watch before its first heartbeat. A successful heartbeat deletes the marker. If the window ends with no successful heartbeat, the watch copies the `.pre-<version>` backup back, writes `$HYPERMESH_STATE_DIR/update-bad` (atomic replace in the state dir), and exits. systemd `Restart=on-failure` starts that restored binary. Later checks skip that build until a strictly newer build is published. Build 0 is not written into `update-bad`.

If the new process crash-loops and never reaches the watch, `ExecStartPre` (`deploy/lib/update-rollback`) does the same restore once `deadline` is in the past, and writes the same `update-bad` file when `build` is a positive integer. Before the deadline it is a no-op, so a restart inside the window still gets a chance to heartbeat.

The control plane can be down during that two-minute window. The watch then rolls back a binary that would have been fine, and `update-bad` keeps the host from installing that same build again. A strictly newer build still installs.

Checks run at most once an hour (`HYPERMESH_UPDATE_EVERY`), including while a restart is deferred.

## Before the first release

Chris generates the signing key offline and creates the GitHub Environment `release`. The Azure account, container, app registration, and role live in the infra repo (draft PR 143). This repo does not create them and does not embed the account or container name.

The publish script exits before uploading when the private key is empty, or when the storage account and container variables are empty.

### 1. GPG key, offline

On a machine that is not the runner:

```bash
umask 077
export GNUPGHOME=$(mktemp -d)
chmod 700 "$GNUPGHOME"
# One line. The file is the passphrase. Empty only if you want no passphrase.
printf '%s' 'choose-a-passphrase' > "$GNUPGHOME/pass"
gpg --batch --pinentry-mode loopback --passphrase-file "$GNUPGHOME/pass" \
  --quick-generate-key "Hypermesh Agent Release <release@hyperme.sh>" rsa4096 sign never
fpr=$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1=="fpr" {print $10; exit}')
gpg --batch --armor --export "$fpr" > hypermesh-release.pub
gpg --batch --pinentry-mode loopback --passphrase-file "$GNUPGHOME/pass" \
  --armor --export-secret-keys "$fpr" > hypermesh-release.priv.asc
shred -u "$GNUPGHOME/pass" || rm -f "$GNUPGHOME/pass"
rm -rf "$GNUPGHOME"
unset GNUPGHOME
```

Keep `hypermesh-release.priv.asc` offline. `hypermesh-release.pub` is what hosts trust.

### 2. GitHub Environment `release`

On `FyberLabs/hypermesh-host`, a repo admin creates Environment `release`.

Secrets:

| Name | Value |
|---|---|
| `HYPERMESH_RELEASE_GPG_PRIVATE_KEY` | The whole `hypermesh-release.priv.asc` file, including the armor header |
| `HYPERMESH_RELEASE_GPG_PASSPHRASE` | The passphrase from step 1. Leave empty only when the key has none |

Variables (these are identifiers, not the signing key). Secrets of the same name also work. Do not put a real name in git.

| Name | Value |
|---|---|
| `AZURE_CLIENT_ID` | Application (client) id of the app registration or managed identity |
| `AZURE_TENANT_ID` | Directory (tenant) id |
| `AZURE_SUBSCRIPTION_ID` | Subscription id |
| `AZURE_STORAGE_ACCOUNT` | Storage account name from the entra output. Lowercase, 3–24 letters and digits |
| `AZURE_STORAGE_CONTAINER` | Private container name from that same output |

The release job's token scopes are `contents: write` (the human GitHub Release) and `id-token: write` (OIDC). `azure/login` sets `allow-no-subscriptions: true` because that app has no subscription role. The workflow default and CI stay `contents: read`.

Deployment protection for Environment `release` is not in this repo. A separate infra pull request limits that environment to `main` and tags matching `v*`, and a tag ruleset so only maintainers can push `v*`. The prod path in `.github/workflows/release.yml` fetches `origin/main` and fails unless `git merge-base --is-ancestor HEAD origin/main`. Pushes to `main` publish the test channel from that same file.

The self-hosted runner needs `gpg`, `az`, `python3`, `curl`, and `sha256sum`. Go comes from `setup-go`.

### 3. Azure, in the infra repo

This repo does not create these resources. Infra draft PR 143 does, and its `hypermesh_host_release_next_steps` output is what goes in the variables above.

- A storage account and a private container. Anonymous access stays off. Shared keys stay off.
- Lifecycle rule `expireTestChannelArtifacts` deletes block blobs under `test/` whose blob index tag is `superseded=true`. There is no 20-day age rule. The publisher sets that tag on test releases older than the newest 20 and never on the release `channels/test.json` names. `channels/` and `prod/` are outside that prefix. `expireTestArtifactVersions` deletes previous versions and snapshots under `test/` after 90 days.
- An app registration with a federated credential and no client secret:
  - Issuer: `https://token.actions.githubusercontent.com`
  - Subject: `repo:FyberLabs/hypermesh-host:environment:release`
  - Audience: `api://AzureADTokenExchange`
- Custom role **hypermesh-host publisher** on that container. It can read, write, add, and move blobs. Delete is denied when the path starts with `prod/`. `tags/write` is allowed only under `test/`. Built-in Contributor does not include that tag write.
- No account key and no connection string in GitHub.

### 4. Public key on the hosts

Enroll or Ansible installs `hypermesh-release.pub` to `/etc/hypermesh/update-pubkey.asc` (mode 0644) before the channel is turned on. To rotate, concatenate the new armored block under the old one, ship that file, wait until hosts have it, then ship a file with only the new block.
