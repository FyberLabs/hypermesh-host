//go:build linux

package machineidentity

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const tenant = "12345678-1234-4234-8234-123456789abc"
const device = "12345678-1234-4234-8234-123456789def"
const request = "12345678-1234-4234-8234-123456789aaa"

func identityFixture(t *testing.T) (Provider, *time.Time, *int, *bool) {
	t.Helper()
	now := time.Unix(1791400000, 0)
	calls := 0
	lose := false
	current := ""
	created := int64(0)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/hypermesh/agent/identity/configuration" {
			json.NewEncoder(w).Encode(map[string]string{"authority_id": "local", "control_plane_id": "local"})
			return
		}
		calls++
		var body struct {
			proof
			Contract string `json:"contract_id"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("bad proof")
			w.WriteHeader(400)
			return
		}
		q := body.proof
		raw, e := base64.StdEncoding.DecodeString(q.Public)
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		sig, _ := base64.StdEncoding.DecodeString(q.Signature)
		msg := strings.Join([]string{"hypermesh-machine-identity-v1", tenant, device, request, q.Authority, q.Plane, q.Public, fmt.Sprint(q.Issued), q.Nonce, q.Next}, "\n")
		if body.Contract != "" {
			msg += "\n" + body.Contract
		}
		if len(raw) != 32 || !ed25519.Verify(raw, []byte(msg), sig) || q.Request != request || q.Device != device || q.Issued != now.Unix() {
			t.Error("invalid proof binding")
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/api/v1/hypermesh/agent/identity/contract-token" {
			json.NewEncoder(w).Encode(map[string]any{"access_token": "hm_ct_" + strings.Repeat("a", 43), "token_type": "Bearer", "expires_in": 300, "device_id": device, "contract_id": body.Contract})
			return
		}
		if r.URL.Path == "/api/v1/hypermesh/agent/identity/bind" {
			if r.Header.Get("Authorization") != "Bearer hm_dev_fixture" {
				t.Error("missing bootstrap")
			}
			if current != "" && current != q.Public {
				w.WriteHeader(409)
				return
			}
			current = q.Public
			created = now.Unix()
		} else {
			if r.Header.Get("Authorization") != "" {
				t.Error("long-lived bearer sent after binding")
			}
			if q.Public != current {
				w.WriteHeader(401)
				return
			}
			if q.Next != "" {
				current = q.Next
				created = now.Unix()
			}
		}
		if lose {
			lose = false
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(tokenReply{Token: "hm_msi_" + device + "." + strings.Repeat("a", 43), Type: "Bearer", TTL: 600, Public: current, Created: created})
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	p := Provider{Path: filepath.Join(dir, "identity.json"), HTTP: server.Client(), Now: func() time.Time { return now }}
	// Keep test origin alongside the provider without exposing it to production state.
	origins.Store(p.Path, server.URL)
	t.Cleanup(func() { origins.Delete(p.Path) })
	return p, &now, &calls, &lose
}

var origins sync.Map

func get(t *testing.T, p Provider) (string, error) {
	t.Helper()
	base, _ := origins.Load(p.Path)
	return p.Token(context.Background(), base.(string), tenant, device, request, "hm_dev_fixture")
}
func TestIdentityBindCacheRenewAndDurableRotation(t *testing.T) {
	p, now, calls, lose := identityFixture(t)
	token, e := get(t, p)
	if e != nil {
		t.Fatal(e)
	}
	s, e := read(p.Path)
	if e != nil || !s.Bound {
		t.Fatal(e)
	}
	old := s.Key
	if _, e = get(t, p); e != nil || *calls != 1 {
		t.Fatal("cached token not reused", e, *calls)
	}
	if e = p.Invalidate(context.Background(), "different-token"); e != nil {
		t.Fatal(e)
	}
	if _, e = get(t, p); e != nil || *calls != 1 {
		t.Fatal("concurrent refresh was discarded")
	}
	if e = p.Invalidate(context.Background(), token); e != nil {
		t.Fatal(e)
	}
	if _, e = get(t, p); e != nil || *calls != 2 {
		t.Fatal("did not renew rejected token", e)
	}
	*now = now.Add(31 * 24 * time.Hour)
	*lose = true
	if _, e = get(t, p); e == nil {
		t.Fatal("lost rotation reply ignored")
	}
	s, e = read(p.Path)
	if e != nil || s.Pending == "" || s.Key != old {
		t.Fatal("rotation was not checkpointed", e)
	}
	pending := s.Pending
	// A reboot starts a new Provider and may occur after the old-key overlap.
	*now = now.Add(2 * 24 * time.Hour)
	restarted := Provider{Path: p.Path, HTTP: p.HTTP, Now: p.Now}
	if _, e = get(t, restarted); e != nil {
		t.Fatal("new key did not recover after lost reply", e)
	}
	s, e = read(p.Path)
	if e != nil || s.Key != pending || s.Pending != "" {
		t.Fatal("replacement was not adopted", e)
	}
	if strings.Contains(fmt.Sprint(s), s.Key) {
		t.Fatal("key leaks through diagnostics")
	}
}
func TestIdentityLostBindReplyPreservesKey(t *testing.T) {
	p, _, _, lose := identityFixture(t)
	*lose = true
	if _, e := get(t, p); e == nil {
		t.Fatal("expected lost reply")
	}
	before, _ := read(p.Path)
	if before.Bound {
		t.Fatal("unconfirmed binding")
	}
	if _, e := get(t, p); e != nil {
		t.Fatal(e)
	}
	after, _ := read(p.Path)
	if before.Key != after.Key || !after.Bound {
		t.Fatal("lost binding changed identity")
	}
}
func TestIdentityConcurrentProcessesReuseBinding(t *testing.T) {
	p, _, calls, _ := identityFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := get(t, p); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if *calls != 1 {
		t.Fatal("multiple bindings", *calls)
	}
}
func TestIdentityRejectsTamperingAndNetworkChanges(t *testing.T) {
	for _, kind := range []string{"symlink", "world-readable", "wrong-host", "wrong-network", "broken-key"} {
		t.Run(kind, func(t *testing.T) {
			p, _, _, _ := identityFixture(t)
			if _, e := get(t, p); e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "symlink":
				os.Rename(p.Path, p.Path+".saved")
				os.Symlink(p.Path+".saved", p.Path)
			case "world-readable":
				os.Chmod(p.Path, 0644)
			default:
				s, _ := read(p.Path)
				if kind == "wrong-host" {
					s.Device = request
				}
				if kind == "wrong-network" {
					s.Base = "https://other.example"
				}
				if kind == "broken-key" {
					s.Key = "broken"
				}
				if e := save(p.Path, s); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := get(t, p); e == nil {
				t.Fatal("accepted invalid identity", kind)
			}
		})
	}
}

func TestContractProofAndPeerIsolation(t *testing.T) {
	p, _, _, _ := identityFixture(t)
	base, _ := origins.Load(p.Path)
	token, e := p.ContractToken(context.Background(), base.(string), tenant, device, request, "hm_dev_fixture", "contract-a")
	if e != nil || !strings.HasPrefix(token, "hm_ct_") {
		t.Fatalf("contract proof: %v", e)
	}
	saved, e := read(p.Path)
	if e != nil {
		t.Fatal(e)
	}
	if saved.Token == token {
		t.Fatal("contract token replaced management token")
	}
	a, e := p.Peer("aiuditor-a")
	if e != nil {
		t.Fatal(e)
	}
	b, e := p.Peer("aiuditor-b")
	if e != nil {
		t.Fatal(e)
	}
	if a.Path == b.Path || a.Path == p.Path || a.Authority == b.Authority {
		t.Fatal("peer credential collision")
	}
	if _, e := p.Peer("../../untrusted"); e == nil {
		t.Fatal("invalid authority accepted")
	}
}
