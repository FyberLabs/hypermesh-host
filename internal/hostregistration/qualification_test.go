package hostregistration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQualificationBindingAndPurposeSeparation(t *testing.T) {
	for _, kind := range []string{"valid", "tenant", "nonce", "purpose", "activation", "selling", "failure-planned", "failure-passed", "failure-failed", "success-planned", "success-failed", "success-passed"} {
		t.Run(kind, func(t *testing.T) {
			r := fixture(t)
			r.ClassID = "agx-large"
			id, _ := NewRunID()
			m := CertificationManifest{Purpose: "lab-qualification-v1", Repetitions: 3, MeasurementIDs: []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}, SuiteID: "pack-v1", CatalogID: "model", ClassID: r.ClassID, FileHash: "sha256:" + strings.Repeat("a", 64), ImageDigest: "example.test/runtime@sha256:" + strings.Repeat("b", 64), SourceRef: "unused", SizeBytes: 12, Runtime: "nvidia", Backend: "cuda-jetson", ContextTokens: 4096, FullContext: true}
			if m.Validate() == nil || m.ValidateQualification() != nil {
				t.Fatal("purpose validation")
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				op := "plan"
				if strings.Contains(kind, "-") {
					op = "result"
				}
				if q.URL.Path != "/api/v1/hypermesh/agent/onboarding/qualification/"+op || q.Header.Get("Authorization") != "Bearer hm_dev_"+strings.Repeat("s", 32) {
					t.Error("wrong request")
				}
				var body map[string]any
				json.NewDecoder(q.Body).Decode(&body)
				s := QualificationRun{Purpose: "lab-qualification-v1", Repetitions: 3, CertificationRun: CertificationRun{ProtocolVersion: 1, Nonce: body["nonce"].(string), RunID: id, TenantID: testTenant, DeviceID: r.RequestID, RequestID: r.RequestID, Manifest: m, ManifestSHA256: strings.Repeat("c", 64), State: "planned", ExpiresAt: time.Now().Add(time.Hour), SellingAllowed: flagValue(false)}}
				if strings.Contains(kind, "-") {
					s.State = strings.Split(kind, "-")[1]
				}
				switch kind {
				case "tenant":
					s.TenantID = id
				case "nonce":
					s.Nonce = "stale"
				case "purpose":
					s.Purpose = "certification"
				case "activation":
					s.ActivationAllowed = flagValue(true)
				case "selling":
					s.SellingAllowed = flagValue(true)
				}
				json.NewEncoder(w).Encode(s)
			}))
			defer server.Close()
			c, _ := New(server.URL, testTenant)
			c.http = server.Client()
			var err error
			if strings.Contains(kind, "-") {
				previous := QualificationRun{CertificationRun: CertificationRun{RunID: id, Manifest: m, ManifestSHA256: strings.Repeat("c", 64)}}
				report := QualificationReport{}
				if strings.HasPrefix(kind, "failure-") {
					report.FailureCode = "workload"
				}
				_, err = c.ReportQualification(context.Background(), r, r.RequestID, "hm_dev_"+strings.Repeat("s", 32), previous, report)
			} else {
				_, err = c.PlanQualification(context.Background(), r, r.RequestID, "hm_dev_"+strings.Repeat("s", 32), id)
			}
			if (err == nil) != (kind == "valid" || kind == "failure-failed" || kind == "success-passed") {
				t.Fatal(err)
			}
		})
	}
}

func TestAutomaticQualificationDecisionBinding(t *testing.T) {
	for _, kind := range []string{"qualification", "review", "attention", "certification", "nonce", "tenant", "activation", "state", "missing-run", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			r := fixture(t)
			r.ClassID = "agx-large"
			run, _ := NewRunID()
			state := "planned"
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var body map[string]any
				json.NewDecoder(q.Body).Decode(&body)
				if q.URL.Path != "/api/v1/hypermesh/agent/onboarding/qualification/next" || q.Header.Get("Authorization") != "Bearer hm_dev_"+strings.Repeat("s", 32) {
					t.Error("wrong authentication or route")
				}
				out := QualificationNext{ProtocolVersion: 1, Nonce: body["nonce"].(string), TenantID: testTenant, DeviceID: r.RequestID, RequestID: r.RequestID, Action: "qualification", RunID: &run, State: &state, SellingAllowed: flagValue(false), ActivationAllowed: flagValue(false)}
				switch kind {
				case "review":
					out.Action = "review"
					state = "passed"
				case "attention":
					out.Action = "attention"
					state = "failed"
				case "certification":
					out.Action = "certification"
					out.RunID = nil
					out.State = nil
				case "nonce":
					out.Nonce = "wrong"
				case "tenant":
					out.TenantID = run
				case "activation":
					out.ActivationAllowed = flagValue(true)
				case "state":
					state = "passed"
				case "missing-run":
					out.RunID = nil
				case "unknown":
					out.Action = "activate"
				}
				json.NewEncoder(w).Encode(out)
			}))
			defer server.Close()
			client, _ := New(server.URL, testTenant)
			client.http = server.Client()
			_, err := client.NextQualification(context.Background(), r, r.RequestID, "hm_dev_"+strings.Repeat("s", 32))
			valid := kind == "qualification" || kind == "review" || kind == "attention" || kind == "certification"
			if (err == nil) != valid {
				t.Fatalf("%s: %v", kind, err)
			}
		})
	}
}
