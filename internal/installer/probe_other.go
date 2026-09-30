//go:build !linux

package installer

import (
	"context"
	"fmt"
)

func probeResources(context.Context, string, string, int64) (Resources, error) {
	return Resources{}, fmt.Errorf("resource collection currently requires Linux; no installation changes made")
}
