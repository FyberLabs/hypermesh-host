package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/deploy/agx"
	"github.com/FyberLabs/hypermesh-host/internal/agent"
	"github.com/FyberLabs/hypermesh-host/internal/pull"
)

const (
	testTenant  = "8f8e8d8c-7b6a-4a59-8c3d-2e1f0a9b8c7d"
	labSerial   = "lab-host-1"
	labDeviceID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func goodFacts() Facts {
	return Facts{
		Arch:          pinArch,
		L4T:           pinL4T,
		Kernel:        "6.8.12-tegra",
		Ubuntu:        pinUbuntu,
		CUDA:          pinCUDA,
		DefaultTarget: pinTarget,
		NvpmodelID:    pinNvpmodelID,
		NvpmodelWatts: pinNvpmodelWatts,
		JetsonClocks:  clocksStopped,
		Tegrastats:    true,
		Docker:        true,
		NvidiaRuntime: true,
		WG:            true,
	}
}

func TestPreflightFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Facts)
		want string
	}{
		{"arch", func(f *Facts) { f.Arch = "x86_64" }, "arch:"},
		{"l4t", func(f *Facts) { f.L4T = "R36.4.3" }, "l4t:"},
		{"kernel", func(f *Facts) { f.Kernel = "6.11.0" }, "kernel:"},
		{"kernel minor glued", func(f *Facts) { f.Kernel = "6.80.0" }, "kernel:"},
		{"ubuntu point", func(f *Facts) { f.Ubuntu = "24.04" }, "ubuntu:"},
		{"ubuntu longer", func(f *Facts) { f.Ubuntu = "24.04.50" }, "ubuntu:"},
		{"cuda", func(f *Facts) { f.CUDA = "12.6.0" }, "cuda:"},
		{"graphical target", func(f *Facts) { f.DefaultTarget = "graphical.target" }, "default target:"},
		{"nvpmodel id", func(f *Facts) { f.NvpmodelID = 0 }, "nvpmodel:"},
		{"nvpmodel watts", func(f *Facts) { f.NvpmodelWatts = "MAXN" }, "nvpmodel:"},
		{"clocks running", func(f *Facts) { f.JetsonClocks = clocksRunning }, "jetson_clocks:"},
		{"clocks unknown", func(f *Facts) { f.JetsonClocks = "" }, "jetson_clocks:"},
		{"tegrastats", func(f *Facts) { f.Tegrastats = false }, "tegrastats:"},
		{"docker", func(f *Facts) { f.Docker = false }, "docker:"},
		{"nvidia runtime", func(f *Facts) { f.NvidiaRuntime = false }, "nvidia container runtime:"},
		{"wg", func(f *Facts) { f.WG = false }, "wg:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := goodFacts()
			tc.edit(&f)
			err := CheckAGX(f)
			if err == nil {
				t.Fatal("expected preflight to fail")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %s", err, tc.want)
			}
		})
	}
	if err := CheckAGX(goodFacts()); err != nil {
		t.Fatal(err)
	}
}

func TestDeployStopsOnPreflightFailure(t *testing.T) {
	root := t.TempDir()
	facts := goodFacts()
	facts.Arch = "x86_64"
	var steps []string
	var units [][]string
	opt := readyOptions(t, root, &planeLab{}, &units)
	opt.Facts = &facts
	err := AGX{}.Pipeline(opt).Run(context.Background(), func(name string) {
		steps = append(steps, name)
	})
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	if !reflect.DeepEqual(steps, []string{StepPreflight}) {
		t.Fatalf("steps %v", steps)
	}
	if len(units) != 0 {
		t.Fatalf("units started: %v", units)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/hypermesh/agent.env")); !os.IsNotExist(err) {
		t.Fatal("configure wrote agent.env after preflight failed")
	}
}

func TestDeployConfigureFailureStopsBeforeConnect(t *testing.T) {
	root := t.TempDir()
	facts := goodFacts()
	var steps []string
	var units [][]string
	opt := readyOptions(t, root, &planeLab{}, &units)
	opt.Facts = &facts
	opt.TenantID = ""
	err := AGX{}.Pipeline(opt).Run(context.Background(), func(name string) {
		steps = append(steps, name)
	})
	if err == nil {
		t.Fatal("expected configure failure")
	}
	if !reflect.DeepEqual(steps, []string{StepPreflight, StepConfigure}) {
		t.Fatalf("steps %v", steps)
	}
	if len(units) != 0 {
		t.Fatalf("connect started units: %v", units)
	}
}

func TestDeployCommandOrder(t *testing.T) {
	root := t.TempDir()
	lab := newPlaneLab(t)
	var units [][]string
	var steps []string
	facts := goodFacts()
	opt := readyOptions(t, root, lab, &units)
	opt.Facts = &facts
	opt.HTTP = lab.client
	opt.PollEvery = time.Millisecond
	opt.HeartbeatWait = time.Second
	err := AGX{}.Pipeline(opt).Run(context.Background(), func(name string) {
		steps = append(steps, name)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(steps, CommandOrder) {
		t.Fatalf("order %v want %v", steps, CommandOrder)
	}
	wantUnits := [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", "--now", wgUnitName},
		{"systemctl", "enable", "--now", agentUnitName},
	}
	if !reflect.DeepEqual(units, wantUnits) {
		t.Fatalf("units %v", units)
	}
	if lab.health != 0 {
		t.Fatalf("health was consulted %d times; catalog is the edge check", lab.health)
	}
	envPath := filepath.Join(root, "etc/hypermesh/agent.env")
	body, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, line := range []string{
		"HYPERMESH_CONTROL_PLANE_URL=" + lab.base,
		"HYPERMESH_TENANT_ID=" + testTenant,
		"HYPERMESH_ENROLL_TOKEN=one-time-token",
		"HYPERMESH_STATE_DIR=/var/lib/hypermesh",
		"HYPERMESH_GGUF_PATH=/home/chris/hypermesh/workload/model.gguf",
		"HYPERMESH_WG_USERSPACE=1",
	} {
		if !strings.Contains(text, line) {
			t.Fatalf("env missing %s\n%s", line, text)
		}
	}
	if strings.Contains(text, "hm_dev_") {
		t.Fatal("device secret was written into agent.env")
	}
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("env mode %o", info.Mode().Perm())
	}
	unit := filepath.Join(root, "etc/systemd/system", agentUnitName)
	got, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(agx.AgentUnit) {
		t.Fatal("agent unit is not the existing enroll unit")
	}
	if !strings.Contains(string(got), "ExecStart=/usr/local/bin/hypermesh-agent") {
		t.Fatal("unit does not start the existing agent")
	}
	stateInfo, err := os.Stat(filepath.Join(root, "var/lib/hypermesh"))
	if err != nil {
		t.Fatal(err)
	}
	if stateInfo.Mode().Perm() != 0o700 {
		t.Fatalf("state mode %o", stateInfo.Mode().Perm())
	}
}

func TestConnectRejectsHealthWithoutCatalog(t *testing.T) {
	root := t.TempDir()
	lab := newPlaneLab(t)
	lab.catalogStatus = http.StatusBadGateway
	var units [][]string
	facts := goodFacts()
	opt := readyOptions(t, root, lab, &units)
	opt.Facts = &facts
	opt.HTTP = lab.client
	opt.HeartbeatWait = 50 * time.Millisecond
	err := AGX{}.Pipeline(opt).Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected catalog failure")
	}
	if !strings.Contains(err.Error(), "catalog") {
		t.Fatalf("error %v", err)
	}
	if len(units) != 0 {
		t.Fatalf("units started without catalog: %v", units)
	}
	if lab.health == 0 && lab.catalog == 0 {
		t.Fatal("catalog was not requested")
	}
}

func TestConnectTakesDeviceIDFromListedHost(t *testing.T) {
	rows := []HostRecord{
		{ID: seedDeviceID, Serial: "seed-row"},
		{ID: labDeviceID, Serial: labSerial},
	}
	id, err := DeviceIDFromList(rows, labSerial)
	if err != nil {
		t.Fatal(err)
	}
	if id != labDeviceID {
		t.Fatalf("id %s", id)
	}
	if _, err := DeviceIDFromList([]HostRecord{{ID: seedDeviceID, Serial: "seed-row"}}, labSerial); err == nil {
		t.Fatal("missing listed host fell back to the seed row")
	}
	if _, err := DeviceIDFromList([]HostRecord{{ID: seedDeviceID, Serial: labSerial}}, labSerial); err == nil {
		t.Fatal("seed id on the listed serial was accepted")
	}
}

func TestHeartbeatMustBeTheListedHost(t *testing.T) {
	root := t.TempDir()
	lab := newPlaneLab(t)
	lab.seedFresh = true
	lab.armLive = false
	var units [][]string
	facts := goodFacts()
	opt := readyOptions(t, root, lab, &units)
	opt.Facts = &facts
	opt.HTTP = lab.client
	opt.PollEvery = time.Millisecond
	opt.HeartbeatWait = 80 * time.Millisecond
	err := AGX{}.Pipeline(opt).Run(context.Background(), nil)
	if err == nil {
		t.Fatal("seed heartbeat was treated as the live host")
	}
	if !strings.Contains(err.Error(), labDeviceID) {
		t.Fatalf("error should name the listed device, got %v", err)
	}
	if strings.Contains(err.Error(), seedDeviceID) {
		t.Fatalf("error named the seed id: %v", err)
	}
}

func TestRenderAgentEnvUsesTestPlane(t *testing.T) {
	body, err := RenderAgentEnv("", testTenant, "tok", "", "/models/model.gguf", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "HYPERMESH_CONTROL_PLANE_URL=https://api.test.hyperme.sh") {
		t.Fatalf("body %s", body)
	}
	if !strings.Contains(body, "HYPERMESH_WG_USERSPACE=1") {
		t.Fatalf("body %s", body)
	}
}

func TestCollectParsesAGXPins(t *testing.T) {
	dir := t.TempDir()
	tegra := filepath.Join(dir, "nv_tegra_release")
	osrel := filepath.Join(dir, "os-release")
	kernel := filepath.Join(dir, "osrelease")
	cuda := filepath.Join(dir, "version.json")
	docker := filepath.Join(dir, "daemon.json")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(tegra, "# R39 (release), REVISION: 2.1, GCID: 1, BOARD: jetson-agx-orin-devkit, EABI: aarch64\n")
	write(osrel, "VERSION_ID=\"24.04\"\nVERSION=\"24.04.5 LTS (Noble Numbat)\"\n")
	write(kernel, "6.8.12-tegra\n")
	write(cuda, `{"cuda":{"version":"13.2.1"}}`)
	write(docker, `{"runtimes":{"nvidia":{"path":"nvidia-container-runtime"}}}`)
	facts := Collect(context.Background(), Source{
		Tegra:      tegra,
		OSRelease:  osrel,
		Kernel:     kernel,
		CUDAJSON:   cuda,
		DockerJSON: docker,
		Output: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "uname":
				return []byte("aarch64\n"), nil
			case "systemctl":
				return []byte("multi-user.target\n"), nil
			case "nvpmodel":
				return []byte("NV Power Mode: MODE_30W\n2\n"), nil
			case "jetson_clocks":
				return []byte("jetson_clocks is not running\n"), nil
			default:
				return nil, os.ErrNotExist
			}
		},
		LookPath: func(name string) (string, error) {
			switch name {
			case "tegrastats", "docker", "wg":
				return "/usr/bin/" + name, nil
			default:
				return "", os.ErrNotExist
			}
		},
	})
	if err := CheckAGX(facts); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseArchivesAreLinuxAMD64AndARM64(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"cmd/hypermesh-deploy", "linux", "amd64", "arm64", "hypermesh-deploy"} {
		if !strings.Contains(text, want) {
			t.Fatalf("goreleaser missing %s", want)
		}
	}
	formula, err := os.ReadFile(filepath.Join("..", "..", "packaging", "homebrew", "hypermesh-deploy.rb"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(formula), "cmd/hypermesh-deploy") {
		t.Fatal("formula does not build hypermesh-deploy")
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "packaging", "homebrew", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "homebrew-tap") || !strings.Contains(string(readme), "go install") {
		t.Fatal("tap notes should say what the tap still needs and keep go install")
	}
}

func TestSerialRequired(t *testing.T) {
	root := t.TempDir()
	facts := goodFacts()
	var units [][]string
	opt := readyOptions(t, root, &planeLab{}, &units)
	opt.Facts = &facts
	opt.Serial = ""
	err := AGX{}.Pipeline(opt).Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "serial") {
		t.Fatalf("error %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "etc/hypermesh/agent.env")); !os.IsNotExist(statErr) {
		t.Fatal("configure wrote agent.env without a serial")
	}
	if len(units) != 0 {
		t.Fatalf("units started: %v", units)
	}
}

func TestRerunSkipsEnrollToken(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "var/lib/hypermesh")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"device_id":"` + labDeviceID + `","device_secret":"hm_dev_fixture"}` + "\n"
	if err := os.WriteFile(filepath.Join(state, "device.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	lab := newPlaneLab(t)
	var units [][]string
	facts := goodFacts()
	opt := readyOptions(t, root, lab, &units)
	opt.Facts = &facts
	opt.EnrollToken = "stale-one-time-token"
	opt.HTTP = lab.client
	opt.PollEvery = time.Millisecond
	opt.HeartbeatWait = time.Second
	if err := (AGX{}).Pipeline(opt).Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(root, "etc/hypermesh/agent.env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), "HYPERMESH_ENROLL_TOKEN") {
		t.Fatalf("rerun wrote an enroll token\n%s", env)
	}
}

func TestEnrollTokenFromEnvOrFile(t *testing.T) {
	got, err := LoadEnrollToken("from-env", "")
	if err != nil || got != "from-env" {
		t.Fatalf("env %q %v", got, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = LoadEnrollToken("", path)
	if err != nil || got != "from-file" {
		t.Fatalf("file %q %v", got, err)
	}
	if _, err := LoadEnrollToken("from-env", path); err == nil {
		t.Fatal("mismatched env and file were accepted")
	}
	got, err = LoadEnrollToken("from-file", path)
	if err != nil || got != "from-file" {
		t.Fatalf("match %q %v", got, err)
	}
}

func TestEnrollTokenNotFromFlag(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "cmd", "hypermesh-deploy", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, `"enroll-token"`) {
		t.Fatal("enroll token is still a command-line flag")
	}
	if !strings.Contains(text, `"enroll-token-file"`) {
		t.Fatal("enroll token file flag is missing")
	}
	roots := []string{
		filepath.Join("..", "..", "cmd", "hypermesh-deploy"),
		".",
	}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), "FYBER-AGX64") || strings.Contains(string(b), "f6124d28") {
				t.Fatalf("%s hardcodes a live serial or device id", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

type planeLab struct {
	mu            sync.Mutex
	base          string
	client        *http.Client
	health        int
	catalog       int
	catalogStatus int
	seedFresh     bool
	armLive       bool
	liveHB        *time.Time
}

func newPlaneLab(t *testing.T) *planeLab {
	t.Helper()
	lab := &planeLab{catalogStatus: http.StatusOK, armLive: true}
	srv := httptest.NewServer(lab)
	t.Cleanup(srv.Close)
	lab.client = srv.Client()
	lab.base = srv.URL
	return lab
}

func (l *planeLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch r.URL.Path {
	case "/api/v1/health", "/api/v1/health/":
		l.health++
		w.WriteHeader(http.StatusInternalServerError)
	case catalogPath:
		l.catalog++
		w.WriteHeader(l.catalogStatus)
		_, _ = w.Write([]byte("[]"))
	case devicesPath:
		now := time.Now()
		old := now.Add(-24 * time.Hour)
		rows := []HostRecord{
			{ID: seedDeviceID, Serial: "seed-row"},
			{ID: labDeviceID, Serial: labSerial},
		}
		if l.seedFresh {
			rows[0].LastHeartbeat = &now
		} else {
			rows[0].LastHeartbeat = &old
		}
		if l.liveHB != nil {
			rows[1].LastHeartbeat = l.liveHB
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rows)
	default:
		http.NotFound(w, r)
	}
}

func (l *planeLab) arm(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.armLive || id != labDeviceID {
		return
	}
	now := time.Now()
	l.liveHB = &now
}

func TestConfigureDefaultsModelPathAndDirectory(t *testing.T) {
	root := t.TempDir()
	var units [][]string
	opt := readyOptions(t, root, nil, &units)
	opt.GGUFPath = ""
	opt.StateDir = "/var/lib/hypermesh"
	if err := configure(context.Background(), opt); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(root, "etc/hypermesh/agent.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "HYPERMESH_GGUF_PATH="+agent.DefaultModelPath+"\n") {
		t.Fatalf("env default path missing:\n%s", env)
	}
	models := filepath.Join(root, "var/lib/hypermesh/models")
	fi, err := os.Stat(models)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("models mode %o", fi.Mode().Perm())
	}
	state, err := os.Stat(filepath.Join(root, "var/lib/hypermesh"))
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode().Perm() != 0o700 {
		t.Fatalf("state mode %o", state.Mode().Perm())
	}
	unit, err := os.ReadFile(filepath.Join(root, "etc/systemd/system", agentUnitName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "HYPERMESH_GGUF_PATH="+agent.DefaultModelPath) {
		t.Fatal("unit missing default GGUF path")
	}
	if !strings.Contains(string(unit), "HYPERMESH_MODEL_CACHE_MAX_BYTES") {
		t.Fatal("unit missing model cache budget")
	}
	cache := filepath.Join(models, "cache")
	cfi, err := os.Stat(cache)
	if err != nil {
		t.Fatal(err)
	}
	if cfi.Mode().Perm() != 0o755 {
		t.Fatalf("cache mode %o", cfi.Mode().Perm())
	}
	capBytes, err := pull.CapacityBytes(models)
	if err != nil {
		t.Fatal(err)
	}
	wantBudget := "HYPERMESH_MODEL_CACHE_MAX_BYTES=" + strconv.FormatUint(uint64(capBytes)-pull.DefaultFloorBytes, 10) + "\n"
	if !strings.Contains(string(env), wantBudget) {
		t.Fatalf("env budget missing %s\n%s", wantBudget, env)
	}
}

func TestRenderAgentEnvKeepsExplicitGGUFPath(t *testing.T) {
	const cottage = "/home/chris/hypermesh/workload/model.gguf"
	body, err := RenderAgentEnv("https://api.test.hyperme.sh", testTenant, "tok", "/var/lib/hypermesh", cottage, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "HYPERMESH_GGUF_PATH="+cottage+"\n") {
		t.Fatalf("explicit path lost:\n%s", body)
	}
	body, err = RenderAgentEnv("https://api.test.hyperme.sh", testTenant, "tok", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "HYPERMESH_GGUF_PATH="+agent.DefaultModelPath+"\n") {
		t.Fatalf("empty path did not default:\n%s", body)
	}
}

func readyOptions(t *testing.T, root string, lab *planeLab, units *[][]string) Options {
	t.Helper()
	base := "https://api.test.hyperme.sh"
	if lab != nil && lab.base != "" {
		base = lab.base
	}
	return Options{
		ControlPlaneURL: base,
		TenantID:        testTenant,
		Serial:          labSerial,
		EnrollToken:     "one-time-token",
		GGUFPath:        "/home/chris/hypermesh/workload/model.gguf",
		Root:            root,
		Run: func(_ context.Context, name string, args ...string) error {
			*units = append(*units, append([]string{name}, args...))
			if lab != nil && len(args) > 0 && args[len(args)-1] == agentUnitName {
				lab.arm(labDeviceID)
			}
			return nil
		},
	}
}
