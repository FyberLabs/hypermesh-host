# Private model delivery

This host capability is opt-in. The control-plane grant/admission integration is a
separate dependency; these changes do not enable private placements by themselves.
Set `HYPERMESH_MODEL_BLOB_ACCOUNT` to the exact approved Azure account to allow the
private path. Empty is the default and refuses private downloads.

A private job carries `source_ref: hmblob:<revision UUID>`, its ordinary job ID,
content hash and size, pinned OCI image, backend, and reviewed `model_configuration`
(`context_tokens`, `jinja`, `timeout_seconds`) plus `model_configuration_sha256`.
The configuration hash is SHA-256 of sorted compact JSON. The private path uses
those settings rather than host defaults, with one inference slot. It executes no
uploaded setup scripts or dependencies.

Before using any private bytes, including an exact verified cache hit and a running
same-lease retry, the agent calls
`POST /api/v1/hypermesh/agent/jobs/{job_id}/model-grant` with its device credential
and `{revision_id}`. The server must reauthorize the active job, enrolled host,
revision, scan, scope and lab/human verdict for every grant. A failed grant closes
the path even if the cached content is intact.

The transient response binds job/revision, backend, runtime image, configuration
hash, SHA-256, size, ETag, immutable Azure version and expiration to a read-only SAS.
The agent accepts only HTTPS on the configured account and exact quarantine path,
with a version-specific read-only grant lasting at most 15 minutes. It rejects
redirects, alternate containers/paths/accounts, duplicate query keys and extra
storage operations. Azure receives no device/Hugging Face bearer token. The signed
URL is never put in job state, cache manifests, result bodies or logs.

A download requires matching response ETag, version and content length; the existing
bounded atomic pull checks every byte and SHA-256, enforces disk/cache budgets and
removes failed partials. Transfers have an 80-minute HTTP timeout. Expired grants
are renewed by retrying the authenticated job. Revocation prevents new grants;
already-issued storage credentials remain valid until expiry. Runtime shutdown and
idle private-cache cleanup depend on the control-plane revocation/cleanup workflow.
Artifacts above 50 GB are refused and remain v1+.

Validation covers every immutable grant binding, hostile URL/SAS inputs, response
version/ETag/size checks, unchanged server configuration and authorization failure
on an otherwise valid cached artifact. Live Azure delivery and GPU lab/lease
acceptance remain separate checks.

Private cache provenance is written atomically and synced before private bytes arrive.
The credential-free sidecar survives cache index reconstruction and pending-pin expiry.
Every lease or certification mount requires matching tenant, revision, runtime image,
backend, configuration and size, followed by a fresh device-bound grant. Missing source
references and malformed provenance fail closed. Eviction removes provenance only after
removing the model file. Existing running lease retries also check their persisted private
binding before acknowledgement.
