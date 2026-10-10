package pathb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/telem"
)

// bootFile is last-seen boot identity in the state dir. Not a secret.
const bootFile = "boot.json"

// bootTimeSlackS is how far derived boot time may drift on the same
// boot before we treat it as a reboot (uptime integer rounding).
const bootTimeSlackS int64 = 5

// Record is last-seen boot identity. Not a secret; do not put it in
// device.json.
type Record struct {
	BootID       string `json:"boot_id,omitempty"`
	UptimeS      int64  `json:"uptime_s,omitempty"`
	BootTimeUnix int64  `json:"boot_time_unix,omitempty"`
}

// Store persists last-seen boot identity for the lab soak.
type Store interface {
	Load() (Record, error)
	Save(Record) error
}

// FileStore writes boot.json under Dir (the agent state dir).
type FileStore struct {
	Dir string
}

func (s FileStore) path() string {
	return filepath.Join(s.Dir, bootFile)
}

// Load returns a zero Record when the file is missing or empty.
func (s FileStore) Load() (Record, error) {
	if strings.TrimSpace(s.Dir) == "" {
		return Record{}, nil
	}
	b, err := os.ReadFile(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			return Record{}, nil
		}
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(b, &rec); err != nil {
		return Record{}, nil
	}
	return rec, nil
}

// Save writes last-seen identity. Mode 0644: this is not a secret.
func (s FileStore) Save(rec Record) error {
	if strings.TrimSpace(s.Dir) == "" {
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r Runner) identity(snap telem.Snapshot) Record {
	bootTime := r.now().Unix() - snap.UptimeS
	if bootTime < 0 {
		bootTime = 0
	}
	id := strings.TrimSpace(snap.BootID)
	if id == "" {
		id = "boottime:" + strconv.FormatInt(bootTime, 10)
	}
	return Record{BootID: id, UptimeS: snap.UptimeS, BootTimeUnix: bootTime}
}

func kernelBootID(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" && !strings.HasPrefix(id, "boottime:")
}

// rebooted reports a boot change since the last recorded identity.
// First run (no prior boot_id) is not a reboot.
func rebooted(prior, cur Record) bool {
	if strings.TrimSpace(prior.BootID) == "" {
		return false
	}
	if kernelBootID(prior.BootID) && kernelBootID(cur.BootID) {
		return prior.BootID != cur.BootID
	}
	if cur.UptimeS < prior.UptimeS {
		return true
	}
	if prior.BootTimeUnix > 0 && cur.BootTimeUnix > 0 && abs64(cur.BootTimeUnix-prior.BootTimeUnix) > bootTimeSlackS {
		return true
	}
	return false
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// checkBoot compares current identity to last-seen and records the new
// one. ok is false only when this is a reboot (reason "reboot").
func (r Runner) checkBoot(snap telem.Snapshot) (passed bool, rec Record) {
	cur := r.identity(snap)
	if r.Store == nil {
		return true, cur
	}
	prior, err := r.Store.Load()
	if err != nil {
		prior = Record{}
	}
	reboot := rebooted(prior, cur)
	_ = r.Store.Save(cur)
	if reboot {
		return false, cur
	}
	return true, cur
}
