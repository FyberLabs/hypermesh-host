package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestInspectWorkloadStateReadsBridgeAddress(t *testing.T) {
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "inspect --format {{json .}} cid" {
			t.Fatalf("inspect args %v", args)
		}
		return []byte(`{"State":{"Running":true,"Status":"running","ExitCode":0},"NetworkSettings":{"Networks":{"hypermesh-workload":{"IPAddress":"172.18.0.2"}}}}`), nil
	}
	state, err := InspectWorkloadState(context.Background(), run, "cid")
	if err != nil || !state.Running || state.IP != "172.18.0.2" || state.Exited() {
		t.Fatalf("%+v %v", state, err)
	}
}

func TestInspectWorkloadStateExitAndRejectsBadAddress(t *testing.T) {
	exited, err := InspectWorkloadState(context.Background(), func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"State":{"Running":false,"Status":"exited","ExitCode":1},"NetworkSettings":{"Networks":{"hypermesh-workload":{"IPAddress":"172.18.0.2"}}}}`), nil
	}, "cid")
	if err != nil || !exited.Exited() || exited.ExitCode != 1 {
		t.Fatalf("%+v %v", exited, err)
	}
	gone, err := InspectWorkloadState(context.Background(), func(context.Context, ...string) ([]byte, error) {
		return []byte("Error: No such container: cid"), errors.New("exit status 1")
	}, "cid")
	if err != nil || !gone.Exited() {
		t.Fatalf("gone %+v %v", gone, err)
	}
	for _, body := range []string{
		`{"State":{"Running":true,"Status":"running"},"NetworkSettings":{"Networks":{"bridge":{"IPAddress":"172.18.0.2"}}}}`,
		`{"State":{"Running":true,"Status":"running"},"NetworkSettings":{"Networks":{"hypermesh-workload":{"IPAddress":"8.8.8.8"}}}}`,
		`{"State":{"Running":true,"Status":"running"},"NetworkSettings":{"Networks":{"hypermesh-workload":{"IPAddress":"127.0.0.1"}}}}`,
		`not-json`,
	} {
		if _, err := InspectWorkloadState(context.Background(), func(context.Context, ...string) ([]byte, error) {
			return []byte(body), nil
		}, "cid"); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
