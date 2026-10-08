//go:build linux

package installer

import (
	"context"
	"github.com/FyberLabs/hypermesh-host/internal/hostregistration"
	"github.com/FyberLabs/hypermesh-host/internal/machineidentity"
)

func newRegistrationClient(base, tenant string) (*hostregistration.Client, error) {
	c, e := hostregistration.New(base, tenant)
	if e == nil {
		c.SetMachineIdentity(machineidentity.DefaultPath)
	}
	return c, e
}
func bindMachineIdentity(ctx context.Context, base, tenant string, j registrationJournal) error {
	_, e := (machineidentity.Provider{}).Token(ctx, base, tenant, j.DeviceID, j.Request.RequestID, j.DeviceSecret)
	return e
}
