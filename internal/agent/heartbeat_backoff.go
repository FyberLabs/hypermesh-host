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
	// It sits under HeartbeatEvery (30s) and inside the plane's ~35s
	// grace (HYPERMESH_HEARTBEAT_GRACE_WAIT_S) so a beat still lands
	// before placement treats the host as stale. The plane drops a
	// host whose heartbeat is older than 180s.
	// Panopticon #256's 503 Retry-After is for placement, lease_start,
	// and chat callers after a restart. It is not sent on heartbeat.
	// A proxy 503 on POST /agent/heartbeat is still honored, then
	// clamped into this range. That hint is not the grace window.
	heartbeatBackoffCap = 25 * time.Second
	// heartbeatJitterFrac is ±20% on exponential waits and 0–20% added
	// on top of a Retry-After hint. Exponential jitter is applied after
	// the clamp, so a step at the cap still spreads through [0.8*cap, cap].
	// Retry-After jitter only moves later than the hint, and never past
	// the cap. A hint already at the cap waits exactly the cap.
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
// unit is in [0, 1]. The base is clamped to [floor, cap] first.
// Exponential jitter then spreads inside that window, including at the cap.
// Retry-After jitter is only added on top of the clamped hint. It never
// waits less than that hint, and a hint already at the cap stays there.
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
	}
	if base < heartbeatBackoffFloor {
		base = heartbeatBackoffFloor
	}
	if base > heartbeatBackoffCap {
		base = heartbeatBackoffCap
	}
	lo, hi := heartbeatJitterBounds(base, hasHint)
	if hi <= lo {
		return lo
	}
	return lo + time.Duration(float64(hi-lo)*unit)
}

// heartbeatJitterBounds is the closed interval unit interpolates.
// positive is the Retry-After path: add up to 20%, never earlier than
// the clamped hint, never later than the cap. When that clamps the high
// side onto the hint, the wait is exactly the hint. An exponential wait
// uses ±20% clipped to [floor, cap], so a base at the cap still spreads
// through [0.8*cap, cap].
func heartbeatJitterBounds(base time.Duration, positive bool) (lo, hi time.Duration) {
	if positive {
		lo = base
		hi = time.Duration(float64(base) * (1 + heartbeatJitterFrac))
	} else {
		lo = time.Duration(float64(base) * (1 - heartbeatJitterFrac))
		hi = time.Duration(float64(base) * (1 + heartbeatJitterFrac))
	}
	if lo < heartbeatBackoffFloor {
		lo = heartbeatBackoffFloor
	}
	if hi > heartbeatBackoffCap {
		hi = heartbeatBackoffCap
	}
	if lo > hi {
		lo = hi
	}
	return lo, hi
}

func (a *Agent) armHeartbeatHold(d time.Duration) {
	until := a.clock().Add(d)
	a.healthMu.Lock()
	a.hbHoldUntil = until
	a.healthMu.Unlock()
	// A job-loop success during the heartbeat request can queue a wake
	// before this backoff exists. Drop it so that wait does not return
	// immediately and skip Retry-After.
	a.drainHeartbeatWake()
}

func (a *Agent) clearHeartbeatHold() {
	a.healthMu.Lock()
	a.hbHoldUntil = time.Time{}
	a.healthMu.Unlock()
}

func (a *Agent) heartbeatHoldActive() bool {
	a.healthMu.Lock()
	until := a.hbHoldUntil
	a.healthMu.Unlock()
	if until.IsZero() {
		return false
	}
	return a.clock().Before(until)
}

// notePlaneReachable wakes a failing heartbeat after the job loop got a
// response from the plane. observeTunnel runs inside a heartbeat attempt,
// so a plane that returns while the tunnel stays up would otherwise sit
// through the whole backoff.
func (a *Agent) notePlaneReachable() {
	a.healthMu.Lock()
	failing := a.hbStreak > 0
	a.healthMu.Unlock()
	if failing {
		a.pokeHeartbeat()
	}
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
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case <-wake:
			// The token may have been queued before the backoff was armed.
			// Keep waiting until the deadline while the backoff is active.
			if a.heartbeatHoldActive() {
				continue
			}
			return true
		}
	}
}

func (a *Agent) pokeHeartbeat() {
	if a.heartbeatHoldActive() {
		return
	}
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
