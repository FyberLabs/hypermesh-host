package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/hardware"
)

type Plan struct {
	SchemaVersion int `json:"schema_version"`
	Resources ResourceReport `json:"resources"`
	Hardware hardware.Profile `json:"hardware"`
	EndpointReachable bool `json:"endpoint_reachable"`
	PreliminaryChecksPassed bool `json:"preliminary_checks_passed"`
	InstallAuthorized bool `json:"install_authorized"`
	Blockers []string `json:"blockers"`
	Pending []string `json:"pending"`
}

type endpointProbe func(context.Context, string) error

// RunPlan collects a read-only preflight report. It never authorizes mutations.
func RunPlan(ctx context.Context, args []string, out, diagnostic io.Writer) int {
	return runPlan(ctx, args, out, diagnostic, probeResources, hardware.Detect, checkEndpoint)
}

func runPlan(ctx context.Context, args []string, out, diagnostic io.Writer,
	resources resourceProbe, detect func(context.Context) hardware.Profile, endpoint endpointProbe) int {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	mode := flags.String("memory-mode", "", "uma or discrete")
	directory := flags.String("model-dir", "", "existing model-cache directory")
	allocation := flags.Int64("uma-gib", 0, "optional diagnostic allocation in GiB; omit for minimum-only installation")
	address := flags.String("aiuditor-check-url", "", "explicit HTTPS readiness URL; no credentials or query parameters")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp { return 0 }
		return 2
	}
	if flags.NArg() != 0 || validateEndpoint(*address) != nil {
		fmt.Fprintln(diagnostic, "provide --aiuditor-check-url as an HTTPS URL without credentials, query or fragment; positional arguments are not accepted")
		return 2
	}
	var resourceJSON bytes.Buffer
	code := runResourceCheck(ctx, []string{"--memory-mode", *mode, "--model-dir", *directory,
		"--uma-gib", strconv.FormatInt(*allocation, 10)}, &resourceJSON, diagnostic, resources)
	if code == 2 { return 2 }
	p := Plan{SchemaVersion: 1, Blockers: []string{}, Pending: []string{
		"Verify readiness-service identity and enrollment protocol compatibility",
		"Validate GPU architecture, runtime allocation limits, model fit and OS/runtime memory headroom",
		"Select a tested versioned OS/kernel/driver profile and signed installer release",
		"Review exact package, headless, service and reboot changes before approving installation",
		"Enroll, verify tunnel/heartbeat, and benchmark before enabling selling",
	}}
	if err := json.Unmarshal(resourceJSON.Bytes(), &p.Resources); err != nil {
		fmt.Fprintln(diagnostic, "resource report could not be generated")
		return 1
	}
	p.Blockers = append(p.Blockers, p.Resources.Admission.Reasons...)
	p.Hardware = detect(ctx)
	switch p.Hardware.Facts.Arch {
	case "amd64", "arm64", "aarch64":
	default:
		p.Blockers = append(p.Blockers, "CPU architecture is not supported by the current Linux agent builds")
	}
	if p.Hardware.Backend == "cpu" || p.Hardware.Backend == "" {
		p.Blockers = append(p.Blockers, "no accelerator backend detected; inspect GPU drivers and hardware recommendations")
	}
	if err := endpoint(ctx, *address); err != nil {
		p.Blockers = append(p.Blockers, err.Error())
	} else {
		p.EndpointReachable = true
	}
	p.PreliminaryChecksPassed = len(p.Blockers) == 0
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(p); err != nil {
		fmt.Fprintln(diagnostic, "cannot write preflight plan")
		return 1
	}
	if !p.PreliminaryChecksPassed { return 1 }
	return 0
}

func validateEndpoint(address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("invalid HTTPS readiness URL")
	}
	return nil
}

func checkEndpoint(ctx context.Context, address string) error {
	client := &http.Client{Timeout: 10*time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return probeEndpoint(ctx, address, client)
}

func probeEndpoint(ctx context.Context, address string, client *http.Client) error {
	if err := validateEndpoint(address); err != nil { return err }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil { return fmt.Errorf("invalid readiness request") }
	response, err := client.Do(req)
	if err != nil {
		// Do not echo URLs, proxy credentials or server response bodies.
		return fmt.Errorf("readiness endpoint unreachable: check DNS, TLS trust, network access and readiness URL")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("readiness check returned HTTP %d; check the configured readiness endpoint", response.StatusCode)
	}
	return nil
}
