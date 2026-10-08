# Agent update channel

The agent checks for a new binary on a channel set in `/etc/hypermesh/agent.env` (mode 0600). An empty `HYPERMESH_UPDATE_CHANNEL` does not check. The check does not restart `hypermesh-wg` and does not reboot. The host has no GitHub token.

`design/test-reliability.md` (hypermesh-docs pull request 54, adopted 2026-09-26) says the test channel tracks `main` and the prod channel tracks tagged releases. Hosts do not download those bytes from GitHub. The release workflow puts them in a private Azure Blob container. The control plane mints a short-lived read URL. The agent asks the plane with the same device credential it already uses.

## Channels

| `HYPERMESH_UPDATE_CHANNEL` | What it tracks | Blob prefix |
|---|---|---|
| `test` | Builds of `main`. The commit in `release.json` is the identity. The binary version stays `0.0.0`. | `test/<release-id>/`. release-id is `<build>-<first 12 hex chars of the commit>` (`58-0123456789ab`). The directory is not overwritten. Order is the integer `build`, not the string order of the release-id. |
| `prod` | Tagged semver `vX.Y.Z`. A newer version installs. An older one does not. | `prod/<X.Y.Z>/`. release-id is the semver with no leading `v`. The directory is not overwritten. The publisher also refuses to move the pointer to a semver or a build number that is not strictly newer. |

One workflow, `.github/workflows/release.yml`, publishes both channels: a push to `main` is `test`, and a tag `vX.Y.Z` is `prod`. It runs `packaging/release/publish-agent.sh`. A GitHub Release is also created so a person can download the files. The agent does not read it. `workflow_dispatch` uses the same ref guard: test only when `github.ref` is `refs/heads/main`, prod only when `github.ref` matches `refs/tags/v*` and that commit is an ancestor of `origin/main`. Any other ref fails before login.

The channel pointer is a different object, outside `test/` and `prod/`:

- `channels/test.json`
- `channels/prod.json`

Each one is exactly `channel`, `version`, `commit`, `release_id`, `build`, and `artifacts` of `name`, `sha256`, and `size`. It has no `url` and no `path`. The plane builds each blob URL as `<channel>/<release_id>/<name>` (`release_id` defaults to `version` only when the index omits it; this publisher always sets it). The publisher writes the index only after every object under `<channel>/<release-id>/` (both binaries, `release.json`, `checksums.txt`, both signature files, and the pubkey) has been uploaded and downloaded back for a SHA-256 match. The upload is one Put Blob of a few hundred bytes, so a reader sees the previous document or the whole new one. A crash before that write leaves the channel on the last complete build. An existing object in a release-id directory is left in place; if its bytes differ, the check fails and the channel manifest is not moved. `channels/*.json` stays outside `test/`, so the test-channel cleanup rule cannot delete the pointer. A missing `channels/<channel>.json` is an empty channel: the plane returns 204 and the host skips.

Infra does not delete `test/` blobs by age. Rule `expireTestChannelArtifacts` deletes a block blob under `test/` only when that blob's index tag is `superseded=true`. This publisher sets that tag before it writes `channels/test.json`. It keeps the newest 20 test releases, the release being published, and the release the current pointer still names. A failed tag does not move the pointer. `channels/` and `prod/` are outside the prefix and are not tagged. `az storage blob tag set` comes from the `storage-blob-preview` extension, pinned at `1.0.0b3` (`az extension add --name storage-blob-preview --version 1.0.0b3 --yes`) in the test job, which then sets `extension.use_dynamic_install=no`. Set Blob Tags does not change Last-Modified, and the rule's day count runs from last modification, so a tag on an already-old blob is eligible at the next lifecycle run. `expireTestArtifactVersions` deletes previous versions and snapshots under `test/` after 90 days and does not delete the current blob. If a download 404s, nothing is installed, and the next check tries again.

Objects in `<channel>/<release-id>/`:

- `hypermesh-agent_<version>_linux_arm64`
- `hypermesh-agent_<version>_linux_amd64`
- `release.json` (`channel`, `version`, `commit`, `build`, `release_id`). `build` is a JSON number. This file is signed via `checksums.txt`.
- `checksums.txt` (SHA-256 of the two binaries and `release.json`)
- `checksums.txt.sig` and `checksums.txt.asc` (detached GPG signatures over `checksums.txt`)
- `hypermesh-release.pub` (for an operator to install once; the agent does not trust a pubkey from the container)

`build` is one sequence across channels. While the `release-publish` group is held, the publisher sets it to `max(build in channels/test.json, build in channels/prod.json, GITHUB_RUN_NUMBER) + 1`. A missing index is left out of the max. That number is written into signed `release.json`, the binary ldflags, and the channel index. A host switching channel only moves to a strictly newer build: test build 57 accepts prod build 58 and refuses a replayed build of 57 or 3. ldflags set `buildinfo.Commit`, `buildinfo.Version`, and `buildinfo.Build` to the same number. A development binary has build 0.

The host refuses a candidate whose signed `build` is less than its own, on whichever channel it is checking. An equal build whose commit is the one already running is up to date: the agent logs that at debug and does not install. An equal build with a different commit is refused. Build 0 on a candidate is not newer than a running build of 0, so it is refused. A development binary can install the first positive build. Before replacing `channels/prod.json`, the publisher reads both channel indexes again and fails unless the new build is strictly greater than both. It also refuses a prod semver that is not strictly greater. A missing `channels/prod.json` is the first prod publish.

A re-run of a prod tag whose `prod/<X.Y.Z>/release.json` is already stored verifies that signature and reuses the file. It does not sign again. It uploads only blobs that are missing and whose sha256 matches the signed checksums. A local file that does not match fails with `bump the version`. The build number on that path is the one in the stored `release.json`. If `channels/prod.json` already names that `release_id`, `build`, and commit, the pointer is not written again. The same `release_id` and `build` with a different commit fails with `bump the version`. The GitHub Release for the tag is reused. An asset whose name and sha256 already match is left in place. A different sha256 fails with `bump the version`. Test still deletes the rolling GitHub Release named `test` and creates it again. If a test publish landed after the signed prod build, so that build is no longer greater than `channels/test.json`, the re-run fails with `bump the version` only when `channels/prod.json` will actually be written. A re-run whose pointer already names this release still repairs a GitHub Release that uploaded only some assets. The publisher renews the `locks/release-publish` lease immediately before the pointer write and does not move the pointer if that lease is gone. The release job refuses to publish when `az version` is older than 2.90.0. CI checks az flags in a private `AZURE_CONFIG_DIR` under `$RUNNER_TEMP`, once without `storage-blob-preview` and once with it pinned at 1.0.0b3, so the extension is not installed into the runner's default Azure config.

This version does not expire a frozen old head. There is no freshness check on the signed release. A host that never sees a higher build stays on the binary it has.

Binaries are `CGO_ENABLED=0` builds of `./cmd/agent` for `linux/arm64` and `linux/amd64`.

The storage account name and container name are repository variables. This repo does not write them into the workflow or the publish script. Upload uses `az storage blob upload --auth-mode login` after `azure/login` (`allow-no-subscriptions: true`, because the release app has no subscription role). There is no storage account key in the workflow.

Test and prod publish through one concurrency group, `release-publish`, with `cancel-in-progress: false` and `queue: max`. `queue: max` keeps every pending run of that group. actionlint 1.7.12's schema does not list the key, so `.github/actionlint.yaml` ignores that exact error on `.github/workflows/release*.yml`. The publish script also acquires a 60-second lease on the blob `locks/release-publish`, retries while another publisher holds it, and renews the lease until the job exits. That blob is the one write and lease both publishers have outside their channel prefix. `max+1` is computed only after that lease is held. `AZURE_CONFIG_DIR` is set from `$RUNNER_TEMP` in a step (`$GITHUB_ENV`), not in the job `env` block. An always-run step shreds `GNUPGHOME` when that directory is still present, removes it, runs `gpgconf --kill all`, logs `az` out, and removes the Azure config directory. Third-party actions are pinned by commit SHA. `gpg` and `az` must be installed on the self-hosted runners. There is no deployment environment on this job. Test publishes only from `refs/heads/main` and logs in with repository variable `HYPERMESH_HOST_RELEASE_TEST_CLIENT_ID`. Prod publishes only from `refs/tags/v*` after `git merge-base --is-ancestor HEAD origin/main` succeeds, and logs in with `HYPERMESH_HOST_RELEASE_PROD_CLIENT_ID`. `AZURE_TENANT_ID` is shared. The OIDC subjects are `repo:FyberLabs/hypermesh-host:ref:refs/heads/main` for test and `repo:FyberLabs/hypermesh-host:ref:refs/tags/v*` for prod. The test identity writes `test/`, `channels/test.json`, and `locks/release-publish`. It reads `channels/prod.json` and does not write it. It sets `superseded=true` on older test artifacts. The prod identity writes `prod/`, `channels/prod.json`, and `locks/release-publish`. It reads `channels/test.json` and does not write it. Prod does not delete blobs and does not set blob tags.

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

The release maintainer generates the signing key offline and stores it in Key Vault `kv-fyber-hmhost` (`rg-fyber`) as `hypermesh-host-release-gpg-private-key` and `hypermesh-host-release-gpg-passphrase`. This repo does not create the vault, the storage account, the container, the app registration, or the role. This private repository is on GitHub Free, so there is no Environment `release` and no environment secrets to create.

The publish script exits before uploading when the Key Vault name or secret names are empty, when the key secret is empty, or when the storage account and container variables are empty.

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

### 2. Repository variables

On `FyberLabs/hypermesh-host`, a repo admin sets repository variables. Do not create a GitHub Environment, and do not put the signing key in GitHub. The key is already named in Key Vault `kv-fyber-hmhost`: secrets `hypermesh-host-release-gpg-private-key` and `hypermesh-host-release-gpg-passphrase`.

| Name | Value |
|---|---|
| `HYPERMESH_HOST_RELEASE_TEST_CLIENT_ID` | Client id of the main-branch app. Output `hypermesh_host_test_client_id`. Writes `test/`, `channels/test.json`, and `locks/release-publish`. Reads `channels/prod.json`. Sets `superseded=true` on older `test/` blobs |
| `HYPERMESH_HOST_RELEASE_PROD_CLIENT_ID` | Client id of the tag app. Output `hypermesh_host_prod_client_id`. Writes `prod/`, `channels/prod.json`, and `locks/release-publish`. Reads `channels/test.json`. No blob delete and no blob tags |
| `AZURE_TENANT_ID` | Directory (tenant) id. Output `hypermesh_host_github_tenant_id`. Shared by both jobs. This spelling is what `hypermesh_host_release_next_steps` prints |
| `AZURE_SUBSCRIPTION_ID` | Subscription id. Output `hypermesh_host_github_subscription_id` |
| `AZURE_STORAGE_ACCOUNT` | Storage account name. Output `hypermesh_host_storage_account_name`. Lowercase, 3–24 letters and digits |
| `AZURE_STORAGE_CONTAINER` | Private container name. Output `hypermesh_host_container_name` |
| `HYPERMESH_RELEASE_KV_NAME` | Optional. Default `kv-fyber-hmhost` in `rg-fyber` |
| `HYPERMESH_RELEASE_GPG_KEY_SECRET` | Optional. Default `hypermesh-host-release-gpg-private-key` |
| `HYPERMESH_RELEASE_GPG_PASSPHRASE_SECRET` | Optional. Default `hypermesh-host-release-gpg-passphrase`. The secret value may be empty when the key has none |

The release job's token scopes are `contents: write` (the human GitHub Release) and `id-token: write` (OIDC). `azure/login` reads the client, tenant, and subscription variables and sets `allow-no-subscriptions: true` because that app has no subscription role. The workflow default and CI stay `contents: read`.

The workflow refuses every ref except `refs/heads/main` (test) and `refs/tags/v*` whose commit is an ancestor of `origin/main` (prod). That includes `workflow_dispatch` from any other ref. A tag ruleset so only maintainers can push `v*` lives in infra, not in this repo.

The first real publish needs infra #143 applied and these six repository variables set: `HYPERMESH_HOST_RELEASE_TEST_CLIENT_ID`, `HYPERMESH_HOST_RELEASE_PROD_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`, `AZURE_STORAGE_ACCOUNT`, and `AZURE_STORAGE_CONTAINER`.

The self-hosted runner needs `gpg`, `az`, `python3`, `curl`, `sha256sum`, and `shred` when it is available. Go comes from `setup-go`. After login the publisher runs `az keyvault secret show --vault-name kv-fyber-hmhost --name hypermesh-host-release-gpg-private-key --query value -o tsv`, and the same command for `hypermesh-host-release-gpg-passphrase`, into a `chmod 700` `GNUPGHOME`. That command has no `--auth-mode`. Storage blob commands still pass `--auth-mode login`. It masks the passphrase and imports with `--batch --pinentry-mode loopback --passphrase-file`. It does not enable xtrace. The always-run step shreds that directory, runs `gpgconf --kill all`, and runs `az logout`. CI installs azure-cli 2.90.0 and `storage-blob-preview` 1.0.0b3, then checks every `az` flag in the release workflow and the publish scripts against that CLI's `--help`.

### 3. Azure, in the infra repo

This repo does not create these resources. Infra draft PR 143 does, and its `hypermesh_host_release_next_steps` output is what goes in the variables above.

- A storage account and a private container. Anonymous access stays off. Shared keys stay off.
- Lifecycle rule `expireTestChannelArtifacts` deletes block blobs under `test/` whose blob index tag is `superseded=true`. There is no 20-day age rule. The publisher sets that tag before the channel pointer moves, on test releases older than the newest 20, and never on the release being published or the release `channels/test.json` still names. A tag failure leaves the pointer where it is. `channels/` and `prod/` are outside that prefix. `expireTestArtifactVersions` deletes previous versions and snapshots under `test/` after 90 days.
- An app registration with two federated credentials and no client secret:
  - Issuer: `https://token.actions.githubusercontent.com`
  - Audience: `api://AzureADTokenExchange`
  - Test subject: `repo:FyberLabs/hypermesh-host:ref:refs/heads/main`
  - Prod subject: `repo:FyberLabs/hypermesh-host:ref:refs/tags/v*`
- Key Vault `kv-fyber-hmhost` in `rg-fyber`. Both publisher apps are Key Vault Secrets User on that vault. The secret names are `hypermesh-host-release-gpg-private-key` and `hypermesh-host-release-gpg-passphrase`. The values are the armored private key and the passphrase. They are not GitHub secrets. The workflow uses those names unless the repository variables override them.
- Custom role **hypermesh-host publisher** on that container. It can read, write, add, and move blobs. Both publishers may write and lease exactly `locks/release-publish` and nothing else outside their channel. Test may read `channels/prod.json`. Prod may read `channels/test.json`. Those reads do not grant a write. Delete stays denied for prod on every path. `tags/write` is on `test/` only, and the test publisher sets `superseded=true` itself. Prod has no tag operation. Built-in Contributor does not include that tag write.
- No account key and no connection string in GitHub.

### 4. Public key on the hosts

Enroll or Ansible installs `hypermesh-release.pub` to `/etc/hypermesh/update-pubkey.asc` (mode 0644) before the channel is turned on. To rotate, concatenate the new armored block under the old one, ship that file, wait until hosts have it, then ship a file with only the new block.
