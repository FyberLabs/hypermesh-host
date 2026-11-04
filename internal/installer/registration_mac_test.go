//go:build linux || darwin

package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	mac "github.com/FyberLabs/hypermesh-host/deploy/mac"
	"github.com/FyberLabs/hypermesh-host/internal/agent"
	"github.com/FyberLabs/hypermesh-host/internal/hardware"
	"github.com/FyberLabs/hypermesh-host/internal/hostregistration"
	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

const macTestTenant = "12345678-1234-4234-8234-123456789abc"

var macTestArgs = []string{"--issuer", "https://auth.test.example/realms/test", "--dashboard-url", "https://portal.test.example"}
var macAbsent = errors.New("service absent")

type macFakeRemote struct {
	t                          *testing.T
	root, owner, state         string
	request                    hostregistration.Request
	creates, completes, logins int
	approved, loseCompletion   bool
	beforeComplete             func()
}

func (r *macFakeRemote) Login(_ context.Context, _ string, bind func(string) error) error {
	r.logins++
	return bind(r.owner)
}
func (r *macFakeRemote) SetSessionRecovery(func(context.Context) error) {}
func (r *macFakeRemote) status() hostregistration.Status {
	s := hostregistration.Status{State: r.state, ExpiresAt: "2030-01-01T00:00:00Z", ApprovalPath: "/dashboard/hypermesh/host/approve/" + r.request.RequestID}
	if r.state == "completed" {
		id := "12345678-1234-4234-8234-123456789aaa"
		s.DeviceID = &id
	}
	return s
}
func (r *macFakeRemote) Create(_ context.Context, q hostregistration.Request) (hostregistration.Status, error) {
	r.creates++
	if r.request.RequestID != "" && q != r.request {
		r.t.Fatal("retry changed registration identity")
	}
	if r.request.RequestID == "" && r.state == "denied" {
		r.state = "pending"
	}
	r.request = q
	if r.state == "" {
		r.state = "pending"
	}
	return r.status(), nil
}
func (r *macFakeRemote) Get(context.Context, hostregistration.Request) (hostregistration.Status, error) {
	if r.approved && r.state == "pending" {
		r.state = "approved"
	}
	return r.status(), nil
}
func (r *macFakeRemote) Complete(context.Context, hostregistration.Request) (hostregistration.Status, error) {
	if !r.approved || r.state != "approved" {
		r.t.Fatal("completion without browser approval")
	}
	if _, err := os.Stat(macPath(r.root, "/var/lib/hypermesh/device.json")); !os.IsNotExist(err) {
		r.t.Fatal("identity published before server completion")
	}
	if r.beforeComplete != nil {
		r.beforeComplete()
	}
	r.completes++
	r.state = "completed"
	if r.loseCompletion {
		r.loseCompletion = false
		return hostregistration.Status{}, errors.New("lost completion response")
	}
	return r.status(), nil
}

type macFixture struct {
	d          macRegistrationDeps
	r          *macFakeRemote
	commands   []string
	loaded     map[string]bool
	utun       bool
	authorized int
}

func newMacFixture(t *testing.T) *macFixture {
	t.Helper()
	f := &macFixture{loaded: map[string]bool{}}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.r = &macFakeRemote{t: t, root: root, owner: "verified-owner"}
	f.d = macRegistrationDeps{root: root,
		detect: func(context.Context) hardware.Profile {
			return hardware.Recommend(hardware.Facts{Arch: "arm64", Model: "Macmini9,1", Processor: "Apple M1", Metal: true, MemoryBytes: 16 << 30, AvailableBytes: 10 << 30})
		},
		remote: func(string, string) (macRegistrationRemote, error) { return f.r, nil },
		authorize: func(context.Context, string, string, string, io.Writer) (string, error) {
			f.authorized++
			return "browser-secret", nil
		},
		absent: func(err error) bool { return errors.Is(err, macAbsent) },
		wait:   func(context.Context, time.Duration) error { f.r.approved = true; return nil },
	}
	f.d.command = func(_ context.Context, name string, args ...string) ([]byte, error) {
		f.commands = append(f.commands, name+" "+strings.Join(args, " "))
		if name == "/sbin/ifconfig" {
			if f.utun {
				return nil, nil
			}
			return nil, macAbsent
		}
		switch args[0] {
		case "print":
			if f.loaded[strings.TrimPrefix(args[1], "system/")] {
				return nil, nil
			}
			return nil, macAbsent
		case "bootstrap":
			label := strings.TrimSuffix(filepath.Base(args[2]), ".plist")
			f.loaded[label] = true
			if label == "com.fyberlabs.hypermesh-wg" {
				f.utun = true
			}
			return nil, nil
		default:
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		return nil, nil
	}
	env := "HYPERMESH_CONTROL_PLANE_URL=https://api.test.example\nHYPERMESH_TENANT_ID=" + macTestTenant + "\nHYPERMESH_HOST_NAME=macmini-1\nHYPERMESH_STATE_DIR=/var/lib/hypermesh\nHYPERMESH_WG_USERSPACE=1\nHYPERMESH_ENROLL_TOKEN=\n"
	for _, spec := range []struct {
		path, data string
		mode       os.FileMode
	}{
		{"/etc/hypermesh/agent.env", env, 0o600},
		{"/usr/local/bin/hypermesh-agent", "agent binary", 0o755},
		{"/usr/local/libexec/hypermesh/wireguard-go", "wg-go binary", 0o755},
		{"/usr/local/libexec/hypermesh/wg", "wg binary", 0o755},
		{"/Library/LaunchDaemons/com.fyberlabs.hypermesh-agent.plist", mac.AgentPlist, 0o644},
		{"/Library/LaunchDaemons/com.fyberlabs.hypermesh-wg.plist", mac.WireGuardPlist, 0o644},
	} {
		path := macPath(root, spec.path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(spec.data), spec.mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(macPath(root, "/var/lib/hypermesh"), 0o700); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *macFixture) run(extra ...string) (int, string, string) {
	var out, diag bytes.Buffer
	args := append(append([]string{}, macTestArgs...), extra...)
	code := runMacRegister(context.Background(), args, &out, &diag, f.d)
	return code, out.String(), diag.String()
}
func (f *macFixture) journal(t *testing.T) registrationJournal {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(macPath(f.d.root, macRegistrationState), "registration-*.json"))
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	raw, err := macRead(paths[0], 0o600, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	var j registrationJournal
	if err = macDecode(raw, &j); err != nil {
		t.Fatal(err)
	}
	return j
}
func TestMacOwnerRegistrationApprovalIdentityAndLaunchOrder(t *testing.T) {
	f := newMacFixture(t)
	code, out, diag := f.run("--start-services")
	if code != 0 {
		t.Fatal(code, diag)
	}
	j := f.journal(t)
	if j.State != "registered-inactive" || j.Owner != "verified-owner" || f.r.completes != 1 || j.Request.ClassID != macLabClass {
		t.Fatal("registration did not complete")
	}
	h := j.Request.Hardware
	if h.Profile != "apple-silicon" || h.Architecture != "arm64" || h.CPU != "Apple M1" || h.GPU != "metal accelerator" || h.ModelMemoryBytes > h.SystemMemoryBytes {
		t.Fatal("report violates the test-plane class contract")
	}
	raw, err := macRead(macPath(f.d.root, "/var/lib/hypermesh/device.json"), 0o600, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	var identity agent.State
	if json.Unmarshal(raw, &identity) != nil || identity.DeviceID != j.DeviceID || identity.DeviceSecret != j.DeviceSecret {
		t.Fatal("wrong identity contract")
	}
	key, err := macRead(macPath(f.d.root, identity.WGKeyDir+"/hypermesh.key"), 0o600, 1024)
	if err != nil {
		t.Fatal(err)
	}
	public, err := tunnel.PublicFromPrivate(strings.TrimSpace(string(key)))
	if err != nil || public != j.Request.WGPublicKey {
		t.Fatal("key changed")
	}
	var starts []string
	for _, cmd := range f.commands {
		if strings.Contains(cmd, " bootstrap ") {
			starts = append(starts, cmd)
		}
	}
	if len(starts) != 2 || !strings.Contains(starts[0], "hypermesh-wg.plist") || !strings.Contains(starts[1], "hypermesh-agent.plist") {
		t.Fatal(starts)
	}
	if !strings.Contains(diag, "https://portal.test.example/dashboard/hypermesh/host/approve/") {
		t.Fatal("no remote approval instructions")
	}
	for _, secret := range []string{j.PrivateKey, j.DeviceSecret, "browser-secret"} {
		if strings.Contains(out+diag, secret) {
			t.Fatal("credential leaked")
		}
	}
	var statusOut, diagnostic bytes.Buffer
	if runMacRegistrationStatus([]string{"--plan-sha", j.PlanSHA256}, &statusOut, &diagnostic, f.d.root) != 0 || strings.Contains(statusOut.String(), j.DeviceSecret) {
		t.Fatal(diagnostic.String())
	}
	// A completed retry authenticates the original owner and reuses both keys.
	before := len(starts)
	code, _, diag = f.run("--start-services")
	if code != 0 {
		t.Fatal(diag)
	}
	for _, cmd := range f.commands {
		if strings.Contains(cmd, " bootstrap ") {
			before--
		}
	}
	if before != 0 || f.r.completes != 1 || f.journal(t).PrivateKey != j.PrivateKey {
		t.Fatal("retry restarted or replaced the host")
	}
}
func TestMacRegistrationLostCompletionRecoversSameIdentity(t *testing.T) {
	f := newMacFixture(t)
	f.r.loseCompletion = true
	if code, _, _ := f.run(); code == 0 {
		t.Fatal("lost response reported success")
	}
	j := f.journal(t)
	if j.DeviceID != "" {
		t.Fatal("invented completion")
	}
	if code, _, diag := f.run(); code != 0 {
		t.Fatal(diag)
	}
	next := f.journal(t)
	if next.PrivateKey != j.PrivateKey || next.DeviceSecret != j.DeviceSecret || f.r.completes != 1 {
		t.Fatal("lost response replaced identity")
	}
}
func TestMacRegistrationOwnerAndInstallationBinding(t *testing.T) {
	for _, kind := range []string{"owner", "binary", "env", "identity", "active-service", "plist", "non-apple"} {
		t.Run(kind, func(t *testing.T) {
			f := newMacFixture(t)
			if kind == "owner" || kind == "binary" || kind == "env" {
				f.d.wait = func(context.Context, time.Duration) error { return errors.New("interrupted") }
				if code, _, _ := f.run(); code == 0 {
					t.Fatal("expected pending interruption")
				}
			}
			switch kind {
			case "owner":
				f.r.owner = "another-owner"
			case "binary":
				os.WriteFile(macPath(f.d.root, "/usr/local/bin/hypermesh-agent"), []byte("changed executable"), 0o755)
			case "env":
				path := macPath(f.d.root, "/etc/hypermesh/agent.env")
				file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				file.WriteString("HYPERMESH_IMAGE_HASH=changed\n")
				file.Close()
			case "identity":
				os.WriteFile(macPath(f.d.root, "/var/lib/hypermesh/device.json"), []byte(`{"device_id":"other"}`), 0o600)
			case "active-service":
				f.loaded["com.fyberlabs.hypermesh-agent"] = true
			case "plist":
				os.WriteFile(macPath(f.d.root, "/Library/LaunchDaemons/com.fyberlabs.hypermesh-agent.plist"), []byte("changed plist"), 0o644)
			case "non-apple":
				f.d.detect = func(context.Context) hardware.Profile {
					return hardware.Recommend(hardware.Facts{Arch: "arm64", Model: "Orin NX", Tegra: true, CUDA: true, MemoryBytes: 16 << 30, AvailableBytes: 10 << 30})
				}
			}
			creates := f.r.creates
			if code, _, _ := f.run("--start-services"); code == 0 {
				t.Fatal("unsafe registration accepted")
			}
			if f.r.creates != creates || f.r.completes != 0 || f.loaded["com.fyberlabs.hypermesh-wg"] {
				t.Fatal("side effects after refusal")
			}
		})
	}
}
func TestMacRegistrationFilesystemGuards(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "public", "directory", "unsafe-parent"} {
		t.Run(kind, func(t *testing.T) {
			f := newMacFixture(t)
			path := macPath(f.d.root, "/etc/hypermesh/agent.env")
			switch kind {
			case "symlink":
				os.Rename(path, path+".saved")
				os.Symlink(path+".saved", path)
			case "hardlink":
				os.Link(path, path+".linked")
			case "public":
				os.Chmod(path, 0o644)
			case "directory":
				os.Remove(path)
				os.Mkdir(path, 0o600)
			case "unsafe-parent":
				os.Chmod(filepath.Dir(path), 0o777)
			}
			if code, _, _ := f.run(); code == 0 || f.authorized != 0 {
				t.Fatal("unsafe input reached login")
			}
		})
	}
}
func TestMacRegistrationNoAgentBeforeWireGuardReady(t *testing.T) {
	f := newMacFixture(t)
	base := f.d.command
	f.d.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "/sbin/ifconfig" {
			return nil, macAbsent
		}
		return base(ctx, name, args...)
	}
	if code, _, _ := f.run("--start-services"); code == 0 {
		t.Fatal("missing utun reported ready")
	}
	if f.loaded["com.fyberlabs.hypermesh-agent"] || f.journal(t).State != "registered-inactive" {
		t.Fatal("agent started before WG or identity lost")
	}
}
func TestMacRegistrationSnapshotIgnoresTransientFreeMemory(t *testing.T) {
	f := newMacFixture(t)
	raw, _ := macRead(macPath(f.d.root, "/etc/hypermesh/agent.env"), 0o600, 2<<20)
	c, _ := macConfig(raw, macTestArgs[1], macTestArgs[3])
	before, _, err := macSnapshot(context.Background(), f.d, c)
	if err != nil {
		t.Fatal(err)
	}
	old := f.d.detect
	f.d.detect = func(ctx context.Context) hardware.Profile { p := old(ctx); p.ModelBudgetBytes /= 2; return p }
	after, _, err := macSnapshot(context.Background(), f.d, c)
	if err != nil || before != after {
		t.Fatal("transient memory changed journal binding")
	}
}
func TestMacConfigRefusesOverrides(t *testing.T) {
	f := newMacFixture(t)
	raw, _ := macRead(macPath(f.d.root, "/etc/hypermesh/agent.env"), 0o600, 2<<20)
	for _, line := range []string{"HYPERMESH_DEVICE_SECRET=other", "HYPERMESH_WG_KEY_DIR=/tmp/other", "HYPERMESH_REENROLL=1", "HYPERMESH_WG_IFACE=utun9", "HYPERMESH_HOST_NAME=another", "malformed"} {
		if _, err := macConfig(append(append([]byte{}, raw...), []byte(line+"\n")...), macTestArgs[1], macTestArgs[3]); err == nil {
			t.Fatal(line)
		}
	}
}
func TestMacRegistrationDeniedRestartRequiresServerAndFreshApproval(t *testing.T) {
	f := newMacFixture(t)
	f.d.wait = func(context.Context, time.Duration) error { f.r.state = "denied"; return nil }
	if code, _, _ := f.run(); code == 0 {
		t.Fatal("denial accepted")
	}
	old := f.journal(t)
	// The server-confirmed denied request is archived before replacement.
	f.r.request = hostregistration.Request{}
	f.r.state = "denied"
	f.d.wait = func(context.Context, time.Duration) error { return errors.New("new browser approval needed") }
	code, _, diagnostic := f.run("--restart-approval")
	if code == 0 {
		t.Fatal("restart bypassed approval")
	}
	paths, _ := filepath.Glob(filepath.Join(macPath(f.d.root, macRegistrationState), "registration-history-*.json"))
	if len(paths) != 1 {
		t.Fatal("history missing")
	}
	raw, _ := macRead(paths[0], 0o600, 2<<20)
	var archived registrationJournal
	json.Unmarshal(raw, &archived)
	if !reflect.DeepEqual(archived, old) {
		t.Fatal("history changed")
	}
	raw, _ = macRead(filepath.Join(macPath(f.d.root, macRegistrationState), "registration-"+old.PlanSHA256+".json"), 0o600, 2<<20)
	var next registrationJournal
	json.Unmarshal(raw, &next)
	if !strings.Contains(diagnostic, "Host setup URL: "+next.Dashboard+"/dashboard/hypermesh/host/connect/"+next.Request.RequestID+"\n") ||
		!strings.Contains(diagnostic, "Host approval URL: "+next.Dashboard+"/dashboard/hypermesh/host/approve/"+next.Request.RequestID+"\n") {
		t.Fatal("replacement request did not refresh browser handoff and approval fallback")
	}
	if next.State != "pending" || f.r.completes != 0 || next.PrivateKey == old.PrivateKey || next.Request.RequestID == old.Request.RequestID {
		t.Fatal("restart reused identity")
	}
}

func TestMacRegistrationChangedInstallationAfterCompletionKeepsRecoverableReceipt(t *testing.T) {
	f := newMacFixture(t)
	f.r.beforeComplete = func() {
		os.WriteFile(macPath(f.d.root, "/usr/local/bin/hypermesh-agent"), []byte("changed executable"), 0o755)
	}
	code, _, _ := f.run("--start-services")
	if code == 0 || f.r.completes != 1 || f.journal(t).State != "completed" {
		t.Fatal("completion receipt was not retained")
	}
	if _, err := os.Stat(macPath(f.d.root, "/var/lib/hypermesh/device.json")); !os.IsNotExist(err) {
		t.Fatal("identity published after installation changed")
	}
	if f.loaded["com.fyberlabs.hypermesh-wg"] || f.loaded["com.fyberlabs.hypermesh-agent"] {
		t.Fatal("service started after changed installation")
	}
}
func TestMacRegistrationPendingCannotBeRestarted(t *testing.T) {
	f := newMacFixture(t)
	f.d.wait = func(context.Context, time.Duration) error { return errors.New("interrupted") }
	if code, _, _ := f.run(); code == 0 {
		t.Fatal("expected pending interruption")
	}
	before := f.journal(t)
	if code, _, _ := f.run("--restart-approval"); code == 0 {
		t.Fatal("pending request replaced")
	}
	if !reflect.DeepEqual(before, f.journal(t)) {
		t.Fatal("pending request changed")
	}
}
