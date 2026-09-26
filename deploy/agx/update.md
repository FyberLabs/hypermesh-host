# Agent update channel

The agent checks for a new binary on a channel set in `/etc/hypermesh/agent.env` (mode 0600). An empty `HYPERMESH_UPDATE_CHANNEL` does not check. The check does not restart `hypermesh-wg` and does not reboot.

`design/test-reliability.md` is not on `FyberLabs/hypermesh-docs` main (looked up 2026-09-26, commit `808dd58`). This repo's only release convention before this file was GoReleaser checksums for `hypermesh-deploy`, with no signature. Agent releases follow that checksum file and sign it with GPG detach-sign. There is no other signing scheme in this org to follow.

## Channels

| `HYPERMESH_UPDATE_CHANNEL` | What it tracks | Where it is published |
|---|---|---|
| `test` | Builds of `main`. The commit in `release.json` is the identity. | Rolling **prerelease** tag `test` on `FyberLabs/hypermesh-host`. Each main build deletes and recreates that release. |
| `prod` | Tagged semver `vX.Y.Z` (no prerelease suffix). A newer version installs. An older one does not. | GitHub's latest **stable** release. Prereleases, including `test`, are not latest. |

Both workflows are `.github/workflows/release.yml` (tag) and `.github/workflows/release-test.yml` (push to `main`). They run `packaging/release/publish-agent.sh`. Assets on the release:

- `hypermesh-agent_<version>_linux_arm64`
- `hypermesh-agent_<version>_linux_amd64`
- `release.json` (`channel`, `version`, `commit`)
- `checksums.txt` (SHA-256)
- `checksums.txt.sig` (detached GPG signature over `checksums.txt`)
- `hypermesh-release.pub` (the public key, for the operator to install once)

`test` builds use version `0.0.0`. The commit in the signed `release.json` is what the agent compares. Binaries are `CGO_ENABLED=0` builds of `./cmd/agent` for `linux/arm64` and `linux/amd64`. ldflags set `buildinfo.Commit` and `buildinfo.Version`.

## What must be provisioned before the first release

The signing key does not exist yet. A repo admin (Chris) creates the GitHub Environment **`release`** on `FyberLabs/hypermesh-host` and adds:

| Secret | What it is |
|---|---|
| `HYPERMESH_RELEASE_GPG_PRIVATE_KEY` | ASCII-armored GPG private key used only to sign `checksums.txt` |
| `HYPERMESH_RELEASE_GPG_PASSPHRASE` | Passphrase for that key. Omit or leave empty only when the key has none. |

The release job's token permission is `contents: write`. The workflow default stays `contents: read`, same as CI. No other scope is requested. The script exits before creating a release when the private key is empty, so an unsigned upload is not published.

The repo is private. Each box also needs a GitHub token that can read release assets, in `agent.env`:

```bash
HYPERMESH_UPDATE_GITHUB_TOKEN=
```

That token is not the signing key. It is not logged. A check without it fails closed.

Install the public key once, out of band, from a release you already trust (or from the admin who created the key). The agent does not download the pubkey from the release it is verifying.

```bash
sudo install -m 0644 hypermesh-release.pub /etc/hypermesh/update-pubkey.asc
sudo install -m 0755 deploy/lib/update-rollback /usr/local/lib/hypermesh/update-rollback
```

`gpg` must be on `PATH`. A missing pubkey, a bad signature, or a checksum mismatch is rejected and the current binary stays.

Copy the unit that contains `ExecStartPre=/usr/local/lib/hypermesh/update-rollback` only after that script is installed. Without the script, systemd will not start the agent.

## Verification

1. Download the GitHub release document (`/releases/tags/test` or `/releases/latest`).
2. Download `checksums.txt` and `checksums.txt.sig`.
3. `gpg --verify` the signature with the installed pubkey. Failure rejects.
4. Download `release.json` and require its SHA-256 in the signed checksum file.
5. Require `release.json` `channel` to match this box.
6. Download the `linux/<arch>` agent binary and require its SHA-256. Failure rejects.
7. Same test commit, or a prod version that is not newer, skips. A lease or an unacked job result defers. Otherwise install.

## Install and rollback

Install writes `hypermesh-agent.new` in the same directory, fsyncs the file and the directory, renames the current binary to `hypermesh-agent.pre-<version>`, renames the new file onto the destination, and fsyncs the directory again. `<version>` is the version (or commit) being replaced.

The marker `$HYPERMESH_STATE_DIR/update-pending` records `binary`, `backup`, `version`, and `deadline`. The default window is 2 minutes (`HYPERMESH_UPDATE_ROLLBACK`). Then the agent runs `systemctl restart hypermesh-agent` only.

The new process starts a watch before its first heartbeat. A successful heartbeat deletes the marker. If the window ends with no successful heartbeat, the watch copies the `.pre-<version>` backup back and exits. systemd `Restart=on-failure` starts that restored binary.

If the new process crash-loops and never reaches the watch, `ExecStartPre` (`deploy/lib/update-rollback`) does the same restore once `deadline` is in the past. Before the deadline it is a no-op, so a restart inside the window still gets a chance to heartbeat.

Checks run at most once an hour (`HYPERMESH_UPDATE_EVERY`), including while a restart is deferred.
