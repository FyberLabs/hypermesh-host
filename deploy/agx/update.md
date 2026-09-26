# Agent update channel

The agent checks for a new binary on a channel set in `/etc/hypermesh/agent.env` (mode 0600). An empty `HYPERMESH_UPDATE_CHANNEL` does not check. The check does not restart `hypermesh-wg` and does not reboot. The host has no GitHub token.

`design/test-reliability.md` (hypermesh-docs pull request 54, locked 2026-09-26) says the test channel tracks `main` and the prod channel tracks tagged releases. Hosts do not download those bytes from GitHub. The release workflow puts them in a private Azure Blob container. The control plane mints a short-lived read URL. The agent asks the plane with the same device credential it already uses.

## Channels

| `HYPERMESH_UPDATE_CHANNEL` | What it tracks | Blob prefix |
|---|---|---|
| `test` | Builds of `main`. The commit in `release.json` is the identity. Version is `0.0.0`. | `test/0.0.0/` overwritten on each main build. `test/current.json` is the same `release.json`. |
| `prod` | Tagged semver `vX.Y.Z`. A newer version installs. An older one does not. | `prod/<X.Y.Z>/`. `prod/current.json` points at the version this workflow just published. |

Workflows: `.github/workflows/release.yml` (tag `vX.Y.Z`) and `.github/workflows/release-test.yml` (push to `main`). Both run `packaging/release/publish-agent.sh`. A GitHub Release is also created so a person can download the files. The agent does not read it.

Objects in `<channel>/<version>/`:

- `hypermesh-agent_<version>_linux_arm64`
- `hypermesh-agent_<version>_linux_amd64`
- `release.json` (`channel`, `version`, `commit`)
- `checksums.txt` (SHA-256 of the two binaries and `release.json`)
- `checksums.txt.sig` and `checksums.txt.asc` (detached GPG signatures over `checksums.txt`)
- `hypermesh-release.pub` (for an operator to install once; the agent does not trust a pubkey from the container)

Binaries are `CGO_ENABLED=0` builds of `./cmd/agent` for `linux/arm64` and `linux/amd64`. ldflags set `buildinfo.Commit` and `buildinfo.Version`.

The storage account name and container name are GitHub Environment variables. This repo does not name a real account. Upload uses `az storage blob upload --auth-mode login` after `azure/login`. There is no storage account key in the workflow.

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
  "artifacts": [
    {
      "name": "checksums.txt",
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
      "size": 180,
      "url": "https://<account>.blob.core.windows.net/<container>/test/0.0.0/checksums.txt?<sas>"
    },
    {
      "name": "checksums.txt.sig",
      "sha256": "<64 hex>",
      "size": 566,
      "url": "https://<account>.blob.core.windows.net/<container>/test/0.0.0/checksums.txt.sig?<sas>"
    },
    {
      "name": "release.json",
      "sha256": "<64 hex>",
      "size": 96,
      "url": "https://<account>.blob.core.windows.net/<container>/test/0.0.0/release.json?<sas>"
    },
    {
      "name": "hypermesh-agent_0.0.0_linux_arm64",
      "sha256": "<64 hex>",
      "size": 12000000,
      "url": "https://<account>.blob.core.windows.net/<container>/test/0.0.0/hypermesh-agent_0.0.0_linux_arm64?<sas>"
    }
  ]
}
```

`checksums.txt.asc` may be sent instead of, or in addition to, `checksums.txt.sig`. One signature that verifies is enough. Include the binary for the host's arch (`linux_arm64` or `linux_amd64`). `sha256` is 64 hex characters. `size` is the exact byte length. The agent caps the download at `size` and rejects a different length.

`204` means that channel has no build. The agent skips.

`401` and `403` match the other agent routes. The agent does not install.

Panopticon owns this route. This repo only calls it.

## Verification

The signature and the SHA-256 in `checksums.txt` are the trust anchor. The plane's `sha256` has to match that signed file. A mismatch is a reject.

1. `GET` the path above with the device credential.
2. Download `checksums.txt` and `checksums.txt.sig` (or `.asc`) from the SAS URLs. The download sends no `Authorization` header. The read stops at `size`.
3. `gpg --verify` with the keys in `/etc/hypermesh/update-pubkey.asc`. Failure rejects.
4. Download `release.json`. Its SHA-256 must be in the signed checksum file, and it must match the plane's channel, version, and commit.
5. Download the `linux/<arch>` binary. Its SHA-256 must be in the signed checksum file. Failure rejects.
6. Same test commit, or a prod version that is not newer, skips. A lease or an unacked job result defers. Otherwise install.

Query strings are removed from errors. A missing pubkey, a bad signature, a size mismatch, or a checksum mismatch leaves the current binary in place.

## Trusted keys

`/etc/hypermesh/update-pubkey.asc` is one or more ASCII-armored public keys, concatenated. The agent accepts a signature that any key in that file verifies. It does not read a directory, and it does not download the pubkey from the release.

During rotation, enroll or Ansible writes the old block and the new block into that same file. After every host has the new block, remove the old block. A signature from the removed key then fails.

`gpg` must be on `PATH`.

```bash
sudo install -m 0644 hypermesh-release.pub /etc/hypermesh/update-pubkey.asc
sudo install -m 0755 deploy/lib/update-rollback /usr/local/lib/hypermesh/update-rollback
```

Copy the unit that contains `ExecStartPre=/usr/local/lib/hypermesh/update-rollback` only after that script is installed. Without the script, systemd will not start the agent.

## Install and rollback

Install writes `hypermesh-agent.new` in the same directory, fsyncs the file and the directory, renames the current binary to `hypermesh-agent.pre-<version>`, renames the new file onto the destination, and fsyncs the directory again. `<version>` is the version (or commit) being replaced.

The marker `$HYPERMESH_STATE_DIR/update-pending` records `binary`, `backup`, `version`, and `deadline`. The default window is 2 minutes (`HYPERMESH_UPDATE_ROLLBACK`). Then the agent runs `systemctl restart hypermesh-agent` only.

The new process starts a watch before its first heartbeat. A successful heartbeat deletes the marker. If the window ends with no successful heartbeat, the watch copies the `.pre-<version>` backup back and exits. systemd `Restart=on-failure` starts that restored binary.

If the new process crash-loops and never reaches the watch, `ExecStartPre` (`deploy/lib/update-rollback`) does the same restore once `deadline` is in the past. Before the deadline it is a no-op, so a restart inside the window still gets a chance to heartbeat.

Checks run at most once an hour (`HYPERMESH_UPDATE_EVERY`), including while a restart is deferred.

## Before the first release

Chris generates the signing key offline and creates the GitHub Environment `release`. The Azure account, container, app registration, and role live in the infra repo. This pull request does not create them.

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
| `AZURE_STORAGE_ACCOUNT` | Storage account name (lowercase, 3–24 letters and digits) |
| `AZURE_STORAGE_CONTAINER` | Private container name |

The release job's token scopes are `contents: write` (the human GitHub Release) and `id-token: write` (OIDC). The workflow default and CI stay `contents: read`.

The self-hosted runner needs `gpg`, `az`, `python3`, `curl`, and `sha256sum`. Go comes from `setup-go`.

### 3. Azure, in the infra repo

This repo does not create these resources.

- A storage account and a private container. Anonymous access stays off.
- An app registration, or a user-assigned managed identity, with a federated credential:
  - Issuer: `https://token.actions.githubusercontent.com`
  - Subject: `repo:FyberLabs/hypermesh-host:environment:release`
  - Audience: `api://AzureADTokenExchange`
- Role **Storage Blob Data Contributor** on that container (not the subscription).
- No account key and no connection string in GitHub.

### 4. Public key on the hosts

Enroll or Ansible installs `hypermesh-release.pub` to `/etc/hypermesh/update-pubkey.asc` (mode 0644) before the channel is turned on. To rotate, concatenate the new armored block under the old one, ship that file, wait until hosts have it, then ship a file with only the new block.
