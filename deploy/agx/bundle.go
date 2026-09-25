package agx

import _ "embed"

// AgentUnit and WGUnit are the systemd units the enroll path already
// expects. hypermesh-deploy writes these bytes; it does not invent a
// second unit or a second agent.

//go:embed hypermesh-agent.service
var AgentUnit []byte

//go:embed hypermesh-wg.service
var WGUnit []byte
