//go:build linux

package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

// The original approval is retained. A separate, machine-bound approval permits
// only the deterministic kernel -> userspace configuration transition.
type wireguardTransition struct {
	Schema   int              `json:"schema"`
	Machine  string           `json:"machine"`
	Original agentInstallPlan `json:"original"`
	Target   agentInstallPlan `json:"target"`
	Complete bool             `json:"complete"`
}
type wireguardTransitionDeps struct {
	machine    func() (string, error)
	guard      func(context.Context, agentInstallPlan) error
	preflight  func(context.Context, agentInstallPlan, agentInstallPlan, registrationJournal) error
	reload     func(context.Context) error
	afterWrite func(string) error
}

func (d wireguardTransitionDeps) defaults() wireguardTransitionDeps {
	if d.machine == nil {
		d.machine = func() (string, error) { m, _, e := rebootIdentity(""); return m, e }
	}
	if d.guard == nil {
		d.guard = func(ctx context.Context, p agentInstallPlan) error {
			if err := inactiveMemoryServices(ctx, setupSource{}); err != nil {
				return err
			}
			return checkAgentUnitPaths(ctx, setupSource{}, "", agentPayloads(p, nil))
		}
	}
	if d.preflight == nil {
		d.preflight = func(ctx context.Context, old, p agentInstallPlan, j registrationJournal) error {
			setup, e := reviewedRegistrationSetup(old)
			if e != nil {
				return e
			}
			setup.WireGuardMode = p.Setup.WireGuardMode
			setup.Profile = p.Setup.Profile
			if _, e = registrationPreflightSetup(ctx, p, j.Request.ClassID, setup); e != nil {
				return e
			}
			info, e := os.Stat("/dev/net/tun")
			if e != nil || info.Mode()&os.ModeCharDevice == 0 {
				return fmt.Errorf("userspace WireGuard requires /dev/net/tun")
			}
			_, e = exec.LookPath("wireguard-go")
			if e != nil {
				return fmt.Errorf("userspace WireGuard executable is unavailable")
			}
			return nil
		}
	}
	if d.reload == nil {
		d.reload = func(ctx context.Context) error {
			_, e := (setupSource{}).output(ctx, "systemctl", "daemon-reload")
			return e
		}
	}
	return d
}
func userspaceTransitionPlan(old agentInstallPlan) (agentInstallPlan, error) {
	var p agentInstallPlan
	raw, e := json.Marshal(old)
	if e != nil {
		return p, e
	}
	if e = json.Unmarshal(raw, &p); e != nil {
		return p, e
	}
	if old.Setup.WireGuardMode != "kernel" || old.Setup.Profile == nil || len(old.Files) == 0 || agentPlanDigest(old) != old.SHA256 {
		return p, fmt.Errorf("WireGuard repair requires a valid kernel-mode plan")
	}
	p.Setup.WireGuardMode = "userspace"
	p.Setup.Profile.RequiredPackages = append(p.Setup.Profile.RequiredPackages, p.Setup.Profile.UserspacePackages...)
	sort.Strings(p.Setup.Profile.RequiredPackages)
	p.Setup.PlanSHA256 = setupPlanDigest(p.Setup)
	p.Files = agentFileManifest(agentPayloads(p, nil))
	p.Files[0] = old.Files[0]
	p.SHA256 = agentPlanDigest(p)
	return p, validateInstalledAgentInputs(p)
}
func repairGuidedWireguard(ctx context.Context, dir string, p agentInstallPlan, ui guidedUI) (agentInstallPlan, error) {
	if p.Setup.WireGuardMode != "kernel" || tunnel.KernelAvailable() {
		return p, nil
	}
	target, err := userspaceTransitionPlan(p)
	if err != nil {
		return p, err
	}
	deps := wireguardTransitionDeps{}.defaults()
	if err = verifyRegistrationService(ctx, p); err != nil {
		return p, err
	}
	machine, err := deps.machine()
	if err != nil {
		return p, err
	}
	record := wireguardTransition{Schema: 1, Machine: machine, Original: p, Target: target}
	if err = validateWireguardTransition(ctx, "", record, deps); err != nil {
		return p, err
	}
	fmt.Fprintln(ui.out, "This kernel has no available WireGuard module. Userspace WireGuard is installed and can use /dev/net/tun.")
	fmt.Fprintln(ui.out, " - Update /etc/hypermesh/agent.env and hypermesh-agent.service to use userspace WireGuard")
	fmt.Fprintln(ui.out, " - Install the activation-gated hypermesh-wg.service and reload systemd; services remain inactive")
	if err = ui.confirm("Approve this WireGuard repair? Your host registration, keys and model files are preserved."); err != nil {
		return p, err
	}
	if err = validateWireguardTransition(ctx, "", record, deps); err != nil {
		return p, err
	}
	if err = writeGuided(filepath.Join(dir, "wireguard-transition.json"), record); err != nil {
		return p, err
	}
	if err = resumeWireguardTransition(ctx, dir, "", deps); err != nil {
		return p, err
	}
	return target, nil
}
func validateWireguardTransition(ctx context.Context, root string, r wireguardTransition, d wireguardTransitionDeps) error {
	machine, err := d.machine()
	if err != nil {
		return err
	}
	target, err := userspaceTransitionPlan(r.Original)
	if err != nil || r.Schema != 1 || machine != r.Machine || !sameSetupJSON(target, r.Target) {
		return fmt.Errorf("WireGuard transition approval does not match this installation")
	}
	if err = validateInstalledAgentInputs(r.Original); err != nil {
		return err
	}
	for _, path := range []string{"/var/lib/hypermesh/activation-approved", "/var/lib/hypermesh/lease.json"} {
		if _, e := os.Lstat(agentDest(root, path)); !os.IsNotExist(e) {
			return fmt.Errorf("active host state prevents WireGuard transition")
		}
	}
	var installed agentInstallJournal
	if err = guidedRead(agentDest(root, filepath.Join(defaultSetupState, "agent-"+r.Original.SHA256+".json")), &installed, true); err != nil {
		return err
	}
	expected := agentPayloads(r.Original, nil)
	if installed.Schema != 1 || !installed.Started || installed.State != "installed-awaiting-enrollment" || installed.PlanSHA256 != r.Original.SHA256 || len(installed.Completed) != len(expected) || len(r.Original.Files) != len(expected) {
		return fmt.Errorf("incomplete original installation")
	}
	for i, f := range r.Original.Files {
		if installed.Completed[i] != f.Path || f.Path != expected[i].Path || f.Mode != expected[i].Mode || !setupDigestRE.MatchString(f.SHA256) || (i != 0 && f.SHA256 != expected[i].SHA256) {
			return fmt.Errorf("invalid original installation manifest")
		}
	}
	var j registrationJournal
	if err = guidedRead(agentDest(root, registrationPath(defaultSetupState, r.Original.SHA256)), &j, true); err != nil {
		return err
	}
	if j.State != "registered-inactive" {
		return fmt.Errorf("WireGuard repair requires a registered inactive host")
	}
	if err = validateRegistration(j, r.Original, j.Issuer, j.Dashboard, j.Request.ClassID); err != nil {
		return err
	}
	if err = checkRegistrationIdentity(root, j, true); err != nil {
		return err
	}
	if err = d.guard(ctx, r.Target); err != nil {
		return err
	}
	if err = d.preflight(ctx, r.Original, r.Target, j); err != nil {
		return err
	}
	// Crash recovery accepts only exact original or exact approved replacement
	// bytes, including ownership/mode checks; missing original files are errors.
	for _, f := range r.Target.Files {
		err = verifyInstalledPayload(ctx, root, r.Original, f)
		if err == nil {
			continue
		}
		found := false
		for _, old := range r.Original.Files {
			if old.Path == f.Path {
				found = true
				err = verifyInstalledPayload(ctx, root, r.Original, old)
				break
			}
		}
		if !found {
			if _, e := os.Lstat(agentDest(root, f.Path)); os.IsNotExist(e) {
				continue
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func replaceWireguardPayload(path string, f agentPayload) error {
	if err := agentDirectory(filepath.Dir(path), true); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wireguard-transition-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(f.Data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Chmod(os.FileMode(f.Mode)); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
func resumeWireguardTransition(ctx context.Context, dir, root string, deps wireguardTransitionDeps) error {
	path := filepath.Join(dir, "wireguard-transition.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	var r wireguardTransition
	if err := guidedRead(path, &r, true); err != nil {
		return err
	}
	if r.Complete {
		return nil
	}
	d := deps.defaults()
	if err := validateWireguardTransition(ctx, root, r, d); err != nil {
		return err
	}
	// The wizard must still reference one of this transaction's two plans.
	var current agentInstallPlan
	if err := guidedRead(filepath.Join(dir, "agent.json"), &current, true); err != nil {
		return err
	}
	if !sameSetupJSON(current, r.Original) && !sameSetupJSON(current, r.Target) {
		return fmt.Errorf("WireGuard transition does not match the guided plan")
	}
	for i, f := range agentPayloads(r.Target, nil) {
		if i == 0 {
			continue
		} // Never replace the signed agent executable.
		if err := verifyInstalledPayload(ctx, root, r.Target, f.agentFile); err == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := replaceWireguardPayload(agentDest(root, f.Path), f); err != nil {
			return err
		}
		if d.afterWrite != nil {
			if err := d.afterWrite(f.Path); err != nil {
				return err
			}
		}
	}
	if err := d.reload(ctx); err != nil {
		return err
	}
	state := agentDest(root, defaultSetupState)
	j := agentInstallJournal{Schema: 1, PlanSHA256: r.Target.SHA256, State: "installed-awaiting-enrollment", Started: true}
	for _, f := range r.Target.Files {
		j.Completed = append(j.Completed, f.Path)
	}
	if err := saveAgentJournal(filepath.Join(state, "agent-"+r.Target.SHA256+".json"), j); err != nil {
		return err
	}
	var identity registrationJournal
	if err := guidedRead(registrationPath(state, r.Original.SHA256), &identity, true); err != nil {
		return err
	}
	identity.PlanSHA256 = r.Target.SHA256
	if err := saveRegistration(registrationPath(state, r.Target.SHA256), identity); err != nil {
		return err
	}
	oldReview := agentDest(root, platformReviewPath(r.Original))
	if _, err := os.Lstat(oldReview); err == nil {
		var review installedPlatformReview
		if err = guidedRead(oldReview, &review, true); err != nil {
			return err
		}
		if review.Schema != 1 || review.PlanSHA256 != r.Original.SHA256 || review.Machine != r.Machine {
			return fmt.Errorf("invalid platform review during WireGuard transition")
		}
		review.PlanSHA256 = r.Target.SHA256
		if err = writeGuided(agentDest(root, platformReviewPath(r.Target)), review); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := verifyRegistrationInstallation(ctx, root, r.Target); err != nil {
		return err
	}
	if err := writeGuided(filepath.Join(dir, "setup.json"), r.Target.Setup); err != nil {
		return err
	}
	if err := writeGuided(filepath.Join(dir, "agent.json"), r.Target); err != nil {
		return err
	}
	r.Complete = true
	return writeGuided(path, r)
}
