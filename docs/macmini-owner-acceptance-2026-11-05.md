# MACMINI-1 owner enrollment: November 5, 2026

Issue #161 remains open. This record separates release, deployment and live owner
enrollment. `physical_acceptance=false`.

## Released and verified

Signed test build **115** / **0.0.115**, source
`479f537b103ecba3a60f9c37c8d95ff609c365d1`, was published by the normal
[Release workflow](https://github.com/hyperme-sh/hypermesh-host/actions/runs/41936038215).
The immutable release is
[test-115-479f537b103e](https://github.com/hyperme-sh/hypermesh-host/releases/tag/test-115-479f537b103e).
The public test-channel index returned that release after publication. Main CI
passed on the exact source revision.

The detached checksum signature was verified against reviewed primary key
`1540ED8CD1EC4BDF72949FC78D741FF786A369FD`. Darwin arm64 SHA-256 values:

| Artifact | SHA-256 |
| --- | --- |
| hypermesh-agent_0.0.115_darwin_arm64 | d7e28550de9ba09e82a591839fbc8f3e231824a5551accaa5eba41cab01c6981 |
| hypermesh-deploy_0.0.115_darwin_arm64 | 9a2ba058cc1d7e1eaf262f29e7d7060558e76995f5be4764f05dfa7e0000568b |

Both binaries were staged privately on MACMINI-1, their hashes rechecked and native
`register-agent --help` exercised. A fresh root-only six-file backup was verified.
Original backups and the earlier owner-bound journal were preserved. Staging did
not install binaries, stop services or retire the old enrollment.

## Deployment prerequisites

[Infra #374](https://github.com/FyberLabs/infra/pull/374) corrected the release
identity after the repository transfer. Entra applied before release retry, with
the same repository ID, ref restrictions and storage permissions. No human
GitHub approval gate was added.

[Edge #21](https://github.com/hyperme-sh/controlplane-edge/pull/21) supplies a
bounded download bridge on the pinned existing runtime; complete independent
edge composition remains unqualified. [Portal #25](https://github.com/hyperme-sh/controlplane-portal/pull/25)
wraps the exact qualified standalone archive without rebuilding application
bytes. [Infra #377](https://github.com/FyberLabs/infra/pull/377) selects immutable
ACR digests and deploys through the existing serialized lane.

The bridge passed application import and public/protected dispatch checks. Edge
source, installed wheel and rebuilt sdist each passed 271 tests. The unchanged
portal archive passed its successful main-push qualification; its non-root
container passed readiness and runtime URL/secret isolation checks. All 38
refresh regressions passed, including registry-login refusal and restoring the
prior GHCR edge after the ACR candidate failed. #377's Actions configuration and
Terraform plans passed; its Codex rollback finding was fixed and resolved.
These checks do not establish live owner acceptance.

Live Auth was independently observed using the narrow host-membership session
path. App Service readback has the expected test API/chat/realm URLs and client
`controlplane-frontend`. Actual rollout and public download checks must be
recorded before registration starts.

## Fresh observation and remaining acceptance

At **2026-11-05T00:15:48.969284Z**, the Mac still used device
`38564b54-3a47-408b-af56-b684ed9c62a1`. Agent and WireGuard LaunchDaemons were
active. `utun8` existed without a configured interface key, peers or handshake;
this is not tunnel acceptance. The probe changed no identity.

Before the approved replacement, freshly verify the authoritative empty lease
set, local lease/checkpoint absence, exact old device and private backups. Run
one signed CLI invocation with its actual private journal. The human owner must
complete device-code sign-in and portal approval. Verify portal continuation and
the terminal backup URL using the real request, without impersonation or native
credential self-approval.

Require new request/device/key and fresh accepted heartbeat to agree with owner
rows: experimental darwin-arm64-lab, enrolled, paused, uncertified, no model leases
or qualification jobs. Verify old enrollment archived and credentials revoked by
the real reinstall contract. Record ordered LaunchDaemon startup and WireGuard
readiness separately from peer/handshake evidence. Never restore a retired
credential. Enrollment proves neither sustained performance nor hardware/memory
protection. AGX and its separate expiry acceptance are outside this operation.

## Live deployment observations

The [portal deployment](https://github.com/FyberLabs/infra/actions/runs/41941593997)
succeeded. Azure readback selects
`acrfybercg47.azurecr.io/controlplane-portal@sha256:70d35de5ac638e10d9373e2ea6a5da5656cba140157fc983df8e72e3a8ecb207`.
At **2026-11-05T00:59:30.982654Z**, the actual public portal returned healthy and
served its host-connect page chunk with SHA-256
`99bc50fe3674d075f442a1cd4a06469c14452c0f990ed90341201eeb602b9c31`, matching the
qualified archive byte-for-byte. This verifies deployed handoff assets, not a
completed human sign-in or approval.

The [scoped test-plane apply](https://github.com/FyberLabs/infra/actions/runs/41943437950)
succeeded with convergence disabled. The redundant queued full apply from #377
was replaced with this scoped apply; no running deployment was interrupted.
Test-host-only convergence and public download verification remain pending.

The saved registration is bound to signed agent build 113. Its agent checksum
and signature were freshly reverified using the same reviewed trust anchor.
Resume that exact installation with installer 115, preserving the private journal
and owner binding. Agent 115 remains staged separately; substituting it before
recovery would change the installation digest and correctly fail the CLI guard.
No journal owner, request, key or digest is to be rewritten to bypass that guard.
