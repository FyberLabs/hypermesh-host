package installer

import (
	"fmt"
	"io"
)

// displayHostApproval is shared across platforms. The caller supplies the
// already-validated dashboard URL and server approval path, never a credential.
func displayHostApproval(display io.Writer, address, setupURL string) error {
	if _, err := fmt.Fprintf(display, "Sign-in complete. Continue on the current host setup page, or open this backup approval link on your phone or laptop. Review this installation before approving. If the request was replaced, use this current setup URL instead of an older link.\nHost setup URL: %s\nHost approval URL: %s\n", setupURL, address); err != nil {
		return fmt.Errorf("cannot display host approval instructions")
	}
	return nil
}
