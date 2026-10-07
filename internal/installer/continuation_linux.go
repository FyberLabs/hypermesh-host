//go:build linux

package installer

import (
	"context"
	"fmt"
	"github.com/FyberLabs/hypermesh-host/internal/update"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/deviceauth"
	"github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)

type continuationConsent struct{}
type continuationInteractive struct{}

func withContinuationConsent(ctx context.Context) context.Context {
	return context.WithValue(ctx, continuationConsent{}, ctx.Value(continuationInteractive{}) != true)
}

const continuationBinary = "/var/lib/hypermesh/installer/continuation/hypermesh-deploy"

// Consent authorizes only these onboarding operations. It is not a general
// "yes" stream: unexpected changes and maintenance approvals remain blocked.
func continuationPrompt(label string) bool {
	for _, allowed := range []string{
		"Apply these prerequisite and headless changes?",
		"Install these verified files and the inactive systemd service?",
		"Reboot this machine now?",
		"Request the reboot again now?",
		"Download this model and run its hardware certification test?",
		"Activate the host service in paused mode?",
		"The previous model test expired or failed. Keep its history and obtain a fresh plan to rebenchmark?",
		"Download the pinned model and run these tests? Evidence goes to operator review; services remain inactive and selling stays disabled.",
	} {
		if label == allowed {
			return true
		}
	}
	return false
}

func earlyEnrollment(ctx context.Context, dir string, s *guidedState, p SetupPlan, out io.Writer) error {
 if err:=completeEarlyEnrollment(ctx,dir,s,p,out);err!=nil{return err}
 var j registrationJournal
 if err:=guidedRead(filepath.Join(dir,"enrollment.json"),&j,true);err!=nil{return err}
 n,err:=installationNetwork(s.Network);if err!=nil{return err}
 return bindMachineIdentity(ctx,n.Plane,s.Tenant,j)
}
func completeEarlyEnrollment(ctx context.Context, dir string, s *guidedState, p SetupPlan, out io.Writer) error {
	n, err := installationNetwork(s.Network)
	if err != nil {
		return err
	}
	// This identity is bound to the reviewed setup plan before installing files.
	// Registration is paused/unqualified on the plane; it grants no workload rights.
	ap := agentInstallPlan{SHA256: p.PlanSHA256, Setup: p, HostName: s.Host, TenantID: s.Tenant, ControlPlane: n.Plane}
	path := filepath.Join(dir, "enrollment.json")
	var j registrationJournal
	if _, e := os.Lstat(path); e == nil {
		if err = guidedRead(path, &j, true); err != nil {
			return err
		}
		if err = validateRegistration(j, ap, n.Issuer, n.Dashboard, s.Class); err != nil {
			return err
		}
		if j.State == "registered-inactive" {
			return nil
		}
		if j.State == "completed" {
			j.State = "registered-inactive"
			return saveRegistration(path, j)
		}
	} else if os.IsNotExist(e) {
		j, err = newRegistration(ap, n.Issuer, n.Dashboard, s.Class, p.Preflight)
		if err != nil {
			return err
		}
		if err = saveRegistration(path, j); err != nil {
			return err
		}
	} else {
		return e
	}
	remote, err := newRegistrationClient(n.Plane, s.Tenant)
	if err != nil {
		return err
	}
	save := func(value registrationJournal) error { return saveRegistration(path, value) }
	if ctx.Value(continuationConsent{}) == true {
		return fmt.Errorf("durable owner enrollment is missing; automatic continuation cannot perform browser sign-in")
	}
	renew := func(status hostregistration.Status) error {
		if status.State != "expired" {
			return fmt.Errorf("only expired enrollment can be renewed")
		}
		var e error
		j, e = restartRegistration(j, status, ap, p.Preflight, func(old registrationJournal) error { return archiveRegistration(dir, old) }, save)
		return e
	}
	if savedRegistrationExpired(j, time.Now()) {
		if err = renew(hostregistration.Status{State: "expired"}); err != nil {
			return err
		}
	}
	login := func(ctx context.Context) error {
		token, e := deviceauth.Authorize(ctx, n.Issuer, out)
		if e != nil {
			return e
		}
		return remote.Login(ctx, token.Bearer(), func(owner string) error {
			if j.Owner != "" && j.Owner != owner {
				return fmt.Errorf("registration belongs to another owner")
			}
			j.Owner = owner
			return save(j)
		})
	}
	if err = login(ctx); err != nil {
		return err
	}
	remote.SetSessionRecovery(login)
	if err = prepareSavedRegistration(ctx, j, remote, renew); err != nil {
		return err
	}
	return recoverRegistrationExpiry(func() error {
		return finishRegistration(ctx, &j, remote, save, func(registrationJournal) error { return nil }, out, waitRegistration)
	}, func() error {
		status, e := remote.Get(ctx, j.Request)
		if e != nil {
			return e
		}
		return renew(status)
	}, out)

}

func adoptEarlyEnrollment(ctx context.Context, dir string, p agentInstallPlan, s guidedState) (bool, error) {
	path := filepath.Join(dir, "enrollment.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var j registrationJournal
	if err := guidedRead(path, &j, true); err != nil {
		return false, err
	}
	n, err := installationNetwork(s.Network)
	if err != nil {
		return false, err
	}
	early := p
	early.SHA256 = p.Setup.PlanSHA256
	if err = validateRegistration(j, early, n.Issuer, n.Dashboard, s.Class); err != nil {
		return false, err
	}
	if j.State != "registered-inactive" {
		return false, fmt.Errorf("owner enrollment is incomplete")
	}
	if err = verifyRegistrationService(ctx, p); err != nil {
		return false, err
	}
	if _, err = registrationPreflight(ctx, p, s.Class); err != nil {
		return false, err
	}
	j.PlanSHA256 = p.SHA256
	target := registrationPath(defaultSetupState, p.SHA256)
	if _, e := os.Lstat(target); e == nil {
		var existing registrationJournal
		if err = guidedRead(target, &existing, true); err != nil {
			return false, err
		}
		if !sameSetupJSON(existing, j) {
			return false, fmt.Errorf("existing registration differs from early enrollment")
		}
	} else if os.IsNotExist(e) {
		if err = saveRegistration(target, j); err != nil {
			return false, err
		}
	} else {
		return false, e
	}
	if err = publishRegistration("", j); err != nil {
		return false, err
	}
	return true, nil
}

func continuationFiles(binary []byte) []agentPayload {
	unit := `[Unit]
Description=Hypermesh onboarding continuation
Wants=network-online.target
After=network-online.target
ConditionPathExists=/var/lib/hypermesh/installer/guided/session.json
[Service]
Type=oneshot
ExecStart=/var/lib/hypermesh/installer/continuation/hypermesh-deploy install --continue
User=root
UMask=0077
TimeoutStartSec=3h
StandardOutput=journal
StandardError=journal
`
	timer := `[Unit]
Description=Resume Hypermesh onboarding after boot or interruption
[Timer]
OnBootSec=45s
OnUnitInactiveSec=60s
RandomizedDelaySec=15s
Unit=hypermesh-onboarding.service
[Install]
WantedBy=timers.target
`
	makeFile := func(path string, data []byte, mode os.FileMode) agentPayload {
		return agentPayload{agentFile{path, digestSetupBytes(data), uint32(mode)}, data}
	}
	return []agentPayload{makeFile(continuationBinary, binary, 0700),
		makeFile("/etc/systemd/system/hypermesh-onboarding.service", []byte(unit), 0644),
		makeFile("/etc/systemd/system/hypermesh-onboarding.timer", []byte(timer), 0644)}
}

// Read the executing inode, never the replaceable path returned by os.Executable.
func runningContinuation() ([]byte, error) {
	f, err := os.Open("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 256<<20 {
		return nil, fmt.Errorf("invalid continuation executable")
	}
	binary, err := io.ReadAll(io.LimitReader(f, (256<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(binary) > 256<<20 {
		return nil, fmt.Errorf("continuation executable exceeds limit")
	}
	return binary, nil
}
func continuationImage(sha string) (string, error) {
	if !setupDigestRE.MatchString(sha) {
		return "", fmt.Errorf("invalid continuation digest")
	}
	return filepath.Join(filepath.Dir(continuationBinary), "images", sha, "hypermesh-deploy"), nil
}
func continuationTarget(s guidedState) (string, error) {
	binary, err := runningContinuation()
	if err != nil {
		return "", err
	}
	if digestSetupBytes(binary) == s.ContinuationSHA {
		return "", nil
	}
	path, err := continuationImage(s.ContinuationSHA)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (256<<20)+1))
	if err != nil {
		return "", err
	}
	payload := agentPayload{agentFile{path, s.ContinuationSHA, 0700}, data}
	if digestSetupBytes(data) != s.ContinuationSHA {
		return "", fmt.Errorf("continuation image checksum differs")
	}
	if ok, err := agentFileMatches(path, payload); err != nil || !ok {
		return "", fmt.Errorf("invalid continuation image: %v", err)
	}
	return path, nil
}
func runContinuation(ctx context.Context, path string, input io.Reader, out, diagnostic io.Writer) int {
	cmd := exec.CommandContext(ctx, path, "install", "--continue")
	cmd.Stdin = input
	cmd.Stdout = out
	cmd.Stderr = diagnostic
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(diagnostic, "continuation failed:", err)
		return 1
	}
	return 0
}
func installContinuation(ctx context.Context, s *guidedState) error {
	binary, err := runningContinuation()
	if err != nil {
		return err
	}
	sha := digestSetupBytes(binary)
	// A saved digest is root-owned verification evidence. A new build must match
	// the signed release artifact before it becomes eligible for the timer.
	if s.ContinuationSHA != sha {
		key, err := readSetupRegular(s.Download.TrustedKey, false)
		if err != nil {
			return err
		}
		fps, err := update.ParseFingerprints(s.Download.Fingerprint)
		if err != nil {
			return err
		}
		metadata, verified, err := fetchInstallerUpdate(ctx, installerUpdatePlan{ReleaseURL: s.Download.ReleaseURL, Channel: s.Download.Channel, Arch: runtime.GOARCH, PublicKey: string(key), Fingerprints: fps, Release: releaseMetadata{Build: int64(s.Download.Build)}})
		if err != nil {
			return err
		}
		if metadata.Build != int64(s.Download.Build) || metadata.Commit != s.Download.Commit || metadata.Channel != s.Download.Channel || digestSetupBytes(verified) != sha {
			return fmt.Errorf("running continuation does not match signed release")
		}
	}
	image, err := continuationImage(sha)
	if err != nil {
		return err
	}
	payload := agentPayload{agentFile{image, sha, 0700}, binary}
	if exists, e := agentFileMatches(image, payload); e != nil {
		return e
	} else if !exists {
		if e = writeAgentFile(image, payload); e != nil {
			return e
		}
	}
	// The first verified binary remains a stable dispatcher. Only session.json
	// selects the active immutable image, so failed upgrades retain the old one.
	launcher := binary
	if s.LauncherSHA != "" {
		if !setupDigestRE.MatchString(s.LauncherSHA) {
			return fmt.Errorf("invalid launcher digest")
		}
		f, e := os.Open(continuationBinary)
		if e != nil {
			return e
		}
		launcher, e = io.ReadAll(io.LimitReader(f, (256<<20)+1))
		f.Close()
		if e != nil {
			return e
		}
		if digestSetupBytes(launcher) != s.LauncherSHA {
			return fmt.Errorf("continuation launcher checksum differs")
		}
	}

	files := continuationFiles(launcher)
	defaults, err := (setupSource{}).output(ctx, "systemd-analyze", "--system", "unit-paths")
	if err != nil {
		return err
	}
	actual, err := (setupSource{}).output(ctx, "systemctl", "--system", "show", "-p", "UnitPath", "--value")
	if err != nil {
		return err
	}
	if err = checkContinuationUnits("", files, string(defaults)+"\n"+string(actual)); err != nil {
		return err
	}
	for _, file := range files {
		exists, e := agentFileMatches(file.Path, file)
		if e != nil {
			return e
		}
		if !exists {
			if e = writeAgentFile(file.Path, file); e != nil {
				return e
			}
		}
	}
	if _, err = (setupSource{}).output(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	_, err = (setupSource{}).output(ctx, "systemctl", "enable", "--now", "hypermesh-onboarding.timer")
	if err == nil {
		s.ContinuationSHA = sha
		s.LauncherSHA = digestSetupBytes(launcher)
	}
	return err
}

func checkContinuationUnits(root string, files []agentPayload, raw string) error {
	paths := strings.Fields(raw)
	if len(paths) == 0 {
		return fmt.Errorf("systemd unit search paths unavailable")
	}
	managed := map[string]bool{}
	for _, f := range files {
		managed[f.Path] = true
	}
	for _, dir := range paths {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
			return fmt.Errorf("invalid systemd unit search path")
		}
		for _, name := range []string{"hypermesh-onboarding.service", "hypermesh-onboarding.timer", "hypermesh-onboarding.service.d", "hypermesh-onboarding.timer.d", "hypermesh-.service.d", "hypermesh-.timer.d", "service.d", "timer.d"} {
			path := filepath.Join(dir, name)
			if managed[path] {
				continue
			}
			if _, e := os.Lstat(agentDest(root, path)); !os.IsNotExist(e) {
				return fmt.Errorf("unreviewed continuation unit or drop-in: %s", path)
			}
		}
	}
	return nil
}

type continuationStatus struct {
	Schema      int       `json:"schema"`
	Stage       int       `json:"stage"`
	State       string    `json:"state"`
	CheckedAt   time.Time `json:"checked_at"`
	Failures    int       `json:"failures"`
	NextAttempt time.Time `json:"next_attempt"`
}

func nextContinuation(previous continuationStatus, s guidedState, failed bool, now time.Time) continuationStatus {
	result := continuationStatus{Schema: 1, Stage: s.Stage, State: "waiting", CheckedAt: now, NextAttempt: now.Add(time.Minute)}
	if s.Stage == 7 {
		result.State = "complete"
		result.NextAttempt = time.Time{}
	}
	if failed {
		result.State = "retrying"
		result.Failures = 1
		if previous.Stage == s.Stage && previous.State == "retrying" {
			result.Failures = previous.Failures + 1
		}
		if result.Failures > 7 {
			result.Failures = 7
		}
		delay := time.Minute * time.Duration(1<<uint(result.Failures-1))
		if delay > time.Hour {
			delay = time.Hour
		}
		result.NextAttempt = now.Add(delay)
	}
	return result
}

func readContinuation(dir string) (continuationStatus, error) {
	var status continuationStatus
	path := filepath.Join(dir, "continuation-status.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return status, nil
	} else if err != nil {
		return status, err
	}
	if err := guidedRead(path, &status, true); err != nil {
		return status, err
	}
	if status.Schema != 1 || status.Stage < 0 || status.Stage > 7 || status.Failures < 0 || status.Failures > 7 ||
		!supportOneOf(status.State, "waiting", "complete", "retrying") || status.CheckedAt.IsZero() {
		return status, fmt.Errorf("invalid continuation checkpoint")
	}
	return status, nil
}

func recordContinuation(dir string, s guidedState, err error) error {
	previous, e := readContinuation(dir)
	if e != nil {
		return e
	}
	// Public status deliberately excludes raw errors, paths, tokens and keys.
	return writeGuided(filepath.Join(dir, "continuation-status.json"), nextContinuation(previous, s, err != nil, time.Now().UTC()))
}
