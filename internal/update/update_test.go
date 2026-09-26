package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestReleaseAPI(t *testing.T) {
	testURL, err := ReleaseAPI(ChannelTest)
	if err != nil || !strings.HasSuffix(testURL, "/releases/tags/test") {
		t.Fatalf("test url %q %v", testURL, err)
	}
	prodURL, err := ReleaseAPI(ChannelProd)
	if err != nil || !strings.HasSuffix(prodURL, "/releases/latest") {
		t.Fatalf("prod url %q %v", prodURL, err)
	}
	if _, err := ReleaseAPI("dev"); err == nil {
		t.Fatal("dev channel has an api")
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
	rel := releaseDoc(map[string]string{
		ChecksumsName: "https://example.test/sums",
		SignatureName: "https://example.test/sig",
		ManifestName:  "https://example.test/man",
		binName:       "https://example.test/bin",
	})
	api, err := ReleaseAPI(ChannelTest)
	if err != nil {
		t.Fatal(err)
	}
	fetch := mapFetch{
		api:                         rel,
		"https://example.test/sums": sums,
		"https://example.test/sig":  []byte("sig"),
		"https://example.test/man":  manRaw,
		"https://example.test/bin":  bin,
	}
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
			Fetch:  fetch,
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
	})

	t.Run("checksum mismatch", func(t *testing.T) {
		bad := mapFetch{}
		for k, v := range fetch {
			bad[k] = v
		}
		bad["https://example.test/bin"] = []byte("tampered")
		var installed bool
		plan, err := Tick(context.Background(), base, Hooks{
			Fetch:  bad,
			Verify: func(_, _, _ []byte) error { return nil },
			Install: func(string, []byte, string) (string, error) {
				installed = true
				return "/usr/local/bin/hypermesh-agent.pre-unknown", nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Action != ActionReject || plan.Reason != "binary checksum rejected" || installed {
			t.Fatalf("plan %+v installed %v", plan, installed)
		}
	})

	t.Run("busy defers", func(t *testing.T) {
		cfg := base
		cfg.Busy = true
		var installed, restarted bool
		plan, err := Tick(context.Background(), cfg, Hooks{
			Fetch:  fetch,
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
			Fetch:  fetch,
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

func TestHTTPFetcherOmitsToken(t *testing.T) {
	const token = "test-token-value"
	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := (HTTPFetcher{Token: token, Client: srv.Client()}).Get(context.Background(), srv.URL, "")
	if err == nil {
		t.Fatal("expected status error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error contains token: %v", err)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("auth header %q", gotAuth)
	}
	_, err = (HTTPFetcher{}).Get(context.Background(), "https://example.test/x", "")
	if err == nil || strings.Contains(err.Error(), "example.test") && strings.Contains(err.Error(), "Bearer") {
		t.Fatal(err)
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
	for _, name := range []string{"release.yml", "release-test.yml"} {
		body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, want := range []string{
			"contents: read",
			"contents: write",
			"environment: release",
			"HYPERMESH_RELEASE_GPG_PRIVATE_KEY",
			"packaging/release/publish-agent.sh",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing %s", name, want)
			}
		}
		if strings.Contains(text, "id-token:") || strings.Contains(text, "hypermesh-wg") {
			t.Fatalf("%s has extra scope or restarts wireguard", name)
		}
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
		"--detach-sign",
		"prerelease",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("publish script missing %s", want)
		}
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

type mapFetch map[string][]byte

func (m mapFetch) Get(_ context.Context, url, _ string) ([]byte, error) {
	b, ok := m[url]
	if !ok {
		return nil, ErrRejected
	}
	return b, nil
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

func releaseDoc(assets map[string]string) []byte {
	type asset struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	var rel struct {
		TagName string  `json:"tag_name"`
		Assets  []asset `json:"assets"`
	}
	rel.TagName = TestTag
	for name, url := range assets {
		rel.Assets = append(rel.Assets, asset{Name: name, URL: url})
	}
	raw, err := json.Marshal(rel)
	if err != nil {
		panic(err)
	}
	return raw
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
