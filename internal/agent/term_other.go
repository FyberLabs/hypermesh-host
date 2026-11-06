//go:build !linux && !darwin

package agent

func stdinIsTerminal() bool { return false }
