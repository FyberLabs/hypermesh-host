//go:build linux || darwin

package installer

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	mac "github.com/FyberLabs/hypermesh-host/deploy/mac"
	"github.com/FyberLabs/hypermesh-host/internal/agent"
	"github.com/FyberLabs/hypermesh-host/internal/hardware"
	"github.com/FyberLabs/hypermesh-host/internal/hostregistration"
	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

const macRegistrationState = "/var/lib/hypermesh/installer"
const macLabClass = "darwin-arm64-lab"

var macSHA = regexp.MustCompile(`^[a-f0-9]{64}$`)

type macRegistrationConfig struct{ ControlPlane, Tenant, Host, Issuer, Dashboard string }
type macRegistrationDeps struct {
	root      string // Fixtures only; never exposed as a CLI flag.
	detect    func(context.Context) hardware.Profile
	remote    func(string, string) (macRegistrationRemote, error)
	authorize func(context.Context, string, io.Writer) (string, error)
	command   func(context.Context, string, ...string) ([]byte, error)
	absent    func(error) bool
	wait      func(context.Context, time.Duration) error
}

type macRegistrationRemote interface {
	registrationRemote
	Login(context.Context, string, func(string) error) error
	SetSessionRecovery(func(context.Context) error)
}

func macConfig(raw []byte, issuer, dashboard string) (macRegistrationConfig, error) {
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok {
			return macRegistrationConfig{}, fmt.Errorf("agent.env must contain literal KEY=VALUE assignments")
		}
		if _, exists := values[key]; exists {
			return macRegistrationConfig{}, fmt.Errorf("duplicate agent.env key")
		}
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	c := macRegistrationConfig{values["HYPERMESH_CONTROL_PLANE_URL"], values["HYPERMESH_TENANT_ID"], values["HYPERMESH_HOST_NAME"], issuer, strings.TrimRight(dashboard, "/")}
	if !hostregistration.BaseURL(c.ControlPlane) || !hostregistration.UUID.MatchString(c.Tenant) || !hostregistration.BaseURL(issuer) || !hostregistration.Origin(dashboard) || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`).MatchString(c.Host) {
		return c, fmt.Errorf("configure HTTPS control plane, tenant UUID and explicit host name in agent.env; provide trusted --issuer and --dashboard-url")
	}
	if values["HYPERMESH_STATE_DIR"] != "/var/lib/hypermesh" || values["HYPERMESH_WG_USERSPACE"] != "1" || (values["HYPERMESH_WG_IFACE"] != "" && values["HYPERMESH_WG_IFACE"] != "utun8") {
		return c, fmt.Errorf("owner registration requires standard state path and userspace utun8")
	}
	for _, key := range []string{"HYPERMESH_ENROLL_TOKEN", "HYPERMESH_DEVICE_SECRET", "HYPERMESH_REENROLL", "HYPERMESH_WG_KEY_DIR"} {
		if values[key] != "" {
			return c, fmt.Errorf("owner registration refuses token, identity and reenrollment overrides; retain existing identities for explicit recovery")
		}
	}
	return c, nil
}
func macSnapshot(ctx context.Context, d macRegistrationDeps, c macRegistrationConfig) (string, hostregistration.Hardware, error) {
	p := d.detect(ctx)
	f := p.Facts
	if f.Arch != "arm64" || p.Family != "apple-silicon" || !f.Metal || !regexp.MustCompile(`(?i)^Apple M[1-9][0-9]*( (Pro|Max|Ultra))?$`).MatchString(f.Processor) || f.MemoryBytes <= 0 || p.ModelBudgetBytes <= 0 || p.ModelBudgetBytes > f.MemoryBytes {
		return "", hostregistration.Hardware{}, fmt.Errorf("owner registration requires measured Apple silicon memory and hardware; no Jetson class substitution")
	}
	h := hostregistration.Hardware{Profile: "apple-silicon", Architecture: "arm64", CPU: f.Processor, GPU: "metal accelerator", SystemMemoryBytes: f.MemoryBytes, ModelMemoryBytes: p.ModelBudgetBytes}
	files := map[string]string{}
	for _, spec := range []struct {
		path     string
		mode     os.FileMode
		expected string
	}{
		{"/usr/local/bin/hypermesh-agent", 0o755, ""},
		{"/usr/local/libexec/hypermesh/wireguard-go", 0o755, ""},
		{"/usr/local/libexec/hypermesh/wg", 0o755, ""},
		{"/Library/LaunchDaemons/com.fyberlabs.hypermesh-agent.plist", 0o644, mac.AgentPlist},
		{"/Library/LaunchDaemons/com.fyberlabs.hypermesh-wg.plist", 0o644, mac.WireGuardPlist},
		{"/etc/hypermesh/agent.env", 0o600, ""},
	} {
		raw, err := macRead(macPath(d.root, spec.path), spec.mode, 256<<20)
		if err != nil {
			return "", h, err
		}
		if spec.expected != "" && string(raw) != spec.expected {
			return "", h, fmt.Errorf("installed LaunchDaemon differs from the reviewed definition")
		}
		files[spec.path] = macDigest(raw)
	}
	// Free memory is a transient estimate, not a model allocation qualification.
	// Bind the stable hardware to the journal; retain the initial estimate there.
	stable := h
	stable.ModelMemoryBytes = 0
	raw, _ := json.Marshal(struct {
		Fingerprint string
		Config      macRegistrationConfig
		Hardware    hostregistration.Hardware
		Files       map[string]string
	}{p.Fingerprint, c, stable, files})
	return macDigest(raw), h, nil
}
func macNewJournal(sha string, c macRegistrationConfig, h hostregistration.Hardware) (registrationJournal, error) {
	private, public, err := tunnel.GenerateKeypair()
	if err != nil {
		return registrationJournal{}, fmt.Errorf("cannot generate host key")
	}
	id, secret := make([]byte, 16), make([]byte, 24)
	if _, err = rand.Read(id); err != nil {
		return registrationJournal{}, err
	}
	if _, err = rand.Read(secret); err != nil {
		return registrationJournal{}, err
	}
	id[6] = (id[6] & 15) | 64
	id[8] = (id[8] & 63) | 128
	credential := "hm_dev_" + base64.RawURLEncoding.EncodeToString(secret)
	j := registrationJournal{Schema: 1, PlanSHA256: sha, Issuer: c.Issuer, Dashboard: c.Dashboard, PrivateKey: private, DeviceSecret: credential, State: "prepared", Request: hostregistration.Request{RequestID: fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), HostName: c.Host, ClassID: macLabClass, Hardware: h, WGPublicKey: public, DeviceSecretHash: macDigest([]byte(credential)), CredentialPrefix: credential[7:15]}}
	return j, j.Request.Validate()
}
func macValidate(j registrationJournal, sha string, c macRegistrationConfig) error {
	pub, err := tunnel.PublicFromPrivate(j.PrivateKey)
	secret, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(j.DeviceSecret, "hm_dev_"))
	if j.Schema != 1 || j.PlanSHA256 != sha || j.Issuer != c.Issuer || j.Dashboard != c.Dashboard || j.Request.HostName != c.Host || j.Request.ClassID != macLabClass || j.Request.Validate() != nil || err != nil || pub != j.Request.WGPublicKey || e != nil || len(secret) != 24 || "hm_dev_"+base64.RawURLEncoding.EncodeToString(secret) != j.DeviceSecret || macDigest([]byte(j.DeviceSecret)) != j.Request.DeviceSecretHash || j.Request.CredentialPrefix != j.DeviceSecret[7:15] {
		return fmt.Errorf("registration journal or installed configuration changed; preserve all files for recovery")
	}
	switch j.State {
	case "prepared", "pending", "approved", "expired", "denied":
		if j.DeviceID != "" {
			return fmt.Errorf("unexpected journal device identity")
		}
	case "completed", "registered-inactive", "retired":
		if !hostregistration.UUID.MatchString(j.DeviceID) {
			return fmt.Errorf("invalid journal device identity")
		}
	default:
		return fmt.Errorf("invalid registration state")
	}
	if j.State != "prepared" && j.Owner == "" {
		return fmt.Errorf("registration owner is missing")
	}
	return nil
}
func macIdentity(d macRegistrationDeps, j registrationJournal, publish bool) error {
	keyDir := "/var/lib/hypermesh/registration-keys/" + j.Request.RequestID
	raw, _ := json.Marshal(agent.State{WGKeyDir: keyDir, DeviceID: j.DeviceID, CredentialPrefix: j.Request.CredentialPrefix, DeviceSecret: j.DeviceSecret})
	for _, item := range []struct {
		path string
		data []byte
	}{{keyDir + "/hypermesh.key", []byte(j.PrivateKey + "\n")}, {"/var/lib/hypermesh/device.json", raw}} {
		path := macPath(d.root, item.path)
		if publish {
			if !hostregistration.UUID.MatchString(j.DeviceID) {
				return fmt.Errorf("server-assigned device ID required")
			}
			if err := macWrite(path, item.data, false); err != nil {
				return err
			}
		} else {
			if _, err := os.Lstat(path); os.IsNotExist(err) {
				if j.State == "registered-inactive" {
					return fmt.Errorf("completed identity is missing; repair required")
				}
				continue
			}
			got, err := macRead(path, 0o600, 2<<20)
			if err != nil || string(got) != string(item.data) || j.DeviceID == "" {
				return fmt.Errorf("existing identity conflicts; preserve files for recovery")
			}
		}
	}
	return nil
}
func macServices(ctx context.Context, d macRegistrationDeps, allowExisting, start bool) error {
	for _, label := range []string{"com.fyberlabs.hypermesh-wg", "com.fyberlabs.hypermesh-agent"} {
		_, err := d.command(ctx, "/bin/launchctl", "print", "system/"+label)
		if err == nil {
			if !allowExisting {
				return fmt.Errorf("host services must be unloaded before first owner registration")
			}
		} else {
			if !d.absent(err) {
				return fmt.Errorf("cannot determine LaunchDaemon state")
			}
			if start {
				if label == "com.fyberlabs.hypermesh-wg" {
					if _, e := d.command(ctx, "/sbin/ifconfig", "utun8"); e == nil {
						return fmt.Errorf("utun8 already exists without the owned WireGuard service; choose explicit recovery")
					}
				}
				if _, err = d.command(ctx, "/bin/launchctl", "bootstrap", "system", "/Library/LaunchDaemons/"+label+".plist"); err != nil {
					return fmt.Errorf("LaunchDaemon start failed; identity retained, rerun to recover")
				}
			}
		}
		if start && label == "com.fyberlabs.hypermesh-wg" {
			ready := false
			for i := 0; i < 20; i++ {
				if _, err = d.command(ctx, "/sbin/ifconfig", "utun8"); err == nil {
					ready = true
					break
				}
				if err = d.wait(ctx, 250*time.Millisecond); err != nil {
					return err
				}
			}
			if !ready {
				return fmt.Errorf("WireGuard utun8 is not ready; agent was not started")
			}
		}
	}
	return nil
}
func runMacRegister(ctx context.Context, args []string, out, diagnostic io.Writer, d macRegistrationDeps) int {
	flags := flag.NewFlagSet("register-agent", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	issuer := flags.String("issuer", "", "trusted HTTPS Keycloak realm issuer")
	dashboard := flags.String("dashboard-url", "", "trusted HTTPS portal origin")
	restart := flags.Bool("restart-approval", false, "replace a server-confirmed denied or expired request with fresh keys, preserving history")
	start := flags.Bool("start-services", false, "after approval, load WireGuard then agent; host remains paused and unqualified")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || !hostregistration.BaseURL(*issuer) || !hostregistration.Origin(*dashboard) {
		fmt.Fprintln(diagnostic, "require --issuer and --dashboard-url; configuration comes from protected agent.env")
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(diagnostic, err); return 1 }
	ctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	state := macPath(d.root, macRegistrationState)
	unlock, err := macLock(state)
	if err != nil {
		return fail(err)
	}
	defer unlock()
	raw, err := macRead(macPath(d.root, "/etc/hypermesh/agent.env"), 0o600, 2<<20)
	if err != nil {
		return fail(err)
	}
	c, err := macConfig(raw, *issuer, *dashboard)
	if err != nil {
		return fail(err)
	}
	sha, h, err := macSnapshot(ctx, d, c)
	if err != nil {
		return fail(err)
	}
	path := filepath.Join(state, "registration-"+sha+".json")
	save := func(j registrationJournal) error {
		raw, err := json.Marshal(j)
		if err != nil {
			return err
		}
		return macWrite(path, raw, true)
	}
	var j registrationJournal
	raw, err = macRead(path, 0o600, 2<<20)
	if err == nil {
		err = macDecode(raw, &j)
		if err == nil {
			err = macValidate(j, sha, c)
		}
	} else if _, e := os.Lstat(path); os.IsNotExist(e) {
		// Any prior journal belongs to a different reviewed installation. Never
		// silently replace its request after an env or binary change.
		previous, e := filepath.Glob(filepath.Join(state, "registration-*.json"))
		if e != nil || len(previous) > 0 {
			return fail(fmt.Errorf("existing registration requires recovery with its original configuration"))
		}
		if _, e = os.Lstat(macPath(d.root, "/var/lib/hypermesh/device.json")); !os.IsNotExist(e) {
			return fail(fmt.Errorf("existing device identity requires explicit recovery"))
		}
		if e = macServices(ctx, d, false, false); e != nil {
			return fail(e)
		}
		j, err = macNewJournal(sha, c, h)
		if err == nil {
			err = save(j)
		}
	}
	if err != nil {
		return fail(err)
	}
	savedHardware, currentHardware := j.Request.Hardware, h
	savedHardware.ModelMemoryBytes = 0
	currentHardware.ModelMemoryBytes = 0
	if savedHardware != currentHardware || j.Request.Hardware.ModelMemoryBytes > h.SystemMemoryBytes {
		return fail(fmt.Errorf("journal hardware differs from the measured Apple silicon host"))
	}
	if err = macIdentity(d, j, false); err != nil {
		return fail(err)
	}
	if err = macServices(ctx, d, j.DeviceID != "", false); err != nil {
		return fail(err)
	}
	if j.State == "retired" {
		return fail(fmt.Errorf("registration %s; preserve journal and use explicit recovery, no automatic replacement", j.State))
	}
	remote, err := d.remote(c.ControlPlane, c.Tenant)
	if err != nil {
		return fail(err)
	}
	login := func(ctx context.Context) error {
		token, e := d.authorize(ctx, c.Issuer, diagnostic)
		if e != nil {
			return e
		}
		return remote.Login(ctx, token, func(owner string) error {
			if j.Owner != "" && j.Owner != owner {
				return fmt.Errorf("login belongs to a different owner; preserve registration")
			}
			j.Owner = owner
			return save(j)
		})
	}
	if err = login(ctx); err != nil {
		return fail(err)
	}
	remote.SetSessionRecovery(login)
	if *restart {
		status, e := remote.Get(ctx, j.Request)
		if e != nil {
			return fail(e)
		}
		if (status.State != "denied" && status.State != "expired") || status.DeviceID != nil || j.DeviceID != "" {
			return fail(fmt.Errorf("only a server-confirmed denied or expired approval can restart"))
		}
		if e = macIdentity(d, j, false); e != nil {
			return fail(e)
		}
		current, fresh, e := macSnapshot(ctx, d, c)
		if e != nil {
			return fail(e)
		}
		if current != sha {
			return fail(fmt.Errorf("installation changed before restarting approval"))
		}
		old := j
		old.State = status.State
		history, _ := json.Marshal(old)
		if e = macWrite(filepath.Join(state, "registration-history-"+j.Request.RequestID+".json"), history, false); e != nil {
			return fail(e)
		}
		next, e := macNewJournal(sha, c, fresh)
		if e != nil {
			return fail(e)
		}
		next.Owner = j.Owner
		if e = save(next); e != nil {
			return fail(e)
		}
		j = next
	}

	check := func(ctx context.Context) error {
		current, _, e := macSnapshot(ctx, d, c)
		if e != nil {
			return e
		}
		if current != sha {
			return fmt.Errorf("installed configuration changed during approval")
		}
		if e = macIdentity(d, j, false); e != nil {
			return e
		}
		return macServices(ctx, d, j.DeviceID != "", false)
	}
	checked := preflightMacRemote{remote, check}
	err = finishRegistration(ctx, &j, checked, save, func(value registrationJournal) error {
		if e := check(ctx); e != nil {
			return e
		}
		return macIdentity(d, value, true)
	}, diagnostic, d.wait)
	if err == nil && *start {
		err = check(ctx)
		if err == nil {
			err = macServices(ctx, d, true, true)
		}
	}
	status := macPublicRegistration(j)
	if err == nil {
		status.NextAction = "Host identity saved. Host remains paused and enrollment-only; certification and selling are unavailable for this class. Without --start-services, rerun the same command with it to load WireGuard then agent."
	}
	if json.NewEncoder(out).Encode(status) != nil {
		return 1
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

type preflightMacRemote struct {
	registrationRemote
	check func(context.Context) error
}

func (r preflightMacRemote) Complete(ctx context.Context, request hostregistration.Request) (hostregistration.Status, error) {
	if err := r.check(ctx); err != nil {
		return hostregistration.Status{}, err
	}
	return r.registrationRemote.Complete(ctx, request)
}
func runMacRegistrationStatus(args []string, out, diagnostic io.Writer, root string) int {
	flags := flag.NewFlagSet("registration-status", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	sha := flags.String("plan-sha", "", "plan_sha256 from register-agent output")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || !macSHA.MatchString(*sha) {
		fmt.Fprintln(diagnostic, "require --plan-sha SHA256")
		return 2
	}
	path := filepath.Join(macPath(root, macRegistrationState), "registration-"+*sha+".json")
	raw, err := macRead(path, 0o600, 2<<20)
	var j registrationJournal
	if err == nil {
		err = macDecode(raw, &j)
	}
	if err != nil || j.Schema != 1 || j.PlanSHA256 != *sha || macValidate(j, *sha, macRegistrationConfig{Host: j.Request.HostName, Issuer: j.Issuer, Dashboard: j.Dashboard}) != nil {
		fmt.Fprintln(diagnostic, "registration journal unavailable or invalid")
		return 1
	}
	if json.NewEncoder(out).Encode(macPublicRegistration(j)) != nil {
		return 1
	}
	return 0
}

func macPublicRegistration(j registrationJournal) registrationStatus {
	s := publicRegistration(j)
	switch j.State {
	case "expired", "denied":
		s.NextAction = "Rerun the original register-agent command with --restart-approval after signing in as the original owner; this archives the old request and requires fresh browser approval."
	case "retired":
		return s
	case "registered-inactive":
		s.NextAction = "Identity saved; host remains paused and enrollment-only. Rerun with --start-services to load WireGuard then agent if needed."
	default:
		s.NextAction = "Rerun the same register-agent command with the original agent.env and installed files; preserve the private journal."
	}
	return s
}
