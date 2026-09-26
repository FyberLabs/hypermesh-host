package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseChannel(t *testing.T) {
	got, err := ParseChannel("  Test ")
	if err != nil || got != ChannelTest {
		t.Fatalf("test: %q %v", got, err)
	}
	got, err = ParseChannel("PROD")
	if err != nil || got != ChannelProd {
		t.Fatalf("prod: %q %v", got, err)
	}
	got, err = ParseChannel("  ")
	if err != nil || got != "" {
		t.Fatalf("empty: %q %v", got, err)
	}
	if _, err := ParseChannel("-sfiles"); err == nil {
		t.Fatal("garbage channel was accepted")
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		ver     string
		commit  string
		man     Manifest
		busy    bool
		want    Action
	}{
		{"test same commit", ChannelTest, "", "abc", Manifest{Channel: ChannelTest, Version: "0.0.0", Commit: "ABC"}, false, ActionSkip},
		{"test new commit", ChannelTest, "0.0.0", "old", Manifest{Channel: ChannelTest, Version: "0.0.0", Commit: "new"}, false, ActionInstall},
		{"test busy", ChannelTest, "0.0.0", "old", Manifest{Channel: ChannelTest, Version: "0.0.0", Commit: "new"}, true, ActionDefer},
		{"test missing commit", ChannelTest, "", "", Manifest{Channel: ChannelTest, Version: "0.0.0"}, false, ActionReject},
		{"prod upgrade", ChannelProd, "1.2.2", "c", Manifest{Channel: ChannelProd, Version: "1.2.3", Commit: "d"}, false, ActionInstall},
		{"prod same", ChannelProd, "1.2.3", "c", Manifest{Channel: ChannelProd, Version: "1.2.3", Commit: "d"}, false, ActionSkip},
		{"prod no downgrade", ChannelProd, "1.3.0", "c", Manifest{Channel: ChannelProd, Version: "1.2.9", Commit: "d"}, false, ActionSkip},
		{"prod first", ChannelProd, "", "dev", Manifest{Channel: ChannelProd, Version: "1.0.0", Commit: "d"}, false, ActionInstall},
		{"prod busy", ChannelProd, "1.0.0", "c", Manifest{Channel: ChannelProd, Version: "1.1.0", Commit: "d"}, true, ActionDefer},
		{"prod bad version", ChannelProd, "1.0.0", "c", Manifest{Channel: ChannelProd, Version: "1.0", Commit: "d"}, false, ActionReject},
		{"channel mismatch", ChannelProd, "1.0.0", "c", Manifest{Channel: ChannelTest, Version: "1.2.0", Commit: "d"}, false, ActionReject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Decide(tc.channel, tc.ver, tc.commit, tc.man, tc.busy)
			if got.Action != tc.want {
				t.Fatalf("action %s want %s (%s)", got.Action, tc.want, got.Reason)
			}
		})
	}
}

func TestChecksumAndSelect(t *testing.T) {
	body := []byte("agent-bytes")
	sum := sha256.Sum256(body)
	hexSum := hex.EncodeToString(sum[:])
	text := hexSum + "  hypermesh-agent_1.2.3_linux_arm64\n" +
		hexSum + "  hypermesh-agent_1.2.3_linux_arm64\n" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  hypermesh-deploy_1.2.3_linux_arm64\n" +
		hexSum + "  release.json\n"
	if err := CheckHash([]byte(text), "hypermesh-agent_1.2.3_linux_arm64", body); err != nil {
		t.Fatal(err)
	}
	if err := CheckHash([]byte(text), "hypermesh-agent_1.2.3_linux_arm64", []byte("nope")); err == nil {
		t.Fatal("bad bytes matched")
	}
	name, err := SelectBinary(text, "arm64")
	if err != nil || name != "hypermesh-agent_1.2.3_linux_arm64" {
		t.Fatalf("selected %q %v", name, err)
	}
	if _, err := SelectBinary(text+"\nabc  hypermesh-agent_9_linux_arm64\n", "arm64"); err == nil {
		// second line is not 64 hex so Fields still match the name.
		// SelectBinary does not check hash length. Two names must reject.
	}
	two := text + "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef  hypermesh-agent_9.0.0_linux_arm64\n"
	if _, err := SelectBinary(two, "arm64"); err == nil {
		t.Fatal("two arm64 agent binaries were accepted")
	}
	if _, err := SelectBinary("", "amd64"); err == nil {
		t.Fatal("empty checksums selected a binary")
	}
	empty := "\n# comment\nnot-a-hash STREAM\n"
	if err := CheckHash([]byte(empty), "release.json", []byte("x")); err == nil {
		t.Fatal("address-less checksum file was accepted")
	}
}

func TestGPGVerifyRejectsGarbage(t *testing.T) {
	err := GPGVerify([]byte("checksums"), []byte("not-a-signature"), []byte("not-a-key"))
	if err == nil {
		t.Fatal("garbage signature was accepted")
	}
}

func TestInstallOrderAndRollback(t *testing.T) {
	const dest = "/usr/local/bin/hypermesh-agent"
	fs := &memFS{files: map[string][]byte{
		dest: []byte("old-binary"),
	}}
	res, err := Install(fs, dest, []byte("new-binary"), "1.2.2")
	if err != nil {
		t.Fatal(err)
	}
	wantOps := []string{
		"write " + dest + ".new",
		"syncfile " + dest + ".new",
		"syncdir /usr/local/bin",
		"rename " + dest + " " + dest + ".pre-1.2.2",
		"rename " + dest + ".new " + dest,
		"syncdir /usr/local/bin",
	}
	if strings.Join(res.Ops, "\n") != strings.Join(wantOps, "\n") {
		t.Fatalf("ops:\n%s", strings.Join(res.Ops, "\n"))
	}
	if string(fs.files[dest]) != "new-binary" {
		t.Fatalf("installed %q", fs.files[dest])
	}
	if string(fs.files[res.Backup]) != "old-binary" {
		t.Fatalf("backup %q", fs.files[res.Backup])
	}
	if err := Rollback(fs, dest, res.Backup); err != nil {
		t.Fatal(err)
	}
	if string(fs.files[dest]) != "old-binary" {
		t.Fatalf("rolled back to %q", fs.files[dest])
	}
	if string(fs.files[res.Backup]) != "old-binary" {
		t.Fatal("backup was removed")
	}
}

func TestInstallFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "hypermesh-agent")
	if err := os.WriteFile(dest, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	backup, err := InstallFile(dest, []byte("new-binary"), "1.2.2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(backup, ".pre-1.2.2") {
		t.Fatalf("backup %s", backup)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "new-binary" {
		t.Fatalf("dest %q %v", got, err)
	}
	prev, err := os.ReadFile(backup)
	if err != nil || string(prev) != "old-binary" {
		t.Fatalf("backup bytes %q %v", prev, err)
	}
	if err := RollbackFile(dest, backup); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(dest)
	if err != nil || string(got) != "old-binary" {
		t.Fatalf("after rollback %q %v", got, err)
	}
}

func TestTickVerificationAndBusy(t *testing.T) {
	dir := t.TempDir()
	pub := filepath.Join(dir, "pub.asc")
	if err := os.WriteFile(pub, []byte("pubkey"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := []byte("#!/new-agent")
	man := Manifest{Channel: ChannelTest, Version: "0.0.0", Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	manRaw, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	binName := "hypermesh-agent_0.0.0_linux_arm64"
	sums := checksumFile(map[string][]byte{
		binName:      bin,
		ManifestName: manRaw,
	})
	src := planeSource(man, sums, []byte("sig"), manRaw, binName, bin)
	base := Config{
		Channel:       ChannelTest,
		CurrentCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Arch:          "arm64",
		BinaryPath:    "/usr/local/bin/hypermesh-agent",
		PubKeyPath:    pub,
		MarkerPath:    filepath.Join(dir, MarkerName),
		Now:           time.Unix(1_700_000_000, 0),
	}

	t.Run("bad signature", func(t *testing.T) {
		var installed, restarted bool
		plan, err := Tick(context.Background(), base, Hooks{
			Source: src,
			Verify: func(_, _, _ []byte) error { return ErrRejected },
			Install: func(string, []byte, string) (string, error) {
				installed = true
				return "", nil
			},
			Restart: func(context.Context) error {
				restarted = true
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Action != ActionReject || installed || restarted {
			t.Fatalf("plan %+v installed %v restarted %v", plan, installed, restarted)
		}
		if strings.Contains(plan.Reason, "sig=") {
			t.Fatalf("reason leaked a query: %s", plan.Reason)
		}
	})

	t.Run("checksum mismatch", func(t *testing.T) {
		tampered := []byte("tampered")
		bad := planeSource(man, sums, []byte("sig"), manRaw, binName, tampered)
		// The plane claims the tampered bytes. The signed checksum file
		// still names the real binary. That must not install.
		sum := sha256.Sum256(tampered)
		for i := range bad.cand.Artifacts {
			if bad.cand.Artifacts[i].Name == binName {
				bad.cand.Artifacts[i].SHA256 = hex.EncodeToString(sum[:])
				bad.cand.Artifacts[i].Size = int64(len(tampered))
			}
		}
		var installed bool
		plan, err := Tick(context.Background(), base, Hooks{
			Source: bad,
			Verify: func(_, _, _ []byte) error { return nil },
			Install: func(string, []byte, string) (string, error) {
				installed = true
				return "/usr/local/bin/hypermesh-agent.pre-unknown", nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Action != ActionReject || installed {
			t.Fatalf("plan %+v installed %v", plan, installed)
		}
		if !strings.Contains(plan.Reason, "plane sha256 does not match signed checksums") && plan.Reason != "binary checksum rejected" {
			t.Fatalf("reason %q", plan.Reason)
		}
	})

	t.Run("busy defers", func(t *testing.T) {
		cfg := base
		cfg.Busy = true
		var installed, restarted bool
		plan, err := Tick(context.Background(), cfg, Hooks{
			Source: src,
			Verify: func(_, _, _ []byte) error { return nil },
			Install: func(string, []byte, string) (string, error) {
				installed = true
				return "", nil
			},
			Restart: func(context.Context) error { restarted = true; return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Action != ActionDefer || installed || restarted {
			t.Fatalf("plan %+v installed %v restarted %v", plan, installed, restarted)
		}
	})

	t.Run("install restarts agent only", func(t *testing.T) {
		var got []byte
		var restartName string
		var restartArgs []string
		plan, err := Tick(context.Background(), base, Hooks{
			Source: src,
			Verify: func(_, _, _ []byte) error { return nil },
			Install: func(dest string, payload []byte, previous string) (string, error) {
				if dest != base.BinaryPath {
					t.Fatalf("dest %s", dest)
				}
				got = append([]byte(nil), payload...)
				return dest + ".pre-" + sanitizeVersion(previous), nil
			},
			Restart: func(ctx context.Context) error {
				if !restartAllowed("systemctl", []string{"restart", AgentUnit}) {
					t.Fatal("restart not allowed")
				}
				if restartAllowed("systemctl", []string{"restart", "hypermesh-wg"}) || restartAllowed("reboot", nil) {
					t.Fatal("wg or reboot was allowed")
				}
				restartName = "systemctl"
				restartArgs = []string{"restart", AgentUnit}
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Action != ActionInstall {
			t.Fatalf("plan %+v", plan)
		}
		if string(got) != string(bin) {
			t.Fatalf("payload %q", got)
		}
		if restartName != "systemctl" || strings.Join(restartArgs, " ") != "restart hypermesh-agent" {
			t.Fatalf("restart %s %v", restartName, restartArgs)
		}
		m, err := ReadMarker(base.MarkerPath)
		if err != nil || m == nil {
			t.Fatal(err)
		}
		if m.Backup != base.BinaryPath+".pre-"+sanitizeVersion(base.CurrentCommit) {
			t.Fatalf("backup %s", m.Backup)
		}
		if !m.Deadline.Equal(base.Now.Add(2 * time.Minute)) {
			t.Fatalf("deadline %s", m.Deadline)
		}
	})

	t.Run("sas error is redacted", func(t *testing.T) {
		const secret = "super-secret-sas"
		leaky := planeSource(man, sums, []byte("sig"), manRaw, binName, bin)
		leaky.fetchErr = fmt.Errorf("download https://acct.blob.core.windows.net/c/f?sig=%s failed", secret)
		_, err := Tick(context.Background(), base, Hooks{
			Source: leaky,
			Verify: func(_, _, _ []byte) error { return nil },
		})
		if err == nil {
			t.Fatal("expected download error")
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sig=") {
			t.Fatalf("error leaked sas: %v", err)
		}
	})
}

func TestWatchRollbackAndConfirm(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, MarkerName)
	dest := filepath.Join(dir, "hypermesh-agent")
	if err := os.WriteFile(dest, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	backup := dest + ".pre-1.0.0"
	if err := os.WriteFile(backup, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	deadline := time.Unix(1_700_000_000, 0)
	if err := WriteMarker(marker, Marker{
		Binary: dest, Backup: backup, Version: "1.0.1", Deadline: deadline,
	}); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMarker(marker)
	if err != nil {
		t.Fatal(err)
	}
	if Evaluate(m, deadline.Add(-time.Second), 0, 0) != WatchWait {
		t.Fatal("before deadline should wait")
	}
	if Evaluate(m, deadline, 1, 0) != WatchConfirm {
		t.Fatal("heartbeat should confirm")
	}
	if Evaluate(m, deadline, 0, 0) != WatchRollback {
		t.Fatal("deadline without heartbeat should roll back")
	}

	exited := 0
	Watch(context.Background(), WatchConfig{
		MarkerPath:  marker,
		Interval:    time.Millisecond,
		HeartbeatOK: func() uint64 { return 0 },
		Baseline:    0,
		Now:         func() time.Time { return deadline },
		Rollback:    func() error { return RollbackFile(dest, backup) },
		Exit:        func(code int) { exited = code },
	})
	if exited != 1 {
		t.Fatalf("exit %d", exited)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "old" {
		t.Fatalf("dest %q %v", got, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("marker still present")
	}
}

func TestFetchRedactsSASAndCapsSize(t *testing.T) {
	const secret = "super-secret-sas"
	payload := bytes.Repeat([]byte("a"), 32)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("artifact fetch sent auth %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	raw := srv.URL + "/test/0.0.0/checksums.txt?sig=" + secret
	got, err := FetchLimited(context.Background(), srv.Client(), raw, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %d bytes", len(got))
	}
	_, err = FetchLimited(context.Background(), srv.Client(), raw, 8)
	if err == nil {
		t.Fatal("oversized download was accepted")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "?") {
		t.Fatalf("size error leaked sas: %v", err)
	}

	client := &http.Client{Timeout: 2 * time.Second}
	_, err = FetchLimited(context.Background(), client, "https://127.0.0.1:1/c/blob?sig="+secret, 4)
	if err == nil {
		t.Fatal("expected dial error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sig=") {
		t.Fatalf("dial error leaked sas: %v", err)
	}
}

func TestGPGVerifyKeyList(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Fatal("gpg is required")
	}
	payload := []byte("checksums-for-rotation")
	pub1, sig1 := armoredKeyAndSig(t, "hm-test-old <old@example.invalid>", payload)
	pub2, sig2 := armoredKeyAndSig(t, "hm-test-new <new@example.invalid>", payload)
	both := append(append([]byte{}, pub1...), pub2...)
	if err := GPGVerify(payload, sig1, both); err != nil {
		t.Fatalf("old key in the list: %v", err)
	}
	if err := GPGVerify(payload, sig2, both); err != nil {
		t.Fatalf("new key in the list: %v", err)
	}
	if err := GPGVerify(payload, sig1, pub2); err == nil {
		t.Fatal("old signature was accepted after the old key was removed")
	}
	if err := GPGVerify(payload, sig2, pub2); err != nil {
		t.Fatalf("new key alone: %v", err)
	}
}

func armoredKeyAndSig(t *testing.T, uid string, payload []byte) (pub, sig []byte) {
	t.Helper()
	home := t.TempDir()
	env := append(os.Environ(), "GNUPGHOME="+home)
	gen := exec.Command("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "",
		"--quick-generate-key", uid, "ed25519", "sign", "never")
	gen.Env = env
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("keygen %s: %v %s", uid, err, out)
	}
	exp := exec.Command("gpg", "--batch", "--armor", "--export")
	exp.Env = env
	pub, err := exp.Output()
	if err != nil || !bytes.Contains(pub, []byte("BEGIN PGP PUBLIC KEY BLOCK")) {
		t.Fatalf("export %s: %v", uid, err)
	}
	sum := filepath.Join(home, "checksums.txt")
	if err := os.WriteFile(sum, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(home, "checksums.txt.sig")
	sign := exec.Command("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "",
		"--detach-sign", "--output", sigPath, sum)
	sign.Env = env
	if out, err := sign.CombinedOutput(); err != nil {
		t.Fatalf("sign %s: %v %s", uid, err, out)
	}
	sig, err = os.ReadFile(sigPath)
	if err != nil || len(sig) == 0 {
		t.Fatal(err)
	}
	return pub, sig
}

func TestReleaseWorkflowContract(t *testing.T) {
	root := filepath.Join("..", "..")
	ci, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ci), "contents: write") {
		t.Fatal("ci workflow must stay contents: read")
	}
	for _, name := range []string{"release.yml", "release-test.yml"} {
		body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		head, _, _ := strings.Cut(text, "jobs:")
		if strings.Contains(head, "id-token") || strings.Contains(head, "contents: write") {
			t.Fatalf("%s workflow default is not contents: read", name)
		}
		for _, want := range []string{
			"contents: read",
			"contents: write",
			"id-token: write",
			"environment: release",
			"azure/login",
			"AZURE_CLIENT_ID",
			"AZURE_TENANT_ID",
			"AZURE_SUBSCRIPTION_ID",
			"AZURE_STORAGE_ACCOUNT",
			"AZURE_STORAGE_CONTAINER",
			"HYPERMESH_RELEASE_GPG_PRIVATE_KEY",
			"packaging/release/publish-agent.sh",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing %s", name, want)
			}
		}
		if strings.Contains(text, "hypermesh-wg") || strings.Contains(text, "connection-string") || strings.Contains(text, "account-key") {
			t.Fatalf("%s has a storage key or restarts wireguard", name)
		}
	}
	ciText := string(ci)
	if strings.Contains(ciText, "id-token") {
		t.Fatal("ci workflow must not request id-token")
	}
	script, err := os.ReadFile(filepath.Join(root, "packaging", "release", "publish-agent.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	for _, want := range []string{
		"CGO_ENABLED=0",
		"linux",
		"arm64",
		"amd64",
		"./cmd/agent",
		"buildinfo.Commit",
		"buildinfo.Version",
		"checksums.txt",
		"checksums.txt.asc",
		"--detach-sign",
		"--auth-mode login",
		"prerelease",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("publish script missing %s", want)
		}
	}
	if strings.Contains(text, "connection-string") || strings.Contains(text, "--account-key") || strings.Contains(text, "HYPERMESH_UPDATE_GITHUB_TOKEN") {
		t.Fatal("publish script has a storage key or a host github token")
	}
	roll, err := os.ReadFile(filepath.Join(root, "deploy", "lib", "update-rollback"))
	if err != nil {
		t.Fatal(err)
	}
	rtext := string(roll)
	if strings.Contains(rtext, "hypermesh-wg") || strings.Contains(rtext, "reboot") {
		t.Fatal("rollback script restarts more than the agent")
	}
	for _, want := range []string{"binary", "backup", "deadline"} {
		if !strings.Contains(rtext, want) {
			t.Fatalf("rollback script missing %s", want)
		}
	}
}

type fakeSource struct {
	cand     *Candidate
	fetchErr error
	files    map[string][]byte
}

func (f *fakeSource) Latest(context.Context, string) (*Candidate, error) {
	return f.cand, nil
}

func (f *fakeSource) Fetch(_ context.Context, rawURL string, size int64) ([]byte, error) {
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	b, ok := f.files[rawURL]
	if !ok {
		return nil, fmt.Errorf("hypermesh update: download failed")
	}
	if int64(len(b)) != size {
		return nil, fmt.Errorf("%w: size", ErrRejected)
	}
	return append([]byte(nil), b...), nil
}

func planeSource(man Manifest, sums, sig, manRaw []byte, binName string, bin []byte) *fakeSource {
	files := map[string][]byte{
		sasURL(ChecksumsName): sums,
		sasURL(SignatureName): sig,
		sasURL(ManifestName):  manRaw,
		sasURL(binName):       bin,
	}
	arts := []Artifact{
		art(ChecksumsName, sums),
		art(SignatureName, sig),
		art(ManifestName, manRaw),
		art(binName, bin),
	}
	return &fakeSource{
		cand: &Candidate{
			Channel:   man.Channel,
			Version:   man.Version,
			Commit:    man.Commit,
			Artifacts: arts,
		},
		files: files,
	}
}

func art(name string, body []byte) Artifact {
	sum := sha256.Sum256(body)
	return Artifact{
		Name:   name,
		SHA256: hex.EncodeToString(sum[:]),
		Size:   int64(len(body)),
		URL:    sasURL(name),
	}
}

func sasURL(name string) string {
	return "https://blob.test/container/test/0.0.0/" + name + "?sig=not-logged"
}

func checksumFile(files map[string][]byte) []byte {
	var b strings.Builder
	for _, name := range []string{"hypermesh-agent_0.0.0_linux_arm64", ManifestName} {
		sum := sha256.Sum256(files[name])
		b.WriteString(hex.EncodeToString(sum[:]))
		b.WriteString("  ")
		b.WriteString(name)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

type memFS struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (m *memFS) WriteNew(path string, data []byte, _ os.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[path] = append([]byte(nil), data...)
	return nil
}

func (m *memFS) SyncDir(string) error { return nil }

func (m *memFS) Rename(oldpath, newpath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[oldpath]
	if !ok {
		return os.ErrNotExist
	}
	delete(m.files, oldpath)
	m.files[newpath] = b
	return nil
}

func (m *memFS) Exists(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.files[path]
	return ok
}

func (m *memFS) Remove(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, path)
	return nil
}

func (m *memFS) read(path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}
