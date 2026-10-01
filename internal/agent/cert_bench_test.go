package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

const certTestPin = "example.invalid/selected-runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func certFixture(t *testing.T, status int) (*Agent, api.Job, *[][]string) {
	t.Helper()
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0600); err != nil { t.Fatal(err) }
	a := newTestAgent(t, "http://127.0.0.1:1", dir, uptime, "", "")
	a.state.DeviceID = testDevice
	a.client.DeviceSecret = testSecret
	gguf, hash := writeTestGGUF(t, dir)
	a.cfg.GGUFPath = gguf
	job := api.Job{JobID:testJob, Kind:api.KindCompactCert, DeviceID:testDevice,
		SuiteID:"pack-v1", CatalogID:"different-catalog", ImageDigest:certTestPin,
		FileHash:hash, Runtime:"nvidia", Backend:"cuda-jetson"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct { Model string `json:"model"` }
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != job.CatalogID {
			t.Error("probe did not use selected catalog alias")
		}
		if status != http.StatusOK { w.WriteHeader(status); return }
		time.Sleep(3*time.Millisecond)
		writeJSON(w, 200, map[string]any{
			"choices": []map[string]any{{"message":map[string]string{"content":"ready"}}},
			"usage":map[string]int{"completion_tokens":4},
		})
	}))
	t.Cleanup(server.Close)
	a.certEndpoint = server.URL
	a.pathb.Endpoint = "http://unrelated.invalid/v1/chat/completions"
	a.certReadyWait = 20*time.Millisecond
	a.inspectCatalog = func(context.Context,string)string { t.Error("mutable image lookup"); return "" }
	calls := new([][]string)
	a.runtime = &runtime.NVIDIA{Run:func(_ context.Context,_ string,args ...string)([]byte,error) {
		*calls = append(*calls, append([]string{},args...))
		return []byte("cert-container"),nil
	}}
	return a,job,calls
}

func TestCertRunsOnlyRequestedPinsAndCleansBeforeReturning(t *testing.T) {
	for _,kind := range []api.JobKind{api.KindCompactCert,api.KindPathB} {
		t.Run(string(kind),func(t *testing.T) {
			a,job,calls := certFixture(t,200)
			job.Kind=kind
			got:=a.runJob(context.Background(),job)
			if !got.Passed || !got.Measured || !got.WorkloadVerified || got.DecodeTokS<=0 {t.Fatalf("%+v",got)}
			if a.localLease()!=nil {t.Fatal("cert became a lease")}
			joined:=""
			for _,args:=range *calls {joined+=strings.Join(args," ")+"\n"}
			for _,part:=range []string{"pull "+certTestPin,"127.0.0.1:8080:8080",job.CatalogID,"stop cert-container","rm hypermesh-cert-bench-"} {
				if !strings.Contains(joined,part){t.Fatalf("missing %q in %s",part,joined)}
			}
			if got.ImageHash==certTestPin || got.ImageHash==job.FileHash {t.Fatal("OS image hash changed meaning")}
		})
	}
}

func TestCertRejectsUnpinnedOrBusyWorkBeforeDocker(t *testing.T) {
	cases:=[]struct{name string; change func(*Agent,*api.Job)}{
		{"missing image",func(a *Agent,j *api.Job){j.ImageDigest=""}},
		{"tag",func(a *Agent,j *api.Job){j.ImageDigest="example.invalid/model:latest"}},
		{"truncated digest",func(a *Agent,j *api.Job){j.ImageDigest="example.invalid/model@sha256:a"}},
		{"missing model pin",func(a *Agent,j *api.Job){j.FileHash=""}},
		{"wrong model",func(a *Agent,j *api.Job){j.FileHash="sha256:"+strings.Repeat("b",64)}},
		{"missing catalog",func(a *Agent,j *api.Job){j.CatalogID=""}},
		{"unknown suite",func(a *Agent,j *api.Job){j.SuiteID="unknown"}},
		{"missing job",func(a *Agent,j *api.Job){j.JobID=""}},
		{"missing runtime",func(a *Agent,j *api.Job){j.Runtime=""}},
		{"unsupported backend",func(a *Agent,j *api.Job){j.Backend="llama-cpp-vulkan"}},
		{"live lease",func(a *Agent,j *api.Job){a.setLease(&runtime.Handle{LeaseID:"busy"})}},
		{"missing file",func(a *Agent,j *api.Job){a.cfg.GGUFPath=filepath.Join(a.cfg.StateDir,"absent")}},
	}
	for _,tc:=range cases {t.Run(tc.name,func(t *testing.T){
		a,job,calls:=certFixture(t,200);tc.change(a,&job)
		got:=a.runJob(context.Background(),job)
		if got.Passed||got.Measured||got.WorkloadVerified||len(*calls)!=0{t.Fatalf("got %+v calls=%v",got,*calls)}
	})}
}

func TestCertFailedReadinessStillCleansAndCannotScore(t *testing.T) {
	a,job,calls:=certFixture(t,503)
	got:=a.runJob(context.Background(),job)
	if got.Passed||got.WorkloadVerified||got.Measured{t.Fatalf("%+v",got)}
	if last:=(*calls)[len(*calls)-1];last[0]!="rm"{t.Fatalf("cleanup missing: %v",*calls)}
}

func TestCertCleanupFailureInvalidatesMeasuredSuccess(t *testing.T) {
	a,job,_:=certFixture(t,200)
	base:=a.runtime.Run
	a.runtime.Run=func(ctx context.Context,name string,args ...string)([]byte,error){
		if args[0]=="stop"{return nil,errors.New("stop failed")}
		return base(ctx,name,args...)
	}
	got:=a.runJob(context.Background(),job)
	if got.Passed||got.Measured||got.WorkloadVerified||!strings.Contains(got.Reason,"cleanup failed"){t.Fatalf("%+v",got)}
}

func TestCertModelMutationInvalidatesSample(t *testing.T) {
	a,job,_:=certFixture(t,200)
	base:=a.runtime.Run
	a.runtime.Run=func(ctx context.Context,name string,args ...string)([]byte,error){
		if args[0]=="run"{if err:=os.WriteFile(a.cfg.GGUFPath,[]byte("changed"),0600);err!=nil{t.Fatal(err)}}
		return base(ctx,name,args...)
	}
	got:=a.runJob(context.Background(),job)
	if got.Passed||got.Measured||got.WorkloadVerified{t.Fatalf("%+v",got)}
}

func TestCertScoresOnlyExecutedCatalogAndRequiresAcceptance(t *testing.T) {
	for _,status:=range []int{200,503} {t.Run(http.StatusText(status),func(t *testing.T){
		a,job,calls:=certFixture(t,200)
		var mu sync.Mutex
		var scores []api.ScorecardReport
		var passed bool
		mux:=http.NewServeMux()
		mux.HandleFunc(api.PathJobs,func(w http.ResponseWriter,r *http.Request){writeJSON(w,200,job)})
		mux.HandleFunc(api.PathScorecard,func(w http.ResponseWriter,r *http.Request){
			var score api.ScorecardReport
			if err:=json.NewDecoder(r.Body).Decode(&score);err!=nil{t.Error(err)}
			mu.Lock();scores=append(scores,score);mu.Unlock()
			w.WriteHeader(status)
		})
		mux.HandleFunc("/api/v1/hypermesh/agent/jobs/"+testJob+"/result",func(w http.ResponseWriter,r *http.Request){
			var body api.JobResultRequest
			if err:=json.NewDecoder(r.Body).Decode(&body);err!=nil{t.Error(err)}
			mu.Lock();passed=body.Passed;mu.Unlock()
			w.WriteHeader(200)
		})
		server:=httptest.NewServer(mux);defer server.Close()
		a.client.BaseURL=server.URL
		if err:=a.pullAndRun(context.Background());err!=nil{t.Fatal(err)}
		mu.Lock();defer mu.Unlock()
		if len(scores)!=1||scores[0].CatalogID!=job.CatalogID||scores[0].ImageDigest!=certTestPin||scores[0].SampleMs<1 {t.Fatalf("scores %+v",scores)}
		if passed!=(status==200){t.Fatalf("passed=%v status=%d",passed,status)}
		if (*calls)[len(*calls)-1][0]!="rm"{t.Fatal("cleanup incomplete")}
	})}
}

func TestCertHashHonorsCancellation(t *testing.T) {
	a,_,_:=certFixture(t,200)
	ctx,cancel:=context.WithCancel(context.Background());cancel()
	if _,err:=certFileHash(ctx,a.cfg.GGUFPath);err==nil{t.Fatal("cancelled hash accepted")}
}
