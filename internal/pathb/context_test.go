package pathb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFullContextEvidence(t *testing.T) {
	for _, kind := range []string{"pass", "boundary", "truncated", "missing-context", "multiple-slots", "short-prompt", "short-generation", "wrong-context", "native-model-path", "missing-truncation", "missing-stop", "early-stop", "invalid-tokens", "missing-tokenizer", "oversized", "malformed", "http-error"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/props" {
					n := 512
					slots := 1
					if kind == "wrong-context" {
						n = 1024
					}
					if kind == "multiple-slots" {
						slots = 2
					}
					if kind == "missing-context" {
						_, _ = w.Write([]byte(`{}`))
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"default_generation_settings": map[string]int{"n_ctx": n}, "total_slots": slots})
					return
				}
				if r.URL.Path == "/tokenize" {
					if kind == "missing-tokenizer" {
						w.WriteHeader(404)
						return
					}
					if kind == "invalid-tokens" {
						_, _ = w.Write([]byte(`{"tokens":[-1]}`))
						return
					}
					_, _ = w.Write([]byte(`{"tokens":[123,456]}`))
					return
				}
				if r.URL.Path != "/completion" {
					t.Error("wrong endpoint")
					w.WriteHeader(404)
					return
				}
				var req struct {
					Prompt    []int `json:"prompt"`
					Predict   int   `json:"n_predict"`
					Cache     bool  `json:"cache_prompt"`
					IgnoreEOS bool  `json:"ignore_eos"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Prompt) != 496 || req.Predict != 16 || req.Cache || !req.IgnoreEOS {
					t.Error("wrong token budget or cache/EOS policy")
				}
				for i, id := range req.Prompt {
					if id != []int{123, 456}[i%2] {
						t.Error("token IDs changed")
						break
					}
				}
				if kind == "oversized" {
					_, _ = w.Write([]byte(strings.Repeat(" ", (4<<20)+1)))
					return
				}
				if kind == "malformed" {
					_, _ = w.Write([]byte("PRIVATE_BODY"))
					return
				}
				if kind == "http-error" {
					w.WriteHeader(503)
					return
				}
				response := map[string]any{"model": "fixture", "tokens_evaluated": 496, "tokens_predicted": 16,
					"truncated": false, "stop": true, "stop_type": "limit", "generation_settings": map[string]int{"n_ctx": 512}}
				switch kind {
				case "boundary":
					response["truncated"] = true
					delete(response, "generation_settings")
				case "truncated":
					response["truncated"] = true
					response["tokens_predicted"] = 15
				case "short-prompt":
					response["tokens_evaluated"] = 100
				case "short-generation":
					response["tokens_predicted"] = 1
				case "wrong-context":
					response["generation_settings"] = map[string]int{"n_ctx": 1024}
				case "wrong-model":
					response["model"] = "/models/approved.gguf"
				case "missing-truncation":
					delete(response, "truncated")
				case "missing-stop":
					delete(response, "stop")
				case "early-stop":
					response["stop_type"] = "eos"
				}
				time.Sleep(2 * time.Millisecond)
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			fit, err := (Runner{Endpoint: server.URL + "/v1/chat/completions", Model: "fixture"}).CheckContextFit(context.Background(), 512)
			if kind == "pass" || kind == "native-model-path" || kind == "boundary" {
				if err != nil || fit == nil || !fit.ValidFor(512) {
					t.Fatalf("%+v %v", fit, err)
				}
			} else {
				if err == nil || fit != nil {
					t.Fatalf("accepted %s: %+v", kind, fit)
				}
				if strings.Contains(err.Error(), "PRIVATE") {
					t.Fatal("raw response leaked")
				}
			}
		})
	}
}

func TestFullContextCancellationBoundsTransfer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if fit, err := (Runner{Endpoint: server.URL, Model: "fixture"}).CheckContextFit(ctx, 512); err == nil || fit != nil {
		t.Fatal("cancelled transfer accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("whole transfer deadline not honored")
	}
}

func TestFullContextRejectsRedirectAndRemoteEndpoints(t *testing.T) {
	for _, endpoint := range []string{"https://example.com", "http://example.com", "http://127.0.0.1/?secret=x", "http://user@127.0.0.1"} {
		if _, err := (Runner{Endpoint: endpoint, Model: "fixture"}).CheckContextFit(context.Background(), 512); err == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid", http.StatusFound)
	}))
	defer server.Close()
	if _, err := (Runner{Endpoint: server.URL, Model: "fixture"}).CheckContextFit(context.Background(), 512); err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestAGXExactContextBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			if r.Method != "GET" {
				t.Error("metadata must use GET")
			}
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":16384},"total_slots":1}`))
		case "/tokenize":
			_, _ = w.Write([]byte(`{"tokens":[123,456]}`))
		case "/completion":
			var body struct {
				Prompt []int `json:"prompt"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if len(body.Prompt) != 16368 {
				t.Fatal("wrong prompt budget")
			}
			time.Sleep(2 * time.Millisecond)
			_, _ = w.Write([]byte(`{"tokens_evaluated":16368,"tokens_predicted":16,"truncated":true,"stop":true,"stop_type":"limit","generation_settings":{"n_predict":16}}`))
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	fit, err := (Runner{Endpoint: server.URL, Model: "llama-3.1-8b-q4"}).CheckContextFit(context.Background(), 16384)
	if err != nil || fit == nil || !fit.ValidFor(16384) {
		t.Fatal(fit, err)
	}
}

func TestFullContextVerifiedBridgeEndpoint(t *testing.T) {
	const verified = "http://172.18.0.2:8080/v1/chat/completions"
	for _, tc := range []struct {
		name, endpoint, trusted string
		pass                    bool
	}{
		{"inspected", verified, verified, true},
		{"unverified", verified, "", false},
		{"different-host", "http://172.18.0.3:8080/v1/chat/completions", verified, false},
		{"different-port", "http://172.18.0.2:9999/v1/chat/completions", verified, false},
		{"public", "http://8.8.8.8:8080", "http://8.8.8.8:8080", false},
		{"hostname", "http://workload:8080", "http://workload:8080", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			transport := contextBridgeTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host != "172.18.0.2:8080" {
					t.Fatal("unexpected host")
				}
				body := ""
				switch req.URL.Path {
				case "/props":
					body = `{"default_generation_settings":{"n_ctx":512},"total_slots":1}`
				case "/tokenize":
					body = `{"tokens":[123]}`
				case "/completion":
					time.Sleep(2 * time.Millisecond)
					body = `{"tokens_evaluated":496,"tokens_predicted":16,"truncated":true,"stop":true,"stop_type":"limit"}`
				default:
					t.Fatal("unexpected path")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			runner := Runner{Endpoint: tc.endpoint, VerifiedContainerEndpoint: tc.trusted, Model: "fixture", HTTP: &http.Client{Transport: transport}}
			fit, err := runner.CheckContextFit(context.Background(), 512)
			if tc.pass {
				if err != nil || fit == nil || !fit.ValidFor(512) || calls != 3 {
					t.Fatalf("%+v %v calls=%d", fit, err, calls)
				}
			} else if err == nil || fit != nil || calls != 0 {
				t.Fatalf("unauthorized endpoint reached: %v calls=%d", err, calls)
			}
		})
	}
}

type contextBridgeTransport func(*http.Request) (*http.Response, error)

func (f contextBridgeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
