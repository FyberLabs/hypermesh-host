package installer

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestHeadlessHostApprovalInstructions(t *testing.T) {
	const address = "https://portal.example.test/dashboard/hypermesh/host/approve/request-id"
	var diagnostic bytes.Buffer
	if err := displayHostApproval(&diagnostic, address, "https://portal.example.test/dashboard/hypermesh/host/connect/request-id"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(diagnostic.String(), "Host approval URL: "+address+"\n") != 1 ||
		!strings.Contains(diagnostic.String(), "Sign-in complete") ||
		!strings.Contains(diagnostic.String(), "backup approval link") {
		t.Fatalf("SSH approval instructions incomplete: %s", diagnostic.String())
	}
	if err := displayHostApproval(failedApprovalDisplay{}, address, "https://portal.example.test/dashboard/hypermesh/host/connect/request-id"); err == nil ||
		err.Error() != "cannot display host approval instructions" {
		t.Fatalf("unredacted display failure: %v", err)
	}
}

type failedApprovalDisplay struct{}

func (failedApprovalDisplay) Write([]byte) (int, error) {
	return 0, errors.New("private credential in terminal transport failure")
}
