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
