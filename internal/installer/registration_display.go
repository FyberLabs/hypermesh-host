package installer

import (
	"fmt"
	"io"
)

// displayHostApproval is shared across platforms. The caller supplies the
// already-validated dashboard URL and server approval path, never a credential.
func displayHostApproval(display io.Writer, address string) error {
	if _, err := fmt.Fprintf(display, "Open the host approval URL on another device after signing in. Review this installation before approving.\nHost approval URL: %s\n", address); err != nil {
		return fmt.Errorf("cannot display host approval instructions")
	}
	return nil
}
