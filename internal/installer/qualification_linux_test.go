//go:build linux

package installer

import (
	"context"
	"errors"
	"github.com/FyberLabs/hypermesh-host/internal/hostregistration"
	"testing"
)

func TestQualificationRecordsThreeSamplesAndRetriesOnlyUpload(t *testing.T) {
	j := qualificationJournal{State: "approved"}
	calls, reports := 0, 0
	d := qualificationDeps{
		Check: func(context.Context) error { return nil }, Acquire: func(context.Context) error { return nil }, Tunnel: func(context.Context) error { return nil },
		Sample: func(context.Context) (hostregistration.CertificationEvidence, error) {
			calls++
			return hostregistration.CertificationEvidence{TokensPerSecond: float64(calls)}, nil
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
