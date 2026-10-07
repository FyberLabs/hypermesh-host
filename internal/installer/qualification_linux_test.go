//go:build linux

package installer

import (
	"bufio"
	"context"
	"errors"
	"github.com/FyberLabs/hypermesh-host/internal/agent"
	"github.com/FyberLabs/hypermesh-host/internal/hostregistration"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQualificationRecordsThreeSamplesAndRetriesOnlyUpload(t *testing.T) {
	j := qualificationJournal{State: "approved"}
	j.Run.Manifest.MeasurementIDs = []string{"a", "b", "c"}
	calls, reports := 0, 0
	d := qualificationDeps{
		Check: func(context.Context) error { return nil }, Acquire: func(context.Context) error { return nil }, Tunnel: func(context.Context) error { return nil },
		Sample: func(_ context.Context, id string) (hostregistration.QualificationSample, error) {
			calls++
			return hostregistration.QualificationSample{MeasurementID: id, CertificationEvidence: hostregistration.CertificationEvidence{TokensPerSecond: float64(calls)}}, nil
		},
		Save: func(qualificationJournal) error { return nil }, Report: func(_ context.Context, r hostregistration.QualificationReport) error {
			reports++
			if len(r.Samples) != 3 || !r.Samples[2].TunnelVerified {
				t.Fatal("missing evidence")
			}
			if reports == 1 {
				return errors.New("offline")
			}
			return nil
		},
	}
	if performQualification(context.Background(), &j, d) == nil || j.State != "evidence-ready" {
		t.Fatal("upload failure lost")
	}
	if err := performQualification(context.Background(), &j, d); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || reports != 2 || j.State != "reported" {
		t.Fatalf("replayed workload: %d %d %s", calls, reports, j.State)
	}
}
func TestQualificationInterruptedRunCannotBecomeSuccess(t *testing.T) {
	j := qualificationJournal{State: "running"}
	d := qualificationDeps{Save: func(qualificationJournal) error { return nil }, Report: func(_ context.Context, r hostregistration.QualificationReport) error {
		if r.FailureCode != "interrupted" {
			t.Fatal("lost failure")
		}
		return nil
	}}
	if performQualification(context.Background(), &j, d) == nil || j.State != "reported" {
		t.Fatal("interruption accepted")
	}
	if performQualification(context.Background(), &j, d) == nil {
		t.Fatal("reported failure became success")
	}
}
func TestQualificationAcquisitionFailureRetainsReason(t *testing.T) {
	j := qualificationJournal{State: "approved"}
	j.Run.Manifest.MeasurementIDs = []string{"a", "b", "c"}
	d := qualificationDeps{Check: func(context.Context) error { return nil }, Acquire: func(context.Context) error { return errors.New("download failed") }, Save: func(qualificationJournal) error { return nil }, Report: func(_ context.Context, r hostregistration.QualificationReport) error {
		if r.FailureCode != "acquisition" || len(r.Samples) != 0 {
			t.Fatal("incorrect failure")
		}
		return nil
	}}
	if performQualification(context.Background(), &j, d) == nil {
		t.Fatal("failed acquisition accepted")
	}
}

func TestQualificationReadsOrdinaryPlanButRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	var plan agentInstallPlan
	if err := readQualificationAgentPlan(path, &plan); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if readQualificationAgentPlan(link, &plan) == nil {
		t.Fatal("symlink accepted")
	}
}
func TestQualificationRejectsReplayedSample(t *testing.T) {
	j := qualificationJournal{State: "approved"}
	j.Run.Manifest.MeasurementIDs = []string{"a", "b", "c"}
	d := qualificationDeps{Check: func(context.Context) error { return nil }, Acquire: func(context.Context) error { return nil }, Tunnel: func(context.Context) error { return nil }, Sample: func(context.Context, string) (hostregistration.QualificationSample, error) {
		return hostregistration.QualificationSample{MeasurementID: "a"}, nil
	}, Save: func(qualificationJournal) error { return nil }, Report: func(_ context.Context, r hostregistration.QualificationReport) error {
		if r.FailureCode != "workload" || len(r.Samples) != 1 {
			t.Fatal("replay accepted")
		}
		return nil
	}}
	if performQualification(context.Background(), &j, d) == nil {
		t.Fatal("replayed sample became success")
	}
}

func TestGuidedQualificationCredentialPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hf-token")
	if err := os.WriteFile(path, []byte("hf_private_test_token"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, answer := range []string{"", path} {
		ui := guidedUI{in: bufio.NewReader(strings.NewReader(answer + "\n")), out: io.Discard}
		args, err := qualificationArgs(ui, "agent.json", "run-id")
		if err != nil {
			t.Fatal(err)
		}
		if answer == "" && len(args) != 4 {
			t.Fatal(args)
		}
		if answer != "" && (len(args) != 6 || args[4] != "--model-token-file" || args[5] != path) {
			t.Fatal(args)
		}
	}
	ui := guidedUI{in: bufio.NewReader(strings.NewReader("/missing/token\n")), out: io.Discard}
	if _, err := qualificationArgs(ui, "agent.json", "run-id"); err == nil {
		t.Fatal("invalid credential accepted before run")
	}
}

func TestQualificationPersistsSafeWorkloadDiagnostic(t *testing.T) {
	j := qualificationJournal{State: "approved"}
	j.Run.Manifest.MeasurementIDs = []string{"a", "b", "c"}
	calls := 0
	d := qualificationDeps{
		Check: func(context.Context) error { return nil }, Acquire: func(context.Context) error { return nil }, Tunnel: func(context.Context) error { return nil },
		Sample: func(context.Context, string) (hostregistration.QualificationSample, error) {
			calls++
			return hostregistration.QualificationSample{}, &agent.WorkloadFailure{Reason: "context token counts differ: prompt=496 generated=15 expected=496+16"}
		},
		Save: func(v qualificationJournal) error { return nil }, Report: func(context.Context, hostregistration.QualificationReport) error { return nil },
	}
	for range 2 {
		err := performQualification(context.Background(), &j, d)
		if err == nil || !strings.Contains(err.Error(), "generated=15") || !strings.Contains(j.FailureDetail, "generated=15") {
			t.Fatal("lost safe detail", err, j)
		}
	}
	if calls != 1 || j.State != "reported" || len(j.Report.Samples) != 0 {
		t.Fatal("failure replayed or promoted")
	}
}

func TestReportedQualificationFailureOffersHostRetryWithoutRepeatingWork(t *testing.T) {
	j := qualificationJournal{State: "reported", Report: hostregistration.QualificationReport{FailureCode: "workload"}}
	err := performQualification(context.Background(), &j, qualificationDeps{})
	if err == nil || !strings.Contains(err.Error(), "rerun hypermesh-deploy") || !strings.Contains(err.Error(), "for manual qualify-host, ask the operator to authorize a new run") || !strings.Contains(err.Error(), "same --agent-plan and the new --run-id") {
		t.Fatalf("expected host retry guidance, got %v", err)
	}
	if j.State != "reported" || j.Report.FailureCode != "workload" {
		t.Fatal("changed retained evidence")
	}
}
