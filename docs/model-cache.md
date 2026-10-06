# On-demand model storage

Implemented: hosts cache content-addressed GGUF artifacts for accepted leases and
approved certification/qualification work. Discovery only records candidates;
it does not populate every host with every discovered model.

Guided setup asks for the model directory, a disk quota, and a free-space reserve
(at least 2 GiB). Existing installations get this prompt once when they resume
with the updated installer. To review storage settings later:

```sh
sudo ./hypermesh-deploy install --cache-settings
```

The host-owned policy is `MODEL_DIRECTORY/.hypermesh-cache.json`, separate from
installation payload hashes. Changing the quota does not immediately remove files
or interrupt work. The next cache miss evicts idle files in least-recently-used
order until both quota and free-disk requirements are met. If protected files
prevent that, the download is refused. Quota zero in the policy prevents new
admissions; existing verified files remain reusable.

Active lease models, pending workload reservations, and explicit owner pins are
protected. The last-downloaded file alone is not permanently protected. Owners
can add canonical `sha256:` hashes to `pinned_hashes` in the root-owned policy to
keep chosen models cached. A cache pin does not start a live inference service.
Pending reservations retain the existing bounded TTL and clear at lease start,
stop, cancellation or expiry. Temporary benchmark workloads hold the cache lock
while running and cleaning up.

All acquisition paths use the same cache index and quota. Installer and agent
transfers serialize through a process-shared lock. A verified cache hit avoids a
second download; each incoming file is checked against the exact expected size
and SHA-256 before atomic publication. Cache metadata uses durable last-use times,
not filesystem access times. Aborted transfer files are removed; agent startup
cleans crash leftovers. Cached bytes, evictable bytes, and effective admission
capacity are reported in heartbeats. The plane uses capacity to filter offers;
the host checks disk and current runtime/memory again before starting demand work.

The daily crawler resolves Hugging Face branches to immutable commits. An
accepted job carries the model hash, size, runtime pin, and download source; retries
do not silently follow catalog changes. The initial source provider is Hugging
Face. Storage identity is the content hash, so a later approved blob-cache source
can reuse the same files and benchmark identity; blob serving is not implemented.

For gated models, accept the model's license with the provider first. Installer
qualification retains its private token-file prompt. For later agent downloads,
set `huggingface_token_file` in the cache policy to an absolute private file owned
by the service user, with mode 0600. The file contains only the access token.
Credentials are read at use time, never stored in cache metadata or sent to CDN
redirects. Cache policy changes preserve the token-file reference and owner pins.

Validation covers quota refusal before transfer, eviction ordering, protected
models, concurrent reuse, lease-triggered acquisition, guided approval, and
installer acquisition. Physical AGX qualification and lease testing remain the
next deployment acceptance checks. Rental billing rules are unchanged.
