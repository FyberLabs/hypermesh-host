//go:build linux

package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func wireguardTransitionFixture(t *testing.T) (string, string, wireguardTransition, wireguardTransitionDeps, registrationJournal) {
	t.Helper()
	p, identity := registrationFixture(t)
	p.Setup.WireGuardMode = "kernel"
	profile, err := selectSetupProfile(p.Setup.Preflight, p.Setup.Platform, p.Setup.Profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.Setup.Profile = profile
	p.Setup.PlanSHA256 = setupPlanDigest(p.Setup)
	p.Files = agentFileManifest(agentPayloads(p, []byte("verified agent")))
	p.SHA256 = agentPlanDigest(p)
	identity.PlanSHA256 = p.SHA256
	identity.State = "registered-inactive"
	identity.Owner = "owner"
	identity.DeviceID = identity.Request.RequestID
	root := t.TempDir()
	state := agentDest(root, defaultSetupState)
	if err = secureSetupDirectory(state, true); err != nil {
		t.Fatal(err)
	}
	installed := agentInstallJournal{Schema: 1, PlanSHA256: p.SHA256}
	if err = installAgentFiles(context.Background(), root, agentPayloads(p, []byte("verified agent")), false, &installed, func(j agentInstallJournal) error {
		return saveAgentJournal(filepath.Join(state, "agent-"+p.SHA256+".json"), j)
	}); err != nil {
		t.Fatal(err)
	}
	if err = publishRegistration(root, identity); err != nil {
		t.Fatal(err)
	}
	if err = saveRegistration(registrationPath(state, p.SHA256), identity); err != nil {
		t.Fatal(err)
	}
	target, err := userspaceTransitionPlan(p)
	if err != nil {
		t.Fatal(err)
	}
	record := wireguardTransition{Schema: 1, Machine: "machine", Original: p, Target: target}
	dir := filepath.Join(state, "guided")
	if err = secureSetupDirectory(dir, true); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]any{"agent.json": p, "setup.json": p.Setup, "wireguard-transition.json": record} {
		if err = writeGuided(filepath.Join(dir, path), value); err != nil {
			t.Fatal(err)
		}
	}
	deps := wireguardTransitionDeps{machine: func() (string, error) { return "machine", nil }, guard: func(context.Context, agentInstallPlan) error { return nil }, preflight: func(context.Context, agentInstallPlan, agentInstallPlan, registrationJournal) error { return nil }, reload: func(context.Context) error { return nil }}
	return root, dir, record, deps, identity
}
func TestWireguardTransitionRetainsIdentityAndCompleteManifest(t *testing.T) {
	root, dir, r, d, identity := wireguardTransitionFixture(t)
	before, err := os.ReadFile(agentDest(root, "/var/lib/hypermesh/device.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = resumeWireguardTransition(context.Background(), dir, root, d); err != nil {
		t.Fatal(err)
	}
	if err = verifyRegistrationInstallation(context.Background(), root, r.Target); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(agentDest(root, "/var/lib/hypermesh/device.json"))
	if string(before) != string(after) {
		t.Fatal("device state changed")
	}
	var migrated registrationJournal
	if err = guidedRead(agentDest(root, registrationPath(defaultSetupState, r.Target.SHA256)), &migrated, true); err != nil {
		t.Fatal(err)
	}
	identity.PlanSHA256 = r.Target.SHA256
	if !sameSetupJSON(identity, migrated) {
		t.Fatal("registration changed")
	}
	if err = validateRegistration(migrated, r.Target, migrated.Issuer, migrated.Dashboard, migrated.Request.ClassID); err != nil {
		t.Fatal(err)
	}
	if err = checkRegistrationIdentity(root, migrated, true); err != nil {
		t.Fatal(err)
	}
	if err = resumeWireguardTransition(context.Background(), dir, root, d); err != nil {
		t.Fatal("repeat", err)
	}
	if _, err = os.Stat(agentDest(root, registrationPath(defaultSetupState, r.Original.SHA256))); err != nil {
		t.Fatal("original journal lost")
	}
	if _, err = os.Stat(agentDest(root, "/var/lib/hypermesh/activation-approved")); !os.IsNotExist(err) {
		t.Fatal("activated without approval")
	}
}
func TestWireguardTransitionRecoversEveryPayloadBoundary(t *testing.T) {
	for _, boundary := range []string{"/etc/hypermesh/agent.env", "/etc/systemd/system/hypermesh-wg.service", "/etc/systemd/system/hypermesh-agent.service", "reload"} {
		t.Run(boundary, func(t *testing.T) {
			root, dir, r, d, _ := wireguardTransitionFixture(t)
			d.afterWrite = func(path string) error {
				if path == boundary {
					return fmt.Errorf("interrupted")
				}
				return nil
			}
			if boundary == "reload" {
				d.reload = func(context.Context) error { return fmt.Errorf("interrupted") }
			}
			if resumeWireguardTransition(context.Background(), dir, root, d) == nil {
				t.Fatal("missing injected failure")
			}
			d.afterWrite = nil
			d.reload = func(context.Context) error { return nil }
			if err := resumeWireguardTransition(context.Background(), dir, root, d); err != nil {
				t.Fatal(err)
			}
			if err := verifyRegistrationInstallation(context.Background(), root, r.Target); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestWireguardTransitionRejectsUnapprovedState(t *testing.T) {
	for _, kind := range []string{"machine", "target", "binary", "environment", "symlink", "active", "identity", "service", "prerequisite", "journal"} {
		t.Run(kind, func(t *testing.T) {
			root, dir, r, d, _ := wireguardTransitionFixture(t)
			switch kind {
			case "machine":
				d.machine = func() (string, error) { return "other", nil }
			case "target":
				r.Target.ControlPlane = "https://other.example"
				_ = writeGuided(filepath.Join(dir, "wireguard-transition.json"), r)
			case "binary":
				_ = os.WriteFile(agentDest(root, "/usr/local/bin/hypermesh-agent"), []byte("changed"), 0755)
			case "environment":
				_ = os.WriteFile(agentDest(root, "/etc/hypermesh/agent.env"), []byte("changed"), 0600)
			case "symlink":
				path := agentDest(root, "/etc/systemd/system/hypermesh-wg.service")
				_ = os.Symlink("/tmp/foreign-unit", path)
			case "active":
				_ = os.WriteFile(agentDest(root, "/var/lib/hypermesh/activation-approved"), []byte("active"), 0600)
			case "identity":
				_ = os.WriteFile(agentDest(root, "/var/lib/hypermesh/device.json"), []byte("{}"), 0600)
			case "service":
				d.guard = func(context.Context, agentInstallPlan) error { return fmt.Errorf("active service") }
			case "prerequisite":
				d.preflight = func(context.Context, agentInstallPlan, agentInstallPlan, registrationJournal) error {
					return fmt.Errorf("missing userspace package")
				}
			case "journal":
				_ = os.Remove(agentDest(root, filepath.Join(defaultSetupState, "agent-"+r.Original.SHA256+".json")))
			}
			if resumeWireguardTransition(context.Background(), dir, root, d) == nil {
				t.Fatal("accepted", kind)
			}
			if _, err := os.Stat(agentDest(root, registrationPath(defaultSetupState, r.Target.SHA256))); !os.IsNotExist(err) {
				t.Fatal("published new journal despite failed validation")
			}
		})
	}
}

func TestGuidedWireguardCapabilityPriority(t *testing.T) {
	for _, tt := range []struct {
		name                   string
		kernel, tun, userspace bool
		mode                   string
		fail                   bool
	}{
		{"kernel_without_userspace", true, false, false, "kernel", false},
		{"kernel_preferred", true, true, true, "kernel", false},
		{"userspace_ready", false, true, true, "userspace", false},
		{"userspace_package_needs_review", false, true, false, "userspace", false},
		{"binary_without_tun", false, false, true, "", true},
		{"neither_backend", false, false, false, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mode, detail, err := selectGuidedWireguard(tt.kernel, tt.tun, tt.userspace)
			if mode != tt.mode || (err != nil) != tt.fail || (!tt.fail && detail == "") {
				t.Fatal(mode, detail, err)
			}
		})
	}
}

func TestWireguardTransitionRejectsUnrelatedPayloadUpgrade(t *testing.T) {
	for _, path := range []string{"/usr/local/lib/hypermesh/update-rollback", "/etc/hypermesh/update-pubkey.asc"} {
		t.Run(path, func(t *testing.T) {
			_, _, r, _, _ := wireguardTransitionFixture(t)
			old := r.Original
			for i := range old.Files {
				if old.Files[i].Path == path {
					old.Files[i].SHA256 = digestSetupBytes([]byte("previous release content"))
				}
			}
			old.SHA256 = agentPlanDigest(old)
			if _, err := userspaceTransitionPlan(old); err == nil {
				t.Fatal("adopted unrelated payload from newer installer")
			}
		})
	}
}
func TestWireguardRepairWritesOnlyApprovedPaths(t *testing.T) {
	root, dir, _, d, _ := wireguardTransitionFixture(t)
	writes := 0
	d.afterWrite = func(path string) error {
		if !wireguardTransitionPath(path) {
			t.Fatalf("unapproved write %s", path)
		}
		writes++
		return nil
	}
	if err := resumeWireguardTransition(context.Background(), dir, root, d); err != nil {
		t.Fatal(err)
	}
	if writes != 3 {
		t.Fatalf("expected environment and two units; wrote %d files", writes)
	}
}
func TestGuidedKernelRequiresSuccessfulInsertion(t *testing.T) {
	for _, kind := range []string{"loaded", "absent", "success", "signature_failure", "no_sys_module", "declined"} {
		t.Run(kind, func(t *testing.T) {
			loaded := kind == "loaded"
			dry, insert, approvals := 0, 0, 0
			available, err := checkGuidedKernel(func() bool { return loaded }, func(args ...string) error {
				if len(args) == 3 {
					dry++
					if kind == "absent" {
						return fmt.Errorf("not found")
					}
					return nil
				}
				insert++
				if kind == "signature_failure" {
					return fmt.Errorf("insertion failed")
				}
				loaded = kind == "success"
				return nil
			}, func(string) error {
				approvals++
				if kind == "declined" {
					return fmt.Errorf("declined")
				}
				return nil
			})
			want := kind == "loaded" || kind == "success"
			if available != want || (err != nil) != (kind == "declined") {
				t.Fatal(available, err)
			}
			if kind == "loaded" && (dry != 0 || insert != 0 || approvals != 0) {
				t.Fatal("loaded module reprobed")
			}
			if (kind == "absent" || kind == "declined") && insert != 0 {
				t.Fatal("unapproved insertion")
			}
			if insert > 0 && approvals != 1 {
				t.Fatal("insertion without approval")
			}
		})
	}
}
