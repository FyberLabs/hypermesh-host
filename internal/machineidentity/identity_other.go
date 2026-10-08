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
func (Provider) ExistingToken(context.Context, string, string, string, string) (string, error) {
	return "", fmt.Errorf("machine identity requires Linux")
}
func (Provider) Invalidate(context.Context, string) error {
	return fmt.Errorf("machine identity requires Linux")
}

func (Provider) Retire(context.Context) error { return nil }

func (Provider) RetireEnrollment(context.Context, string, bool) error { return nil }
