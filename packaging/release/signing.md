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
