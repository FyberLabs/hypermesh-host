//go:build !linux

package machineidentity

import (
	"context"
	"testing"
)

func TestUnsupportedPlatformsKeepTheEnrollmentSecret(t *testing.T) {
	token, err := Provider{}.ExistingToken(context.Background(), "https://cp.example", "t1", "device-1", "bootstrap-secret")
	if err != nil || token != "bootstrap-secret" {
		t.Fatalf("credential = %q, %v", token, err)
	}
	if err := (Provider{}).Invalidate(context.Background(), "token"); err != nil {
		t.Fatal(err)
	}
}
