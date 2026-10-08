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
	return identityFixtureFor(t, device, request)
}

func identityFixtureFor(t *testing.T, device, request string) (Provider, *time.Time, *int, *bool) {
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

func TestExistingTokenRejectsDifferentActiveDevice(t *testing.T) {
	p, _, calls, _ := identityFixture(t)
	if _, err := get(t, p); err != nil {
		t.Fatal(err)
	}
	base, _ := origins.Load(p.Path)
	before := *calls
	for _, id := range []string{"", "replacement-device"} {
		token, err := p.ExistingToken(context.Background(), base.(string), tenant, id, "new-bootstrap")
		if err == nil || token != "" {
			t.Fatal("returned retired enrollment credential", token, err)
		}
	}
	if *calls != before {
		t.Fatal("renewed wrong enrollment")
	}
}

func TestRetirementArchivesPrimaryAndPeersAndPreparesFreshKey(t *testing.T) {
	p, _, _, _ := identityFixture(t)
	if _, err := get(t, p); err != nil {
		t.Fatal(err)
	}
	old, err := read(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := p.Peer("other-aiuditor")
	if err != nil {
		t.Fatal(err)
	}
	if err = save(peer.Path, old); err != nil {
		t.Fatal(err)
	}
	if err = p.Retire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = p.Retire(context.Background()); err != nil {
		t.Fatal("retry failed", err)
	}
	for _, path := range []string{p.Path, peer.Path} {
		if _, err = os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("old identity remains active", err)
		}
		files, err := filepath.Glob(path + ".retired-*")
		if err != nil || len(files) != 1 {
			t.Fatal(files, err)
		}
		archived, err := read(files[0])
		if err != nil || archived.Key != old.Key {
			t.Fatal("lost private history", err)
		}
	}
	newDevice := "12345678-1234-4234-8234-123456789aaa"
	replacement, _, _, _ := identityFixtureFor(t, newDevice, newDevice)
	base, _ := origins.Load(replacement.Path)
	replacement.Path = p.Path
	token, err := replacement.ExistingToken(context.Background(), base.(string), tenant, newDevice, "hm_dev_fixture")
	if err != nil || !strings.HasPrefix(token, "hm_msi_"+newDevice+".") {
		t.Fatal("replacement did not bind", err)
	}

	fresh, err := read(p.Path)
	if err != nil || fresh.Key == old.Key {
		t.Fatal("reused signing key", err)
	}
}

func TestRetirementRefusesUnsafeCheckpoint(t *testing.T) {
	p, _, _, _ := identityFixture(t)
	target := filepath.Join(filepath.Dir(p.Path), "other")
	if err := os.WriteFile(target, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p.Path); err != nil {
		t.Fatal(err)
	}
	if err := p.Retire(context.Background()); err == nil {
		t.Fatal("accepted symlink")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "private" {
		t.Fatal("changed target", err)
	}
}

type timeoutTransport struct {
	t     *testing.T
	limit time.Duration
}

func (tr timeoutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	deadline, ok := r.Context().Deadline()
	if !ok || time.Until(deadline) > tr.limit {
		tr.t.Fatal("exchange has no bounded deadline")
	}
	return nil, fmt.Errorf("test transport stopped")
}
func TestIdentityRequestsBoundInjectedClientTimeout(t *testing.T) {
	key, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, timeout := range []time.Duration{0, time.Second, time.Hour} {
		limit := 15 * time.Second
		if timeout > 0 && timeout < limit {
			limit = timeout
		}
		client := &http.Client{Timeout: timeout, Transport: timeoutTransport{t, limit}}
		p := Provider{HTTP: client}
		_, err = p.exchange(context.Background(), state{Base: "https://example.test", Tenant: tenant, Device: device, Request: request, Authority: "local", Plane: "local"}, key, "", "bootstrap", true, time.Now())
		if err == nil {
			t.Fatal("expected transport failure")
		}
		// Discovery runs before the first exchange and has the same bound.
		if _, _, _, err = p.discover(context.Background(), "https://example.test", tenant); err == nil {
			t.Fatal("expected discovery transport failure")
		}
		if client.Timeout != timeout {
			t.Fatal("mutated injected client")
		}
	}
}

func TestRetirementChecksEveryCheckpointBeforeArchiving(t *testing.T) {
	p, _, _, _ := identityFixture(t)
	if _, err := get(t, p); err != nil {
		t.Fatal(err)
	}
	peer, err := p.Peer("other-aiuditor")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(p.Path), "other")
	if err = os.WriteFile(target, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, peer.Path); err != nil {
		t.Fatal(err)
	}
	if err = p.Retire(context.Background()); err == nil {
		t.Fatal("accepted unsafe peer checkpoint")
	}
	if _, err = read(p.Path); err != nil {
		t.Fatal("active identity archived before every checkpoint was checked", err)
	}
	if files, _ := filepath.Glob(p.Path + ".retired-*"); len(files) != 0 {
		t.Fatal("partial retirement", files)
	}
}

func TestRetiredEnrollmentIsNotRecreated(t *testing.T) {
	p, _, calls, _ := identityFixture(t)
	if _, err := get(t, p); err != nil {
		t.Fatal(err)
	}
	old, err := read(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Retire(context.Background()); err != nil {
		t.Fatal(err)
	}
	base, _ := origins.Load(p.Path)
	before := *calls
	// A process that still holds the retired enrollment races re-enrollment.
	token, err := p.ExistingToken(context.Background(), base.(string), tenant, old.Device, "old-bootstrap")
	if err == nil || token != "" {
		t.Fatal("recreated retired enrollment", token, err)
	}
	if _, err = os.Stat(p.Path); !os.IsNotExist(err) {
		t.Fatal("wrote a checkpoint for the retired enrollment", err)
	}
	if *calls != before {
		t.Fatal("contacted the authority for a retired enrollment")
	}
	if _, err = p.tokenLocked(context.Background(), old.Base, tenant, old.Device, old.Request, "old-bootstrap", false); err == nil {
		t.Fatal("recreated a checkpoint retired after it was chosen")
	}
}

func TestRetiredPeerIsNotRecreated(t *testing.T) {
	p, _, calls, _ := identityFixture(t)
	if _, err := get(t, p); err != nil {
		t.Fatal(err)
	}
	old, err := read(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := p.Peer("other-aiuditor")
	if err != nil {
		t.Fatal(err)
	}
	if err = save(peer.Path, old); err != nil {
		t.Fatal(err)
	}
	if err = p.Retire(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := *calls
	// A process that still holds the retired enrollment asks for a contract.
	if _, err = peer.ContractToken(context.Background(), old.Base, tenant, old.Device, old.Request, "old-bootstrap", "contract-1"); err == nil {
		t.Fatal("recreated a retired peer checkpoint")
	}
	if _, err = os.Stat(peer.Path); !os.IsNotExist(err) {
		t.Fatal("wrote a peer checkpoint for the retired enrollment", err)
	}
	// A peer that never had a checkpoint is covered by the primary retirement.
	fresh, err := p.Peer("third-aiuditor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fresh.Token(context.Background(), old.Base, tenant, old.Device, old.Request, "old-bootstrap"); err == nil {
		t.Fatal("created a peer checkpoint for the retired enrollment")
	}
	if _, err = os.Stat(fresh.Path); !os.IsNotExist(err) {
		t.Fatal("wrote a peer checkpoint for the retired enrollment", err)
	}
	if *calls != before {
		t.Fatal("contacted the authority for a retired enrollment")
	}
	if primaryPath(peer.Path) != p.Path || primaryPath(p.Path) != "" {
		t.Fatal("peer path does not map to its primary")
	}
}

func TestMismatchedPeerDiscoveryIsNotSaved(t *testing.T) {
	p, _, _, _ := identityFixture(t)
	base, _ := origins.Load(p.Path)
	// The fixture's discovery always answers for the "local" authority.
	peer, err := p.Peer("other-aiuditor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = peer.Token(context.Background(), base.(string), tenant, device, request, "hm_dev_fixture"); err == nil {
		t.Fatal("accepted another authority's configuration")
	}
	if _, err = os.Stat(peer.Path); !os.IsNotExist(err) {
		t.Fatal("saved a checkpoint for another authority", err)
	}
}

func TestLegacyReenrollmentBindsASigningKey(t *testing.T) {
	newDevice := "12345678-1234-4234-8234-123456789bbb"
	p, _, calls, _ := identityFixtureFor(t, newDevice, newDevice)
	base, _ := origins.Load(p.Path)
	oldDevice := "12345678-1234-4234-8234-123456789ccc"
	supervisor := Provider{Path: p.Path}
	if err := supervisor.RetireEnrollment(context.Background(), oldDevice, false); err != nil {
		t.Fatal(err)
	}
	if token, err := p.ExistingToken(context.Background(), base.(string), tenant, newDevice, "hm_dev_fixture"); err != nil || token != "hm_dev_fixture" {
		t.Fatal("non-signing host lost its enrollment secret", token, err)
	}
	if err := p.RetireEnrollment(context.Background(), oldDevice, true); err != nil {
		t.Fatal(err)
	}
	before := *calls
	if token, err := p.ExistingToken(context.Background(), base.(string), tenant, oldDevice, "old-bootstrap"); err == nil || token != "" {
		t.Fatal("retired legacy enrollment bound a key", token, err)
	}
	if *calls != before {
		t.Fatal("contacted the authority for the retired enrollment")
	}
	token, err := p.ExistingToken(context.Background(), base.(string), tenant, newDevice, "hm_dev_fixture")
	if err != nil || !strings.HasPrefix(token, "hm_msi_"+newDevice+".") {
		t.Fatal("legacy replacement kept its bootstrap secret", token, err)
	}
}
