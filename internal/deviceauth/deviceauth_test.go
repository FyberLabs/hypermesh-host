package deviceauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const deviceSecret = "private-device-code"
const accessSecret = "private-access-token"

type fixture struct {
	server       *httptest.Server
	issuer       string
	polls        atomic.Int32
	codes        atomic.Int32
	challenge    string
	verifier     string
	deviceStatus int
	discovery    func(map[string]any)
	code         func(map[string]any)
	poll         func(http.ResponseWriter, *http.Request, int)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/realm/.well-known/openid-configuration":
			if r.Method != "GET" {
				t.Error("discovery must be GET")
			}
			body := map[string]any{"issuer": f.issuer, "device_authorization_endpoint": f.server.URL + "/device", "token_endpoint": f.server.URL + "/token"}
			if f.discovery != nil {
				f.discovery(body)
			}
			_ = json.NewEncoder(w).Encode(body)
		case "/device":
			f.codes.Add(1)
			_ = r.ParseForm()
			f.challenge = r.PostForm.Get("code_challenge")
			decoded, err := base64.RawURLEncoding.DecodeString(f.challenge)
			if err != nil || len(decoded) != 32 {
				t.Error("missing S256 challenge")
			}
			want := url.Values{"client_id": {"hypermesh-host-setup"}, "scope": {"openid"}, "code_challenge": {f.challenge}, "code_challenge_method": {"S256"}}
			if r.Method != "POST" || !reflect.DeepEqual(r.PostForm, want) {
				t.Error("unexpected device grant request")
			}
			body := map[string]any{"device_code": deviceSecret, "user_code": "ABCD-EFGH", "verification_uri": f.server.URL + "/verify", "verification_uri_complete": f.server.URL + "/verify?device_code=" + deviceSecret, "expires_in": 600}
			if f.code != nil {
				f.code(body)
			}
			if f.deviceStatus != 0 {
				w.WriteHeader(f.deviceStatus)
			}
			_ = json.NewEncoder(w).Encode(body)
		case "/token":
			n := int(f.polls.Add(1))
			_ = r.ParseForm()
			verifier := r.PostForm.Get("code_verifier")
			if len(verifier) != 43 {
				t.Error("missing 256-bit verifier")
			}
			digest := sha256.Sum256([]byte(verifier))
			if base64.RawURLEncoding.EncodeToString(digest[:]) != f.challenge {
				t.Error("verifier does not match challenge")
			}
			if f.verifier != "" && f.verifier != verifier {
				t.Error("verifier changed while polling")
			}
			f.verifier = verifier
			want := url.Values{"client_id": {"hypermesh-host-setup"}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {deviceSecret}, "code_verifier": {verifier}}
			if r.Method != "POST" || !reflect.DeepEqual(r.PostForm, want) {
				t.Error("unexpected polling request")
			}
			if r.Header.Get("Authorization") != "" {
				t.Error("public client sent authorization header")
			}
			if f.poll != nil {
				f.poll(w, r, n)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"`+accessSecret+`","token_type":"Bearer","expires_in":300,"refresh_token":"private-refresh-token"}`)
		default:
			t.Error("unexpected endpoint")
			http.NotFound(w, r)
		}
	}))
	f.issuer = f.server.URL + "/realm"
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixture) run(ctx context.Context, client *http.Client) (Token, string, []time.Duration, error) {
	var output bytes.Buffer
	var delays []time.Duration
	token, err := authorize(ctx, f.issuer, &output, client, func(ctx context.Context, d time.Duration) error {
		delays = append(delays, d)
		return ctx.Err()
	})
	return token, output.String(), delays, err
}

func TestPendingSlowDownAndSuccess(t *testing.T) {
	f := newFixture(t)
	f.poll = func(w http.ResponseWriter, _ *http.Request, n int) {
		switch n {
		case 1, 3:
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"authorization_pending"}`)
		case 2:
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"slow_down"}`)
		default:
			_, _ = io.WriteString(w, `{"access_token":"`+accessSecret+`","token_type":"Bearer","expires_in":300}`)
		}
	}
	token, output, delays, err := f.run(context.Background(), f.server.Client())
	if err != nil || token.Bearer() != accessSecret {
		t.Fatalf("authorization failed: %v", err)
	}
	if !reflect.DeepEqual(delays, []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second, 10 * time.Second}) {
		t.Fatalf("poll delays: %v", delays)
	}
	if !strings.Contains(output, f.server.URL+"/verify") || !strings.Contains(output, "ABCD-EFGH") {
		t.Fatal("browser instructions missing")
	}
	for _, secret := range []string{deviceSecret, accessSecret, f.verifier, "private-refresh-token", "device_code="} {
		if strings.Contains(output, secret) {
			t.Fatal("credential displayed")
		}
	}
	raw, _ := json.Marshal(token)
	for _, value := range []string{fmt.Sprint(token), fmt.Sprintf("%+v", token), fmt.Sprintf("%#v", token), string(raw)} {
		if strings.Contains(value, accessSecret) {
			t.Fatal("token formatting leaked credential")
		}
	}
}

func TestTerminalResponsesDoNotRetryOrLeak(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		message string
	}{
		{"denied", 400, `{"error":"access_denied","error_description":"` + deviceSecret + `"}`, "denied"},
		{"expired", 400, `{"error":"expired_token"}`, "expired"},
		{"unknown", 400, `{"error":"` + deviceSecret + `"}`, "rejected"},
		{"invalid grant", 400, `{"error":"invalid_grant"}`, "rejected"},
		{"server failure", 503, `{"error":"` + deviceSecret + `"}`, "503"},
		{"wrong status", 200, `{"error":"authorization_pending"}`, "200"},
		{"empty", 200, `{}`, "200"},
		{"wrong token type", 200, `{"access_token":"secret","token_type":"Basic","expires_in":30}`, "200"},
		{"expired access", 200, `{"access_token":"secret","token_type":"Bearer","expires_in":0}`, "200"},
		{"token injection", 200, `{"access_token":"secret\nInjected","token_type":"Bearer","expires_in":30}`, "200"},
		{"duplicate member", 200, `{"access_token":"x","access_token":"y","token_type":"Bearer","expires_in":30}`, "invalid"},
		{"trailing data", 200, `{} {}`, "invalid"},
		{"null", 200, `null`, "invalid"},
		{"array", 200, `[]`, "invalid"},
		{"html", 502, "<html>" + deviceSecret + "</html>", "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.poll = func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}
			token, output, _, err := f.run(context.Background(), f.server.Client())
			if err == nil || !strings.Contains(err.Error(), tc.message) || token.Bearer() != "" || f.polls.Load() != 1 {
				t.Fatalf("unexpected outcome: %v", err)
			}
			if strings.Contains(output+err.Error(), deviceSecret) {
				t.Fatal("credential leaked")
			}
		})
	}
}

func TestUntrustedDiscoveryStopsBeforeRequestingCode(t *testing.T) {
	for _, field := range []string{"issuer", "device_authorization_endpoint", "token_endpoint"} {
		for _, bad := range []string{"http://other.example/path", "https://other.example/path", "https://user:password@other.example/path"} {
			t.Run(field+"/"+bad, func(t *testing.T) {
				f := newFixture(t)
				f.discovery = func(v map[string]any) { v[field] = bad }
				_, output, _, err := f.run(context.Background(), f.server.Client())
				if err == nil || f.codes.Load() != 0 || output != "" {
					t.Fatal("untrusted discovery accepted")
				}
			})
		}
	}
}

func TestMalformedCodesAreNeverDisplayedOrPolled(t *testing.T) {
	for _, tc := range []struct {
		field string
		value any
	}{
		{"device_code", ""}, {"device_code", strings.Repeat("x", 4097)},
		{"user_code", "AB\nCD"}, {"user_code", "\x1b[31mBAD"}, {"user_code", ""},
		{"expires_in", 0}, {"expires_in", 601}, {"expires_in", -1},
		{"interval", -1}, {"interval", 601},
		{"verification_uri", "https://other.example/verify"},
		{"verification_uri", "http://other.example/verify"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			f := newFixture(t)
			f.code = func(v map[string]any) { v[tc.field] = tc.value }
			_, output, _, err := f.run(context.Background(), f.server.Client())
			if err == nil || f.polls.Load() != 0 || output != "" {
				t.Fatal("invalid code accepted")
			}
		})
	}
}

func TestExplicitInterval(t *testing.T) {
	f := newFixture(t)
	f.code = func(v map[string]any) { v["interval"] = 7 }
	_, _, delays, err := f.run(context.Background(), f.server.Client())
	if err != nil || !reflect.DeepEqual(delays, []time.Duration{7 * time.Second}) {
		t.Fatalf("interval ignored: %v %v", delays, err)
	}
}

func TestCancellationBeforePoll(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	token, err := authorize(ctx, f.issuer, &output, f.server.Client(), func(context.Context, time.Duration) error { cancel(); return nil })
	if err == nil || token.Bearer() != "" || f.polls.Load() != 0 {
		t.Fatal("cancelled grant polled")
	}
}

func TestExpiryBoundsWaiting(t *testing.T) {
	f := newFixture(t)
	f.code = func(v map[string]any) { v["expires_in"] = 1; v["interval"] = 600 }
	started := time.Now()
	_, err := authorize(context.Background(), f.issuer, io.Discard, f.server.Client(), wait)
	if err == nil || f.polls.Load() != 0 || time.Since(started) > 5*time.Second {
		t.Fatal("expiry did not bound polling wait")
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (fn transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

type timeoutError struct{}

func (timeoutError) Error() string   { return deviceSecret }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type timeoutBody struct{}

func (timeoutBody) Read([]byte) (int, error) { return 0, timeoutError{} }
func (timeoutBody) Close() error             { return nil }

func TestTimeoutsBackOffIncludingBodyRead(t *testing.T) {
	for _, bodyTimeout := range []bool{false, true} {
		t.Run(fmt.Sprint(bodyTimeout), func(t *testing.T) {
			f := newFixture(t)
			client := f.server.Client()
			base := client.Transport
			attempts := 0
			client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/token" {
					attempts++
					if attempts <= 2 {
						if bodyTimeout {
							return &http.Response{StatusCode: 200, Body: timeoutBody{}, Header: make(http.Header)}, nil
						}
						return nil, timeoutError{}
					}
				}
				return base.RoundTrip(r)
			})
			_, _, delays, err := f.run(context.Background(), client)
			if err != nil || !reflect.DeepEqual(delays, []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}) {
				t.Fatalf("backoff: %v %v", delays, err)
			}
		})
	}
}

func TestRedirectNeverForwardsDeviceCode(t *testing.T) {
	f := newFixture(t)
	f.poll = func(w http.ResponseWriter, r *http.Request, _ int) { http.Redirect(w, r, f.server.URL+"/leak", 307) }
	client := f.server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { t.Error("caller redirect policy used"); return nil }
	_, _, _, err := f.run(context.Background(), client)
	if err == nil || !strings.Contains(err.Error(), "redirects") || f.polls.Load() != 1 {
		t.Fatal("redirect accepted")
	}
}

func TestSlowBodyBoundedByContext(t *testing.T) {
	f := newFixture(t)
	f.poll = func(w http.ResponseWriter, r *http.Request, _ int) {
		_, _ = io.WriteString(w, "{")
		w.(http.Flusher).Flush()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := io.WriteString(w, " "); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, _, err := f.run(ctx, f.server.Client())
	if err == nil || time.Since(started) > 3*time.Second {
		t.Fatal("trickling body escaped deadline")
	}
}

func TestResponseLimitAndTransportRedaction(t *testing.T) {
	f := newFixture(t)
	f.poll = func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxResponse+1))
	}
	_, _, _, err := f.run(context.Background(), f.server.Client())
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatal("large response accepted")
	}
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("https://" + deviceSecret) })}
	_, output, _, err := f.run(context.Background(), client)
	if err == nil || strings.Contains(err.Error()+output, deviceSecret) {
		t.Fatal("transport error not redacted")
	}
}

type brokenDisplay struct{}

func (brokenDisplay) Write([]byte) (int, error) { return 0, errors.New(accessSecret) }

func TestDisplayFailureDoesNotPoll(t *testing.T) {
	f := newFixture(t)
	_, err := authorize(context.Background(), f.issuer, brokenDisplay{}, f.server.Client(), wait)
	if err == nil || f.polls.Load() != 0 || strings.Contains(err.Error(), accessSecret) {
		t.Fatal("display failure mishandled")
	}
}

func TestInvalidIssuerMakesNoRequest(t *testing.T) {
	for _, issuer := range []string{"http://example.test", "https://user:pass@example.test", "https://example.test?secret=x", "https://example.test#x", "https://example.test/\x1b[31m"} {
		_, err := Authorize(context.Background(), issuer, io.Discard)
		if err == nil {
			t.Fatal("invalid issuer accepted")
		}
	}
}

func TestFreshPKCEForEachAuthorization(t *testing.T) {
	f := newFixture(t)
	_, _, _, err := f.run(context.Background(), f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	previous := f.challenge
	f.verifier = ""
	_, _, _, err = f.run(context.Background(), f.server.Client())
	if err != nil || previous == f.challenge {
		t.Fatal("proof reused", err)
	}
}

func TestDeviceRejectionHasSafeStatusAndDoesNotPoll(t *testing.T) {
	for _, category := range []string{"invalid_request", "invalid_client", "unauthorized_client", "invalid_scope", deviceSecret} {
		f := newFixture(t)
		f.deviceStatus = 400
		f.code = func(body map[string]any) {
			for key := range body {
				delete(body, key)
			}
			body["error"] = category
			body["error_description"] = accessSecret
		}
		_, output, _, err := f.run(context.Background(), f.server.Client())
		if err == nil || !strings.Contains(err.Error(), "HTTP 400") || f.polls.Load() != 0 || output != "" {
			t.Fatal("invalid rejection", err)
		}
		if strings.Contains(err.Error(), deviceSecret) || strings.Contains(err.Error(), accessSecret) {
			t.Fatal("server error leaked")
		}
	}
}
