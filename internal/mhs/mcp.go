// Package mhs is a scaffold for host / MHS MCP (role C): edge-box device
// tools for off-box agents. It is not the renter-laptop tool MCP path and
// does not talk to the Hypermesh control plane.
package mhs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

const ProtocolVersion = "2024-11-05"
const ServerName = "hypermesh-mhs"
const ServerVersion = "0.0.0-stub"

// Tool is one MCP tool descriptor.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// StubTools are placeholders until host device I/O is wired.
func StubTools() []Tool {
	return []Tool{
		{
			Name:        "list_devices",
			Description: "List MHS devices on this host (stub — not implemented)",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "device_status",
			Description: "Status for one MHS device id (stub — not implemented)",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"device_id": map[string]any{"type": "string"},
				},
				"required": []string{"device_id"},
			},
		},
	}
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ServeStdio runs a line-delimited JSON-RPC MCP stub on in/out.
func ServeStdio(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	// MCP tool schemas can be large; allow bigger lines than the default.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytesTrimSpace(line)) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Method == "" {
			continue
		}
		switch msg.Method {
		case "initialize":
			if err := writeResult(out, msg.ID, map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{
					"name":    ServerName,
					"version": ServerVersion,
				},
			}); err != nil {
				return err
			}
		case "notifications/initialized":
			// no reply
		case "tools/list":
			if err := writeResult(out, msg.ID, map[string]any{"tools": StubTools()}); err != nil {
				return err
			}
		case "tools/call":
			var params callParams
			_ = json.Unmarshal(msg.Params, &params)
			if err := writeResult(out, msg.ID, map[string]any{
				"content": []map[string]any{{
					"type": "text",
					"text": fmt.Sprintf("mhs stub: tool %q is not implemented", params.Name),
				}},
				"isError": true,
			}); err != nil {
				return err
			}
		default:
			if len(msg.ID) > 0 {
				if err := writeError(out, msg.ID, -32601, "method not found"); err != nil {
					return err
				}
			}
		}
	}
	return scanner.Err()
}

func writeResult(out io.Writer, id json.RawMessage, result any) error {
	body := rpcMessage{JSONRPC: "2.0", ID: id, Result: result}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", raw)
	return err
}

func writeError(out io.Writer, id json.RawMessage, code int, message string) error {
	body := rpcMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", raw)
	return err
}

func bytesTrimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\r' || b[j-1] == '\n') {
		j--
	}
	return b[i:j]
}
