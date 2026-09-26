package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/pathb"
)

const sellFile = "sell.json"

// SellPosture is the host-local sell actuator state. Not Path B.
type SellPosture struct {
	SellState api.SellState `json:"sell_state"`
	Until     string        `json:"until,omitempty"`
}

// jobOutcome is the host-local job result. Sell fields are only POSTed for sell_pause.
// Egress fields are POSTed for path_b lab soaks (admission sandbox).
type jobOutcome struct {
	pathb.Result
	SellState    api.SellState
	Until        string
	DeviceID     string
	EgressOk     *bool
	EgressReason string
}

func (a *Agent) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now().UTC()
}

func (a *Agent) sellPath() string {
	return filepath.Join(a.cfg.StateDir, sellFile)
}

func (a *Agent) loadSell() SellPosture {
	b, err := os.ReadFile(a.sellPath())
	if err != nil {
		return SellPosture{}
	}
	var p SellPosture
	if err := json.Unmarshal(b, &p); err != nil {
		return SellPosture{}
	}
	return p
}

// saveSell writes sell.json. Caller must hold sellMu: heartbeat and the
// job loop both read-modify-write this file.
func (a *Agent) saveSell(p SellPosture) error {
	if err := os.MkdirAll(a.cfg.StateDir, 0o700); err != nil {
		return err
	}
	return writeJSON0600(a.sellPath(), p)
}

// effectiveSell returns the real sell posture after until expiry.
// After the actuator exists the default is selling, never n/a.
func (a *Agent) effectiveSell() SellPosture {
	a.sellMu.Lock()
	defer a.sellMu.Unlock()
	return a.effectiveSellLocked()
}

func (a *Agent) effectiveSellLocked() SellPosture {
	p := a.loadSell()
	if p.SellState == "" || p.SellState == api.SellNA {
		p = SellPosture{SellState: api.SellSelling}
		if err := a.saveSell(p); err != nil {
			a.log.Warn("sell posture not saved", "err", err)
		}
		return p
	}
	if p.SellState == api.SellPaused && untilReached(p.Until, a.clock()) {
		a.log.Info("sell pause expired", "until", p.Until)
		p = SellPosture{SellState: api.SellSelling}
		if err := a.saveSell(p); err != nil {
			a.log.Warn("sell expiry not saved", "err", err)
		}
	}
	return p
}

func untilReached(until string, now time.Time) bool {
	t, ok := parseUntil(until)
	if !ok || t.IsZero() {
		return false
	}
	return !now.Before(t)
}

func parseUntil(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

func formatUntil(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (a *Agent) sellPause(job api.Job) jobOutcome {
	a.sellMu.Lock()
	defer a.sellMu.Unlock()
	current := a.effectiveSellLocked()
	localID := strings.TrimSpace(a.state.DeviceID)
	jobDev := strings.TrimSpace(job.DeviceID)
	echoDev := jobDev

	if jobDev == "" || jobDev != localID {
		a.log.Info("sell_pause refused",
			"reason", "device_id mismatch",
			"job_device_id", job.DeviceID,
			"device_id", localID,
			"sell_state", current.SellState,
		)
		return jobOutcome{
			Result:    pathb.Result{Passed: false, Reason: "device_id mismatch"},
			SellState: current.SellState,
			Until:     current.Until,
			DeviceID:  echoDev,
		}
	}

	untilRaw := strings.TrimSpace(job.Until)
	var storeUntil string
	if untilRaw != "" {
		t, ok := parseUntil(untilRaw)
		if !ok {
			a.log.Info("sell_pause refused", "reason", "invalid until", "until", job.Until, "sell_state", current.SellState)
			return jobOutcome{
				Result:    pathb.Result{Passed: false, Reason: "invalid until"},
				SellState: current.SellState,
				Until:     current.Until,
				DeviceID:  localID,
			}
		}
		storeUntil = formatUntil(t)
	}

	next := SellPosture{SellState: api.SellPaused, Until: storeUntil}
	if current.SellState == api.SellPaused && current.Until == storeUntil {
		a.log.Info("sell_pause", "reason", "already paused", "until", storeUntil)
	} else if err := a.saveSell(next); err != nil {
		a.log.Warn("sell pause not saved", "err", err)
		return jobOutcome{
			Result:    pathb.Result{Passed: false, Reason: "sell persist failed"},
			SellState: current.SellState,
			Until:     current.Until,
			DeviceID:  localID,
		}
	} else {
		a.log.Info("sell_pause", "sell_state", api.SellPaused, "until", storeUntil)
	}

	return jobOutcome{
		Result:    pathb.Result{Passed: true, Reason: "sell_pause"},
		SellState: api.SellPaused,
		Until:     storeUntil,
		DeviceID:  localID,
	}
}

func jobResultBody(kind api.JobKind, res jobOutcome) api.JobResultRequest {
	req := api.JobResultRequest{
		Passed:    res.Passed,
		ImageHash: res.ImageHash,
	}
	if kind == api.KindSellPause {
		req.SellState = res.SellState
		req.Until = res.Until
		req.DeviceID = res.DeviceID
	}
	if kind == api.KindPathB {
		req.EgressOk = res.EgressOk
		req.EgressReason = res.EgressReason
	}
	return req
}
