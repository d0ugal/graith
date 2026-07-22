package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
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
		{name: "existing daemon handshake rejection is permanent", err: errors.New("handshake rejected while connecting to existing daemon: handshake_err"), want: true},
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

func testMCPProxyIdentityEnvNames() []string {
	const prefix = "GRAITH_MCP_00112233445566778899AABBCCDDEEFF_"

	return []string{prefix + "SESSION_ID", prefix + "TOKEN", prefix + "PROFILE", prefix + "SOCKET_PATH"}
}

// TestMCPProxyCmdArgs verifies the hidden proxy command requires a server name
// and all four explicitly named identity aliases.
func TestMCPProxyCmdArgs(t *testing.T) {
	valid := append([]string{"blether"}, testMCPProxyIdentityEnvNames()...)
	if err := mcpProxyCmd.Args(mcpProxyCmd, valid); err != nil {
		t.Errorf("five args should be accepted, got %v", err)
	}

	if err := mcpProxyCmd.Args(mcpProxyCmd, nil); err == nil {
		t.Errorf("zero args should be rejected")
	}

	if err := mcpProxyCmd.Args(mcpProxyCmd, valid[:4]); err == nil {
		t.Errorf("four args should be rejected")
	}
}

func TestMCPProxyIdentityFromEnv(t *testing.T) {
	names := testMCPProxyIdentityEnvNames()
	values := map[string]string{
		names[0]: "canny-session",
		names[1]: "dreich-secret-token",
		names[2]: "",
		names[3]: "/canny/graith.sock",
	}
	lookup := func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}

	identity, err := mcpProxyIdentityFromEnv(names, lookup)
	if err != nil {
		t.Fatalf("mcpProxyIdentityFromEnv() error = %v", err)
	}

	if identity.sessionID != "canny-session" || identity.token != "dreich-secret-token" || identity.profile != "" || identity.socketPath != "/canny/graith.sock" {
		t.Fatal("resolved MCP proxy identity does not match aliased values")
	}
}

func TestMCPProxyIdentityFromEnvFailsClosed(t *testing.T) {
	const secret = "thrawn-token-must-not-leak" //nolint:gosec // Deliberately recognizable test credential for leak assertions.

	names := testMCPProxyIdentityEnvNames()

	tests := []struct {
		name   string
		names  []string
		values map[string]string
	}{
		{
			name:  "canonical variables are not a fallback",
			names: names,
			values: map[string]string{
				"GRAITH_SESSION_ID": "wrong-session", "GRAITH_TOKEN": secret,
				"GRAITH_PROFILE": "wrong", "GRAITH_SOCKET_PATH": "/wrong.sock",
			},
		},
		{
			name:  "partial aliases",
			names: names,
			values: map[string]string{
				names[0]: "canny-session", names[1]: secret,
			},
		},
		{
			name: "mixed nonce",
			names: []string{
				names[0], names[1], names[2],
				"GRAITH_MCP_FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF_SOCKET_PATH",
			},
			values: map[string]string{
				names[0]: "canny-session", names[1]: secret, names[2]: "bothy",
				"GRAITH_MCP_FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF_SOCKET_PATH": "/canny/graith.sock",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(name string) (string, bool) {
				value, ok := tt.values[name]
				return value, ok
			}

			_, err := mcpProxyIdentityFromEnv(tt.names, lookup)
			if err == nil {
				t.Fatal("invalid identity aliases should fail")
			}

			if strings.Contains(err.Error(), secret) {
				t.Fatal("identity alias error exposed a token value")
			}
		})
	}
}

func TestRedactMCPProxyDiagnosticPreservesOnlyNonIdentityContext(t *testing.T) {
	identity := mcpProxyIdentity{
		sessionID:  "canny-session",
		token:      "dreich-secret-token",
		profile:    "bothy-profile",
		socketPath: "/private/canny/graith.sock",
	}
	message := "dial unix /private/canny/graith.sock for canny-session in bothy-profile using dreich-secret-token: connection refused"

	got := redactMCPProxyDiagnostic(message, identity)
	for _, value := range []string{identity.sessionID, identity.token, identity.profile, identity.socketPath} {
		if strings.Contains(got, value) {
			t.Fatalf("redacted diagnostic exposed identity value %q: %q", value, got)
		}
	}

	for _, want := range []string{"dial unix", "connection refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("redaction removed useful diagnostic context %q: %q", want, got)
		}
	}
}

func TestRedactMCPProxyDiagnosticPreservesWordsContainingShortProfile(t *testing.T) {
	identity := mcpProxyIdentity{
		sessionID:  "canny-session",
		token:      "dreich-secret-token",
		profile:    "e",
		socketPath: "/private/e/graith.sock",
	}
	message := "profile e at /private/e/graith.sock using dreich-secret-token: connection refused"

	got := redactMCPProxyDiagnostic(message, identity)

	want := "profile [redacted] at [redacted] using [redacted]: connection refused"
	if got != want {
		t.Fatalf("redacted diagnostic = %q, want %q", got, want)
	}
}

func TestMCPProxyPreRunUsesOnlyAliasedBootstrapContext(t *testing.T) {
	names := testMCPProxyIdentityEnvNames()

	socketPath := filepath.Join(t.TempDir(), "graith.sock")
	for name, value := range map[string]string{
		names[0]: "canny-session",
		names[1]: "dreich-secret-token",
		names[2]: "bothy",
		names[3]: socketPath,
	} {
		t.Setenv(name, value)
	}

	// These canonical values would make normal CLI bootstrap fail or select the
	// wrong daemon. The internal proxy must not read them before its aliases.
	t.Setenv("GRAITH_PROFILE", "INVALID!")
	t.Setenv("GRAITH_TOKEN", "canonical-secret")
	t.Setenv("GRAITH_SOCKET_PATH", "/wrong.sock")

	oldCfg, oldPaths, oldOut := cfg, paths, out
	oldCfgFile, oldJSONOutput, oldAgentMode := cfgFile, jsonOutput, agentMode
	oldContext := mcpProxyCmd.Context()

	t.Cleanup(func() {
		cfg, paths, out = oldCfg, oldPaths, oldOut
		cfgFile, jsonOutput, agentMode = oldCfgFile, oldJSONOutput, oldAgentMode

		mcpProxyCmd.SetContext(oldContext)
	})
	mcpProxyCmd.SetContext(context.Background())

	args := append([]string{"graith"}, names...)
	if err := rootCmd.PersistentPreRunE(mcpProxyCmd, args); err != nil {
		t.Fatalf("mcp proxy pre-run bootstrap error = %v", err)
	}

	if paths.Profile != "bothy" || paths.SocketPath != socketPath {
		t.Fatalf("proxy paths = %+v, want aliased profile/socket", paths)
	}
}

func TestMCPProxyConnectionPaths(t *testing.T) {
	base := config.Paths{
		Profile:    "canny",
		SocketPath: filepath.Join(t.TempDir(), "default.sock"),
		DataDir:    filepath.Join(t.TempDir(), "data"),
	}

	if _, err := mcpProxyConnectionPaths(base, "bothy", ""); err == nil {
		t.Fatal("empty socket alias should fail closed")
	}

	override := filepath.Join(t.TempDir(), "custom", "..", "graith.sock")

	got, err := mcpProxyConnectionPaths(base, "bothy", override)
	if err != nil {
		t.Fatalf("absolute socket override: %v", err)
	}

	if got.SocketPath != filepath.Clean(override) {
		t.Errorf("SocketPath = %q, want %q", got.SocketPath, filepath.Clean(override))
	}

	if got.Profile != "bothy" || got.DataDir != base.DataDir {
		t.Errorf("identity routing changed unexpected connection paths: got %+v, base %+v", got, base)
	}

	if _, err := mcpProxyConnectionPaths(base, "bothy", filepath.Join("relative", "graith.sock")); err == nil {
		t.Fatal("relative aliased socket path should fail closed")
	}
}
