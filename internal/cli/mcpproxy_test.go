package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/d0ugal/graith/internal/config"
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
		{name: "missing managed identity is permanent", err: errors.New("managed graith MCP requires an authenticated session identity"), want: true},
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

func TestMCPProxyConnectionPaths(t *testing.T) {
	base := config.Paths{
		Profile:    "canny",
		SocketPath: filepath.Join(t.TempDir(), "default.sock"),
		DataDir:    filepath.Join(t.TempDir(), "data"),
	}

	unchanged, err := mcpProxyConnectionPaths(base, "")
	if err != nil {
		t.Fatalf("empty socket override: %v", err)
	}

	if unchanged != base {
		t.Fatalf("empty socket override changed paths: got %+v, want %+v", unchanged, base)
	}

	override := filepath.Join(t.TempDir(), "custom", "..", "graith.sock")
	got, err := mcpProxyConnectionPaths(base, override)
	if err != nil {
		t.Fatalf("absolute socket override: %v", err)
	}

	if got.SocketPath != filepath.Clean(override) {
		t.Errorf("SocketPath = %q, want %q", got.SocketPath, filepath.Clean(override))
	}

	if got.Profile != base.Profile || got.DataDir != base.DataDir {
		t.Errorf("socket override changed unrelated connection paths: got %+v, base %+v", got, base)
	}

	if _, err := mcpProxyConnectionPaths(base, filepath.Join("relative", "graith.sock")); err == nil {
		t.Fatal("relative GRAITH_SOCKET_PATH should fail closed")
	}
}
