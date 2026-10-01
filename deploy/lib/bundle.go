package hostlib

import _ "embed"

// Rollback is shipped inside the signed installer, not fetched as an unsigned script.
//go:embed update-rollback
var Rollback []byte
