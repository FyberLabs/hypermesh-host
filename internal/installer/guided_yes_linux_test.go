//go:build linux

package installer

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/agent"
)

// A reader that fails the test if --yes ever waits for input.
type noInput struct{ t *testing.T }

func (n noInput) Read([]byte) (int, error) {
	n.t.Helper()
	n.t.Fatal("--yes read input")
	return 0, io.EOF
}

func yesUI(t *testing.T, out io.Writer) guidedUI {
	return guidedUI{in: bufio.NewReader(noInput{t}), out: out, ctx: context.Background(), yes: true}
}

func TestYesAnswersQuestionsWithDefaultsAndNeverReadsInput(t *testing.T) {
	var out bytes.Buffer
	ui := yesUI(t, &out)
	got, err := ui.ask("Account network: production or test", "production")
	if err != nil || got != "production" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err = ui.ask("Private Hugging Face token file for a gated model (Enter for public or cached models)", ""); err != nil || got != "" {
		t.Fatalf("%q %v", got, err)
	}
	for _, label := range []string{promptEnroll, promptContinue, promptPausedOnly, promptModule, promptCacheSave,
		"Create /srv/models without changing partitions or mounts?",
		"Apply these prerequisite and headless changes?", "Activate the host service and start selling?"} {
		if err = ui.confirm(label); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	if !strings.Contains(out.String(), "Approved by --yes: "+promptEnroll) {
		t.Fatalf("consent not shown:\n%s", out.String())
	}
}

func TestYesDoesNotCoverMaintenanceQuestions(t *testing.T) {
	for _, label := range []string{
		"Accept these already-installed package versions and continue? No packages will be changed.",
		"The previous browser approval ended. Start a new approval with fresh keys?",
		"Use this newer installer to continue onboarding of the verified installation? Existing files and approvals will be preserved.",
		"Use this newer installer to recheck prerequisites and review fresh plans? Previous plans and model files will be preserved.",
		"Approve this WireGuard repair? Your host registration, keys and model files are preserved.",
		"Delete everything?",
	} {
		err := yesUI(t, io.Discard).confirm(label)
		if err == nil || !strings.Contains(err.Error(), "--yes does not cover this question") || !strings.Contains(err.Error(), "without --yes") {
			t.Fatalf("%s: %v", label, err)
		}
	}
}

func TestWithoutYesQuestionsStillAskAndDeclineStops(t *testing.T) {
	ui := guidedUI{in: bufio.NewReader(strings.NewReader("n\n")), out: io.Discard, ctx: context.Background()}
	if err := ui.confirm(promptEnroll); err == nil {
		t.Fatal("decline accepted")
	}
	if err := ui.confirm(promptEnroll); err == nil {
		t.Fatal("end of input accepted")
	}
}

func TestYesIsThisRunsConsentEvenWithASavedContinuation(t *testing.T) {
	ui := yesUI(t, io.Discard)
	ui.ctx = withContinuationConsent(context.Background())
	if err := ui.confirm(promptPausedOnly); err != nil {
		t.Fatal("--yes could not approve selling for a paused-only session:", err)
	}
	if err := ui.confirm("Accept these already-installed package versions and continue? No packages will be changed."); err == nil ||
		!strings.Contains(err.Error(), "--yes does not cover") {
		t.Fatalf("unlisted question under a saved continuation: %v", err)
	}
	// Without --yes the saved continuation still refuses to start selling.
	plain := guidedUI{ctx: withContinuationConsent(context.Background()), out: io.Discard, in: bufio.NewReader(noInput{t})}
	if err := plain.confirm(promptPausedOnly); err == nil {
		t.Fatal("saved continuation approved selling for a paused-only session")
	}
}

func TestYesRunsWholeFlowWithoutInput(t *testing.T) {
	g, calls, _ := fixtureGuided(t, "")
	g.ui = yesUI(t, io.Discard)
	if err := g.finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"plan-setup", "apply-setup", "plan-agent", "install-agent", "register-agent", "check-agent", "plan-certification", "certify-agent", "activate-agent", "host-status"}
	if !reflect.DeepEqual(*calls, want) || g.state.Stage != 7 {
		t.Fatalf("calls=%v stage=%d", *calls, g.state.Stage)
	}
}

func TestYesStopsWhenAHostCheckFails(t *testing.T) {
	for _, step := range []string{"plan-setup", "plan-agent", "check-agent", "certify-agent"} {
		g, calls, failure := fixtureGuided(t, "")
		g.ui = yesUI(t, io.Discard)
		*failure = step
		if err := g.finish(context.Background()); err == nil {
			t.Fatalf("%s: failure ignored", step)
		}
		if last := (*calls)[len(*calls)-1]; last != step {
			t.Fatalf("%s: continued past the failure: %v", step, *calls)
		}
		for _, call := range *calls {
			if call == "activate-agent" {
				t.Fatalf("%s: activated after a failed check", step)
			}
		}
	}
}

func TestYesRecordsSellingConsentForPausedOnlySession(t *testing.T) {
	for _, saved := range []bool{false, true} {
		g, calls, _ := fixtureGuided(t, "")
		g.ui = yesUI(t, io.Discard)
		if err := g.finish(context.Background()); err != nil {
			t.Fatal(err)
		}
		*calls = nil
		g.state.Stage, g.state.Autonomous, g.state.SellConsent = 6, true, false
		if saved {
			// A resumed autonomous session runs under its saved continuation context.
			g.ui.ctx = withContinuationConsent(context.Background())
		}
		if err := g.finish(context.Background()); err != nil {
			t.Fatalf("saved continuation=%v: %v", saved, err)
		}
		if !g.state.SellConsent || g.state.Stage != 7 || !reflect.DeepEqual(*calls, []string{"activate-agent", "host-status"}) {
			t.Fatalf("saved=%v consent=%v stage=%d calls=%v", saved, g.state.SellConsent, g.state.Stage, *calls)
		}
	}
}

func TestCachePolicyFlagsAndYesNeedNoInput(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	ui := yesUI(t, &out)
	ui.cacheQuota, ui.cacheReserve = "1", "3"
	if err := guidedCachePolicy(dir, ui, false); err != nil {
		t.Fatal(err)
	}
	p, err := agent.ReadCachePolicy(dir)
	if err != nil || p.MaxBytes != 1<<30 || p.ReserveBytes != 3<<30 {
		t.Fatalf("%+v %v", p, err)
	}
	if !strings.Contains(out.String(), "Using --cache-quota-gib 1") {
		t.Fatal(out.String())
	}
}

func TestCachePolicyYesUsesSuggestedQuotaButStillRejectsBadValues(t *testing.T) {
	dir := t.TempDir()
	if err := guidedCachePolicy(dir, yesUI(t, io.Discard), false); err != nil {
		t.Fatal(err)
	}
	if p, err := agent.ReadCachePolicy(dir); err != nil || p == nil || p.MaxBytes == 0 || p.ReserveBytes != 2<<30 {
		t.Fatalf("%+v %v", p, err)
	}
	bad := t.TempDir()
	ui := yesUI(t, io.Discard)
	ui.cacheQuota = "100000000"
	if err := guidedCachePolicy(bad, ui, false); err == nil || !strings.Contains(err.Error(), "quota within the filesystem capacity") {
		t.Fatalf("oversized quota: %v", err)
	}
	ui = yesUI(t, io.Discard)
	ui.cacheReserve = "1"
	if err := guidedCachePolicy(bad, ui, false); err == nil || !strings.Contains(err.Error(), "at least 2 GiB") {
		t.Fatalf("small reserve: %v", err)
	}
	if p, _ := agent.ReadCachePolicy(bad); p != nil {
		t.Fatal("policy written after an error")
	}
}

func TestResumedSessionRejectsDifferentSettings(t *testing.T) {
	s := guidedState{Network: "test", Host: "box", Directory: "/models", Headless: true}
	if err := resumedSettingsMatch(s, "", "", "", false, true); err != nil {
		t.Fatal(err)
	}
	if err := resumedSettingsMatch(s, "test", "box", "/models", true, true); err != nil {
		t.Fatal("same settings rejected:", err)
	}
	err := resumedSettingsMatch(s, "production", "other", "/other", true, false)
	if err == nil {
		t.Fatal("different settings ignored")
	}
	for _, name := range []string{"--network", "--host-name", "--model-dir", "--headless"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
}

func TestGuidedInstallRejectsBadFlagsBeforeAnyAction(t *testing.T) {
	for _, args := range [][]string{
		{"--network", "staging"},
		{"--host-name", "bad name"},
		{"--host-name", strings.Repeat("a", 64)},
		{"--model-dir", "relative/models"},
		{"--model-dir", "/models/../etc"},
		{"--continue", "--yes"},
		{"--continue", "--network", "test"},
		{"--continue", "--cache-quota-gib", "5"},
		{"--yes", "--interactive"},
		{"--yes", "--network="},
		{"--yes", "--model-dir="},
		{"--yes", "--host-name="},
		{"--yes", "--host-name", "  "},
		{"--yes", "--cache-quota-gib="},
		{"--cache-reserve-gib="},
		{"--yes", "--cache-quota-gib", "abc"},
		{"--yes", "--cache-quota-gib", "0"},
		{"--cache-reserve-gib", "1"},
		{"--cache-reserve-gib", "-3"},
		{"unexpected"},
	} {
		var diagnostic bytes.Buffer
		if code := RunGuidedInstall(context.Background(), args, noInput{t}, io.Discard, &diagnostic); code != 2 {
			t.Fatalf("%v: code %d %s", args, code, diagnostic.String())
		}
	}
}

func TestCacheFlagsReplaceAnExistingPolicy(t *testing.T) {
	dir := t.TempDir()
	if err := agent.WriteCachePolicy(dir, agent.CachePolicy{Schema: 1, MaxBytes: 50 << 30, ReserveBytes: 2 << 30}); err != nil {
		t.Fatal(err)
	}
	ui := yesUI(t, io.Discard)
	ui.cacheQuota = "1"
	if err := guidedCachePolicy(dir, ui, true); err != nil {
		t.Fatal(err)
	}
	p, err := agent.ReadCachePolicy(dir)
	if err != nil || p.MaxBytes != 1<<30 || p.ReserveBytes != 2<<30 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestYesUpgradesACompletedPausedOnlySession(t *testing.T) {
	for _, yes := range []bool{true, false} {
		g, calls, _ := fixtureGuided(t, "")
		g.ui = yesUI(t, io.Discard)
		if err := g.finish(context.Background()); err != nil {
			t.Fatal(err)
		}
		*calls = nil
		g.state.Stage, g.state.Autonomous, g.state.SellConsent = 7, true, false
		g.ui = guidedUI{ctx: withContinuationConsent(context.Background()), out: io.Discard, in: bufio.NewReader(noInput{t}), yes: yes}
		if err := g.finish(context.Background()); err != nil {
			t.Fatal(err)
		}
		want := []string{"host-status"}
		if yes {
			want = []string{"activate-agent", "host-status"}
		}
		if !reflect.DeepEqual(*calls, want) || g.state.SellConsent != yes {
			t.Fatalf("yes=%v consent=%v calls=%v", yes, g.state.SellConsent, *calls)
		}
	}
}
