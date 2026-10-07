package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestCertificationEndpointUsesContainerBridgeWithoutPublishedPorts(t *testing.T) {
	n := &NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "inspect --format {{json .}} exact-container" {
			t.Fatalf("wrong lookup: %v", args)
		}
		return []byte(`{"State":{"Running":true},"NetworkSettings":{"Ports":{"8080/tcp":null},"Networks":{"hypermesh-workload":{"IPAddress":"172.18.0.2"}}}}`), nil
	}}
	endpoint, err := n.CertificationEndpoint(context.Background(), &Handle{ContainerID: "exact-container"})
	if err != nil || endpoint != "http://172.18.0.2:8080/v1/chat/completions" {
		t.Fatalf("%q %v", endpoint, err)
	}
}

func TestCertificationEndpointRejectsInvalidNetworkState(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`not-json`,
		`{"State":{"Running":false},"NetworkSettings":{"Networks":{"hypermesh-workload":{"IPAddress":"172.18.0.2"}}}}`,
		`{"State":{"Running":true},"NetworkSettings":{"Networks":{"bridge":{"IPAddress":"172.18.0.2"}}}}`,
		`{"State":{"Running":true},"NetworkSettings":{"Networks":{"hypermesh-workload":{"IPAddress":"172.18.0.2"},"bridge":{"IPAddress":"172.19.0.2"}}}}`,
		`{"State":{"Running":true},"NetworkSettings":{"Networks":{"hypermesh-workload":{"IPAddress":"127.0.0.1"}}}}`,
		`{"State":{"Running":true},"NetworkSettings":{"Networks":{"hypermesh-workload":{"IPAddress":"8.8.8.8"}}}}`,
		`{"State":{"Running":true},"NetworkSettings":{"Networks":{"hypermesh-workload":{"IPAddress":""}}}}`,
	} {
		n := &NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) { return []byte(body), nil }}
		if endpoint, err := n.CertificationEndpoint(context.Background(), &Handle{ContainerID: "exact"}); err == nil || endpoint != "" {
			t.Fatalf("accepted %s", body)
		}
	}
	n := &NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, fmt.Errorf("failed") }}
	if _, err := n.CertificationEndpoint(context.Background(), &Handle{ContainerID: "exact"}); err == nil {
		t.Fatal("accepted inspect failure")
	}
	if _, err := n.CertificationEndpoint(context.Background(), nil); err == nil {
		t.Fatal("accepted missing handle")
	}
}
