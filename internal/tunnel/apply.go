// Package tunnel applies a WireGuard (or equivalent) session allocated
// by the control plane. No custom overlay.
//
// The control plane has no tunnel allocate HTTP yet. Apply with an
// empty payload is a documented no-op. A non-empty payload errors
// until that shape exists — do not invent a peer list or keys here.
package tunnel

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrNoPayload means nothing was allocated. Apply treats this as a no-op.
	ErrNoPayload = errors.New("hypermesh tunnel: no payload from control plane")
	// ErrUnsupportedPayload means the control plane sent bytes this
	// slice cannot apply. The allocate shape is not documented yet.
	ErrUnsupportedPayload = errors.New("hypermesh tunnel: payload received but allocate shape is not documented; not applying a guessed WireGuard config")
)

// Payload is a later control-plane allocation. Empty until that API exists.
type Payload []byte

// Apply installs a WireGuard (or equivalent) session.
//
//   - nil / empty payload: no-op (nil error). Nothing to apply.
//   - any bytes: error. Do not guess wg0.conf.
func Apply(ctx context.Context, payload Payload) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	return fmt.Errorf("%w (%d bytes)", ErrUnsupportedPayload, len(payload))
}
