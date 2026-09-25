package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	catalogPath = "/api/v1/hypermesh/catalog"
	devicesPath = "/api/v1/hypermesh/devices"
	// seedDeviceID is the placeholder row. The device id for a serial
	// is whatever GET /devices lists. This constant is only a rejection.
	seedDeviceID = "a6400000-0640-4000-8000-000000000001"
)

// HostRecord is one device row from GET /api/v1/hypermesh/devices.
type HostRecord struct {
	ID            string     `json:"id"`
	DeviceID      string     `json:"device_id"`
	Serial        string     `json:"serial"`
	LastHeartbeat *time.Time `json:"last_heartbeat_at"`
}

func (h HostRecord) id() string {
	if id := strings.TrimSpace(h.ID); id != "" {
		return id
	}
	return strings.TrimSpace(h.DeviceID)
}

// DeviceIDFromList returns the device id the plane listed for serial.
// The seed id is refused even when it is the only row, or when it is
// attached to that serial by mistake.
func DeviceIDFromList(rows []HostRecord, serial string) (string, error) {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return "", fmt.Errorf("known host serial is required")
	}
	var match string
	for _, row := range rows {
		if strings.TrimSpace(row.Serial) != serial {
			continue
		}
		id := row.id()
		if id == "" {
			return "", fmt.Errorf("known host %s was listed without a device id", serial)
		}
		if strings.EqualFold(id, seedDeviceID) {
			return "", fmt.Errorf("known host %s listed seed device %s; that row is not the live AGX", serial, seedDeviceID)
		}
		if match != "" && !strings.EqualFold(match, id) {
			return "", fmt.Errorf("known host %s listed more than one device id", serial)
		}
		match = id
	}
	if match == "" {
		return "", fmt.Errorf("known host %s is not in the plane device list", serial)
	}
	return match, nil
}

type plane struct {
	base   string
	tenant string
	http   *http.Client
}

func (p plane) client() *http.Client {
	if p.http != nil {
		return p.http
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (p plane) catalog(ctx context.Context) error {
	status, _, err := p.get(ctx, catalogPath)
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("catalog %s returned HTTP %d", catalogPath, status)
	}
	return nil
}

func (p plane) devices(ctx context.Context) ([]HostRecord, error) {
	status, body, err := p.get(ctx, devicesPath)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("devices returned HTTP %d", status)
	}
	var rows []HostRecord
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, fmt.Errorf("devices returned an empty body")
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("devices: %w", err)
	}
	return rows, nil
}

func (p plane) get(ctx context.Context, path string) (int, []byte, error) {
	base := strings.TrimRight(strings.TrimSpace(p.base), "/")
	if base == "" {
		return 0, nil, fmt.Errorf("control plane URL is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return 0, nil, err
	}
	if strings.TrimSpace(p.tenant) != "" {
		req.Header.Set("X-Tenant-ID", p.tenant)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// startExistingAgent enables the units the enroll path already uses.
// hypermesh-wg owns the userspace interface. hypermesh-agent enrolls,
// heartbeats, and applies the tunnel. This process does not POST enroll
// and does not run wg-quick.
func startExistingAgent(ctx context.Context, run func(context.Context, string, ...string) error) error {
	if run == nil {
		return fmt.Errorf("cannot start units")
	}
	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	if err := run(ctx, "systemctl", "enable", "--now", wgUnitName); err != nil {
		return fmt.Errorf("start %s: %w", wgUnitName, err)
	}
	if err := run(ctx, "systemctl", "enable", "--now", agentUnitName); err != nil {
		return fmt.Errorf("start %s: %w", agentUnitName, err)
	}
	return nil
}

func waitHeartbeat(ctx context.Context, p plane, deviceID string, since time.Time, every, timeout time.Duration) error {
	if every <= 0 {
		every = 500 * time.Millisecond
	}
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		rows, err := p.devices(ctx)
		if err == nil {
			for _, row := range rows {
				if !strings.EqualFold(row.id(), deviceID) {
					continue
				}
				if row.LastHeartbeat != nil && row.LastHeartbeat.After(since) {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("heartbeat was not accepted for %s", deviceID)
		}
		timer := time.NewTimer(every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
