// Package deviceauth implements the public hypermesh-native device grant.
// It authenticates a person; it does not approve hardware or register a host.
package deviceauth

import (
	"bytes"
	"context"
 "crypto/rand"
 "crypto/sha256"
 "encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	clientID = "hypermesh-native"
	maxResponse = 1 << 20
	requestTimeout = 15 * time.Second
	flowTimeout = 10 * time.Minute
)

// Token stays in memory until the caller exchanges it for scoped registration
// authority. Do not persist Bearer() or include it in logs or command arguments.
type Token struct { bearer string }

func (t Token) Bearer() string { return t.bearer }
func (Token) String() string { return "[redacted device authorization token]" }
func (Token) GoString() string { return "[redacted device authorization token]" }
func (Token) MarshalJSON() ([]byte, error) { return []byte(`"[redacted device authorization token]"`), nil }

// Authorize displays only the verification URL and user code. The issuer must
// come from trusted environment configuration, not a discovered host response.
// Approval authenticates the user; callers must separately authorize the
// pending installation, tenant and organization before creating a device.
func Authorize(ctx context.Context, issuer string, display io.Writer) (Token, error) {
	return authorize(ctx, issuer, display, &http.Client{}, wait)
}

var errTimeout = errors.New("device authorization request timed out")

func endpoint(address string) bool {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	for _, r := range address {
		if r < 33 || r > 126 { return false }
	}
	return true
}

func sameOrigin(issuer, address string) bool {
	if !endpoint(issuer) || !endpoint(address) { return false }
	a, _ := url.Parse(issuer)
	b, _ := url.Parse(address)
	return strings.EqualFold(a.Host, b.Host)
}

// request never returns server bodies, URLs, tokens or transport error text.
// Its deadline covers headers AND the entire body, including trickle responses.
func request(ctx context.Context, client *http.Client, method, address string, body []byte, dest any) (int, error) {
	if !endpoint(address) { return 0, errors.New("invalid HTTPS authorization endpoint") }
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(body))
	if err != nil { return 0, errors.New("cannot prepare authorization request") }
	req.Header.Set("Accept", "application/json")
	if body != nil { req.Header.Set("Content-Type", "application/x-www-form-urlencoded") }
	// Never follow even same-origin redirects: the device code is a credential.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := copyClient.Do(req)
	if err != nil { return 0, transportError(err) }
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp.StatusCode, errors.New("authorization redirects are refused")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil { return resp.StatusCode, transportError(err) }
	if len(raw) > maxResponse { return resp.StatusCode, errors.New("authorization response exceeds size limit") }
	if err := decodeObject(raw, dest); err != nil {
		return resp.StatusCode, errors.New("invalid authorization response")
	}
	return resp.StatusCode, nil
}

func transportError(err error) error {
	var n net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &n) && n.Timeout()) { return errTimeout }
	if errors.Is(err, context.Canceled) { return errors.New("device authorization cancelled") }
	return errors.New("authorization transport failed; check TLS and connectivity")
}

// Unknown fields are allowed for OIDC extensions, but duplicate JSON members,
// arrays, null and trailing data are not accepted.
func decodeObject(raw []byte, dest any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') { return errors.New("expected object") }
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil { return err }
		name, ok := key.(string)
		if !ok || seen[name] { return errors.New("duplicate member") }
		seen[name] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil { return err }
	}
	if _, err := d.Token(); err != nil { return err }
	if _, err := d.Token(); err != io.EOF { return errors.New("trailing data") }
	return json.Unmarshal(raw, dest)
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done(): return errors.New("device authorization cancelled or expired")
	case <-timer.C: return nil
	}
}

type discovery struct {
	Issuer string `json:"issuer"`
	DeviceEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint string `json:"token_endpoint"`
}
type deviceCode struct {
 Error string `json:"error"`
	DeviceCode string `json:"device_code"`
	UserCode string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn int `json:"expires_in"`
	Interval int `json:"interval"`
}
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType string `json:"token_type"`
	ExpiresIn int `json:"expires_in"`
	Error string `json:"error"`
}

func validToken(t tokenResponse) bool {
	if t.Error != "" || !strings.EqualFold(t.TokenType, "Bearer") || t.ExpiresIn <= 0 ||
		len(t.AccessToken) == 0 || len(t.AccessToken) > 64<<10 { return false }
	for _, r := range t.AccessToken {
		if r < 33 || r > 126 { return false }
	}
	return true
}

func authorize(ctx context.Context, issuer string, display io.Writer, client *http.Client, pause func(context.Context, time.Duration) error) (Token, error) {
	empty := Token{}
	if !endpoint(issuer) || display == nil { return empty, errors.New("require a trusted HTTPS issuer and display") }
	ctx, cancel := context.WithTimeout(ctx, flowTimeout)
	defer cancel()
	var metadata discovery
	status, err := request(ctx, client, "GET", strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", nil, &metadata)
	if err != nil { return empty, err }
	if status != 200 || metadata.Issuer != issuer || !sameOrigin(issuer, metadata.DeviceEndpoint) || !sameOrigin(issuer, metadata.TokenEndpoint) {
		return empty, errors.New("discovery does not match the approved issuer and origin")
	}
	// Bind device authorization and polling with a fresh, memory-only S256 verifier.
 var random [32]byte
 if _, err := rand.Read(random[:]); err != nil { return empty, errors.New("cannot generate device authorization proof") }
 verifier := base64.RawURLEncoding.EncodeToString(random[:])
 digest := sha256.Sum256([]byte(verifier))
 challenge := base64.RawURLEncoding.EncodeToString(digest[:])
 form := url.Values{"client_id": {clientID}, "scope": {"openid"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}.Encode()
	var code deviceCode
	// Conservatively include transfer time in the server's code lifetime.
	issued := time.Now()
	status, err = request(ctx, client, "POST", metadata.DeviceEndpoint, []byte(form), &code)
	if err != nil { return empty, err }
	if status != 200 {
  category := "request rejected"
  switch code.Error {
  case "invalid_request": category = "invalid_request; check native-client PKCE and device-grant policy"
  case "invalid_client", "unauthorized_client": category = "native client unavailable or device grant disabled"
  case "invalid_scope": category = "requested scope is not allowed"
  }
  return empty, fmt.Errorf("device authorization request rejected (HTTP %d; %s)", status, category)
 }
 if code.DeviceCode == "" || len(code.DeviceCode) > 4096 ||
		code.ExpiresIn <= 0 || code.ExpiresIn > 600 || code.Interval < 0 || code.Interval > 600 ||
		!sameOrigin(issuer, code.VerificationURI) || len(code.UserCode) == 0 || len(code.UserCode) > 64 {
		return empty, errors.New("invalid device authorization response")
	}
	for _, r := range code.UserCode {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return empty, errors.New("invalid device authorization user code")
		}
	}
	ctx, expire := context.WithDeadline(ctx, issued.Add(time.Duration(code.ExpiresIn)*time.Second))
	defer expire()
	if ctx.Err() != nil { return empty, errors.New("device authorization expired") }
	if _, err := fmt.Fprintf(display, "Open %s in your browser and enter code %s. Approve only the sign-in you initiated.\n", code.VerificationURI, code.UserCode); err != nil {
		return empty, errors.New("cannot display browser authorization instructions")
	}
	interval := time.Duration(code.Interval)*time.Second
	if interval == 0 { interval = 5*time.Second }
	form = url.Values{"client_id": {clientID}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {code.DeviceCode}, "code_verifier": {verifier}}.Encode()
	for {
		if ctx.Err() != nil { return empty, errors.New("device authorization cancelled or expired") }
		if err := pause(ctx, interval); err != nil { return empty, errors.New("device authorization cancelled or expired") }
		if ctx.Err() != nil { return empty, errors.New("device authorization cancelled or expired") }
		var token tokenResponse
		status, err = request(ctx, client, "POST", metadata.TokenEndpoint, []byte(form), &token)
		if ctx.Err() != nil { return empty, errors.New("device authorization cancelled or expired") }
		if errors.Is(err, errTimeout) {
			interval *= 2
			if interval > flowTimeout { interval = flowTimeout }
			continue
		}
		if err != nil { return empty, err }
		if status == 200 && validToken(token) { return Token{bearer: token.AccessToken}, nil }
		if status != 400 { return empty, fmt.Errorf("authorization token request rejected (HTTP %d)", status) }
		switch token.Error {
		case "authorization_pending":
		case "slow_down":
			interval += 5*time.Second
			if interval > flowTimeout { interval = flowTimeout }
		case "access_denied": return empty, errors.New("browser authorization denied")
		case "expired_token": return empty, errors.New("browser authorization expired; start again for a new code")
		default: return empty, errors.New("browser authorization rejected")
		}
	}
}
