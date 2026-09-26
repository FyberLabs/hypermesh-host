package mhs

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestServeStdioListsStubTools(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_devices","arguments":{}}}`,
		"",
	}, "\n"))
	var out bytes.Buffer
	if err := ServeStdio(in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 3 {
		t.Fatalf("replies=%q", out.String())
	}
	var listed struct {
		Result struct {
			Tools []Tool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listed); err != nil {
		t.Fatalf("list reply: %v %s", err, lines[1])
	}
	if len(listed.Result.Tools) != 2 || listed.Result.Tools[0].Name != "list_devices" {
		t.Fatalf("tools=%+v", listed.Result.Tools)
	}
	if !strings.Contains(lines[2], "not implemented") {
		t.Fatalf("call reply=%s", lines[2])
	}
}
