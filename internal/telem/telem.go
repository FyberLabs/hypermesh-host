// Package telem collects host facts the agent may send to the control
// plane: image hash, nvpmodel, thermals, uptime.
//
// Never log enroll tokens, device secrets, or renter prompts. This
// package does not touch those values.
package telem

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Snapshot is a point-in-time host reading.
type Snapshot struct {
	EnvironmentHash string
	ImageHash       string
	UptimeS         int64
	ThermalC        *float64
	Nvpmodel        string
	// BootID is /proc/sys/kernel/random/boot_id when that file exists.
	// Empty means the caller should fall back to boot time from uptime.
	BootID string
}

// Source reads host files and optional nvpmodel. Tests inject paths.
type Source struct {
	// EnvironmentFiles overrides the version/configuration inventory in tests.
	EnvironmentFiles []string
	ImageHashEnv     string
	ImageHashFile    string
	TegraRelease     string
	OSRelease        string
	UptimePath       string
	BootIDPath       string
	ThermalRoot      string
	NvpmodelBin      string
	LookPath         func(string) (string, error)
	Run              func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Default returns a Source aimed at a Jetson (or any Linux) host.
func Default() Source {
	return Source{
		ImageHashEnv:  "HYPERMESH_IMAGE_HASH",
		ImageHashFile: "/etc/hypermesh/image-hash",
		TegraRelease:  "/etc/nv_tegra_release",
		OSRelease:     "/etc/os-release",
		UptimePath:    "/proc/uptime",
		BootIDPath:    "/proc/sys/kernel/random/boot_id",
		ThermalRoot:   "/sys/class/thermal",
		NvpmodelBin:   "nvpmodel",
	}
}

// Collect reads whatever is available. Missing sensors are not an error.
func (s Source) Collect(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	up, err := s.uptime()
	if err != nil {
		return Snapshot{}, err
	}
	if up < 0 {
		up = 0
	}
	power := s.nvpmodel(ctx)
	return Snapshot{
		EnvironmentHash: s.environmentHash(power),
		ImageHash:       s.imageHash(),
		UptimeS:         up,
		ThermalC:        s.thermal(),
		Nvpmodel:        power,
		BootID:          s.bootID(),
	}, nil
}

func (s Source) bootID() string {
	path := s.BootIDPath
	if path == "" {
		path = "/proc/sys/kernel/random/boot_id"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (s Source) imageHash() string {
	if s.ImageHashEnv != "" {
		if v := strings.TrimSpace(os.Getenv(s.ImageHashEnv)); v != "" {
			return v
		}
	}
	if s.ImageHashFile != "" {
		if b, err := os.ReadFile(s.ImageHashFile); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				return v
			}
		}
	}
	if h := hashFile(s.TegraRelease); h != "" {
		return h
	}
	return hashFile(s.OSRelease)
}

func hashFile(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s Source) uptime() (int64, error) {
	path := s.UptimePath
	if path == "" {
		path = "/proc/uptime"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("telem: uptime: %w", err)
	}
	field := strings.Fields(string(b))
	if len(field) == 0 {
		return 0, fmt.Errorf("telem: uptime: empty")
	}
	f, err := strconv.ParseFloat(field[0], 64)
	if err != nil {
		return 0, fmt.Errorf("telem: uptime: %w", err)
	}
	return int64(f), nil
}

func (s Source) thermal() *float64 {
	root := s.ThermalRoot
	if root == "" {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(root, "thermal_zone*", "temp"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	var max float64
	var found bool
	for _, p := range matches {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil {
			continue
		}
		c := float64(n) / 1000.0
		if !found || c > max {
			max = c
			found = true
		}
	}
	if !found {
		return nil
	}
	return &max
}

func (s Source) nvpmodel(ctx context.Context) string {
	name := s.NvpmodelBin
	if name == "" {
		name = "nvpmodel"
	}
	look := s.LookPath
	if look == nil {
		look = exec.LookPath
	}
	bin, err := look(name)
	if err != nil {
		return ""
	}
	run := s.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	out, err := run(ctx, bin, "-q")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
