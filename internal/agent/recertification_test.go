package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/api"
)

func TestRecertificationBindsMeasuredSoftware(t *testing.T) {
	for _, tc := range []struct {
		name   string
		drift  bool
		status int
	}{
		{"unchanged", false, 200}, {"changed-during-run", true, 200}, {"failed-workload", false, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drift := tc.drift
			a, job, _ := certFixture(t, tc.status)
			file := filepath.Join(t.TempDir(), "toolkit")
			if err := os.WriteFile(file, []byte("13.2.1"), 0600); err != nil {
				t.Fatal(err)
			}
			a.telem.EnvironmentFiles = []string{file}
			snap, err := a.telem.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			job.EnvironmentHash = snap.EnvironmentHash
			base := a.runtime.Run
			a.runtime.Run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
				if drift && len(args) > 0 && args[0] == "run" {
					if err := os.WriteFile(file, []byte("13.2.2"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return base(ctx, bin, args...)
			}
			scores := []api.ScorecardReport{}
			var result api.JobResultRequest
			mux := http.NewServeMux()
			mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, job) })
			mux.HandleFunc(api.PathScorecard, func(w http.ResponseWriter, r *http.Request) {
				var score api.ScorecardReport
				if err := json.NewDecoder(r.Body).Decode(&score); err != nil {
					t.Error(err)
				}
				scores = append(scores, score)
				w.WriteHeader(200)
			})
			mux.HandleFunc("/api/v1/hypermesh/agent/jobs/"+testJob+"/result", func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
					t.Error(err)
				}
				w.WriteHeader(200)
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			a.client.BaseURL = server.URL
			if err := a.pullAndRun(context.Background()); err != nil {
				t.Fatal(err)
			}
			if drift {
				if result.Passed || len(scores) != 0 {
					t.Fatalf("changed stack was accepted: %+v %+v", result, scores)
				}
			} else if tc.status != 200 {
				if result.Passed || result.EnvironmentHash != job.EnvironmentHash || len(scores) != 0 {
					t.Fatalf("failure lost its environment: %+v %+v", result, scores)
				}
			} else {
				if !result.Passed || result.EnvironmentHash != job.EnvironmentHash || len(scores) != 1 {
					t.Fatalf("missing evidence: %+v %+v", result, scores)
				}
				if scores[0].EnvironmentHash != job.EnvironmentHash || scores[0].CertificationJobID != job.JobID {
					t.Fatalf("unbound measurement %+v", scores[0])
				}
			}
		})
	}
}
