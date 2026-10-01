package pathb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFullContextEvidence(t *testing.T){
	for _,kind:=range []string{"pass","truncated","short-prompt","short-generation","wrong-context","wrong-model","missing-truncation","missing-stop","early-stop","invalid-tokens","missing-tokenizer","oversized","malformed","http-error"}{
		t.Run(kind,func(t *testing.T){
			server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
				if r.URL.Path=="/tokenize"{
					if kind=="missing-tokenizer"{w.WriteHeader(404);return}
					if kind=="invalid-tokens"{_,_=w.Write([]byte(`{"tokens":[-1]}`));return}
					_,_=w.Write([]byte(`{"tokens":[123,456]}`));return
				}
				if r.URL.Path!="/completion"{t.Error("wrong endpoint");w.WriteHeader(404);return}
				var req struct{Prompt []int `json:"prompt"`;Predict int `json:"n_predict"`;Cache bool `json:"cache_prompt"`;IgnoreEOS bool `json:"ignore_eos"`}
				if json.NewDecoder(r.Body).Decode(&req)!=nil||len(req.Prompt)!=496||req.Predict!=16||req.Cache||!req.IgnoreEOS{t.Error("wrong token budget or cache/EOS policy")}
				for i,id:=range req.Prompt{if id!=[]int{123,456}[i%2]{t.Error("token IDs changed");break}}
				if kind=="oversized"{_,_=w.Write([]byte(strings.Repeat(" ",(4<<20)+1)));return}
				if kind=="malformed"{_,_=w.Write([]byte("PRIVATE_BODY"));return}
				if kind=="http-error"{w.WriteHeader(503);return}
				response:=map[string]any{"model":"fixture","tokens_evaluated":496,"tokens_predicted":16,
					"truncated":false,"stop":true,"stop_type":"limit","generation_settings":map[string]int{"n_ctx":512}}
				switch kind{
				case "truncated":response["truncated"]=true
				case "short-prompt":response["tokens_evaluated"]=100
				case "short-generation":response["tokens_predicted"]=1
				case "wrong-context":response["generation_settings"]=map[string]int{"n_ctx":1024}
				case "wrong-model":response["model"]="another"
				case "missing-truncation":delete(response,"truncated")
				case "missing-stop":delete(response,"stop")
				case "early-stop":response["stop_type"]="eos"
				}
				time.Sleep(2*time.Millisecond)
				_ = json.NewEncoder(w).Encode(response)
			}));defer server.Close()
			fit,err:=(Runner{Endpoint:server.URL+"/v1/chat/completions",Model:"fixture"}).CheckContextFit(context.Background(),512)
			if kind=="pass"{if err!=nil||fit==nil||!fit.ValidFor(512){t.Fatalf("%+v %v",fit,err)}}else{
				if err==nil||fit!=nil{t.Fatalf("accepted %s: %+v",kind,fit)}
				if strings.Contains(err.Error(),"PRIVATE"){t.Fatal("raw response leaked")}
			}
		})
	}
}

func TestFullContextCancellationBoundsTransfer(t *testing.T){
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		_,_=w.Write([]byte("{"));w.(http.Flusher).Flush()
		<-r.Context().Done()
	}));defer server.Close()
	ctx,cancel:=context.WithTimeout(context.Background(),30*time.Millisecond);defer cancel()
	start:=time.Now()
	if fit,err:=(Runner{Endpoint:server.URL,Model:"fixture"}).CheckContextFit(ctx,512);err==nil||fit!=nil{t.Fatal("cancelled transfer accepted")}
	if time.Since(start)>time.Second{t.Fatal("whole transfer deadline not honored")}
}

func TestFullContextRejectsRedirectAndRemoteEndpoints(t *testing.T){
	for _,endpoint:=range []string{"https://example.com","http://example.com","http://127.0.0.1/?secret=x","http://user@127.0.0.1"}{
		if _,err:=(Runner{Endpoint:endpoint,Model:"fixture"}).CheckContextFit(context.Background(),512);err==nil{t.Fatal("unsafe endpoint accepted")}
	}
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){http.Redirect(w,r,"http://example.invalid",http.StatusFound)}));defer server.Close()
	if _,err:=(Runner{Endpoint:server.URL,Model:"fixture"}).CheckContextFit(context.Background(),512);err==nil{t.Fatal("redirect accepted")}
}
