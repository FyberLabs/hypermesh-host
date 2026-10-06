//go:build linux

package installer

import (
	"context"
	"errors"
	"github.com/FyberLabs/hypermesh-host/internal/hostregistration"
	"os"
	"path/filepath"
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
