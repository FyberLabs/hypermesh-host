//go:build !linux

package machineidentity

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const DefaultPath = "/var/lib/hypermesh/machine-identity.json"

type Provider struct {
	Authority string
	Path      string
	HTTP      *http.Client
	Now       func() time.Time
}

func (Provider) Token(context.Context, string, string, string, string, string) (string, error) {
	return "", fmt.Errorf("machine identity requires Linux")
}

// ExistingToken keeps the enrollment secret on platforms without a signing
// identity, so agents there can still authenticate after enrolling.
func (Provider) ExistingToken(_ context.Context, _, _, _, bootstrap string) (string, error) {
	return bootstrap, nil
}

// Invalidate has nothing to clear: no machine token is cached here.
func (Provider) Invalidate(context.Context, string) error { return nil }

func (Provider) Retire(context.Context) error { return nil }

func (Provider) RetireEnrollment(context.Context, string, bool) error { return nil }
