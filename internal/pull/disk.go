package pull

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	// DefaultFloorBytes is free space that must remain after a model
	// download so the OS is not pinned at 100%. 2 GiB.
	DefaultFloorBytes uint64 = 2 << 30
	// DefaultMarginBytes is extra space required beyond the object size
	// for filesystem metadata. 64 MiB.
	DefaultMarginBytes uint64 = 64 << 20
)

// DiskError is a preflight refusal. Error() is the short job-result reason.
//
// A download whose size is still unknown fails closed. HTTP HEAD without
// a Content-Length, and a job with no size_bytes, cannot guarantee the
// margin, so nothing is written.
type DiskError struct {
	Need      uint64
	Free      int64
	Evictable uint64
	// WithEvictable includes evictable even when it is zero. The cache
	// planner sets it. A plain preflight leaves it false.
	WithEvictable bool
	Unknown       bool
	StatErr       bool
}

func (e *DiskError) Error() string {
	if e == nil {
		return "insufficient_disk"
	}
	switch {
	case e.StatErr:
		return "insufficient_disk: free space unknown"
	case e.Unknown:
		return fmt.Sprintf("insufficient_disk: size unknown, free %d", e.Free)
	default:
		if e.WithEvictable {
			return fmt.Sprintf("insufficient_disk: need %d, free %d, evictable %d", e.Need, e.Free, e.Evictable)
		}
		return fmt.Sprintf("insufficient_disk: need %d, free %d", e.Need, e.Free)
	}
}

// Limits is the free-space budget for one download.
// Expected <= 0 means the caller does not know the size yet.
// FloorSet false treats Floor 0 as DefaultFloorBytes.
// MarginSet false treats Margin 0 as DefaultMarginBytes.
// An explicit 0 floor or margin is honored when the matching Set flag is true.
type Limits struct {
	Expected  int64
	Floor     uint64
	Margin    uint64
	FloorSet  bool
	MarginSet bool
	Stat      func(path string) (free int64, err error)
	// Head resolves Content-Length when Expected <= 0.
	// Nil uses HTTP HEAD. A missing length fails closed.
	Head func(ctx context.Context, rawURL string) (int64, error)
	// Reclaim is bytes the caller will delete after this check passes
	// and before any download byte is written. Preflight adds it to the
	// statfs free value so a stubbed free number still sees the room
	// those files will release. It is not added twice: the files are
	// still on disk during the check.
	Reclaim uint64
	// Evictable is the total bytes of models the cache is allowed to
	// delete. WithEvictable reports that number in the refusal, including 0.
	Evictable     uint64
	WithEvictable bool
	// BeforeWrite runs after preflight succeeds and before the download.
	// The model cache uses it to evict. A refusal must not call it.
	BeforeWrite func() error
}

func (l Limits) floor() uint64 {
	if l.FloorSet || l.Floor > 0 {
		return l.Floor
	}
	return DefaultFloorBytes
}

func (l Limits) margin() uint64 {
	if l.MarginSet || l.Margin > 0 {
		return l.Margin
	}
	return DefaultMarginBytes
}

func (l Limits) stat(path string) (int64, error) {
	if l.Stat != nil {
		return l.Stat(path)
	}
	return FreeBytes(path)
}

// NeedBytes is size + margin + floor. ok is false on overflow.
func NeedBytes(size int64, floor, margin uint64) (uint64, bool) {
	if size <= 0 {
		return 0, false
	}
	n := uint64(size)
	if n > ^uint64(0)-margin {
		return 0, false
	}
	n += margin
	if n > ^uint64(0)-floor {
		return 0, false
	}
	return n + floor, true
}

// Preflight refuses the download when dest's filesystem cannot hold size
// plus the margin and still leave the floor free. size <= 0 is unknown
// and fails closed. Nothing is created.
func Preflight(dest string, size int64, lim Limits) error {
	if err := validateDest(dest); err != nil {
		return err
	}
	free, err := lim.stat(dest)
	if err != nil || free < 0 {
		return &DiskError{StatErr: true}
	}
	if size <= 0 {
		return &DiskError{Unknown: true, Free: free}
	}
	need, ok := NeedBytes(size, lim.floor(), lim.margin())
	if !ok {
		return &DiskError{Unknown: true, Free: free}
	}
	have, ok := freePlus(free, lim.Reclaim)
	if !ok || have < need {
		return &DiskError{
			Need:          need,
			Free:          free,
			Evictable:     lim.Evictable,
			WithEvictable: lim.WithEvictable,
		}
	}
	return nil
}

func freePlus(free int64, reclaim uint64) (uint64, bool) {
	if free < 0 {
		return 0, false
	}
	base := uint64(free)
	if base > ^uint64(0)-reclaim {
		return 0, false
	}
	return base + reclaim, true
}

// ResolveSize returns expected when it is positive. Otherwise it uses
// Head (HTTP HEAD by default). A missing Content-Length is an error so
// the caller can fail closed without downloading.
func ResolveSize(ctx context.Context, rawURL string, expected int64, head func(context.Context, string) (int64, error)) (int64, error) {
	if expected > 0 {
		return expected, nil
	}
	if head == nil {
		head = HTTPHeadLength
	}
	n, err := head(ctx, rawURL)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("hypermesh: download size unknown")
	}
	return n, nil
}

// HTTPHeadLength is Content-Length from HTTP HEAD. Redirects are followed.
// No length, or a non-2xx status, is an error. The body is not saved.
func HTTPHeadLength(ctx context.Context, rawURL string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, fmt.Errorf("hypermesh: head status %d", resp.StatusCode)
	}
	if resp.ContentLength <= 0 {
		return 0, fmt.Errorf("hypermesh: head has no content length")
	}
	return resp.ContentLength, nil
}

// FreeBytes is available bytes (statfs Bavail*Bsize) on the filesystem
// that will hold path. Missing parents are walked until an existing
// directory. The product is an int64; an overflow is an error so a
// wrapped value is never reported as free space. Heartbeat disk_free_bytes
// and the download preflight both call this.
func FreeBytes(path string) (int64, error) {
	dir, err := existingDir(path)
	if err != nil {
		return 0, err
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return freeFromBlocks(st.Bavail, int64(st.Bsize))
}

// CapacityBytes is the filesystem size (statfs Blocks*Bsize) that will
// hold path. The model-cache budget defaults to this minus the disk
// floor. Overflow is an error, same as FreeBytes.
func CapacityBytes(path string) (int64, error) {
	dir, err := existingDir(path)
	if err != nil {
		return 0, err
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return capacityFromBlocks(st.Blocks, int64(st.Bsize))
}

func capacityFromBlocks(blocks uint64, bsize int64) (int64, error) {
	if bsize <= 0 {
		return 0, errors.New("hypermesh: statfs block size")
	}
	if blocks > uint64(math.MaxInt64)/uint64(bsize) {
		return 0, errors.New("hypermesh: statfs capacity bytes overflow")
	}
	return int64(blocks) * bsize, nil
}

// freeFromBlocks is Bavail*Bsize with the same overflow guard the
// heartbeat reporter used before this helper was shared.
func freeFromBlocks(bavail uint64, bsize int64) (int64, error) {
	if bsize <= 0 {
		return 0, errors.New("hypermesh: statfs block size")
	}
	if bavail > uint64(math.MaxInt64)/uint64(bsize) {
		return 0, errors.New("hypermesh: statfs free bytes overflow")
	}
	return int64(bavail) * bsize, nil
}

func existingDir(path string) (string, error) {
	p := filepath.Clean(strings.TrimSpace(path))
	if p == "" {
		return "", fmt.Errorf("hypermesh: empty download path")
	}
	for {
		fi, err := os.Stat(p)
		if err == nil && fi.IsDir() {
			return p, nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", fmt.Errorf("hypermesh: no directory for %s", path)
		}
		p = parent
	}
}

// validateDest rejects an empty path and anything whose parent is the
// root directory. An empty HYPERMESH_GGUF_PATH used to CreateTemp in the
// process cwd, which on systemd is /, producing /lab-pull-*.gguf.
func validateDest(dest string) error {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return fmt.Errorf("hypermesh: no download path")
	}
	if !filepath.IsAbs(dest) {
		return fmt.Errorf("hypermesh: download path must be absolute")
	}
	dir := filepath.Dir(filepath.Clean(dest))
	if dir == "/" || dir == "." || dir == "" {
		return fmt.Errorf("hypermesh: refusing download onto the root filesystem")
	}
	return nil
}
