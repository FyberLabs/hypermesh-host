// hypermesh-mhs-mcp is a stdio MCP stub for host / MHS device tools (role C).
// It does not enroll, lease, or call the control plane.
package main

import (
	"os"

	"github.com/FyberLabs/hypermesh-host/internal/mhs"
)

func main() {
	if err := mhs.ServeStdio(os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
}
