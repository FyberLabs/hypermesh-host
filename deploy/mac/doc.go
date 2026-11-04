// Package mac holds the experimental Darwin launchd install.
// The plist and env example in this directory are the operator artifacts.
// Tests check that they still match the agent.
package mac

import _ "embed"

// AgentPlist and WireGuardPlist are the exact reviewed LaunchDaemon definitions.
// The owner registration flow refuses modified service definitions.
//
//go:embed com.fyberlabs.hypermesh-agent.plist
var AgentPlist string

//go:embed com.fyberlabs.hypermesh-wg.plist
var WireGuardPlist string
