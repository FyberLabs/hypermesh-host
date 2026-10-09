# Durable host authentication

The host signs in as itself after the owner completes initial enrollment. Guided
setup binds a separate Ed25519 signing key before enabling continuation/reboot.
Existing registered installations bind it on their next onboarding operation.
The running agent and installer share this identity; the owner's browser session
and short-lived device-code login are no longer needed for continued operation.

The private checkpoint is `/var/lib/hypermesh/machine-identity.json`, owned by root
with mode 0600. It contains the current private signing key, any pending rotation,
network/device/registration binding and a cached short-lived token. It is separate
from WireGuard and from the immutable enrollment journal. Do not paste it into
support logs or delete it to retry setup. Diagnostics redact machine tokens.

- Access tokens last ten minutes and renew automatically before expiry.
- Signing keys rotate after 30 days, with a 24-hour server overlap.
- The new key is saved before rotation is requested. Restart uses it to recover
  even when the plane committed a rotation but the response was lost.
- A file lock serializes installer/agent renewal. Atomic replacement and fsync
  preserve the checkpoint; symlinks, unsafe permissions and different device or
  network bindings are refused.
- A rejected token triggers one fresh exchange. It never triggers browser login
  or silently falls back to the retired enrollment bearer.
- Server retirement and bans still apply to unexpired tokens. Identity alone
  does not permit workloads or selling; approvals remain server decisions.

Deploy Panopticon's machine-identity migration, service and edge routes first.
An older installed agent retains access to the read-only signed-update endpoint
so it can fetch the new agent automatically; other host operations require the
signing identity after binding. Retirement still revokes update access.
There is no Azure MSI dependency or extra shared infrastructure secret. Legacy
hosts without this binding continue to use their existing credential until
onboarding installs the signing identity. Ownership/network changes require an
explicit fresh enrollment with new keys; this feature does not silently transfer
an existing identity.

Unit/race tests cover renewal, binding response loss, rotation response loss,
restart after overlap, concurrent callers and unsafe local state. Physical AGX
reboot/renewal acceptance remains pending deployment. Dashboard status delivery
and remote updates of the continuation executable remain separate roadmap work.


The credential service owns credential issuance. The HTTPS control-plane route relays
owner-approved enrollment to its configured authority; it
does not store issuer keys or token hashes. Discovery pins authority and control
plane IDs into each signed proof. `Provider.Peer` creates a separate private
checkpoint for another authorized credential-service delegation, preserving the host's
registration. `ContractToken` signs an exact contract and returns a short-lived
credential without replacing the management token. Arbitrary endpoints from
jobs are never accepted.

The existing workload dispatcher has not yet switched to these contract tokens;
multi-authority workload fanout remains integration work. Renewal itself now
goes directly to the saved credential-service HTTPS endpoint, without the enrollment service. Workload fanout across multiple authorities is separate.
