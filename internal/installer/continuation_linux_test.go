//go:build linux

package installer

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestContinuationEnrollsBeforeChangesAndResumesAcrossRebootAndReview(t *testing.T) {
	g, calls, _ := fixtureGuided(t, "") // No stdin is available after the one aggregate consent.
	ctx := context.WithValue(context.Background(), continuationConsent{}, true)
	g.ui.ctx = ctx
	g.prepareContinuation = func(_ context.Context, _ SetupPlan) error {
		if !reflect.DeepEqual(*calls, []string{"plan-setup"}) {
			t.Fatal("changed machine before enrollment and service installation", *calls)
		}
		*calls = append(*calls, "enroll-and-enable-continuation")
		g.state.Autonomous = true
		g.state.SellConsent = true
		return nil
	}
	g.adoptEnrollment = func(context.Context, agentInstallPlan) (bool, error) {
		*calls = append(*calls, "adopt-saved-identity")
		return true, nil
	}
	g.rebootNeeded = func(SetupPlan) (bool, error) { return true, nil }
	reviewed := false
	g.qualification = func(context.Context, string) (bool, error) { return reviewed, nil }
	if err := g.finish(ctx); err != nil {
		t.Fatal(err)
	}
	if g.state.Stage != 1 {
		t.Fatal("did not checkpoint reboot", g.state.Stage)
	}
	want := []string{"plan-setup", "enroll-and-enable-continuation", "apply-setup", "plan-reboot", "reboot-setup"}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatal(*calls)
	}
	// Simulate a new service invocation after boot: no browser auth or approval input.
	*calls = nil
	g.boot = func() (string, string, error) { return "machine", "new-boot", nil }
	if err := g.finish(ctx); err != nil {
		t.Fatal(err)
	}
	if g.state.Stage != 5 {
		t.Fatal("did not wait for network review", g.state.Stage)
	}
	want = []string{"resume-setup", "plan-agent", "install-agent", "adopt-saved-identity", "check-agent"}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatal(*calls)
	}
	*calls = nil
	if err := g.finish(ctx); err != nil || len(*calls) != 0 {
		t.Fatal("review wait repeated installation", err, *calls)
	}
	reviewed = true
	if err := g.finish(ctx); err != nil {
		t.Fatal(err)
	}
	want = []string{"plan-certification", "certify-agent", "activate-agent", "host-status"}
	if !reflect.DeepEqual(*calls, want) || g.state.Stage != 7 {
		t.Fatal("did not complete after review", *calls, g.state.Stage)
	}
}

func TestContinuationEnrollmentFailurePreventsMachineChanges(t *testing.T) {
	g, calls, _ := fixtureGuided(t, "")
	g.prepareContinuation = func(context.Context, SetupPlan) error { return errors.New("owner approval pending") }
	if err := g.finish(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	if g.state.Stage != 0 || !reflect.DeepEqual(*calls, []string{"plan-setup"}) {
		t.Fatal("changed machine before durable enrollment", *calls)
	}
}

func TestContinuationRetriesFailedStageWithoutAuthentication(t *testing.T) {
	g, calls, failure := fixtureGuided(t, "")
	ctx := context.WithValue(context.Background(), continuationConsent{}, true)
	g.ui.ctx = ctx
	g.state.Autonomous = true
	g.state.SellConsent = true
	g.adoptEnrollment = func(context.Context, agentInstallPlan) (bool, error) { return true, nil }
	*failure = "check-agent"
	if err := g.finish(ctx); err == nil || g.state.Stage != 4 {
		t.Fatal("missing interrupted checkpoint", err)
	}
	*failure = ""
	*calls = nil
	if err := g.finish(ctx); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0] != "check-agent" {
		t.Fatal("restarted enrollment", *calls)
	}
	for _, call := range *calls {
		if call == "register-agent" || call == "install-agent" {
			t.Fatal("repeated identity/install", *calls)
		}
	}
}

func TestContinuationConsentDoesNotApproveUnrelatedChanges(t *testing.T) {
	ui := guidedUI{ctx: context.WithValue(context.Background(), continuationConsent{}, true), out: io.Discard, in: bufio.NewReader(strings.NewReader("y\n"))}
	for _, prompt := range []string{"Activate the host service and start selling?", "Request the reboot again now?"} {
		if err := ui.confirm(prompt); err != nil {
			t.Fatal(err)
		}
	}
	for _, prompt := range []string{"Replace the kernel?", "Change owner?", "Enable selling?"} {
		if err := ui.confirm(prompt); err == nil {
			t.Fatal("unreviewed consent", prompt)
		}
	}
	if _, err := ui.ask("Account network", "production"); err == nil {
		t.Fatal("silently changed network")
	}
}

func TestContinuationBackoffIsDurableBoundedAndResetsOnProgress(t *testing.T) {
	now := time.Now().UTC()
	s := guidedState{Stage: 5}
	var previous continuationStatus
	for i := 1; i <= 12; i++ {
		previous = nextContinuation(previous, s, true, now)
		if previous.NextAttempt.Sub(now) > time.Hour || previous.NextAttempt.Sub(now) < time.Minute {
			t.Fatal(previous)
		}
	}
	if previous.Failures != 7 || previous.NextAttempt.Sub(now) != time.Hour {
		t.Fatal(previous)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeGuided(filepath.Join(dir, "continuation-status.json"), previous); err != nil {
		t.Fatal(err)
	}
	restored, err := readContinuation(dir)
	if err != nil || !restored.NextAttempt.Equal(previous.NextAttempt) {
		t.Fatal("retry deadline lost", err)
	}
	s.Stage = 6
	next := nextContinuation(restored, s, true, now)
	if next.Failures != 1 || next.NextAttempt.Sub(now) != time.Minute {
		t.Fatal("progress did not reset backoff", next)
	}
	next = nextContinuation(next, s, false, now)
	if next.State != "waiting" || next.Failures != 0 {
		t.Fatal(next)
	}
	s.Stage = 7
	next = nextContinuation(next, s, false, now)
	if next.State != "complete" || !next.NextAttempt.IsZero() {
		t.Fatal(next)
	}
}

func TestContinuationRejectsEverySystemdSearchPathConflict(t *testing.T) {
	for _, directory := range []string{"/etc/systemd/system", "/run/systemd/system", "/usr/local/lib/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"} {
		for _, name := range []string{"hypermesh-onboarding.service.d", "hypermesh-onboarding.timer.d", "hypermesh-.service.d", "service.d", "timer.d"} {
			t.Run(directory+name, func(t *testing.T) {
				root := t.TempDir()
				path := agentDest(root, filepath.Join(directory, name))
				if err := os.MkdirAll(path, 0755); err != nil {
					t.Fatal(err)
				}
				if err := checkContinuationUnits(root, continuationFiles(nil), directory); err == nil {
					t.Fatal("accepted unreviewed unit configuration")
				}
			})
		}
	}
	for _, raw := range []string{"", "relative/path"} {
		if err := checkContinuationUnits(t.TempDir(), continuationFiles(nil), raw); err == nil {
			t.Fatal("invalid unit search path accepted")
		}
	}
	files := continuationFiles([]byte("verified executable"))
	if files[0].Mode != 0700 || !strings.Contains(string(files[1].Data), "install --continue") || !strings.Contains(string(files[2].Data), "WantedBy=timers.target") {
		t.Fatal("missing durable protected service")
	}
}

func TestEarlyEnrollmentCompletedCheckpointResumesWithoutLogin(t *testing.T) {
	for _, state := range []string{"completed", "registered-inactive"} {
		t.Run(state, func(t *testing.T) {
			p, j := registrationFixture(t)
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			n, _ := installationNetwork("test")
			j.Issuer = n.Issuer
			j.Dashboard = n.Dashboard
			j.PlanSHA256 = p.Setup.PlanSHA256
			j.Owner = "owner"
			j.State = state
			j.DeviceID = j.Request.RequestID
			path := filepath.Join(dir, "enrollment.json")
			if err := saveRegistration(path, j); err != nil {
				t.Fatal(err)
			}
			s := guidedState{Network: "test", Tenant: p.TenantID, Host: p.HostName, Class: j.Request.ClassID}
			ctx := context.WithValue(context.Background(), continuationConsent{}, true)
			if err := earlyEnrollment(ctx, dir, &s, p.Setup, io.Discard); err != nil {
				t.Fatal(err)
			}
			var restored registrationJournal
			if err := guidedRead(path, &restored, true); err != nil {
				t.Fatal(err)
			}
			if restored.State != "registered-inactive" || restored.PrivateKey != j.PrivateKey || restored.DeviceSecret != j.DeviceSecret || restored.DeviceID != j.DeviceID {
				t.Fatal("identity changed during completion recovery")
			}
			// A saved enrollment from a different host must not be reused.
			s.Host = "another-host"
			if err := earlyEnrollment(ctx, dir, &s, p.Setup, io.Discard); err == nil {
				t.Fatal("wrong host binding accepted")
			}
		})
	}
}

func TestDefaultContinuationRetainsConsentAndExplicitRecoveryReadsInput(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		ctx := context.Background()
		if interactive {
			ctx = context.WithValue(ctx, continuationInteractive{}, true)
		}
		ctx = withContinuationConsent(ctx)
		ui := guidedUI{ctx: ctx, in: bufio.NewReader(strings.NewReader("/private/hf-token\n")), out: io.Discard}
		value, err := ui.ask("Private Hugging Face token file", "")
		if interactive {
			if err != nil || value != "/private/hf-token" {
				t.Fatal(value, err)
			}
		} else if err == nil {
			t.Fatal("default continuation unexpectedly requested input")
		}
		if !interactive {
			if err := ui.confirm("Activate the host service and start selling?"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, args := range [][]string{{"--continue", "--interactive"}, {"--continue", "--cache-settings"}} {
		if code := RunGuidedInstall(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard); code != 2 {
			t.Fatal("background accepted interactive option", code)
		}
	}
}

func TestContinuationReadsRunningInodeAfterExecutablePathReplaced(t *testing.T) {
	if os.Getenv("HYPERMESH_TEST_RUNNING_INODE") == "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		expected, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(exe, exe+".old"); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(exe, []byte("replacement"), 0700); err != nil {
			t.Fatal(err)
		}
		got, err := runningContinuation()
		if err != nil || digestSetupBytes(got) != digestSetupBytes(expected) {
			t.Fatal("read replaceable pathname instead of running inode", err)
		}
		return
	}
	binary, err := runningContinuation()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "installer-test")
	if err = os.WriteFile(path, binary, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "-test.run=^TestContinuationReadsRunningInodeAfterExecutablePathReplaced$")
	cmd.Env = append(os.Environ(), "HYPERMESH_TEST_RUNNING_INODE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestLegacyContinuationApprovalAsksBeforeSelling(t *testing.T) {
	g, calls, _ := fixtureGuided(t, "")
	ctx := context.WithValue(context.Background(), continuationConsent{}, true)
	g.ui.ctx = ctx
	// Saved by an earlier installer: continuation approved, selling not.
	g.state.Autonomous = true
	g.adoptEnrollment = func(context.Context, agentInstallPlan) (bool, error) { return true, nil }
	err := g.finish(ctx)
	if err == nil || !strings.Contains(err.Error(), "paused startup only") || g.state.Stage != 6 {
		t.Fatal("legacy approval started selling", err, g.state.Stage)
	}
	for _, call := range *calls {
		if call == "activate-agent" {
			t.Fatal("activated without selling approval", *calls)
		}
	}
	*calls = nil
	g.ui = guidedUI{ctx: context.Background(), out: io.Discard, in: bufio.NewReader(strings.NewReader(strings.Repeat("y\n", 4)))}
	if err := g.finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !g.state.SellConsent || g.state.Stage != 7 || (*calls)[0] != "activate-agent" {
		t.Fatal("interactive approval did not activate", g.state.SellConsent, g.state.Stage, *calls)
	}
}
