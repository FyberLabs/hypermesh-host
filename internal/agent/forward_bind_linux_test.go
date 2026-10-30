//go:build linux

package agent

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

// listenerDevice reads SO_BINDTODEVICE back from a listener.
func listenerDevice(ln net.Listener) (string, error) {
	tcp, ok := ln.(*net.TCPListener)
	if !ok {
		return "", errors.New("not a TCP listener")
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return "", err
	}
	buf := make([]byte, 16)
	size := uint32(len(buf))
	var errno syscall.Errno
	if err := raw.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
	}); err != nil {
		return "", err
	}
	if errno != 0 {
		return "", errno
	}
	return strings.TrimRight(string(buf[:size]), "\x00"), nil
}

func TestListenOnDeviceBindsSocketToInterface(t *testing.T) {
	ln, err := listenOnDevice("127.0.0.1:0", "lo")
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("SO_BINDTODEVICE not permitted in this environment")
		}
		t.Fatal(err)
	}
	defer ln.Close()
	dev, err := listenerDevice(ln)
	if err != nil || dev != "lo" {
		t.Fatalf("listener device %q err %v, want lo", dev, err)
	}
}

func TestListenOnDeviceFailsClosedWithoutDevice(t *testing.T) {
	ln, err := listenOnDevice("127.0.0.1:0", "hmnone0")
	if err == nil {
		ln.Close()
		t.Fatal("listener started on a device that does not exist")
	}
	if _, err := startLeaseForwarder("127.0.0.1:0", "127.0.0.1:1", "hmnone0"); err == nil {
		t.Fatal("forwarder started on a device that does not exist")
	}
	if _, err := startLeaseForwarder("127.0.0.1:0", "127.0.0.1:1", ""); err == nil {
		t.Fatal("forwarder started with no device")
	}
}

func TestStartLeaseForwarderBindsDevice(t *testing.T) {
	fwd, err := startLeaseForwarder("127.0.0.1:0", "127.0.0.1:1", "lo")
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("SO_BINDTODEVICE not permitted in this environment")
		}
		t.Fatal(err)
	}
	defer fwd.Close()
	if dev, err := listenerDevice(fwd.ln); err != nil || dev != "lo" {
		t.Fatalf("forwarder device %q err %v", dev, err)
	}
}

func TestLeaseStartBindsIngressToWireGuardDevice(t *testing.T) {
	a, hash, listen, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	docker := &scriptedDocker{ip: "172.18.0.2"}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	if got := a.leaseStart(context.Background(), ingressJob("lease-dev", hash)); !got.Passed {
		if strings.Contains(got.Reason, "not permitted") {
			t.Skip("SO_BINDTODEVICE not permitted in this environment")
		}
		t.Fatal(got)
	}
	defer a.stopLeaseForwarder()
	a.fwdMu.Lock()
	fwd := a.fwd
	a.fwdMu.Unlock()
	if dev, err := listenerDevice(fwd.ln); err != nil || dev != "lo" {
		t.Fatalf("ingress device %q err %v", dev, err)
	}
	_ = listen
}

// The agent defaults to the hypermesh device. When it is missing, lease_start
// fails instead of listening on the address alone, and nothing is left behind.
func TestLeaseStartFailsClosedWhenWireGuardDeviceMissing(t *testing.T) {
	a, hash, listen, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	a.ingressIface = ""
	if a.ingressDevice() != "hypermesh" {
		t.Fatalf("default device %q", a.ingressDevice())
	}
	a.ingressIface = "hmnone0"
	docker := &scriptedDocker{ip: "172.18.0.2"}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	got := a.leaseStart(context.Background(), ingressJob("lease-nodev", hash))
	if got.Passed || !strings.Contains(got.Reason, "ingress unreachable") {
		t.Fatalf("result %+v", got)
	}
	assertLeaseClean(t, a, listen, docker)
}

func TestInitialLeaseIngressWaitsForAssignedAddress(t *testing.T) {
	a, hash, listen, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	a.cfg.LeaseReadyWait = 5 * time.Second
	a.setBindIP("192.0.2.123/32")
	docker := &scriptedDocker{ip: "172.18.0.2"}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	var logs syncBuf
	a.log = slog.New(slog.NewTextHandler(&logs, nil))
	done := make(chan bool, 1)
	go func() {
		result := a.leaseStart(context.Background(), ingressJob("lease-first-address", hash))
		done <- result.Passed
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(logs.String(), "waiting for tunnel address") {
		select {
		case passed := <-done:
			t.Fatalf("start ended before tunnel convergence: passed=%v", passed)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("initial unavailable address was not observed")
		}
		time.Sleep(time.Millisecond)
	}
	a.setBindIP("127.0.0.1/32")
	select {
	case passed := <-done:
		if !passed {
			t.Fatalf("start failed after convergence: %s", logs.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("start did not finish")
	}
	defer a.stopLeaseForwarder()
	if h := a.currentLease(); h == nil || !h.Serving() || h.PublishAddr != listen {
		t.Fatalf("lease handle %+v", h)
	}
}

func TestInitialUnavailableIngressAddressRemainsBounded(t *testing.T) {
	a, hash, listen, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	a.cfg.LeaseReadyWait = 80 * time.Millisecond
	a.setBindIP("192.0.2.123/32")
	docker := &scriptedDocker{ip: "172.18.0.2"}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	started := time.Now()
	got := a.leaseStart(context.Background(), ingressJob("lease-address-never-ready", hash))
	if got.Passed || !strings.Contains(got.Reason, "ingress unreachable") || time.Since(started) > time.Second {
		t.Fatalf("result %+v after %s", got, time.Since(started))
	}
	assertLeaseClean(t, a, listen, docker)
}
