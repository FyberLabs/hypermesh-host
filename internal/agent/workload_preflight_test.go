package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalWorkloadPreflightUsesApprovedFileWithoutEnrollment(t *testing.T){
	state:=t.TempDir()
	model,hash:=writeTestGGUF(t,state)
	r:=WorkloadPreflight{PlanSHA256:strings.Repeat("a",64),CatalogID:"reviewed-model",
		ImageDigest:certTestPin,ModelPath:model,FileHash:hash,ContextTokens:2048}
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,req *http.Request){
		var body struct{Model string `json:"model"`}
		if json.NewDecoder(req.Body).Decode(&body)!=nil||body.Model!=r.CatalogID{t.Error("wrong model alias")}
		time.Sleep(3*time.Millisecond)
		writeJSON(w,200,map[string]any{"choices":[]map[string]any{{"message":map[string]string{"content":"ready"}}},"usage":map[string]int{"completion_tokens":4}})
	}));defer server.Close()
	var calls []string
	run:=func(_ context.Context,_ string,args ...string)([]byte,error){
		calls=append(calls,strings.Join(args," "))
		return []byte("local-preflight"),nil
	}
	receipt,err:=runLocalWorkloadPreflight(context.Background(),r,state,run,server.URL)
	if err!=nil{t.Fatal(err)}
	if receipt.Request!=r||!receipt.CleanupComplete||receipt.SampleMS<=0{t.Fatalf("%+v",receipt)}
	joined:=strings.Join(calls,"\n")
	for _,part:=range []string{r.ImageDigest,model,"127.0.0.1:18080:8080","LLAMA_ARG_CTX_SIZE=2048","stop local-preflight"}{
		if !strings.Contains(joined,part){t.Fatalf("missing %q in %s",part,joined)}
	}
	entries,err:=os.ReadDir(state);if err!=nil{t.Fatal(err)}
	if len(entries)!=1||entries[0].Name()!="model.gguf"{t.Fatal("local check changed enrollment/state")}
}

func TestLocalWorkloadPreflightRefusesAnyLeaseState(t *testing.T){
	for _,contents:=range []string{"broken",`{"lease_id":"active"}`,"null"}{
		state:=t.TempDir();model,hash:=writeTestGGUF(t,state)
		if err:=os.WriteFile(filepath.Join(state,leaseFile),[]byte(contents),0600);err!=nil{t.Fatal(err)}
		r:=WorkloadPreflight{PlanSHA256:strings.Repeat("a",64),CatalogID:"reviewed-model",
			ImageDigest:certTestPin,ModelPath:model,FileHash:hash,ContextTokens:4096}
		run:=func(context.Context,string,...string)([]byte,error){t.Fatal("Docker called with lease state");return nil,nil}
		if _,err:=RunWorkloadPreflight(context.Background(),r,state,run);err==nil{t.Fatal("lease state ignored")}
	}
}

func TestLocalFullContextFailureStillCleans(t *testing.T){
	for _,pass:=range []bool{true,false}{
		state:=t.TempDir();model,hash:=writeTestGGUF(t,state)
		r:=WorkloadPreflight{PlanSHA256:strings.Repeat("a",64),CatalogID:"reviewed-model",
			ImageDigest:certTestPin,ModelPath:model,FileHash:hash,ContextTokens:512,FullContext:true}
		server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,req *http.Request){
			switch req.URL.Path{
			case "/tokenize":writeJSON(w,200,map[string]any{"tokens":[]int{123}})
			case "/completion":
				time.Sleep(2*time.Millisecond)
				writeJSON(w,200,map[string]any{"model":"/models/model.gguf","tokens_evaluated":496,"tokens_predicted":16,
					"truncated":!pass,"stop":true,"stop_type":"limit","generation_settings":map[string]int{"n_ctx":512}})
			default:
				time.Sleep(2*time.Millisecond)
				writeJSON(w,200,map[string]any{"choices":[]map[string]any{{"message":map[string]string{"content":"ready"}}},"usage":map[string]int{"completion_tokens":4}})
			}
		}))
		var calls []string
		run:=func(_ context.Context,_ string,args ...string)([]byte,error){calls=append(calls,strings.Join(args," "));return []byte("context-container"),nil}
		receipt,err:=runLocalWorkloadPreflight(context.Background(),r,state,run,server.URL)
		server.Close()
		if (err==nil)!=pass{t.Fatalf("pass=%v receipt=%+v err=%v",pass,receipt,err)}
		if pass&&(receipt.ContextFit==nil||!receipt.ContextFit.ValidFor(512)){t.Fatal("context evidence missing")}
		joined:=strings.Join(calls,"\n")
		if !strings.Contains(joined,"LLAMA_ARG_N_PARALLEL=1")||!strings.Contains(joined,"stop context-container")||!strings.Contains(joined,"rm hypermesh-"){t.Fatalf("missing single-slot setup/cleanup: %s",joined)}
	}
}
