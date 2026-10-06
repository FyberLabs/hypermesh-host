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
	for _, kind := range []string{"valid", "tenant", "nonce", "purpose", "activation", "selling"} {
		t.Run(kind, func(t *testing.T) {
			r := fixture(t)
			r.ClassID = "agx-large"
			id, _ := NewRunID()
			m := CertificationManifest{Purpose: "lab-qualification-v1", Repetitions: 3, SuiteID: "pack-v1", CatalogID: "model", ClassID: r.ClassID, FileHash: "sha256:" + strings.Repeat("a", 64), ImageDigest: "example.test/runtime@sha256:" + strings.Repeat("b", 64), SourceRef: "unused", SizeBytes: 12, Runtime: "nvidia", Backend: "cuda-jetson", ContextTokens: 4096, FullContext: true}
			if m.Validate() == nil || m.ValidateQualification() != nil {
				t.Fatal("purpose validation")
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if q.URL.Path != "/api/v1/hypermesh/agent/onboarding/qualification/plan" || q.Header.Get("Authorization") != "Bearer hm_dev_"+strings.Repeat("s", 32) {
					t.Error("wrong request")
				}
				var body map[string]any
				json.NewDecoder(q.Body).Decode(&body)
				s := QualificationRun{Purpose: "lab-qualification-v1", Repetitions: 3, CertificationRun: CertificationRun{ProtocolVersion: 1, Nonce: body["nonce"].(string), RunID: id, TenantID: testTenant, DeviceID: r.RequestID, RequestID: r.RequestID, Manifest: m, ManifestSHA256: strings.Repeat("c", 64), State: "planned", ExpiresAt: time.Now().Add(time.Hour), SellingAllowed: flagValue(false)}}
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
			_, err := c.PlanQualification(context.Background(), r, r.RequestID, "hm_dev_"+strings.Repeat("s", 32), id)
			if (err == nil) != (kind == "valid") {
				t.Fatal(err)
			}
		})
	}
}
