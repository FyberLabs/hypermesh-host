# Publisher failure regression notes

After #62, the signing key imports without a passphrase as intended.
The next publish path exposed an exit-status bug: capturing $? after an if
statement with no else returned the if statement's status, losing the
blob-existence result. A new artifact was rejected instead of uploaded.

The uploader now distinguishes an existing artifact (keep), an absent artifact
(upload), and an existence-check error (abort). CI exercises the actual shell
function with cloud commands stubbed, without publishing artifacts.

CI and release also use job-local Go build/module caches and disable setup-go
cache restoration. Concurrent self-hosted jobs previously restored the shared
cache while Go vet read it, producing missing runtime/debug cache files.
The observed missing-file error is fixed by removing that shared-cache path;
successful production publishing still requires a post-merge release run.
