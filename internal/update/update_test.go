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
	newer := func(ch, ver, commit string) Manifest {
		return Manifest{Channel: ch, Version: ver, Commit: commit, Build: 2}
	}
	cases := []struct {
		name     string
		channel  string
		ver      string
		commit   string
		curBuild int
		badBuild int
		man      Manifest
		busy     bool
		want     Action
	}{
		{"test same commit", ChannelTest, "", "abc", 1, 0, newer(ChannelTest, "0.0.0", "ABC"), false, ActionSkip},
		{"test new commit", ChannelTest, "0.0.0", "old", 1, 0, newer(ChannelTest, "0.0.0", "new"), false, ActionInstall},
		{"test busy", ChannelTest, "0.0.0", "old", 1, 0, newer(ChannelTest, "0.0.0", "new"), true, ActionDefer},
		{"test missing commit", ChannelTest, "", "", 1, 0, newer(ChannelTest, "0.0.0", ""), false, ActionReject},
		{"prod upgrade", ChannelProd, "1.2.2", "c", 1, 0, newer(ChannelProd, "1.2.3", "d"), false, ActionInstall},
		{"prod same", ChannelProd, "1.2.3", "c", 1, 0, newer(ChannelProd, "1.2.3", "d"), false, ActionSkip},
		{"prod no downgrade", ChannelProd, "1.3.0", "c", 1, 0, newer(ChannelProd, "1.2.9", "d"), false, ActionSkip},
		{"prod first", ChannelProd, "", "dev", 0, 0, Manifest{Channel: ChannelProd, Version: "1.0.0", Commit: "d", Build: 1}, false, ActionInstall},
		{"prod busy", ChannelProd, "1.0.0", "c", 1, 0, newer(ChannelProd, "1.1.0", "d"), true, ActionDefer},
		{"prod bad version", ChannelProd, "1.0.0", "c", 1, 0, newer(ChannelProd, "1.0", "d"), false, ActionReject},
		{"channel mismatch", ChannelProd, "1.0.0", "c", 1, 0, Manifest{Channel: ChannelTest, Version: "1.2.0", Commit: "d", Build: 9}, false, ActionReject},
		{"older build", ChannelTest, "0.0.0", "old", 5, 0, Manifest{Channel: ChannelTest, Version: "0.0.0", Commit: "new", Build: 4}, false, ActionReject},
		{"equal build", ChannelTest, "0.0.0", "old", 5, 0, Manifest{Channel: ChannelTest, Version: "0.0.0", Commit: "new", Build: 5}, false, ActionReject},
		{"switch test to newer prod", ChannelProd, "0.0.0", "testcommit", 57, 0, Manifest{Channel: ChannelProd, Version: "1.2.3", Commit: "prodcommit", Build: 58}, false, ActionInstall},
		{"replayed equal prod build", ChannelProd, "0.0.0", "testcommit", 57, 0, Manifest{Channel: ChannelProd, Version: "1.2.3", Commit: "prodcommit", Build: 57}, false, ActionReject},
		{"replayed lower prod build", ChannelProd, "0.0.0", "testcommit", 57, 0, Manifest{Channel: ChannelProd, Version: "1.2.3", Commit: "prodcommit", Build: 3}, false, ActionReject},
		{"zero build", ChannelTest, "0.0.0", "old", 0, 0, Manifest{Channel: ChannelTest, Version: "0.0.0", Commit: "new"}, false, ActionReject},
		{"rolled back build", ChannelTest, "0.0.0", "old", 3, 5, Manifest{Channel: ChannelTest, Version: "0.0.0", Commit: "bad", Build: 5}, false, ActionSkip},
		{"after rolled back build", ChannelTest, "0.0.0", "old", 3, 5, Manifest{Channel: ChannelTest, Version: "0.0.0", Commit: "new", Build: 6}, false, ActionInstall},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Decide(tc.channel, tc.ver, tc.commit, tc.curBuild, tc.badBuild, tc.man, tc.busy)
			if got.Action != tc.want {
				t.Fatalf("action %s want %s (%s)", got.Action, tc.want, got.Reason)
			}
			if tc.want != ActionInstall && got.Action == ActionInstall {
				t.Fatal("refused build was installed")
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
	man := Manifest{Channel: ChannelTest, Version: "0.0.0", Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Build: 2}
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
	if _, err := os.Stat(filepath.Join(dir, BadName)); !os.IsNotExist(err) {
		t.Fatal("build 0 was recorded as a rolled-back build")
	}
}

func TestRollbackSkipPersists(t *testing.T) {
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
		Build: 9, ReleaseID: "1.0.1",
	}); err != nil {
		t.Fatal(err)
	}
	Watch(context.Background(), WatchConfig{
		MarkerPath:  marker,
		Interval:    time.Millisecond,
		HeartbeatOK: func() uint64 { return 0 },
		Baseline:    0,
		Now:         func() time.Time { return deadline },
		Rollback:    func() error { return RollbackFile(dest, backup) },
		Exit:        func(int) {},
	})
	got, err := ReadBadBuild(filepath.Join(dir, BadName))
	if err != nil || got != 9 {
		t.Fatalf("bad build %d %v", got, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, BadName))
	if err != nil || !strings.Contains(string(body), "release_id=1.0.1") {
		t.Fatalf("bad file %q %v", body, err)
	}
	plan := Decide(ChannelTest, "0.0.0", "old", 3, got, Manifest{
		Channel: ChannelTest, Version: "0.0.0", Commit: "rolled", Build: 9,
	}, false)
	if plan.Action != ActionSkip {
		t.Fatalf("rolled-back build action %s", plan.Action)
	}
	plan = Decide(ChannelTest, "0.0.0", "old", 3, got, Manifest{
		Channel: ChannelTest, Version: "0.0.0", Commit: "newer", Build: 10,
	}, false)
	if plan.Action != ActionInstall {
		t.Fatalf("newer than rolled-back build action %s", plan.Action)
	}

	// The ExecStartPre path records the same file.
	dir2 := t.TempDir()
	bin := filepath.Join(dir2, "hypermesh-agent")
	prev := bin + ".pre-1.0.0"
	if err := os.WriteFile(bin, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prev, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	pending := fmt.Sprintf("binary=%s\nbackup=%s\nversion=1.0.1\ndeadline=1\nbuild=9\nrelease_id=1.0.1\n", bin, prev)
	if err := os.WriteFile(filepath.Join(dir2, MarkerName), []byte(pending), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("..", "..", "deploy", "lib", "update-rollback")
	cmd := exec.Command("/bin/sh", script)
	cmd.Env = append(os.Environ(), "HYPERMESH_STATE_DIR="+dir2)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rollback script: %v %s", err, out)
	}
	restored, err := os.ReadFile(bin)
	if err != nil || string(restored) != "old" {
		t.Fatalf("script restored %q %v", restored, err)
	}
	scriptBad, err := os.ReadFile(filepath.Join(dir2, BadName))
	if err != nil || !strings.Contains(string(scriptBad), "build=9") {
		t.Fatalf("script bad file %q %v", scriptBad, err)
	}
	if _, err := os.Stat(filepath.Join(dir2, MarkerName)); !os.IsNotExist(err) {
		t.Fatal("script left the marker")
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

func TestValidSigFingerprint(t *testing.T) {
	const trustedFP = "0123456789ABCDEF0123456789ABCDEF01234567"
	const otherFP = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	trusted := map[string]struct{}{trustedFP: {}}
	valid := "[GNUPG:] GOODSIG " + trustedFP + " Hypermesh\n" +
		"[GNUPG:] VALIDSIG " + trustedFP + " 2026-09-26 1 0 1 0 1 10 0 " + trustedFP + "\n"
	if !signatureTrusted(valid, trusted) {
		t.Fatal("VALIDSIG from a trusted fingerprint was rejected")
	}
	goodOnly := "[GNUPG:] GOODSIG " + trustedFP + " Hypermesh\n"
	if signatureTrusted(goodOnly, trusted) {
		t.Fatal("GOODSIG without VALIDSIG was accepted")
	}
	untrusted := "[GNUPG:] VALIDSIG " + otherFP + " 2026-09-26 1 0 1 0 1 10 0 " + otherFP + "\n"
	if signatureTrusted(untrusted, trusted) {
		t.Fatal("VALIDSIG from an untrusted fingerprint was accepted")
	}
	primary := "[GNUPG:] VALIDSIG " + otherFP + " 2026-09-26 1 0 1 0 1 10 0 " + trustedFP + "\n"
	if !signatureTrusted(primary, trusted) {
		t.Fatal("VALIDSIG whose primary key is trusted was rejected")
	}
	if signatureTrusted(valid, nil) {
		t.Fatal("empty trust set accepted a signature")
	}
	colons := "pub:u:255:1:1:1:::u:::sc\nfpr:::::::::0123456789abcdef0123456789abcdef01234567:\n"
	got := parseColonFingerprints(colons)
	if _, ok := got[trustedFP]; !ok {
		t.Fatalf("fingerprints %v", got)
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
	if _, err := os.Stat(filepath.Join(root, ".github", "workflows", "release-test.yml")); !os.IsNotExist(err) {
		t.Fatal("a second release workflow would give test and prod different build numbers")
	}
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	head, _, _ := strings.Cut(text, "jobs:")
	if strings.Contains(head, "id-token") || strings.Contains(head, "contents: write") {
		t.Fatal("release workflow default is not contents: read")
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
		"allow-no-subscriptions: true",
		"HYPERMESH_RELEASE_GPG_PRIVATE_KEY",
		"packaging/release/publish-agent.sh",
		"- main",
		`v[0-9]+.[0-9]+.[0-9]+`,
		"cancel-in-progress: false",
		"group: release-publish",
		"AZURE_CONFIG_DIR: ${{ runner.temp }}/az",
		"az logout",
		"if: always()",
		"if: github.ref_type == 'tag'",
		"git fetch origin main",
		"git merge-base --is-ancestor HEAD origin/main",
		"one sequence across channels",
		"actions/checkout@11d5960a326750d5838078e36cf38b85af677262",
		"actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e",
		"azure/login@7184910d9eb2b1c5e48f7073824a90609bb9b6d6",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("release.yml missing %s", want)
		}
	}
	if strings.Contains(text, "hypermesh-wg") || strings.Contains(text, "connection-string") || strings.Contains(text, "account-key") || strings.Contains(text, "stfyberhmhost") {
		t.Fatal("release.yml has a storage key, a hardcoded account, or restarts wireguard")
	}
	if strings.Contains(text, "next_build") || strings.Contains(text, "group: release-test") || strings.Contains(text, "group: release-prod") {
		t.Fatal("build max+1 or a per-channel group escaped the release-publish lock")
	}
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "uses:") && strings.Contains(trim, "@v") {
			t.Fatalf("release.yml has an unpinned action: %s", trim)
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
	text = string(script)
	for _, want := range []string{
		"CGO_ENABLED=0",
		"linux",
		"arm64",
		"amd64",
		"./cmd/agent",
		"buildinfo.Commit",
		"buildinfo.Version",
		"buildinfo.Build",
		"GITHUB_RUN_NUMBER",
		"one sequence across channels",
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
	if strings.Contains(text, "stfyberhmhost") || strings.Contains(text, "--container-name hypermesh-host") || strings.Contains(text, "current.json") || strings.Contains(text, "test/0.0.0") {
		t.Fatal("publish script hardcodes storage names or overwrites test/0.0.0")
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
	failURL  string
	files    map[string][]byte
}

func (f *fakeSource) Latest(context.Context, string) (*Candidate, error) {
	return f.cand, nil
}

func (f *fakeSource) Fetch(_ context.Context, rawURL string, size int64) ([]byte, error) {
	if f.failURL != "" && rawURL == f.failURL {
		return nil, fmt.Errorf("hypermesh update: download %s status 404", RedactQuery(rawURL))
	}
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
			ReleaseID: man.ReleaseID,
			Build:     man.Build,
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
	return "https://blob.test/container/test/20260926120000-bbbbbbbbbbbb/" + name + "?sig=not-logged"
}

func TestNextBuild(t *testing.T) {
	layout := filepath.Join("..", "..", "packaging", "release", "layout.sh")
	call := func(testBuild, prodBuild, run string) (string, error) {
		t.Helper()
		out, err := exec.Command("bash", "-c", `source "$1"; next_build "$2" "$3" "$4"`, "bash", layout, testBuild, prodBuild, run).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	got, err := call("", "", "5")
	if err != nil || got != "6" {
		t.Fatalf("both indexes missing: %q %v", got, err)
	}
	got, err = call("", "4", "5")
	if err != nil || got != "6" {
		t.Fatalf("test index missing: %q %v", got, err)
	}
	got, err = call("57", "", "3")
	if err != nil || got != "58" {
		t.Fatalf("prod index missing: %q %v", got, err)
	}
	got, err = call("57", "40", "60")
	if err != nil || got != "61" {
		t.Fatalf("run number floor: %q %v", got, err)
	}
	got, err = call("57", "58", "3")
	if err != nil || got != "59" {
		t.Fatalf("index above run number: %q %v", got, err)
	}
	if _, err := call("", "", ""); err == nil {
		t.Fatal("no inputs was accepted")
	}
}

func TestReleaseIDAndManifestOrder(t *testing.T) {
	layout := filepath.Join("..", "..", "packaging", "release", "layout.sh")
	id := func(fn string, args ...string) string {
		t.Helper()
		cmdArgs := append([]string{"-c", `source "$1"; "$2" "$3" "$4"`, "bash", layout, fn}, args...)
		// release_id_prod takes one argument. Pass an empty fourth.
		for len(cmdArgs) < 7 {
			cmdArgs = append(cmdArgs, "")
		}
		out, err := exec.Command("bash", cmdArgs...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v %s", fn, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	sha := "0123456789abcdef0123456789abcdef01234567"
	early := id("release_id_test", "9", sha)
	later := id("release_id_test", "10", sha)
	if early != "9-0123456789ab" || later != "10-0123456789ab" {
		t.Fatalf("ids %s %s", early, later)
	}
	if _, err := exec.Command("bash", "-c", `source "$1"; release_id_test "$2" "$3"`, "bash", layout, "2026-09-26", sha).CombinedOutput(); err == nil {
		t.Fatal("test release id accepted a timestamp")
	}
	if id("release_id_prod", "1.2.3") != "1.2.3" {
		t.Fatal("prod release id")
	}
	if _, err := exec.Command("bash", "-c", `source "$1"; release_id_prod "$2"`, "bash", layout, "v1.2.3").CombinedOutput(); err == nil {
		t.Fatal("prod release id accepted a leading v")
	}
	pointer := func(args ...string) error {
		t.Helper()
		cmdArgs := []string{"-c", `source "$1"; prod_pointer_ok "$2" "$3" "$4" "$5"`, "bash", layout}
		cmdArgs = append(cmdArgs, args...)
		for len(cmdArgs) < 9 {
			cmdArgs = append(cmdArgs, "")
		}
		out, err := exec.Command("bash", cmdArgs...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s", strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := pointer("1.0.0", "1", "", ""); err != nil {
		t.Fatalf("first prod publish: %v", err)
	}
	if err := pointer("1.2.4", "10", "1.2.3", "9"); err != nil {
		t.Fatalf("newer prod publish: %v", err)
	}
	if err := pointer("1.2.3", "9", "1.2.3", "9"); err == nil {
		t.Fatal("equal prod build was accepted")
	}
	if err := pointer("1.2.4", "8", "1.2.3", "9"); err == nil {
		t.Fatal("older prod build was accepted")
	}
	if err := pointer("1.2.3", "20", "1.2.3", "9"); err == nil {
		t.Fatal("same semver with a higher build was accepted")
	}
	if err := pointer("1.2.2", "20", "1.2.3", "9"); err == nil {
		t.Fatal("older semver with a higher build was accepted")
	}

	script, err := os.ReadFile(filepath.Join("..", "..", "packaging", "release", "publish-agent.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	pointerAt := strings.Index(text, "# publish-order: prod-pointer")
	artifacts := strings.Index(text, "# publish-order: artifacts")
	verify := strings.Index(text, "# publish-order: verify")
	manifest := strings.Index(text, "# publish-order: channel-manifest")
	if pointerAt < 0 || artifacts < pointerAt || verify < artifacts || manifest < verify {
		t.Fatalf("publish order pointer=%d artifacts=%d verify=%d manifest=%d", pointerAt, artifacts, verify, manifest)
	}
	if !strings.Contains(text, "prod_pointer_ok") || !strings.Contains(text, "channels/prod.json") {
		t.Fatal("prod pointer check is missing")
	}
	if !strings.Contains(text, "channels/${channel}.json") || !strings.Contains(text, "--overwrite false") {
		t.Fatal("channel manifest path or immutable upload is missing")
	}
	if !strings.Contains(text, `release_id=$(release_id_test "$build" "$GITHUB_SHA")`) {
		t.Fatal("test release id is not <build>-<shortsha>")
	}
	buildAt := strings.Index(text, "# publish-order: build")
	ldflagsAt := strings.Index(text, "buildinfo.Build=${build}")
	if buildAt < 0 || ldflagsAt < buildAt {
		t.Fatal("build max+1 is not computed before it is baked into the binary")
	}
	if !strings.Contains(text, `build=$(next_build "$test_build" "$prod_build" "$run_number")`) {
		t.Fatal("published build is not max(test, prod, run number)+1")
	}
	if !strings.Contains(text, "read_index_build test") || !strings.Contains(text, "read_index_build prod") {
		t.Fatal("publisher does not read both channel indexes")
	}
	if strings.Contains(text, "build=${GITHUB_RUN_NUMBER") {
		t.Fatal("published build is the raw run number")
	}
	start := strings.Index(text, "write_channel_manifest()")
	end := strings.Index(text, "# Single Put Blob")
	if start < 0 || end < start {
		t.Fatal("channel manifest writer is missing")
	}
	block := text[start:end]
	if strings.Contains(block, `"path"`) || strings.Contains(block, `"url"`) {
		t.Fatal("channel index must not carry path or url")
	}
	for _, key := range []string{`"channel"`, `"version"`, `"commit"`, `"release_id"`, `"build"`, `"name"`, `"sha256"`, `"size"`} {
		if !strings.Contains(block, key) {
			t.Fatalf("channel index missing %s", key)
		}
	}
}

func TestStaleBlobDoesNotInstall(t *testing.T) {
	dir := t.TempDir()
	pub := filepath.Join(dir, "pub.asc")
	if err := os.WriteFile(pub, []byte("pubkey"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := []byte("#!/new-agent")
	man := Manifest{Channel: ChannelTest, Version: "0.0.0", Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Build: 2}
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
	src.failURL = sasURL(binName)
	var installed bool
	plan, err := Tick(context.Background(), Config{
		Channel:       ChannelTest,
		CurrentCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Arch:          "arm64",
		BinaryPath:    "/usr/local/bin/hypermesh-agent",
		PubKeyPath:    pub,
		MarkerPath:    filepath.Join(dir, MarkerName),
	}, Hooks{
		Source: src,
		Verify: func(_, _, _ []byte) error { return nil },
		Install: func(string, []byte, string) (string, error) {
			installed = true
			return "", nil
		},
	})
	if installed || plan.Action == ActionInstall {
		t.Fatalf("installed from a missing blob: %+v", plan)
	}
	if err == nil {
		t.Fatalf("missing blob was not a retryable failure: %+v", plan)
	}
	if strings.Contains(err.Error(), "sig=") || strings.Contains(err.Error(), "not-logged") {
		t.Fatalf("error leaked sas: %v", err)
	}
}

func TestPlaneBuildMustMatchSigned(t *testing.T) {
	dir := t.TempDir()
	pub := filepath.Join(dir, "pub.asc")
	if err := os.WriteFile(pub, []byte("pubkey"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := []byte("#!/new-agent")
	man := Manifest{
		Channel: ChannelTest, Version: "0.0.0", Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ReleaseID: "2-bbbbbbbbbbbb", Build: 2,
	}
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
	src.cand.Build = 9
	var installed bool
	plan, err := Tick(context.Background(), Config{
		Channel:       ChannelTest,
		CurrentCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Arch:          "arm64",
		BinaryPath:    "/usr/local/bin/hypermesh-agent",
		PubKeyPath:    pub,
		MarkerPath:    filepath.Join(dir, MarkerName),
	}, Hooks{
		Source: src,
		Verify: func(_, _, _ []byte) error { return nil },
		Install: func(string, []byte, string) (string, error) {
			installed = true
			return "", nil
		},
	})
	if err != nil || installed || plan.Action != ActionReject {
		t.Fatalf("mismatched build installed: %+v %v", plan, err)
	}
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
