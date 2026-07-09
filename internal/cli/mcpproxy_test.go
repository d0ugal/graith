package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

// TestIsPermanentErrorClassification verifies which daemon errors are treated as
// permanent (stop reconnecting) versus transient (retry with backoff).
func TestIsPermanentErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unknown server is permanent", err: errors.New("unknown MCP server \"blether\""), want: true},
		{name: "manager not initialized is permanent", err: errors.New("MCP manager not initialized"), want: true},
		{name: "not enabled for agent is permanent", err: errors.New("server \"loch\" is not enabled for agent claude"), want: true},
		{name: "connect failure is transient", err: errors.New("connect to daemon: dial unix: no such file"), want: false},
		{name: "generic error is transient", err: errors.New("dreich"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPermanentError(tt.err); got != tt.want {
				t.Errorf("isPermanentError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestWriteJSONRPCErrorNilID verifies a JSON-RPC 2.0 error envelope is emitted
// with a null id and a trailing newline.
func TestWriteJSONRPCErrorNilID(t *testing.T) {
	var buf bytes.Buffer

	writeJSONRPCError(&buf, nil, -32603, "temporarily unavailable")

	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		t.Errorf("output %q should end with a newline", buf.String())
	}

	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", resp.JSONRPC)
	}

	if resp.ID != nil {
		t.Errorf("id = %v, want null", resp.ID)
	}

	if resp.Error.Code != -32603 || resp.Error.Message != "temporarily unavailable" {
		t.Errorf("error = %+v, want code -32603 with message", resp.Error)
	}
}

// TestWriteJSONRPCErrorWithID verifies a caller-supplied request id is echoed
// back in the error envelope.
func TestWriteJSONRPCErrorWithID(t *testing.T) {
	var buf bytes.Buffer

	writeJSONRPCError(&buf, float64(42), -32000, "kirk")

	var resp struct {
		ID float64 `json:"id"`
	}

	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.ID != 42 {
		t.Errorf("id = %v, want 42", resp.ID)
	}
}

// TestMCPProxyCmdArgs verifies the hidden proxy command requires exactly one
// server-name argument.
func TestMCPProxyCmdArgs(t *testing.T) {
	if err := mcpProxyCmd.Args(mcpProxyCmd, []string{"blether"}); err != nil {
		t.Errorf("one arg should be accepted, got %v", err)
	}

	if err := mcpProxyCmd.Args(mcpProxyCmd, nil); err == nil {
		t.Errorf("zero args should be rejected")
	}

	if err := mcpProxyCmd.Args(mcpProxyCmd, []string{"blether", "loch"}); err == nil {
		t.Errorf("two args should be rejected")
	}
}
