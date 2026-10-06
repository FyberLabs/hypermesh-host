package hostregistration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCertificationConflictExplainsMissingBaseline(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		json.NewEncoder(w).Encode(map[string]string{"detail": "A complete supported AIuditor class envelope is required"})
	}))
	defer server.Close()
	c, _ := New(server.URL, testTenant)
	c.http = server.Client()
	var result any
	err := c.request(context.Background(), "POST", "/api/v1/hypermesh/agent/onboarding/certification/plan", "private-device-token", nil, &result)
	if err == nil || !strings.Contains(err.Error(), "real measurements") || strings.Contains(err.Error(), "private-device-token") || strings.Contains(err.Error(), "class, host name, key") {
		t.Fatal(err)
	}
}
func TestCertificationConflictKeepsUnknownBodiesPrivate(t *testing.T) {
	for _, body := range []string{
		`{"detail":"private-server-secret"}`,
		`{"detail":{"credential":"private-server-secret"}}`,
		`{"detail":"A complete supported AIuditor class envelope is required private-server-secret"}`,
		`{"detail":"A complete supported AIuditor class envelope is required"} trailing-private-server-secret`,
		strings.Repeat("private-server-secret", 300),
	} {
		result := certificationPlanConflict(strings.NewReader(body))
		if strings.Contains(result, "private-server-secret") || !strings.Contains(result, "prerequisites") {
			t.Fatal(result)
		}
	}
}
func TestCertificationConflictExplainsReviewedModelPins(t *testing.T) {
	result := certificationPlanConflict(strings.NewReader(`{"detail":"Configure matching reviewed model source, size, hash, runtime and context pins"}`))
	if !strings.Contains(result, "immutable download source") {
		t.Fatal(result)
	}
}
