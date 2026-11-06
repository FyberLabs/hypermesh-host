# Release signing material

The private signing key in Key Vault is required. For a key created without a
passphrase, omit `hypermesh-host-release-gpg-passphrase`; do not create a dummy
passphrase. The publisher accepts only Azure's explicit `SecretNotFound` result
for this optional secret and supplies an empty passphrase to GPG.

Access denial, disabled secrets, network errors and failure to read the private
key still stop publication. Protected keys require the matching passphrase
secret. Import, signing and signature verification remain mandatory; accepting
an absent passphrase never permits unsigned artifacts.

Regression coverage: `python3 packaging/release/test-signing-material.py` tests
absent, empty and populated passphrases, required-key failure, import failure,
and access/disabled/network/unclassified errors using local command stubs.

## Darwin first-install resources

New release payloads include `com.fyberlabs.hypermesh-agent.plist`,
`com.fyberlabs.hypermesh-wg.plist` and the unfilled `agent.env.example` as
individual files. `release_payload_names` drives the checksum list and both
storage/GitHub asset publication. Their exact bytes from `deploy/mac/` are
covered by the existing detached checksum signatures; they are not an archive
or a second download channel.

`hypermesh-host download --artifact darwin-agent-plist|darwin-wg-plist|darwin-env`
selects only these fixed filenames for Darwin ARM64 and uses the same trusted
key, signed release metadata, build floor and checksum verification as agent
binaries. Data outputs use mode 0600 and atomic no-overwrite publication. The
command does not install a plist or overwrite a configured environment file.
Older immutable releases are not retroactively extended; select a new release
containing the resources. Darwin automatic updates remain disabled.

## Historical test-only GitHub releases

An explicit main `workflow_dispatch` with `test_only=true` builds the approved
`expected_source_sha` on the main-only trusted debrunner group. GitHub's built-in
job token cannot create a release when that historical commit's workflow files
differ from main. Contents write alone does not grant workflow authority.

Before using this path, create a dedicated GitHub App, install it only on
`hyperme-sh/hypermesh-host`, and grant these repository permissions:

- Contents: read and write
- Workflows: read and write

Set repository variable `HYPERMESH_TEST_RELEASE_APP_ID` to the App's numeric ID
and repository secret `HYPERMESH_TEST_RELEASE_APP_PRIVATE_KEY` to its PEM key.
No personal access token or organization-wide installation is needed. Keep the
App dedicated to this repository; its authority can modify repository contents
and workflows, so do not reuse a broader deployment or user credential.

The workflow checks configuration before Azure login/signing and uses the
pinned official `actions/create-github-app-token` action to request an
installation token limited to the current repository and those two permissions.
Only explicit main test-only dispatches mint the token. Only the test publisher
receives it; an absent token stops publication instead of falling back to the
built-in token. The action revokes it in its post step, including failed jobs.
The private key is supplied directly to the action, not written into checkout
or signing directories. Default main/tag publication and promotion continue to
use the built-in token.

A missing installation or insufficient App permission fails token creation
before cloud login or signing. Configuration does not publish anything; an
actual release dispatch still requires the owner's specific release approval.
CI exercises the credential guards and publisher handoff with disposable token
fixtures. It does not create GitHub tags/releases, sign artifacts, or call Azure.
A real historical release remains a separate, explicitly authorized acceptance
step after App configuration.

References: [GitHub release permission requirements](https://docs.github.com/en/rest/releases/releases#create-a-release)
and [installation token action](https://github.com/actions/create-github-app-token).
