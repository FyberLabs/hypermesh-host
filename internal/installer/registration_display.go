package installer

import (
	"fmt"
	"io"
)

// displayHostApproval is shared across platforms. The caller supplies the
// already-validated dashboard URL and server approval path, never a credential.
func displayHostApproval(display io.Writer, address string) error {
	if _, err := fmt.Fprintf(display, "Sign-in complete. The host setup page will continue to approval automatically. If needed, open this backup approval link on your phone or laptop. Review this installation before approving.\nHost approval URL: %s\n", address); err != nil {
		return fmt.Errorf("cannot display host approval instructions")
	}
	return nil
}
