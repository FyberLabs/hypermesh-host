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
		{"up to date", ChannelTest, "0.0.0", "abc", 5, 0, Manifest{Channel: ChannelTest, Version: "0.0.0", Commit: "ABC", Build: 5}, false, ActionSkip},
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
			if tc.name == "up to date" && got.Reason != "up to date" {
				t.Fatalf("reason %q", got.Reason)
			}
			if tc.name == "equal build" && got.Reason != "build is not newer" {
				t.Fatalf("reason %q", got.Reason)
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
	err := GPGVerify([]byte("checksums"), []byte("not-a-signature"), []byte("not-a-key"), []string{"0123456789ABCDEF0123456789ABCDEF01234567"})
	if err == nil {
		t.Fatal("garbage signature was accepted")
	}
	if err := GPGVerify([]byte("checksums"), []byte("not-a-signature"), []byte("not-a-key"), nil); err == nil {
		t.Fatal("empty fingerprint list was accepted")
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

	t.Run("busy after install does not restart", func(t *testing.T) {
		var calls int
		var installed, restarted, rolled bool
		plan, err := Tick(context.Background(), base, Hooks{
			Source: src,
			Verify: func(_, _, _ []byte) error { return nil },
			StillBusy: func() bool {
				calls++
				return calls > 1
			},
			Install: func(dest string, payload []byte, previous string) (string, error) {
				installed = true
				if dest != base.BinaryPath {
					t.Fatalf("dest %s", dest)
				}
				return dest + ".pre-test", nil
			},
			Rollback: func(dest, backup string) error {
				rolled = true
				if dest != base.BinaryPath || backup != dest+".pre-test" {
					t.Fatalf("rollback %s %s", dest, backup)
				}
				return nil
			},
			Restart: func(context.Context) error {
				restarted = true
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if calls != 2 || plan.Action != ActionDefer || plan.Reason != "busy" || !installed || !rolled || restarted {
			t.Fatalf("calls %d plan %+v installed %v rolled %v restarted %v", calls, plan, installed, rolled, restarted)
		}
		m, err := ReadMarker(base.MarkerPath)
		if err != nil || m != nil {
			t.Fatalf("marker %v %v", m, err)
		}
	})

	t.Run("busy before restart defers", func(t *testing.T) {
		var installed, restarted bool
		plan, err := Tick(context.Background(), base, Hooks{
			Source:    src,
			Verify:    func(_, _, _ []byte) error { return nil },
			StillBusy: func() bool { return true },
			Install: func(string, []byte, string) (string, error) {
				installed = true
				return "", nil
			},
			Restart: func(context.Context) error { restarted = true; return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Action != ActionDefer || plan.Reason != "busy" || installed || restarted {
			t.Fatalf("plan %+v installed %v restarted %v", plan, installed, restarted)
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
				if !restartAllowed("systemctl", []string{"--no-block", "restart", AgentUnit}) {
					t.Fatal("restart not allowed")
				}
				if restartAllowed("systemctl", []string{"restart", "hypermesh-wg"}) || restartAllowed("reboot", nil) {
					t.Fatal("wg or reboot was allowed")
				}
				restartName = "systemctl"
				restartArgs = []string{"--no-block", "restart", AgentUnit}
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
		if restartName != "systemctl" || strings.Join(restartArgs, " ") != "--no-block restart hypermesh-agent" {
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
	pub1, sig1, fp1 := armoredKeyAndSig(t, "hm-test-old <old@example.invalid>", payload)
	pub2, sig2, fp2 := armoredKeyAndSig(t, "hm-test-new <new@example.invalid>", payload)
	both := append(append([]byte{}, pub1...), pub2...)
	if err := GPGVerify(payload, sig1, both, []string{fp1, fp2}); err != nil {
		t.Fatalf("old key in the list: %v", err)
	}
	if err := GPGVerify(payload, sig2, both, []string{fp1, fp2}); err != nil {
		t.Fatalf("new key in the list: %v", err)
	}
	if err := GPGVerify(payload, sig1, pub2, []string{fp2}); err == nil {
		t.Fatal("old signature was accepted after the old key was removed")
	}
	if err := GPGVerify(payload, sig2, pub2, []string{fp2}); err != nil {
		t.Fatalf("new key alone: %v", err)
	}
	if err := GPGVerify(payload, sig1, both, []string{fp2}); err == nil {
		t.Fatal("signature from a key that is only in the keyring was accepted")
	}
	if err := GPGVerify(payload, sig2, both, nil); err == nil {
		t.Fatal("empty fingerprint list was accepted")
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
	primary := "[GNUPG:] GOODSIG " + trustedFP + " Hypermesh\n" +
		"[GNUPG:] VALIDSIG " + otherFP + " 2026-09-26 1 0 1 0 1 10 0 " + trustedFP + "\n"
	if !signatureTrusted(primary, trusted) {
		t.Fatal("VALIDSIG whose primary key is trusted was rejected")
	}
	if signatureTrusted(valid, nil) {
		t.Fatal("empty trust set accepted a signature")
	}
	validNoGood := "[GNUPG:] VALIDSIG " + trustedFP + " 2026-09-26 1 0 1 0 1 10 0 " + trustedFP + "\n"
	if signatureTrusted(validNoGood, trusted) {
		t.Fatal("VALIDSIG without GOODSIG was accepted")
	}
	for _, tok := range []string{"EXPKEYSIG", "REVKEYSIG", "EXPSIG", "BADSIG", "ERRSIG"} {
		bad := valid + "[GNUPG:] " + tok + " " + trustedFP + " Hypermesh\n"
		if signatureTrusted(bad, trusted) {
			t.Fatalf("%s was accepted", tok)
		}
	}
	colons := "pub:u:255:1:1:1:::u:::sc\nfpr:::::::::0123456789abcdef0123456789abcdef01234567:\n"
	got := parseColonFingerprints(colons)
	if _, ok := got[trustedFP]; !ok {
		t.Fatalf("fingerprints %v", got)
	}
	if !signingKeyUsable(colons, trustedFP, time.Unix(2, 0)) {
		t.Fatal("key with no expiry was rejected")
	}
	expired := "pub:e:255:22:ABCD:1600000000:1600000001::u:::scESC:::::ed25519:::\nfpr:::::::::" + trustedFP + ":\n"
	if signingKeyUsable(expired, trustedFP, time.Unix(1700000000, 0)) {
		t.Fatal("expired key was usable")
	}
	revoked := "pub:r:255:22:ABCD:1600000000::u:::scESC:::::ed25519:::\nfpr:::::::::" + trustedFP + ":\n"
	if signingKeyUsable(revoked, trustedFP, time.Unix(1700000000, 0)) {
		t.Fatal("revoked key was usable")
	}
	past := "pub:u:255:22:ABCD:1600000000:1600000001::u:::scESC:::::ed25519:::\nfpr:::::::::" + trustedFP + ":\n"
	if signingKeyUsable(past, trustedFP, time.Unix(1700000000, 0)) {
		t.Fatal("key past its expiry timestamp was usable")
	}
	if signingKeyUsable(colons, otherFP, time.Unix(2, 0)) {
		t.Fatal("missing key record was usable")
	}
}

func armoredKeyAndSig(t *testing.T, uid string, payload []byte) (pub, sig []byte, fp string) {
	t.Helper()
	home := t.TempDir()
	env := append(os.Environ(), "GNUPGHOME="+home)
	gen := exec.Command("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "",
		"--quick-generate-key", uid, "ed25519", "sign", "never")
	gen.Env = env
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("keygen %s: %v %s", uid, err, out)
	}
	list := exec.Command("gpg", "--batch", "--with-colons", "--fingerprint")
	list.Env = env
	listed, err := list.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(listed), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) >= 10 && parts[0] == "fpr" && parts[9] != "" {
			fp = strings.ToUpper(parts[9])
			break
		}
	}
	if fp == "" {
		t.Fatal("generated key has no fingerprint")
	}
	exp := exec.Command("gpg", "--batch", "--armor", "--export")
	exp.Env = env
	pub, err = exp.Output()
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
	return pub, sig, fp
}

func TestGPGVerifyRejectsExpiredKey(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Fatal("gpg is required")
	}
	home := t.TempDir()
	env := append(os.Environ(), "GNUPGHOME="+home)
	gen := exec.Command("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "",
		"--quick-generate-key", "hm-expired <expired@example.invalid>", "ed25519", "sign", "seconds=4")
	gen.Env = env
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("keygen: %v %s", err, out)
	}
	payload := []byte("checksums-for-expired")
	sum := filepath.Join(home, "checksums.txt")
	if err := os.WriteFile(sum, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(home, "checksums.txt.sig")
	sign := exec.Command("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "",
		"--detach-sign", "--output", sigPath, sum)
	sign.Env = env
	if out, err := sign.CombinedOutput(); err != nil {
		t.Fatalf("sign: %v %s", err, out)
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatal(err)
	}
	exp := exec.Command("gpg", "--batch", "--armor", "--export")
	exp.Env = env
	pub, err := exp.Output()
	if err != nil {
		t.Fatal(err)
	}
	list := exec.Command("gpg", "--batch", "--with-colons", "--fingerprint")
	list.Env = env
	listed, err := list.Output()
	if err != nil {
		t.Fatal(err)
	}
	var fp string
	for _, line := range strings.Split(string(listed), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) >= 10 && parts[0] == "fpr" && parts[9] != "" {
			fp = strings.ToUpper(parts[9])
			break
		}
	}
	if fp == "" {
		t.Fatal("expired test key has no fingerprint")
	}
	time.Sleep(5 * time.Second)
	if err := GPGVerify(payload, sig, pub, []string{fp}); err == nil {
		t.Fatal("expired signing key was accepted")
	}
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
		"workflow_dispatch",
		"azure/login",
		"vars.HYPERMESH_HOST_RELEASE_TEST_CLIENT_ID",
		"vars.HYPERMESH_HOST_RELEASE_PROD_CLIENT_ID",
		"vars.AZURE_TENANT_ID",
		"vars.AZURE_SUBSCRIPTION_ID",
		"vars.AZURE_STORAGE_ACCOUNT",
		"vars.AZURE_STORAGE_CONTAINER",
		"allow-no-subscriptions: true",
		"HYPERMESH_RELEASE_KV_NAME",
		"HYPERMESH_RELEASE_GPG_KEY_SECRET",
		"HYPERMESH_RELEASE_GPG_PASSPHRASE_SECRET",
		"packaging/release/publish-agent.sh",
		"channel_for_ref",
		"refs/heads/main",
		"refs/tags/v*",
		"- main",
		`v[0-9]+.[0-9]+.[0-9]+`,
		"cancel-in-progress: false",
		"queue: max",
		"group: release-publish",
		"AZURE_CONFIG_DIR=$RUNNER_TEMP/az",
		"GITHUB_ENV",
		"az logout",
		"if: always()",
		"gpgconf --kill all",
		"shred",
		"git fetch origin main",
		"git merge-base --is-ancestor HEAD origin/main",
		"one sequence across channels",
		"repo:FyberLabs/hypermesh-host:ref:refs/heads/main",
		"repo:FyberLabs/hypermesh-host:ref:refs/tags/v*",
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
		"actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e",
		"azure/login@a641126d1b8aa4d1fa005f4f92df94a3a4c4c906",
		"if: env.channel == 'test'",
		"if: env.channel == 'prod'",
		"kv-fyber-hmhost",
		"hypermesh-host-release-gpg-private-key",
		"hypermesh-host-release-gpg-passphrase",
		"storage-blob-preview",
		"1.0.0b3",
		"extension.use_dynamic_install=no",
		"older than 2.90.0",
		"az version",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("release.yml missing %s", want)
		}
	}
	if strings.Contains(text, "${{ runner.temp }}") || strings.Contains(text, "runner.temp") {
		t.Fatal("release.yml uses runner.temp in an expression")
	}
	if strings.Contains(text, "environment:") || strings.Contains(text, "secrets.") {
		t.Fatal("release.yml depends on an environment or a secret")
	}
	if strings.Contains(text, "vars.AZURE_CLIENT_ID }}") {
		t.Fatal("release login still uses one client id for both channels")
	}
	if strings.Contains(text, "set -x") {
		t.Fatal("release.yml enables xtrace")
	}
	if strings.Contains(text, "AZURE_CLIENT_ID_TEST") || strings.Contains(text, "AZURE_CLIENT_ID_PROD") {
		t.Fatal("release.yml still uses the old client id variable names")
	}
	if strings.Contains(strings.ReplaceAll(text, "locks/release-publish", ""), "locks/publish") {
		t.Fatal("release.yml still leases locks/publish")
	}
	lintCfg, err := os.ReadFile(filepath.Join(root, ".github", "actionlint.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lintText := string(lintCfg)
	for _, want := range []string{
		".github/workflows/release*.yml:",
		`unexpected key "queue" for "concurrency" section`,
	} {
		if !strings.Contains(lintText, want) {
			t.Fatalf("actionlint.yaml missing %s", want)
		}
	}
	if strings.Contains(text, "hypermesh-wg") || strings.Contains(text, "connection-string") || strings.Contains(text, "account-key") || strings.Contains(text, "stfyberhmhost") {
		t.Fatal("release.yml has a storage key, a hardcoded account, or restarts wireguard")
	}
	extAt := strings.Index(text, "storage-blob-preview")
	publishAt := strings.Index(text, "Publish signed agent")
	if extAt < 0 || publishAt < extAt {
		t.Fatal("storage-blob-preview is not installed before publish")
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
	for _, want := range []string{
		"914e7df21a07ef503a81201c76d2b11c789d3fca",
		"-shellcheck=",
		"shellcheck -x packaging/release/publish-agent.sh",
		"azure-cli==2.90.0",
		"storage-blob-preview",
		"1.0.0b3",
		"packaging/release/check-az-flags.py",
		"--self-test",
		"AZ_FLAG_CHECK=core",
		"AZ_FLAG_CHECK=preview",
		`AZURE_CONFIG_DIR="$RUNNER_TEMP/az-flag-check"`,
	} {
		if !strings.Contains(ciText, want) {
			t.Fatalf("ci.yml missing %s", want)
		}
	}
	script, err := os.ReadFile(filepath.Join(root, "packaging", "release", "publish-agent.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text = string(script)
	layout, err := os.ReadFile(filepath.Join(root, "packaging", "release", "layout.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"build_release_binaries()", "CGO_ENABLED=0", `GOOS="$os"`, "linux_arm64", "linux_amd64", "darwin_arm64", `"./cmd/${command}"`, "command=agent", "command=hypermesh-host", "release_payload_names()", "platforms.json", "write_release_checksums()"} {
		if !strings.Contains(string(layout), want) {
			t.Fatalf("release build helper missing %s", want)
		}
	}
	for _, want := range []string{
		"build_release_binaries",
		"write_release_checksums",
        "release_payload_names",
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
		"locks/release-publish",
		"enforce_publish_blob",
		"enforce_publish_lease",
		"require_builds_ahead",
		"classify_prod_reuse",
		"bump the version",
		"superseded=true",
		`tag="test"`,
		"inherit_errexit",
		"load_signing_material",
		"wipe_gnupg_home",
		"HYPERMESH_RELEASE_KV_NAME",
		"HYPERMESH_RELEASE_GPG_KEY_SECRET",
		"HYPERMESH_RELEASE_GPG_PASSPHRASE_SECRET",
		"prod_pointer_relation",
		"prod_publish_gate",
		"confirm_publish_lease",
		"classify_github_asset",
		"github_asset_digest",
		"skip_pointer",
		`if [ "$skip_pointer" != 1 ]`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("publish script missing %s", want)
		}
	}
	if strings.Contains(strings.ReplaceAll(text, "locks/release-publish", ""), "locks/publish") {
		t.Fatal("publish script still names locks/publish")
	}
	if strings.Contains(text, "connection-string") || strings.Contains(text, "--account-key") || strings.Contains(text, "HYPERMESH_UPDATE_GITHUB_TOKEN") {
		t.Fatal("publish script has a storage key or a host github token")
	}
	if strings.Contains(text, "HYPERMESH_RELEASE_GPG_PRIVATE_KEY") || strings.Contains(text, "set -x") || strings.Contains(text, "GitHub Environment") {
		t.Fatal("publish script still takes a GitHub signing secret or enables xtrace")
	}
	if strings.Contains(text, "keyvault secret show --auth-mode") {
		t.Fatal("publish script passes --auth-mode to keyvault secret show")
	}
	skipAt := strings.Index(text, `if [ "$skip_pointer" != 1 ]`)
	gateAt := strings.Index(text, "prod_publish_gate")
	manifestCall := strings.Index(text, `upload_channel_manifest "channels/${channel}.json"`)
	leaseAt := strings.LastIndex(text[:manifestCall], "confirm_publish_lease")
	if skipAt < 0 || gateAt < skipAt || leaseAt < gateAt || manifestCall < leaseAt {
		t.Fatalf("pointer write order skip=%d gate=%d lease=%d manifest=%d", skipAt, gateAt, leaseAt, manifestCall)
	}
	layoutBody, err := os.ReadFile(filepath.Join(root, "packaging", "release", "layout.sh"))
	if err != nil {
		t.Fatal(err)
	}
	layoutText := string(layoutBody)
	for _, want := range []string{
		"az keyvault secret show",
		"--query value -o tsv",
		"::add-mask::",
		"chmod 700",
		"--pinentry-mode loopback",
		"--passphrase-file",
		"gpgconf --kill all",
		"shred",
		"channel_for_ref",
		"refs/heads/main",
		"refs/tags/v*",
		"kv-fyber-hmhost",
		"hypermesh-host-release-gpg-private-key",
		"hypermesh-host-release-gpg-passphrase",
		"channel_write_allowed",
		"publish_blob_allowed",
		"publish_list_allowed",
		"locks/release-publish",
		"prod_pointer_relation",
		"test_build_behind",
		"prod_publish_gate",
		"confirm_publish_lease",
		"classify_github_asset",
		"github_asset_digest",
		`az keyvault secret show --vault-name "$vault" --name "$key_secret" --query value -o tsv`,
	} {
		if !strings.Contains(layoutText, want) {
			t.Fatalf("layout.sh missing %s", want)
		}
	}
	for _, line := range strings.Split(layoutText, "\n") {
		code, _, _ := strings.Cut(line, "#")
		if strings.Contains(code, "--auth-mode") {
			t.Fatalf("layout.sh passes --auth-mode: %s", strings.TrimSpace(line))
		}
	}
	if strings.Contains(strings.ReplaceAll(layoutText, "locks/release-publish", ""), "locks/publish") {
		t.Fatal("layout.sh still names locks/publish")
	}
	if strings.Contains(layoutText, "set -x") {
		t.Fatal("layout.sh enables xtrace")
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"repo:FyberLabs/hypermesh-host:ref:refs/heads/main",
		"repo:FyberLabs/hypermesh-host:ref:refs/tags/v*",
		"HYPERMESH_RELEASE_KV_NAME",
		"HYPERMESH_RELEASE_GPG_KEY_SECRET",
		"HYPERMESH_RELEASE_GPG_PASSPHRASE_SECRET",
		"HYPERMESH_HOST_RELEASE_TEST_CLIENT_ID",
		"HYPERMESH_HOST_RELEASE_PROD_CLIENT_ID",
		"AZURE_TENANT_ID",
		"AZURE_SUBSCRIPTION_ID",
		"AZURE_STORAGE_ACCOUNT",
		"AZURE_STORAGE_CONTAINER",
		"locks/release-publish",
		"kv-fyber-hmhost",
		"hypermesh-host-release-gpg-private-key",
		"hypermesh-host-release-gpg-passphrase",
		"kv-fyber-hmhost",
	} {
		if !strings.Contains(string(readme), want) {
			t.Fatalf("README missing %s", want)
		}
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
	runbook, err := os.ReadFile(filepath.Join(root, "deploy", "agx", "update.md"))
	if err != nil {
		t.Fatal(err)
	}
	runbookText := string(runbook)
	if strings.Contains(runbookText, "keyvault secret show --auth-mode") {
		t.Fatal("update.md still passes --auth-mode to keyvault secret show")
	}
	for _, want := range []string{
		"az keyvault secret show --vault-name kv-fyber-hmhost --name hypermesh-host-release-gpg-private-key --query value -o tsv",
		"storage-blob-preview",
		"1.0.0b3",
		"before it writes `channels/test.json`",
		"HYPERMESH_HOST_RELEASE_TEST_CLIENT_ID",
	} {
		if !strings.Contains(runbookText, want) {
			t.Fatalf("update.md missing %s", want)
		}
	}
}

func TestChannelWriteScope(t *testing.T) {
	layout := filepath.Join("..", "..", "packaging", "release", "layout.sh")
	script := `
set -euo pipefail
source "$1"
ok() {
  if ! channel_write_allowed "$1" "$2"; then
    echo "rejected $1 $2" >&2
    exit 1
  fi
}
no() {
  if channel_write_allowed "$1" "$2"; then
    echo "allowed $1 $2" >&2
    exit 1
  fi
}
ok test channels/test.json
ok test test/58-0123456789ab/release.json
ok test locks/release-publish
ok prod channels/prod.json
ok prod prod/1.2.3/checksums.txt
ok prod locks/release-publish
no test channels/prod.json
no test prod/1.2.3/release.json
no test locks/publish
no test locks/release-publish/extra
no test test/58-0123456789ab
no test 'test/../channels/prod.json'
no prod channels/test.json
no prod test/58-0123456789ab/release.json
no prod locks/publish
no prod 'prod/../channels/test.json'
if channel_artifact_name test channels/test.json; then
  echo "tag scope included the manifest" >&2
  exit 1
fi
read_ok() {
  if ! publish_blob_allowed "$1" read "$2"; then
    echo "read rejected $1 $2" >&2
    exit 1
  fi
}
read_no() {
  if publish_blob_allowed "$1" read "$2"; then
    echo "read allowed $1 $2" >&2
    exit 1
  fi
}
read_ok test channels/prod.json
read_ok test channels/test.json
read_ok test locks/release-publish
read_ok prod channels/test.json
read_ok prod channels/prod.json
read_ok prod locks/release-publish
read_no test prod/1.2.3/release.json
read_no prod test/58-0123456789ab/release.json
read_no test locks/publish
if publish_blob_allowed prod tag test/58-0123456789ab/release.json; then
  echo "prod tag allowed" >&2
  exit 1
fi
if publish_blob_allowed test tag channels/test.json; then
  echo "manifest tag allowed" >&2
  exit 1
fi
if publish_blob_allowed test tag locks/release-publish; then
  echo "lock tag allowed" >&2
  exit 1
fi
if ! publish_blob_allowed test tag test/58-0123456789ab/release.json; then
  echo "test artifact tag rejected" >&2
  exit 1
fi
if publish_list_allowed prod test/; then
  echo "prod list allowed" >&2
  exit 1
fi
if ! publish_list_allowed test test/; then
  echo "test list rejected" >&2
  exit 1
fi
if publish_list_allowed test 'test/../channels/'; then
  echo "test list escape allowed" >&2
  exit 1
fi
if enforce_publish_blob prod delete --name prod/1.2.3/release.json; then
  echo "prod delete allowed" >&2
  exit 1
fi
if enforce_publish_blob prod tag set --name prod/1.2.3/release.json --tags superseded=true; then
  echo "prod tag call allowed" >&2
  exit 1
fi
if enforce_publish_blob test upload --name channels/prod.json --overwrite true; then
  echo "test write of prod pointer allowed" >&2
  exit 1
fi
if enforce_publish_blob prod upload --name channels/test.json --overwrite true; then
  echo "prod write of test pointer allowed" >&2
  exit 1
fi
if ! enforce_publish_blob test exists --name channels/prod.json; then
  echo "test read of prod pointer rejected" >&2
  exit 1
fi
if ! enforce_publish_blob prod exists --name channels/test.json; then
  echo "prod read of test pointer rejected" >&2
  exit 1
fi
if ! enforce_publish_blob test tag set --name test/58-0123456789ab/release.json --tags superseded=true; then
  echo "test superseded tag rejected" >&2
  exit 1
fi
if enforce_publish_blob test tag set --name test/58-0123456789ab/release.json --tags other=true; then
  echo "other tag allowed" >&2
  exit 1
fi
if ! enforce_publish_lease test acquire --blob-name locks/release-publish --lease-duration 60; then
  echo "test lease rejected" >&2
  exit 1
fi
if ! enforce_publish_lease prod renew --blob-name locks/release-publish --lease-id abc; then
  echo "prod lease rejected" >&2
  exit 1
fi
if enforce_publish_lease prod acquire --blob-name locks/publish; then
  echo "old lock lease allowed" >&2
  exit 1
fi
if enforce_publish_lease test break --blob-name locks/release-publish; then
  echo "lease break allowed" >&2
  exit 1
fi
`
	cmd := exec.Command("bash", "-c", script, "bash", layout)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestKeyVaultSigningMaterial(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Fatal("gpg is required")
	}
	layout := filepath.Join("..", "..", "packaging", "release", "layout.sh")
	script := `
set -euo pipefail
source "$1"
src=$(mktemp -d)
chmod 700 "$src"
trap 'if [ -n "${GNUPGHOME:-}" ]; then wipe_gnupg_home "$GNUPGHOME" || true; fi; rm -rf "$src"' EXIT
printf '%s' 'fixture-passphrase' > "$src/pass"
GNUPGHOME="$src" gpg --batch --pinentry-mode loopback --passphrase-file "$src/pass" \
  --quick-generate-key 'hm-kv <kv@example.invalid>' ed25519 sign never
GNUPGHOME="$src" gpg --batch --pinentry-mode loopback --passphrase-file "$src/pass" \
  --armor --export-secret-keys > "$src/secret.asc"
GNUPGHOME="$src" gpgconf --kill all || true
unset GNUPGHOME
az() {
  local name="" prev="" arg
  for arg in "$@"; do
    if [ "$prev" = "--name" ]; then
      name=$arg
    fi
    prev=$arg
  done
  case " $* " in
    *" --query value "*) ;;
    *) echo "az missing --query value" >&2; return 1 ;;
  esac
  case " $* " in
    *" -o tsv "*) ;;
    *) echo "az missing -o tsv" >&2; return 1 ;;
  esac
  case "$name" in
    signing-key) cat "$src/secret.asc"; printf '\n' ;;
    signing-pass) printf '%s\n' 'fixture-passphrase' ;;
    *) echo "unknown secret name" >&2; return 1 ;;
  esac
}
stdout=$(mktemp)
stderr=$(mktemp)
envfile=$(mktemp)
export GITHUB_ENV="$envfile"
if ! load_signing_material test-vault signing-key signing-pass >"$stdout" 2>"$stderr"; then
  echo "load failed" >&2
  exit 1
fi
if grep -q 'fixture-passphrase' "$stdout" || grep -q 'BEGIN PGP' "$stdout"; then
  echo "stdout leaked signing material" >&2
  exit 1
fi
if grep -q 'BEGIN PGP PRIVATE' "$stderr"; then
  echo "stderr leaked the private key" >&2
  exit 1
fi
if ! grep -q '^::add-mask::' "$stderr"; then
  echo "passphrase was not masked" >&2
  exit 1
fi
mode=$(stat -c '%a' "$GNUPGHOME")
kmode=$(stat -c '%a' "$GNUPGHOME/key.asc")
pmode=$(stat -c '%a' "$GNUPGHOME/pass")
if [ "$mode" != 700 ] || [ "$kmode" != 600 ] || [ "$pmode" != 600 ]; then
  echo "modes home=$mode key=$kmode pass=$pmode" >&2
  exit 1
fi
want=$(mktemp)
printf '%s' 'fixture-passphrase' > "$want"
if ! cmp -s "$want" "$GNUPGHOME/pass"; then
  echo "passphrase file is not the exact secret" >&2
  exit 1
fi
if ! grep -q 'BEGIN PGP PRIVATE KEY BLOCK' "$GNUPGHOME/key.asc"; then
  echo "key file is not an armored private key" >&2
  exit 1
fi
if ! grep -q "^GNUPGHOME=${GNUPGHOME}$" "$envfile"; then
  echo "GITHUB_ENV did not record GNUPGHOME" >&2
  exit 1
fi
if [ -z "${signing_fpr:-}" ]; then
  echo "fingerprint was empty" >&2
  exit 1
fi
home=$GNUPGHOME
marker=$(mktemp)
bindir=$(mktemp -d)
cat > "$bindir/shred" <<'EOF'
#!/bin/sh
echo shred-ran >> "$SHRED_LOG"
exit 0
EOF
chmod 755 "$bindir/shred"
SHRED_LOG=$marker PATH="$bindir:$PATH" wipe_gnupg_home "$home"
if [ -d "$home" ] || [ -n "${GNUPGHOME:-}" ]; then
  echo "signing home was not wiped" >&2
  exit 1
fi
if ! grep -qx shred-ran "$marker"; then
  echo "shred was not used" >&2
  exit 1
fi
az() { printf '\n'; }
if load_signing_material test-vault signing-key signing-pass; then
  echo "empty key was accepted" >&2
  exit 1
fi
if [ -n "${GNUPGHOME:-}" ]; then
  wipe_gnupg_home "$GNUPGHOME"
fi
got=$(channel_for_ref refs/heads/main)
if [ "$got" != test ]; then
  echo "main was not test" >&2
  exit 1
fi
got=$(channel_for_ref refs/tags/v1.2.3)
if [ "$got" != prod ]; then
  echo "tag was not prod" >&2
  exit 1
fi
if channel_for_ref refs/heads/feature >/dev/null; then
  echo "feature branch was accepted" >&2
  exit 1
fi
if channel_for_ref refs/tags/1.2.3 >/dev/null; then
  echo "tag without v was accepted" >&2
  exit 1
fi
`
	cmd := exec.Command("bash", "-c", script, "bash", layout)
	out, err := cmd.CombinedOutput()
	cleaned := bytes.ReplaceAll(out, []byte("fixture-passphrase"), []byte("[redacted]"))
	if err != nil {
		t.Fatalf("%v\n%s", err, cleaned)
	}
	if bytes.Contains(cleaned, []byte("BEGIN PGP PRIVATE")) {
		t.Fatal("test output contained a private key")
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
	tagged := strings.Index(text, "# publish-order: superseded-tag")
	manifest := strings.Index(text, "# publish-order: channel-manifest")
	if pointerAt < 0 || artifacts < pointerAt || verify < artifacts || tagged < verify || manifest < tagged {
		t.Fatalf("publish order pointer=%d artifacts=%d verify=%d tag=%d manifest=%d", pointerAt, artifacts, verify, tagged, manifest)
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

func TestReleaseScriptFailClosedAndReuse(t *testing.T) {
	layout := filepath.Join("..", "..", "packaging", "release", "layout.sh")
	body, err := os.ReadFile(layout)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "# shellcheck shell=bash") {
		t.Fatal("layout.sh has no shellcheck shell directive")
	}
	run := func(t *testing.T, script string) string {
		t.Helper()
		cmd := exec.Command("bash", "-c", script, "bash", layout)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return string(out)
	}
	t.Run("index error is not missing", func(t *testing.T) {
		cmd := exec.Command("bash", "-c", `
set -euo pipefail
source "$1"
az_blob() { echo denied >&2; return 1; }
read_index_build test
echo survived
`, "bash", layout)
		out, err := cmd.CombinedOutput()
		if err == nil || strings.Contains(string(out), "survived") {
			t.Fatalf("failed index read continued: %v %s", err, out)
		}
	})
	t.Run("explicit false is missing", func(t *testing.T) {
		out := run(t, `
set -euo pipefail
source "$1"
az_blob() { printf '%s\n' false; }
out=$(read_index_build test)
if [ -n "$out" ]; then
  echo "want empty, got [$out]" >&2
  exit 1
fi
`)
		if strings.TrimSpace(out) != "" {
			t.Fatalf("output %q", out)
		}
	})
	t.Run("download error fails", func(t *testing.T) {
		cmd := exec.Command("bash", "-c", `
set -euo pipefail
source "$1"
az_blob() {
  case "$1" in
    exists) printf '%s\n' true ;;
    *) return 1 ;;
  esac
}
read_index_build prod
echo survived
`, "bash", layout)
		out, err := cmd.CombinedOutput()
		if err == nil || strings.Contains(string(out), "survived") {
			t.Fatalf("failed download continued: %v %s", err, out)
		}
	})
	t.Run("re-read must be ahead", func(t *testing.T) {
		script := `
set -euo pipefail
source "$1"
az_blob() {
  local name="" file=""
  local cmd=$1
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
      --name) name=$2; shift 2 ;;
      --file) file=$2; shift 2 ;;
      *) shift ;;
    esac
  done
  case "$cmd" in
    exists) printf '%s\n' true ;;
    download)
      case "$name" in
        channels/test.json) printf '%s\n' '{"build":5}' >"$file" ;;
        channels/prod.json) printf '%s\n' '{"build":7}' >"$file" ;;
        *) echo "unexpected $name" >&2; return 1 ;;
      esac
      ;;
    *) echo "unexpected $cmd" >&2; return 1 ;;
  esac
}
require_builds_ahead 8
if ! require_builds_ahead 7; then
  exit 0
fi
echo accepted-equal >&2
exit 1
`
		if out := run(t, script); strings.Contains(out, "accepted-equal") {
			t.Fatal(out)
		}
	})
	t.Run("lease waits then fails closed", func(t *testing.T) {
		out := run(t, `
set -euo pipefail
source "$1"
count=$(mktemp)
echo 0 >"$count"
az_blob() {
  if [ "$1" = exists ]; then
    printf '%s\n' false
    return 0
  fi
  return 0
}
az_lease() {
  n=$(cat "$count")
  n=$((n + 1))
  echo "$n" >"$count"
  if [ "$n" -eq 1 ]; then
    echo LeaseAlreadyPresent >&2
    return 1
  fi
  printf '%s\n' lease-ok
}
export HYPERMESH_LEASE_ATTEMPTS=3 HYPERMESH_LEASE_PAUSE=0
id=$(acquire_publish_lease)
if [ "$id" != lease-ok ]; then
  echo "id=$id" >&2
  exit 1
fi
az_lease() { echo forbidden >&2; return 1; }
if acquire_publish_lease; then
  echo acquired-without-lease >&2
  exit 1
fi
`)
		if strings.Contains(out, "acquired-without-lease") {
			t.Fatal(out)
		}
	})
	t.Run("lease id stays a clean token", func(t *testing.T) {
		out := run(t, `
set -euo pipefail
source "$1"
export HYPERMESH_LEASE_ATTEMPTS=2 HYPERMESH_LEASE_PAUSE=0
calls=$(mktemp)
az_blob() {
  if [ "$1" = exists ]; then
    printf '%s\n' false
    return 0
  fi
  if [ "$1" = upload ]; then
    printf '%s\n' '{' '  "client_request_id": "abc",' '  "etag": "x"' '}'
    return 0
  fi
  echo "unexpected blob $*" >&2
  return 1
}
az_lease() {
  printf '%s\n' "$*" >>"$calls"
  if [ "$1" = acquire ]; then
    printf '%s\n' '11111111-1111-1111-1111-111111111111'
    return 0
  fi
  return 0
}
lease_id=""
if ! lease_id=$(acquire_publish_lease); then
  lease_id=""
  echo "acquire failed" >&2
  exit 1
fi
if [ "$lease_id" != "11111111-1111-1111-1111-111111111111" ]; then
  echo "dirty id [$lease_id]" >&2
  exit 1
fi
release_publish_lease "$lease_id"
if ! grep -q -- '--lease-id 11111111-1111-1111-1111-111111111111' "$calls"; then
  echo "release missed id" >&2
  cat "$calls" >&2
  exit 1
fi
if grep -q 'client_request_id' "$calls"; then
  echo "release got json" >&2
  exit 1
fi
: >"$calls"
release_publish_lease "$(printf '{\n  "etag": "x"\n}')"
if [ -s "$calls" ]; then
  echo "json release called az" >&2
  cat "$calls" >&2
  exit 1
fi
if confirm_publish_lease "$(printf '{\n  "etag": "x"\n}')"; then
  echo "json confirm called az" >&2
  exit 1
fi
`)
		if strings.Contains(out, "dirty id") || strings.Contains(out, "release got json") || strings.Contains(out, "json release") || strings.Contains(out, "json confirm") || strings.Contains(out, "acquire failed") || strings.Contains(out, "release missed") {
			t.Fatal(out)
		}
	})
	t.Run("header error is printed and not retried", func(t *testing.T) {
		out := run(t, `
set -euo pipefail
source "$1"
export HYPERMESH_LEASE_ATTEMPTS=3 HYPERMESH_LEASE_PAUSE=0
count=$(mktemp)
echo 0 >"$count"
az_blob() {
  if [ "$1" = exists ]; then
    printf '%s\n' false
    return 0
  fi
  if [ "$1" = upload ]; then
    printf '%s\n' '{' '  "etag": "x"' '}'
    return 0
  fi
  return 0
}
az_lease() {
  n=$(cat "$count")
  n=$((n + 1))
  echo "$n" >"$count"
  echo 'ERROR: Invalid leading whitespace, reserved character(s), or return character(s) in header value: {"etag":"x"}' >&2
  return 1
}
lease_id=sentinel
if lease_id=$(acquire_publish_lease); then
  echo acquired-header-error >&2
  exit 1
fi
if [ -n "$lease_id" ]; then
  echo "stdout leaked [$lease_id]" >&2
  exit 1
fi
n=$(cat "$count")
if [ "$n" -ne 1 ]; then
  echo "retried header error $n" >&2
  exit 1
fi
`)
		if strings.Contains(out, "acquired-header-error") || strings.Contains(out, "stdout leaked") || strings.Contains(out, "retried header") {
			t.Fatal(out)
		}
		if !strings.Contains(out, "lock acquire failed") || !strings.Contains(out, "Invalid leading whitespace") {
			t.Fatalf("az stderr was hidden:\n%s", out)
		}
	})
	t.Run("superseded tag", func(t *testing.T) {
		out := run(t, `
set -euo pipefail
source "$1"
taglog=$(mktemp)
az_blob() {
  local cmd=$1
  shift
  if [ "$cmd" = list ]; then
    local i
    for i in $(seq 1 25); do
      printf 'test/%s-aaaaaaaaaaaa/agent\n' "$i"
    done
    return 0
  fi
  if [ "$cmd" = tag ]; then
    printf '%s\n' "$*" >>"$taglog"
    return 0
  fi
  echo "bad $cmd" >&2
  return 1
}
tag_superseded_test "25-aaaaaaaaaaaa"
if grep -q 'prod/' "$taglog" || grep -q 'channels/' "$taglog"; then
  echo tagged-outside-test >&2
  exit 1
fi
if grep -q 'test/25-aaaaaaaaaaaa/' "$taglog"; then
  echo tagged-current >&2
  exit 1
fi
if grep -q 'test/6-aaaaaaaaaaaa/' "$taglog"; then
  echo tagged-kept >&2
  exit 1
fi
if ! grep -q 'test/5-aaaaaaaaaaaa/agent' "$taglog"; then
  echo missing-old >&2
  cat "$taglog" >&2
  exit 1
fi
if ! grep -q 'superseded=true' "$taglog"; then
  echo missing-tag-value >&2
  exit 1
fi
az_blob() { return 1; }
if tag_superseded_test "25-aaaaaaaaaaaa"; then
  echo list-failure-ignored >&2
  exit 1
fi
`)
		if strings.Contains(out, "tagged-") || strings.Contains(out, "missing-") || strings.Contains(out, "list-failure") {
			t.Fatal(out)
		}
	})
	t.Run("prod reuse", func(t *testing.T) {
		if _, err := exec.LookPath("gpg"); err != nil {
			t.Fatal("gpg is required")
		}
		dir := t.TempDir()
		home := filepath.Join(dir, "gnupg")
		if err := os.Mkdir(home, 0o700); err != nil {
			t.Fatal(err)
		}
		env := append(os.Environ(), "GNUPGHOME="+home)
		gen := exec.Command("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "",
			"--quick-generate-key", "hm-reuse <reuse@example.invalid>", "ed25519", "sign", "never")
		gen.Env = env
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("keygen: %v %s", err, out)
		}
		store := filepath.Join(dir, "store", "prod", "1.2.3")
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		dist := filepath.Join(dir, "dist")
		if err := os.Mkdir(dist, 0o755); err != nil {
			t.Fatal(err)
		}
		bin := []byte("agent-bytes-v1")
		rel := []byte("{\"channel\":\"prod\",\"version\":\"1.2.3\",\"commit\":\"abc\",\"build\":9,\"release_id\":\"1.2.3\"}\n")
		if err := os.WriteFile(filepath.Join(dist, "hypermesh-agent_1.2.3_linux_arm64"), bin, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store, "release.json"), rel, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dist, "release.json"), rel, 0o644); err != nil {
			t.Fatal(err)
		}
		sums := checksumNamed(map[string][]byte{
			"hypermesh-agent_1.2.3_linux_arm64": bin,
			"release.json":                      rel,
		})
		if err := os.WriteFile(filepath.Join(store, "checksums.txt"), sums, 0o644); err != nil {
			t.Fatal(err)
		}
		sumPath := filepath.Join(home, "checksums.txt")
		if err := os.WriteFile(sumPath, sums, 0o600); err != nil {
			t.Fatal(err)
		}
		sigPath := filepath.Join(store, "checksums.txt.sig")
		sign := exec.Command("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "",
			"--detach-sign", "--output", sigPath, sumPath)
		sign.Env = env
		if out, err := sign.CombinedOutput(); err != nil {
			t.Fatalf("sign: %v %s", err, out)
		}
		ops := filepath.Join(dir, "gpg-ops")
		script := `
set -euo pipefail
source "$1"
store=$2
dist=$3
ops=$4
gpg() {
  case " $* " in
    *" --detach-sign "*) echo detach >>"$ops"; return 99 ;;
  esac
  command gpg "$@"
}
az_blob() {
  local cmd=$1
  shift
  local name="" file=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --name) name=$2; shift 2 ;;
      --file) file=$2; shift 2 ;;
      *) shift ;;
    esac
  done
  case "$cmd" in
    exists)
      if [ -f "$store/$name" ]; then printf '%s\n' true; else printf '%s\n' false; fi
      ;;
    download)
      cp "$store/$name" "$file"
      ;;
    *) echo "unexpected $cmd" >&2; return 1 ;;
  esac
}
classify_prod_reuse 1.2.3 "$dist"
`
		cmd := exec.Command("bash", "-c", script, "bash", layout, filepath.Join(dir, "store"), dist, ops)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("reuse: %v\n%s", err, out)
		}
		text := string(out)
		if !strings.Contains(text, "upload hypermesh-agent_1.2.3_linux_arm64") {
			t.Fatalf("missing artifact was not uploaded:\n%s", text)
		}
		if !strings.Contains(text, "keep release.json") || !strings.Contains(text, "keep checksums.txt") {
			t.Fatalf("stored manifest was not kept:\n%s", text)
		}
		if _, err := os.Stat(ops); err == nil {
			t.Fatal("reuse detached a new signature")
		}
		if err := os.WriteFile(filepath.Join(dist, "hypermesh-agent_1.2.3_linux_arm64"), []byte("different"), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command("bash", "-c", script, "bash", layout, filepath.Join(dir, "store"), dist, ops)
		cmd.Env = env
		out, err = cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "bump the version") {
			t.Fatalf("differing artifact: %v %s", err, out)
		}
	})
	t.Run("prod pointer relation", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "prod.json")
		body := `{"release_id":"1.2.3","build":9,"commit":"ABC"}` + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		rel := func(rid, build, commit string) string {
			t.Helper()
			out, err := exec.Command("bash", "-c", `source "$1"; prod_pointer_relation "$2" "$3" "$4" "$5"`, "bash", layout, path, rid, build, commit).CombinedOutput()
			if err != nil {
				t.Fatalf("%v %s", err, out)
			}
			return strings.TrimSpace(string(out))
		}
		if got := rel("1.2.3", "9", "abc"); got != "match" {
			t.Fatalf("match: %q", got)
		}
		if got := rel("1.2.3", "9", "def"); got != "mismatch" {
			t.Fatalf("mismatch: %q", got)
		}
		if got := rel("1.2.4", "10", "abc"); got != "other" {
			t.Fatalf("other: %q", got)
		}
	})
	t.Run("reused build behind test", func(t *testing.T) {
		call := func(index string) (string, int) {
			t.Helper()
			script := `
set -euo pipefail
source "$1"
doc=$2
az_blob() {
  local cmd=$1
  shift
  local name="" file=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --name) name=$2; shift 2 ;;
      --file) file=$2; shift 2 ;;
      *) shift ;;
    esac
  done
  case "$cmd" in
    exists)
      if [ "$name" = channels/test.json ] && [ -n "$doc" ]; then printf '%s\n' true; else printf '%s\n' false; fi
      ;;
    download) printf '%s\n' "$doc" >"$file" ;;
    *) echo "unexpected $cmd" >&2; return 1 ;;
  esac
}
if test_build_behind 9; then
  echo ahead
  exit 0
else
  rc=$?
  if [ "$rc" -eq 1 ]; then
    echo "hypermesh release: bump the version"
    exit 1
  fi
  echo "read-failed $rc"
  exit "$rc"
fi
`
			cmd := exec.Command("bash", "-c", script, "bash", layout, index)
			out, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					code = exitErr.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			return string(out), code
		}
		if out, code := call(""); code != 0 || !strings.Contains(out, "ahead") {
			t.Fatalf("missing test index: %d %s", code, out)
		}
		if out, code := call(`{"build":8}`); code != 0 || !strings.Contains(out, "ahead") {
			t.Fatalf("smaller test build: %d %s", code, out)
		}
		if out, code := call(`{"build":9}`); code != 1 || !strings.Contains(out, "bump the version") {
			t.Fatalf("equal test build: %d %s", code, out)
		}
		if out, code := call(`{"build":10}`); code != 1 || !strings.Contains(out, "bump the version") {
			t.Fatalf("greater test build: %d %s", code, out)
		}
		if out, code := call(`not-json`); code != 2 || strings.Contains(out, "bump the version") {
			t.Fatalf("bad index: %d %s", code, out)
		}
	})
	t.Run("github asset reuse", func(t *testing.T) {
		dir := t.TempDir()
		assets := filepath.Join(dir, "assets.json")
		raw := `[
{"name":"release.json","digest":"sha256:abc"},
{"name":"checksums.txt","id":42},
{"name":"other","digest":""}
]`
		if err := os.WriteFile(assets, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		digest := func(name string) string {
			t.Helper()
			out, err := exec.Command("bash", "-c", `source "$1"; github_asset_digest "$2" "$3"`, "bash", layout, assets, name).CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v %s", name, err, out)
			}
			return strings.TrimSpace(string(out))
		}
		if got := digest("release.json"); got != "sha abc" {
			t.Fatalf("sha: %q", got)
		}
		if got := digest("checksums.txt"); got != "id 42" {
			t.Fatalf("id: %q", got)
		}
		if got := digest("missing"); got != "absent" {
			t.Fatalf("absent: %q", got)
		}
		class := func(local, existing string) (string, int) {
			t.Helper()
			cmd := exec.Command("bash", "-c", `source "$1"; classify_github_asset "$2" "$3"`, "bash", layout, local, existing)
			out, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					code = exitErr.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			return strings.TrimSpace(string(out)), code
		}
		if got, code := class("abc", ""); code != 0 || got != "upload" {
			t.Fatalf("upload: %d %q", code, got)
		}
		if got, code := class("ABC", "sha256:abc"); code != 0 || got != "skip" {
			t.Fatalf("skip: %d %q", code, got)
		}
		got, code := class("abc", "def")
		if code != 1 || !strings.Contains(got, "bump the version") {
			t.Fatalf("mismatch: %d %q", code, got)
		}
	})
	t.Run("superseded tag keeps the pointer", func(t *testing.T) {
		out := run(t, `
set -euo pipefail
source "$1"
taglog=$(mktemp)
az_blob() {
  local cmd=$1
  shift
  if [ "$cmd" = list ]; then
    local i
    for i in $(seq 1 25); do
      printf 'test/%s-aaaaaaaaaaaa/agent\n' "$i"
    done
    return 0
  fi
  if [ "$cmd" = tag ]; then
    printf '%s\n' "$*" >>"$taglog"
    return 0
  fi
  echo "bad $cmd" >&2
  return 1
}
tag_superseded_test "25-aaaaaaaaaaaa" "5-aaaaaaaaaaaa"
if grep -q 'test/5-aaaaaaaaaaaa/' "$taglog"; then
  echo tagged-pointer >&2
  exit 1
fi
if ! grep -q 'test/4-aaaaaaaaaaaa/agent' "$taglog"; then
  echo missing-older >&2
  cat "$taglog" >&2
  exit 1
fi
az_blob() {
  if [ "$1" = list ]; then
    local i
    for i in $(seq 1 25); do
      printf 'test/%s-aaaaaaaaaaaa/agent\n' "$i"
    done
    return 0
  fi
  echo tag-failed >&2
  return 1
}
if tag_superseded_test "25-aaaaaaaaaaaa" "5-aaaaaaaaaaaa"; then
  echo tag-failure-ignored >&2
  exit 1
fi
`)
		if strings.Contains(out, "tagged-pointer") || strings.Contains(out, "missing-older") || strings.Contains(out, "tag-failure") {
			t.Fatal(out)
		}
	})
	t.Run("pointer already set repairs a partial upload", func(t *testing.T) {
		// Blobs and channels/prod.json were written. The GitHub Release
		// upload stopped part way, and a test publish has since moved
		// ahead. The re-run must not fail with bump the version.
		script := `
set -euo pipefail
source "$1"
doc=$2
az_blob() {
  local cmd=$1
  shift
  local name="" file=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --name) name=$2; shift 2 ;;
      --file) file=$2; shift 2 ;;
      *) shift ;;
    esac
  done
  case "$cmd" in
    exists)
      if [ "$name" = channels/test.json ] && [ -n "$doc" ]; then printf '%s\n' true; else printf '%s\n' false; fi
      ;;
    download) printf '%s\n' "$doc" >"$file" ;;
    *) echo "unexpected $cmd" >&2; return 1 ;;
  esac
}
if ! prod_publish_gate 1 1 9; then
  echo "repair blocked" >&2
  exit 1
fi
if prod_publish_gate 0 1 9; then
  echo "pointer write allowed" >&2
  exit 1
fi
`
		cmd := exec.Command("bash", "-c", script, "bash", layout, `{"build":20}`)
		out, err := cmd.CombinedOutput()
		if err != nil || strings.Contains(string(out), "repair blocked") || strings.Contains(string(out), "pointer write allowed") || !strings.Contains(string(out), "bump the version") {
			t.Fatalf("partial re-run: %v\n%s", err, out)
		}
	})
	t.Run("lease must still be held before the pointer write", func(t *testing.T) {
		out := run(t, `
set -euo pipefail
source "$1"
az_lease() { return 0; }
confirm_publish_lease lease-1
az_lease() { echo renew-failed >&2; return 1; }
if confirm_publish_lease lease-1; then
  echo lost-lease-accepted >&2
  exit 1
fi
if confirm_publish_lease ""; then
  echo empty-lease-accepted >&2
  exit 1
fi
`)
		if strings.Contains(out, "accepted") {
			t.Fatal(out)
		}
		if !strings.Contains(out, "lease was lost") || !strings.Contains(out, "lease is not held") {
			t.Fatalf("lease errors:\n%s", out)
		}
	})
}

func TestAzFlagSelfTest(t *testing.T) {
	script := filepath.Join("..", "..", "packaging", "release", "check-az-flags.py")
	out, err := exec.Command("python3", script, "--self-test").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "az flag self-test ok") {
		t.Fatalf("self-test output:\n%s", out)
	}
}

func checksumNamed(files map[string][]byte) []byte {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sortStrings(names)
	var b strings.Builder
	for _, name := range names {
		sum := sha256.Sum256(files[name])
		b.WriteString(hex.EncodeToString(sum[:]))
		b.WriteString("  ")
		b.WriteString(name)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
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
	mu         sync.Mutex
	files      map[string][]byte
	mtimes     map[string]time.Time
	modes      map[string]os.FileMode
	readDirErr error
	removeErr  error
	removeFail map[string]error
}

func (m *memFS) WriteNew(path string, data []byte, _ os.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	if m.mtimes == nil {
		m.mtimes = map[string]time.Time{}
	}
	m.files[path] = append([]byte(nil), data...)
	m.mtimes[path] = time.Now()
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
	if m.mtimes != nil {
		m.mtimes[newpath] = m.mtimes[oldpath]
		delete(m.mtimes, oldpath)
	}
	if m.modes != nil {
		if mode, ok := m.modes[oldpath]; ok {
			m.modes[newpath] = mode
			delete(m.modes, oldpath)
		}
	}
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
	if m.removeErr != nil {
		return m.removeErr
	}
	if m.removeFail != nil {
		if err := m.removeFail[path]; err != nil {
			return err
		}
	}
	if _, ok := m.files[path]; !ok {
		return os.ErrNotExist
	}
	delete(m.files, path)
	delete(m.mtimes, path)
	delete(m.modes, path)
	return nil
}

func (m *memFS) ReadDir(dir string) ([]DirEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readDirErr != nil {
		return nil, m.readDirErr
	}
	dir = filepath.Clean(dir)
	var out []DirEntry
	for path := range m.files {
		if filepath.Clean(filepath.Dir(path)) != dir {
			continue
		}
		mode := os.FileMode(0o644)
		if m.modes != nil {
			if got, ok := m.modes[path]; ok {
				mode = got
			}
		}
		var mt time.Time
		if m.mtimes != nil {
			mt = m.mtimes[path]
		}
		out = append(out, DirEntry{Name: filepath.Base(path), ModTime: mt, Mode: mode})
	}
	return out, nil
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

func TestInstalledChannelDoesNotConfuseTestPatchWithProductionSemver(t *testing.T) {
 for _,tc:=range []struct{name,origin string;build,bad int;busy bool;want Action}{
  {"test to lower production semver",ChannelTest,58,0,false,ActionInstall},
  {"test same commit promotion",ChannelTest,59,0,false,ActionInstall},
  {"busy test host",ChannelTest,58,0,true,ActionDefer},
  {"equal replay",ChannelTest,57,0,false,ActionReject},
  {"older replay",ChannelTest,56,0,false,ActionReject},
  {"rolled back candidate",ChannelTest,58,58,false,ActionSkip},
  {"production downgrade",ChannelProd,58,0,false,ActionSkip},
  {"legacy conservative","",58,0,false,ActionSkip},
  {"invalid origin","other",58,0,false,ActionReject},
 } {
  t.Run(tc.name,func(t *testing.T){
   commit:="new";if tc.name=="test same commit promotion"{commit="old"}
   m:=Manifest{Channel:ChannelProd,Version:"0.0.1",Commit:commit,Build:tc.build}
   p:=DecideFromChannel(tc.origin,ChannelProd,"0.0.57","old",57,tc.bad,m,tc.busy)
   if p.Action!=tc.want{t.Fatalf("%+v want %s",p,tc.want)}
  })
 }
 m:=Manifest{Channel:ChannelProd,Version:"not-semver",Commit:"new",Build:58}
 if DecideFromChannel(ChannelTest,ChannelProd,"0.0.57","old",57,0,m,false).Action!=ActionReject{t.Fatal("test origin bypassed candidate validation")}
}

func TestTickUsesInstalledOriginForProductionSwitch(t *testing.T){
 for _,origin:=range []string{ChannelTest,ChannelProd,""}{
  t.Run("origin-"+origin,func(t *testing.T){
   dir:=t.TempDir()
   m:=Manifest{Channel:ChannelProd,Version:"0.0.1",Commit:strings.Repeat("b",40),Build:58}
   raw,_:=json.Marshal(m);binary:=[]byte("verified agent")
   name:="hypermesh-agent_0.0.1_linux_arm64"
   sums:=[]byte(fmt.Sprintf("%x  %s\n%x  %s\n",sha256.Sum256(binary),name,sha256.Sum256(raw),ManifestName))
   installed,restarted:=false,false
   p,err:=Tick(context.Background(),Config{
    Channel:ChannelProd,CurrentChannel:origin,CurrentVersion:"0.0.57",CurrentCommit:strings.Repeat("a",40),CurrentBuild:57,
    Arch:"arm64",BinaryPath:filepath.Join(dir,"agent"),PubKeyPath:"fixture-key",MarkerPath:filepath.Join(dir,MarkerName),
   },Hooks{
    Source:planeSource(m,sums,[]byte("signature"),raw,name,binary),
    ReadFile:func(string)([]byte,error){return []byte("trusted fixture"),nil},
    Verify:func(_,_,_ []byte)error{return nil},
    Install:func(dest string,_ []byte,_ string)(string,error){installed=true;return dest+".backup",nil},
    Restart:func(context.Context)error{restarted=true;return nil},
   })
   if err!=nil{t.Fatal(err)}
   want:=ActionSkip;if origin==ChannelTest{want=ActionInstall}
   if p.Action!=want||installed!=(origin==ChannelTest)||restarted!=(origin==ChannelTest){t.Fatalf("%+v installed=%v restarted=%v",p,installed,restarted)}
  })
 }
}

func TestRestartAgentQueuesOnlyItsOwnUnit(t *testing.T) {
 dir := t.TempDir()
 // Reject a blocking restart: the caller is itself the service being stopped.
 script := "#!/bin/sh\n[ \"$#\" -eq 3 ] && [ \"$1\" = --no-block ] && [ \"$2\" = restart ] && [ \"$3\" = hypermesh-agent ]\n"
 if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0700); err != nil { t.Fatal(err) }
 t.Setenv("PATH", dir)
 if err := RestartAgent(context.Background()); err != nil { t.Fatal(err) }
 for _, args := range [][]string{
  {"restart", AgentUnit},
  {"--no-block", "restart", "hypermesh-wg"},
  {"--no-block", "restart", AgentUnit, "other.service"},
 } {
  if restartAllowed("systemctl", args) { t.Fatalf("unsafe restart accepted: %v", args) }
 }
}
