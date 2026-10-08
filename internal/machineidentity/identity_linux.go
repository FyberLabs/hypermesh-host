//go:build linux

// Package machineidentity manages a host signing key independently of human OAuth.
package machineidentity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const DefaultPath = "/var/lib/hypermesh/machine-identity.json"

var errUnauthorized = errors.New("machine identity proof rejected")

type Provider struct {
	Authority string
	Path      string
	HTTP      *http.Client
	Now       func() time.Time
}
type state struct {
	Schema    int    `json:"schema"`
	Renewal   string `json:"renewal_url"`
	Authority string `json:"authority_id"`
	Plane     string `json:"control_plane_id"`
	Base      string `json:"base"`
	Tenant    string `json:"tenant"`
	Device    string `json:"device"`
	Request   string `json:"request"`
	Key       string `json:"key"`
	Pending   string `json:"pending,omitempty"`
	Bound     bool   `json:"bound"`
	Created   int64  `json:"created"`
	Token     string `json:"token,omitempty"`
	Expires   int64  `json:"expires,omitempty"`
}

func (state) String() string   { return "[private machine identity]" }
func (state) GoString() string { return "[private machine identity]" }
func newKey() (string, error) {
	_, key, e := ed25519.GenerateKey(rand.Reader)
	return base64.StdEncoding.EncodeToString(key), e
}
func private(value string) (ed25519.PrivateKey, error) {
	raw, e := base64.StdEncoding.DecodeString(value)
	if e != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid saved machine key")
	}
	key := ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])
	if !bytes.Equal(key, raw) {
		return nil, fmt.Errorf("inconsistent saved machine key")
	}
	return key, nil
}
func public(value string) string {
	key, _ := private(value)
	if key == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}

type proof struct {
	Protocol  int    `json:"protocol_version"`
	Authority string `json:"authority_id"`
	Plane     string `json:"control_plane_id"`
	Request   string `json:"request_id"`
	Device    string `json:"device_id"`
	Nonce     string `json:"nonce"`
	Public    string `json:"public_key"`
	Issued    int64  `json:"issued_at"`
	Signature string `json:"signature"`
	Next      string `json:"next_public_key"`
}
type tokenReply struct {
	Token   string `json:"access_token"`
	Type    string `json:"token_type"`
	TTL     int64  `json:"expires_in"`
	Public  string `json:"public_key"`
	Created int64  `json:"key_created_at"`
}

func signed(s state, key, next string, now time.Time) (proof, error) {
	raw := make([]byte, 32)
	if _, e := rand.Read(raw); e != nil {
		return proof{}, e
	}
	q := proof{Authority: s.Authority, Plane: s.Plane, Protocol: 1, Request: s.Request, Device: s.Device, Nonce: hex.EncodeToString(raw), Public: public(key), Issued: now.Unix(), Next: next}
	k, e := private(key)
	if e != nil {
		return q, e
	}
	message := strings.Join([]string{"hypermesh-machine-identity-v1", s.Tenant, s.Device, s.Request, s.Authority, s.Plane, q.Public, fmt.Sprint(q.Issued), q.Nonce, q.Next}, "\n")
	q.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(k, []byte(message)))
	return q, nil
}
func (p Provider) exchange(ctx context.Context, s state, key, next, bootstrap string, bind bool, now time.Time) (tokenReply, error) {
	var out tokenReply
	q, e := signed(s, key, next, now)
	if e != nil {
		return out, e
	}
	raw, _ := json.Marshal(q)
	endpoint := s.Renewal
	if endpoint == "" {
		endpoint = s.Base + "/api/v1/aiuditor/machine/renew"
	}
	if bind {
		endpoint = s.Base + "/api/v1/hypermesh/agent/identity/bind"
	}
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return out, fmt.Errorf("invalid machine identity endpoint")
	}
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(raw))
	if e != nil {
		return out, fmt.Errorf("invalid machine identity endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", s.Tenant)
	if bind {
		req.Header.Set("Authorization", "Bearer "+bootstrap)
	}
	client := http.Client{Timeout: 15 * time.Second}
	if p.HTTP != nil {
		client = *p.HTTP
	}
	if client.Timeout <= 0 || client.Timeout > 15*time.Second {
		client.Timeout = 15 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, e := client.Do(req)
	if e != nil {
		return out, fmt.Errorf("machine identity service unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == 401 {
		return out, errUnauthorized
	}
	if response.StatusCode != 200 {
		return out, fmt.Errorf("machine identity service returned HTTP %d", response.StatusCode)
	}
	if e = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&out); e != nil {
		return out, fmt.Errorf("invalid machine token response")
	}
	expected := q.Public
	if next != "" {
		expected = next
	}
	if out.Public != expected || out.Type != "Bearer" || out.TTL < 60 || out.TTL > 600 || out.Created <= 0 || out.Created > now.Unix()+60 || !strings.HasPrefix(out.Token, "hm_msi_"+s.Device+".") || len(out.Token) != 7+36+1+43 {
		return tokenReply{}, fmt.Errorf("machine token response binding mismatch")
	}
	return out, nil
}

// Token serializes installer/agent access, checkpoints new keys before sending,
// and never falls back to a human session or retired bootstrap bearer.
func (p Provider) Token(ctx context.Context, base, tenant, device, request, bootstrap string) (string, error) {
	if p.Path == "" {
		p.Path = DefaultPath
	}
	base, e := identityBase(base)
	if e != nil {
		return "", e
	}
	unlock, e := lock(ctx, p.Path)
	if e != nil {
		return "", e
	}
	if primary := primaryPath(p.Path); primary != "" {
		if _, err := os.Lstat(p.Path); os.IsNotExist(err) {
			// Creating a peer checkpoint takes the primary lock first, in the
			// same order as retirement, so it cannot race RetireEnrollment.
			unlock()
			release, err := lock(ctx, primary)
			if err != nil {
				return "", err
			}
			defer release()
			if unlock, e = lock(ctx, p.Path); e != nil {
				return "", e
			}
		}
	}
	defer unlock()
	return p.tokenLocked(ctx, base, tenant, device, request, bootstrap, true)
}

// primaryPath returns the primary checkpoint of a peer checkpoint path, or ""
// when path is not a peer checkpoint.
func primaryPath(path string) string {
	i := strings.LastIndex(path, ".peer-")
	if i < 0 || len(path)-i-6 != 64 {
		return ""
	}
	if _, err := hex.DecodeString(path[i+6:]); err != nil {
		return ""
	}
	return path[:i]
}

// retiredDevice reports whether an archived checkpoint or retirement marker
// for path records device.
func retiredDevice(path, device string) (bool, error) {
	retired, err := filepath.Glob(path + ".retired-*")
	if err != nil {
		return false, err
	}
	for _, file := range retired {
		old, err := read(file)
		if err != nil {
			return false, err
		}
		if old.Device == device {
			return true, nil
		}
	}
	return false, nil
}

func identityBase(base string) (string, error) {
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("machine identity requires HTTPS")
	}
	return strings.TrimRight(base, "/"), nil
}

// tokenLocked requires the checkpoint lock. Without create, a missing
// checkpoint means it was retired after the caller chose it; never recreate it.
func (p Provider) tokenLocked(ctx context.Context, base, tenant, device, request, bootstrap string, create bool) (string, error) {
	s, e := read(p.Path)
	if os.IsNotExist(e) {
		if !create {
			return "", fmt.Errorf("machine identity was retired; complete re-enrollment")
		}
		if request == "" {
			return "", fmt.Errorf("machine identity has not been installed")
		}
		// Never recreate a checkpoint for an enrollment that was retired,
		// whether this checkpoint or the primary records the retirement.
		for _, path := range []string{p.Path, primaryPath(p.Path)} {
			if path == "" {
				continue
			}
			retired, err := retiredDevice(path, device)
			if err != nil {
				return "", err
			}
			if retired {
				return "", fmt.Errorf("machine identity for this enrollment was retired; complete re-enrollment")
			}
		}
		key, err := newKey()
		if err != nil {
			return "", err
		}
		authority, plane, renewal, err := p.discover(ctx, base, tenant)
		if err != nil {
			return "", err
		}
		// Check before saving: a stored mismatch would never be rediscovered.
		if p.Authority != "" && authority != p.Authority {
			return "", fmt.Errorf("machine authority discovery returned another authority")
		}
		s = state{Renewal: renewal, Authority: authority, Plane: plane, Schema: 1, Base: base, Tenant: tenant, Device: device, Request: request, Key: key}
		if e = save(p.Path, s); e != nil {
			return "", e
		}
	} else if e != nil {
		return "", e
	}
	if (p.Authority != "" && s.Authority != p.Authority) || s.Authority == "" || s.Plane == "" || s.Schema != 1 || s.Base != base || s.Tenant != tenant || s.Device != device || s.Request == "" || (request != "" && s.Request != request) {
		return "", fmt.Errorf("saved machine identity belongs to another host or network")
	}
	if _, e = private(s.Key); e != nil {
		return "", e
	}
	if s.Pending != "" {
		if _, e = private(s.Pending); e != nil {
			return "", e
		}
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	if s.Bound && s.Token != "" && s.Expires > now.Unix()+120 {
		return s.Token, nil
	}
	if s.Bound && s.Pending == "" && now.Unix()-s.Created >= 30*86400 {
		s.Pending, e = newKey()
		if e != nil {
			return "", e
		}
		if e = save(p.Path, s); e != nil {
			return "", e
		}
	}
	var reply tokenReply
	if s.Pending != "" {
		// Recover even after the overlap expired if the plane committed the new key.
		reply, e = p.exchange(ctx, s, s.Pending, "", "", false, now)
		if errors.Is(e, errUnauthorized) {
			reply, e = p.exchange(ctx, s, s.Key, public(s.Pending), "", false, now)
		}
	} else {
		reply, e = p.exchange(ctx, s, s.Key, "", bootstrap, !s.Bound, now)
	}
	if e != nil {
		return "", e
	}
	if s.Pending != "" {
		s.Key = s.Pending
		s.Pending = ""
	}
	s.Bound = true
	s.Created = reply.Created
	s.Token = reply.Token
	s.Expires = now.Unix() + reply.TTL - 30
	if e = save(p.Path, s); e != nil {
		return "", e
	}
	return s.Token, nil
}

// ExistingToken is used by the running agent. Legacy enrollment remains valid
// only when no signing identity has been installed at all.
func (p Provider) ExistingToken(ctx context.Context, base, tenant, device, bootstrap string) (string, error) {
	if p.Path == "" {
		p.Path = DefaultPath
	}
	// Inspect and use the checkpoint under one lock so retirement cannot slip
	// between the two.
	unlock, e := lock(ctx, p.Path)
	if e != nil {
		return "", e
	}
	defer unlock()
	s, e := read(p.Path)
	if os.IsNotExist(e) {
		retired, err := filepath.Glob(p.Path + ".retired-*")
		if err != nil {
			return "", err
		}
		if len(retired) > 0 {
			if device == "" {
				return "", fmt.Errorf("active enrollment is required for signing identity replacement")
			}
			// A process still holding a retired enrollment must not recreate
			// its identity while re-enrollment is in progress.
			for _, path := range retired {
				old, err := read(path)
				if err != nil {
					return "", err
				}
				if old.Device == device {
					return "", fmt.Errorf("machine identity for this enrollment was retired; complete re-enrollment")
				}
			}
			if base, err = identityBase(base); err != nil {
				return "", err
			}
			// Enroll-token replacements use their immutable device ID as the
			// authority context. The plane verifies the fresh bootstrap secret.
			return p.tokenLocked(ctx, base, tenant, device, device, bootstrap, true)
		}
		return bootstrap, nil
	}
	if e != nil {
		return "", e
	}
	if device == "" || s.Device != device {
		return "", fmt.Errorf("saved machine identity does not match active enrollment")
	}
	if base, e = identityBase(base); e != nil {
		return "", e
	}
	return p.tokenLocked(ctx, base, tenant, device, s.Request, bootstrap, false)
}

func safeFile(path string) (*os.File, error) {
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	st, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		f.Close()
		return nil, fmt.Errorf("unsafe machine identity file")
	}
	return f, nil
}
func read(path string) (state, error) {
	var s state
	f, e := safeFile(path)
	if e != nil {
		return s, e
	}
	defer f.Close()
	if e = json.NewDecoder(io.LimitReader(f, 16384)).Decode(&s); e != nil {
		return s, fmt.Errorf("invalid machine identity checkpoint")
	}
	return s, nil
}
func save(path string, s state) error {
	if _, e := os.Lstat(path); e == nil {
		f, e := safeFile(path)
		if e != nil {
			return e
		}
		f.Close()
	} else if !os.IsNotExist(e) {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".machine-identity-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = json.NewEncoder(f).Encode(s); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
func lock(ctx context.Context, path string) (func(), error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("machine identity path must be absolute")
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		st, e := os.Lstat(dir)
		if e != nil {
			return nil, e
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || (sys.Uid != 0 && sys.Uid != uint32(os.Geteuid())) || (st.Mode().Perm()&0022 != 0 && !(sys.Uid == 0 && st.Mode()&os.ModeSticky != 0)) {
			return nil, fmt.Errorf("unsafe machine identity directory")
		}
		if dir == "/" {
			break
		}
	}
	fd, e := syscall.Open(path+".lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path+".lock")
	st, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || sys.Uid != uint32(os.Geteuid()) || sys.Nlink != 1 {
		f.Close()
		return nil, fmt.Errorf("unsafe machine identity lock")
	}
	for {
		e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			return func() { syscall.Flock(fd, syscall.LOCK_UN); f.Close() }, nil
		}
		if e != syscall.EWOULDBLOCK && e != syscall.EAGAIN {
			f.Close()
			return nil, e
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// Invalidate only the rejected cached token; a concurrent renewal is preserved.
func (p Provider) Invalidate(ctx context.Context, token string) error {
	if p.Path == "" {
		p.Path = DefaultPath
	}
	unlock, e := lock(ctx, p.Path)
	if e != nil {
		return e
	}
	defer unlock()
	s, e := read(p.Path)
	if e != nil {
		return e
	}
	if s.Token != token {
		return nil
	}
	s.Token = ""
	s.Expires = 0
	return save(p.Path, s)
}

func (p Provider) discover(ctx context.Context, base, tenant string) (string, string, string, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/hypermesh/agent/identity/configuration?authority_id="+url.QueryEscape(p.Authority), nil)
	if e != nil {
		return "", "", "", e
	}
	req.Header.Set("X-Tenant-ID", tenant)
	// Same bound as the exchange: at most 15 seconds, shorter if injected.
	client := &http.Client{Timeout: 15 * time.Second}
	if p.HTTP != nil {
		clone := *p.HTTP
		client = &clone
		if client.Timeout <= 0 || client.Timeout > 15*time.Second {
			client.Timeout = 15 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := client.Do(req)
	if e != nil {
		return "", "", "", fmt.Errorf("machine authority discovery unavailable")
	}
	defer resp.Body.Close()
	var result struct {
		Authority string `json:"authority_id"`
		Plane     string `json:"control_plane_id"`
		Renewal   string `json:"renewal_url"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result) != nil || !validAuthority(result.Authority) || !validAuthority(result.Plane) {
		return "", "", "", fmt.Errorf("invalid machine authority configuration")
	}
	renewal := result.Renewal
	if renewal == "" {
		renewal = "/api/v1/aiuditor/machine/renew"
	}
	if strings.HasPrefix(renewal, "/") && !strings.HasPrefix(renewal, "//") {
		renewal = base + renewal
	}
	u, e := url.Parse(renewal)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", "", fmt.Errorf("invalid AIuditor renewal endpoint")
	}
	return result.Authority, result.Plane, renewal, nil
}
func validAuthority(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._:-", c) {
			return false
		}
	}
	return true
}

// Peer keeps independent keys, rotation journals and credentials for each
// AIuditor delegation while retaining the same host registration identity.
func (p Provider) Peer(authority string) (Provider, error) {
	if !validAuthority(authority) {
		return Provider{}, fmt.Errorf("invalid AIuditor identity")
	}
	if p.Path == "" {
		p.Path = DefaultPath
	}
	hash := sha256.Sum256([]byte(authority))
	p.Path += fmt.Sprintf(".peer-%x", hash[:])
	p.Authority = authority
	return p, nil
}

// ContractToken proves this same host identity for exactly one live contract.
// The control plane routes only to its configured AIuditors; a job cannot
// supply an arbitrary credential endpoint. The returned token is not persisted.
func (p Provider) ContractToken(ctx context.Context, base, tenant, device, request, bootstrap, contract string) (string, error) {
	if contract == "" {
		return "", fmt.Errorf("contract identity required")
	}
	if _, e := p.Token(ctx, base, tenant, device, request, bootstrap); e != nil {
		return "", e
	}
	if p.Path == "" {
		p.Path = DefaultPath
	}
	unlock, e := lock(ctx, p.Path)
	if e != nil {
		return "", e
	}
	defer unlock()
	s, e := read(p.Path)
	if e != nil {
		return "", e
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	q, e := signed(s, s.Key, "", now)
	if e != nil {
		return "", e
	}
	// Extend the signed domain; a management proof cannot mint a contract grant.
	msg := strings.Join([]string{"hypermesh-machine-identity-v1", s.Tenant, s.Device, s.Request, s.Authority, s.Plane, q.Public, fmt.Sprint(q.Issued), q.Nonce, q.Next, contract}, "\n")
	key, e := private(s.Key)
	if e != nil {
		return "", e
	}
	q.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(msg)))
	body := struct {
		proof
		Contract string `json:"contract_id"`
	}{q, contract}
	raw, _ := json.Marshal(body)
	req, e := http.NewRequestWithContext(ctx, "POST", s.Base+"/api/v1/hypermesh/agent/identity/contract-token", bytes.NewReader(raw))
	if e != nil {
		return "", e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tenant)
	client := &http.Client{Timeout: 30 * time.Second}
	if p.HTTP != nil {
		clone := *p.HTTP
		client = &clone
		client.Timeout = 30 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := client.Do(req)
	if e != nil {
		return "", fmt.Errorf("contract authority unavailable")
	}
	defer resp.Body.Close()
	var out struct {
		Token    string `json:"access_token"`
		Type     string `json:"token_type"`
		TTL      int64  `json:"expires_in"`
		Device   string `json:"device_id"`
		Contract string `json:"contract_id"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out) != nil || out.Type != "Bearer" || out.TTL <= 0 || out.TTL > 600 || out.Device != device || out.Contract != contract || !strings.HasPrefix(out.Token, "hm_ct_") || len(out.Token) != 49 {
		return "", fmt.Errorf("contract credential rejected or invalid")
	}
	return out.Token, nil
}

// Retire removes local credentials from active use before exchanging a fresh
// enrollment token. Preserve private history and serialize with token renewal.
// Peer delegations belong to the retired enrollment and must be retired too.
func (p Provider) Retire(ctx context.Context) error {
	return p.RetireEnrollment(ctx, "", false)
}

// RetireEnrollment archives the signing checkpoints before re-enrollment.
// With signing set, a host that never created a checkpoint still records the
// retired device, so the replacement binds a signing key instead of keeping
// its bootstrap secret.
func (p Provider) RetireEnrollment(ctx context.Context, device string, signing bool) error {
	if p.Path == "" {
		p.Path = DefaultPath
	}
	unlock, err := lock(ctx, p.Path)
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := os.ReadDir(filepath.Dir(p.Path))
	if err != nil {
		return err
	}
	paths := []string{p.Path}
	prefix := filepath.Base(p.Path) + ".peer-"
	for _, entry := range entries {
		suffix := strings.TrimPrefix(entry.Name(), prefix)
		if suffix == entry.Name() || len(suffix) != 64 {
			continue
		}
		if _, err := hex.DecodeString(suffix); err != nil {
			continue
		}
		path := filepath.Join(filepath.Dir(p.Path), entry.Name())
		release, err := lock(ctx, path)
		if err != nil {
			return err
		}
		defer release()
		paths = append(paths, path)
	}
	// Check every checkpoint before archiving any, so a bad peer leaves the
	// active enrollment untouched.
	var present []string
	for _, path := range paths {
		f, err := safeFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		f.Close()
		present = append(present, path)
	}
	var moved [][2]string
	marker := ""
	rollback := func(err error) error {
		if marker != "" {
			os.Remove(marker)
		}
		for i := len(moved) - 1; i >= 0; i-- {
			os.Rename(moved[i][1], moved[i][0])
		}
		return err
	}
	for _, path := range present {
		nonce := make([]byte, 16)
		if _, err = rand.Read(nonce); err == nil {
			target := path + ".retired-" + hex.EncodeToString(nonce)
			if err = os.Rename(path, target); err == nil {
				moved = append(moved, [2]string{path, target})
				continue
			}
		}
		return rollback(err)
	}
	// Archived checkpoints record the retired device; so does this marker.
	// Write it only after every checkpoint moved, and undo the moves if it
	// cannot be written, so a failed retirement leaves the host as it was.
	if signing && (len(present) == 0 || present[0] != p.Path) {
		nonce := make([]byte, 16)
		if _, err = rand.Read(nonce); err != nil {
			return rollback(err)
		}
		// Track the marker first: save can fail after the file is visible.
		marker = p.Path + ".retired-" + hex.EncodeToString(nonce)
		if err = save(marker, state{Schema: 1, Device: device}); err != nil {
			return rollback(err)
		}
	}
	// A retirement that cannot be made durable is undone, so the current
	// enrollment keeps working if re-enrollment stops here.
	dir, err := os.Open(filepath.Dir(p.Path))
	if err != nil {
		return rollback(err)
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return rollback(err)
	}
	return nil
}
