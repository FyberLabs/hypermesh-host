# Host release versions

Each new test build now has its own semantic version: global build 57 is
`0.0.57`, build 58 is `0.0.58`, and so on. The patch number uses the existing
monotonically increasing build allocation across test and production. Numbers
may have gaps; versions do not depend on how many successful test releases exist.

The publisher derives the version after allocating the build under its storage
lease. Retrying the same allocation produces the same version, signed metadata,
binary version and artifact names. No separate version counter needs repair.
A later publication that allocates a new build receives a new patch version.

For example, build 57 publishes `hypermesh-deploy_0.0.57_linux_arm64` and
`hypermesh-agent_0.0.57_linux_arm64`. AMD64 follows the same naming. The version
also appears in signed release metadata and the channel manifest; the website,
control plane and verifier already resolve names from that metadata.

Test storage directories remain `test/<build>-<commit-prefix>/`, and permanent
GitHub test tags remain `test-<build>-<commit-prefix>`. Previously signed
`0.0.0` releases remain unchanged and downloadable while retained.

Production continues to require an explicitly selected `vX.Y.Z` tag. Automatic
test versioning does not promote a release or advance the production channel.
The global build number remains the host's update/rollback ordering authority.

Completed: per-build test patch versions, stable allocation retries, real
ARM64/AMD64 nonzero-version payload checks, and signed verifier regression
coverage. Production promotion remains an explicit operator choice.

## Installed channel and update ordering

Release binaries embed their original publication channel alongside version and
build. This value is independent of the configured update destination. A test
binary at version 0.0.57/build 57 can therefore install production version
0.0.1/build 58. The global build floor, failed-build exclusion, signature checks
and busy-host deferral still apply. Production-to-production updates retain
their semver ordering checks.

Legacy binaries without an embedded channel retain the conservative production
semver check. No channel is inferred from a version number or a mutable setting.
The per-build test version change ships together with this channel metadata and
the updater change; do not publish the version-only implementation.
