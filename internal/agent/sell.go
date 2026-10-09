package agent

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/pathb"
)

const (
	sellFile = "sell.json"
	// sellOwnerFile keeps the posture to restore after a maintenance pause:
	// the owner's pause, or selling when there was none.
	sellOwnerFile = "sell-owner.json"
)

// Pause sources recorded in sell.json.
const (
	// SellSourceOwner marks a pause that was explicitly requested through the
	// owner socket, the owner CLI or a control-plane sell_pause job.
	SellSourceOwner = "owner"
	// SellSourceActivation marks the installer's startup guard. It keeps the
	// host paused while activation is verified and is released by the
	// installer once the service is confirmed running.
	SellSourceActivation = "activation"
)

// SellPosture is the host-local sell state. It is separate from
// the control-plane certification state (JSON field path_b_state).
//
// A paused posture with no source and no until predates sources. Earlier
// installers wrote it at activation as the exact compact bytes in
// activationLegacyPause, which the agent itself never produces; that pause is
// cleared. Any other sourceless indefinite pause was written by the agent for
// an owner, a control-plane job or a maintenance pause and is kept as an owner
// pause, since its origin cannot be told apart.
type SellPosture struct {
	SellState api.SellState `json:"sell_state"`
	Until     string        `json:"until,omitempty"`
	Source    string        `json:"source,omitempty"`
}

// activationLegacyPause is the file earlier installers wrote at activation.
// The agent writes indented JSON, so these bytes only come from an installer.
const activationLegacyPause = `{"sell_state":"paused"}`

func (p SellPosture) installerShaped() bool {
	return p.SellState == api.SellPaused && strings.TrimSpace(p.Until) == "" && p.Source == ""
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

func (a *Agent) sellOwnerPath() string {
	return filepath.Join(a.cfg.StateDir, sellOwnerFile)
}

func (a *Agent) sellLog() *slog.Logger {
	if a.log != nil {
		return a.log
	}
	return slog.Default()
}

// loadKeptSell reads the posture kept beside a maintenance pause: an owner
// pause, or selling when no owner pause was in effect.
func (a *Agent) loadKeptSell() (SellPosture, bool) {
	b, err := os.ReadFile(a.sellOwnerPath())
	if err != nil {
		return SellPosture{}, false
	}
	var p SellPosture
	if json.Unmarshal(b, &p) != nil {
		return SellPosture{}, false
	}
	switch p.SellState {
	case api.SellSelling:
		return SellPosture{SellState: api.SellSelling}, true
	case api.SellPaused:
		p.Source = SellSourceOwner
		return p, true
	}
	return SellPosture{}, false
}

// ownerPauseIntent returns the owner pause represented by the stored posture,
// if any. raw is the stored file.
func ownerPauseIntent(p SellPosture, raw []byte) (SellPosture, bool) {
	if p.SellState != api.SellPaused {
		return SellPosture{}, false
	}
	switch {
	case p.Source == SellSourceOwner:
		return p, true
	case p.Source != "":
		return SellPosture{}, false
	case strings.TrimSpace(p.Until) != "":
		// Only an explicit pause request ever stored an end time.
		return SellPosture{SellState: api.SellPaused, Until: p.Until, Source: SellSourceOwner}, true
	case strings.TrimSpace(string(raw)) == activationLegacyPause:
		return SellPosture{}, false
	}
	return SellPosture{SellState: api.SellPaused, Source: SellSourceOwner}, true
}

// settleSourcelessPause runs outside a maintenance pause on a paused posture
// with no source and no until. A posture kept beside a released maintenance pause is
// restored; the pause earlier installers wrote at activation returns to
// selling; anything else is kept and labeled as an owner pause.
// Caller must hold sellMu.
func (a *Agent) settleSourcelessPause(raw []byte) SellPosture {
	next, ok := a.loadKeptSell()
	if !ok {
		next, ok = ownerPauseIntent(SellPosture{SellState: api.SellPaused}, raw)
	}
	if !ok {
		next = SellPosture{SellState: api.SellSelling}
	}
	if err := a.saveSell(next); err != nil {
		a.sellLog().Warn("sell pause not settled", "err", err)
		return SellPosture{SellState: api.SellPaused}
	}
	a.sellLog().Info("sell pause settled", "sell_state", next.SellState, "until", next.Until, "source", next.Source)
	_ = os.Remove(a.sellOwnerPath())
	return next
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
 if a.maintenanceHeld(){return SellPosture{SellState:api.SellPaused}}
	raw, _ := os.ReadFile(a.sellPath())
	p := a.loadSell()
	if p.installerShaped() {
		p = a.settleSourcelessPause(raw)
	} else if _, err := os.Lstat(a.sellOwnerPath()); err == nil {
		// A kept owner pause only applies while sell.json is the maintenance pause.
		_ = os.Remove(a.sellOwnerPath())
	}
	if p.SellState == "" || p.SellState == api.SellNA {
		p = SellPosture{SellState: api.SellSelling}
		if err := a.saveSell(p); err != nil {
			a.sellLog().Warn("sell posture not saved", "err", err)
		}
		return p
	}
	if p.SellState == api.SellPaused && untilReached(p.Until, a.clock()) {
		a.sellLog().Info("sell pause expired", "until", p.Until)
		p = SellPosture{SellState: api.SellSelling}
		if err := a.saveSell(p); err != nil {
			a.sellLog().Warn("sell expiry not saved", "err", err)
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
		a.sellLog().Info("sell_pause refused",
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
			a.sellLog().Info("sell_pause refused", "reason", "invalid until", "until", job.Until, "sell_state", current.SellState)
			return jobOutcome{
				Result:    pathb.Result{Passed: false, Reason: "invalid until"},
				SellState: current.SellState,
				Until:     current.Until,
				DeviceID:  localID,
			}
		}
		storeUntil = formatUntil(t)
	}

	if storeUntil != "" && untilReached(storeUntil, a.clock()) {
		// A pause that has already ended is a resume. The plane sends this to
		// resume selling, and earlier agents reach the same result when the
		// stored pause expires.
		return a.resumeLocked(localID, current)
	}
	next := SellPosture{SellState: api.SellPaused, Until: storeUntil, Source: SellSourceOwner}
	if a.maintenanceHeld() {
		// sell.json keeps the maintenance pause; the owner pause is restored on release.
		if err := writeJSON0600(a.sellOwnerPath(), next); err != nil {
			a.sellLog().Warn("sell pause not saved", "err", err)
			return jobOutcome{
				Result:    pathb.Result{Passed: false, Reason: "sell persist failed"},
				SellState: current.SellState,
				Until:     current.Until,
				DeviceID:  localID,
			}
		}
		storeUntil = ""
		a.sellLog().Info("sell_pause", "reason", "kept for after maintenance", "until", next.Until)
	} else if stored := a.loadSell(); current.SellState == api.SellPaused && current.Until == storeUntil && stored.Source == SellSourceOwner {
		a.sellLog().Info("sell_pause", "reason", "already paused", "until", storeUntil)
	} else if err := a.saveSell(next); err != nil {
		a.sellLog().Warn("sell pause not saved", "err", err)
		return jobOutcome{
			Result:    pathb.Result{Passed: false, Reason: "sell persist failed"},
			SellState: current.SellState,
			Until:     current.Until,
			DeviceID:  localID,
		}
	} else {
		a.sellLog().Info("sell_pause", "sell_state", api.SellPaused, "until", storeUntil)
	}

	return jobOutcome{
		Result:    pathb.Result{Passed: true, Reason: "sell_pause"},
		SellState: api.SellPaused,
		Until:     storeUntil,
		DeviceID:  localID,
	}
}

// sellResume clears any pause so the host sells again. It is the inverse of
// sellPause and takes the same device check. A maintenance pause is not lifted
// here; only the installer resume operation releases it.
func (a *Agent) sellResume(deviceID string) jobOutcome {
	a.sellMu.Lock()
	defer a.sellMu.Unlock()
	current := a.effectiveSellLocked()
	localID := strings.TrimSpace(a.state.DeviceID)
	dev := strings.TrimSpace(deviceID)
	if dev == "" || dev != localID {
		a.sellLog().Info("sell_resume refused", "reason", "device_id mismatch", "job_device_id", deviceID, "device_id", localID)
		return jobOutcome{
			Result:    pathb.Result{Passed: false, Reason: "device_id mismatch"},
			SellState: current.SellState,
			Until:     current.Until,
			DeviceID:  dev,
		}
	}
	return a.resumeLocked(localID, current)
}

// resumeLocked persists selling. Caller must hold sellMu and has checked the device.
func (a *Agent) resumeLocked(localID string, current SellPosture) jobOutcome {
	if a.maintenanceHeld() {
		a.sellLog().Info("sell_resume refused", "reason", "host maintenance hold")
		return jobOutcome{
			Result:    pathb.Result{Passed: false, Reason: "host maintenance hold"},
			SellState: api.SellPaused,
			DeviceID:  localID,
		}
	}
	if current.SellState == api.SellSelling {
		a.sellLog().Info("sell_resume", "reason", "already selling")
		return jobOutcome{
			Result:    pathb.Result{Passed: true, Reason: "sell_resume"},
			SellState: api.SellSelling,
			DeviceID:  localID,
		}
	}
	if err := a.saveSell(SellPosture{SellState: api.SellSelling}); err != nil {
		a.sellLog().Warn("sell resume not saved", "err", err)
		return jobOutcome{
			Result:    pathb.Result{Passed: false, Reason: "sell persist failed"},
			SellState: current.SellState,
			Until:     current.Until,
			DeviceID:  localID,
		}
	}
	a.sellLog().Info("sell_resume", "sell_state", api.SellSelling)
	return jobOutcome{
		Result:    pathb.Result{Passed: true, Reason: "sell_resume"},
		SellState: api.SellSelling,
		DeviceID:  localID,
	}
}

// releaseActivationPause clears the installer's activation guard. Any other
// posture, including an owner pause, is left unchanged.
func (a *Agent) releaseActivationPause() (SellPosture, error) {
	a.sellMu.Lock()
	defer a.sellMu.Unlock()
	if !a.maintenanceHeld() {
		if p := a.loadSell(); p.SellState == api.SellPaused && p.Source == SellSourceActivation {
			if err := a.saveSell(SellPosture{SellState: api.SellSelling}); err != nil {
				return a.effectiveSellLocked(), err
			}
			a.sellLog().Info("activation sell pause released")
		}
	}
	return a.effectiveSellLocked(), nil
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
