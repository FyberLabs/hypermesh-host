package agent

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

const (
	// heartbeatBackoffFloor is the shortest wait after a failed beat.
	// A 503 Retry-After of 0, or jitter on the first step, cannot spin.
	heartbeatBackoffFloor = time.Second
	// heartbeatBackoffCap is the longest wait after a failed beat.
	// The plane drops a host whose heartbeat is older than 180s and
	// allows about one interval (~35s) of grace on a 503. 45s honors
	// that Retry-After and still leaves three waits inside the window.
	heartbeatBackoffCap = 45 * time.Second
	// heartbeatJitterFrac is the ± fraction applied to exponential waits.
	// Retry-After is not jittered; it is only clamped.
	heartbeatJitterFrac = 0.20
)

func (a *Agent) setHBStreak(n int) {
	a.healthMu.Lock()
	a.hbStreak = n
	a.healthMu.Unlock()
}

func (a *Agent) heartbeatBackoff(fails int, err error) time.Duration {
	unit := rand.Float64()
	if a.hbRand != nil {
		unit = a.hbRand()
	}
	var se *api.StatusError
	if errors.As(err, &se) && se != nil && se.StatusCode == http.StatusServiceUnavailable && se.HasRetryAfter {
		return heartbeatDelay(fails, true, se.RetryAfter, unit)
	}
	return heartbeatDelay(fails, false, 0, unit)
}

// heartbeatDelay is the wait before the next beat after a failure.
// fails is 1 on the first failure and grows by one each consecutive miss.
// hasHint selects the 503 Retry-After delay instead of the exponential base.
// unit is in [0, 1] and only affects the exponential path.
func heartbeatDelay(fails int, hasHint bool, hint time.Duration, unit float64) time.Duration {
	if unit < 0 {
		unit = 0
	}
	if unit > 1 {
		unit = 1
	}
	var base time.Duration
	if hasHint {
		if hint > 0 {
			base = hint
		}
	} else {
		shift := fails - 1
		if shift < 0 {
			shift = 0
		}
		if shift > 16 {
			shift = 16
		}
		base = heartbeatBackoffFloor << shift
		factor := 1 + ((unit*2 - 1) * heartbeatJitterFrac)
		base = time.Duration(float64(base) * factor)
	}
	if base < heartbeatBackoffFloor {
		return heartbeatBackoffFloor
	}
	if base > heartbeatBackoffCap {
		return heartbeatBackoffCap
	}
	return base
}

func (a *Agent) waitHeartbeat(ctx context.Context, d time.Duration, interrupt bool) bool {
	if a.hbWait != nil {
		return a.hbWait(ctx, d, interrupt)
	}
	if d < 0 {
		d = 0
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	var wake <-chan struct{}
	if interrupt {
		wake = a.hbWake
	}
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	case <-wake:
		return true
	}
}

func (a *Agent) pokeHeartbeat() {
	if a.hbWake == nil {
		return
	}
	select {
	case a.hbWake <- struct{}{}:
	default:
	}
}

func (a *Agent) drainHeartbeatWake() {
	if a.hbWake == nil {
		return
	}
	select {
	case <-a.hbWake:
	default:
	}
}

// observeTunnel interrupts a failing heartbeat when the tunnel moves to up.
func (a *Agent) observeTunnel(state string) {
	a.healthMu.Lock()
	prev := a.tunnelSeen
	a.tunnelSeen = state
	failing := a.hbStreak > 0
	a.healthMu.Unlock()
	if failing && state == tunnel.StateUp && prev != "" && prev != tunnel.StateUp {
		a.pokeHeartbeat()
	}
}

// observeBind interrupts a failing heartbeat when a tunnel address appears.
func (a *Agent) observeBind(addr string) {
	host := runtime.TunnelBindHost(addr)
	a.healthMu.Lock()
	prev := a.bindSeen
	a.bindSeen = host
	failing := a.hbStreak > 0
	a.healthMu.Unlock()
	if failing && prev == "" && host != "" {
		a.pokeHeartbeat()
	}
}
