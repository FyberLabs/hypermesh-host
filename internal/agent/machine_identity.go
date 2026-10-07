package agent

import (
	"context"
	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/machineidentity"
	"path/filepath"
)

func machineAPIClient(cfg Config) *api.Client {
	c := api.NewClient(cfg.ControlPlaneURL, cfg.TenantID)
	p := machineidentity.Provider{Path: filepath.Join(cfg.StateDir, "machine-identity.json")}
	c.Credential = func(ctx context.Context) (string, error) {
		return p.ExistingToken(ctx, cfg.ControlPlaneURL, cfg.TenantID, c.DeviceID, c.DeviceSecret)
	}
	c.InvalidateCredential = p.Invalidate
	return c
}

func retireMachineIdentity(ctx context.Context, cfg Config) error {
	return (machineidentity.Provider{Path: filepath.Join(cfg.StateDir, "machine-identity.json")}).Retire(ctx)
}
