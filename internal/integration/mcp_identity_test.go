//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemon"
	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pelletier/go-toml/v2"
)

const (
	managedMCPCallerID      = "canny-caller"
	managedMCPCallerToken   = "tok-canny-caller"
	managedMCPAmbientID     = "dreich-unrelated"
	managedMCPAmbientToken  = "tok-dreich-unrelated" //nolint:gosec // Deliberately recognizable integration-test credential.
	managedMCPHelperEnvName = "GRAITH_INTEGRATION_CLI"
	managedMCPAliasPrefix   = "GRAITH_MCP_00112233445566778899AABBCCDDEEFF_"
)

var managedMCPAliasNames = []string{
	managedMCPAliasPrefix + "SESSION_ID",
	managedMCPAliasPrefix + "TOKEN",
	managedMCPAliasPrefix + "PROFILE",
	managedMCPAliasPrefix + "SOCKET_PATH",
}

type managedMCPTestEnv struct {
	*testEnv

	mcpManager *daemon.MCPManager
	messages   *daemon.MsgStore
	profile    string
}

func TestManagedGraithMCPPreservesCallerIdentity(t *testing.T) {
	env := setupManagedMCP(t)
	defer env.teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	proxyArgs := append([]string{"mcp-proxy", "graith"}, managedMCPAliasNames...)
	cmd := exec.Command(os.Args[0], proxyArgs...) //nolint:gosec // Re-executes this fixed integration test binary through the real CLI entrypoint.
	cmd.Env = replaceProcessEnv(os.Environ(), map[string]string{
		managedMCPHelperEnvName: "1",
		managedMCPAliasNames[0]: managedMCPCallerID,
		managedMCPAliasNames[1]: managedMCPCallerToken,
		managedMCPAliasNames[2]: env.profile,
		managedMCPAliasNames[3]: env.socket,
		// Hostile canonical values must be ignored; only the explicitly named
		// aliases are authentication and routing inputs.
		"GRAITH_TOKEN":       managedMCPAmbientToken,
		"GRAITH_PROFILE":     "wrong",
		"GRAITH_SOCKET_PATH": filepath.Join(t.TempDir(), "wrong.sock"),
		// Deliberately make normal XDG discovery point elsewhere. The proxy must
		// use the daemon-injected exact socket and must never auto-start a second
		// daemon from this credential-bearing helper.
		"XDG_RUNTIME_DIR": filepath.Join(t.TempDir(), "wrong-runtime"),
		// The outer request payload claims an unrelated session; the daemon
		// must overwrite it from the authenticated envelope token.
		"GRAITH_SESSION_ID": "spoofed-session",
	})

	var proxyStderr bytes.Buffer

	cmd.Stderr = &proxyStderr

	client := gomcp.NewClient(&gomcp.Implementation{Name: "identity-regression", Version: "1"}, nil)

	session, err := client.Connect(ctx, &gomcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		logs, logErr := env.mcpManager.LogFiles("graith", 100)
		t.Fatalf("connect through managed mcp-proxy: %v; proxy stderr: %q; managed logs: %+v; read logs: %v", err, proxyStderr.String(), logs, logErr)
	}

	defer func() { _ = session.Close() }()

	publish := callManagedMCPTool(t, ctx, session, "publish_message", map[string]any{
		"topic":  "blether",
		"body":   "identity stays canny",
		"sender": "spoofed-human-name",
	})

	var published struct {
		ID string `json:"id"`
	}
	decodeManagedMCPOutput(t, publish, &published)

	if published.ID == "" {
		t.Fatal("publish_message returned no message ID")
	}

	read := callManagedMCPTool(t, ctx, session, "read_messages", map[string]any{
		"topic": "blether",
		"all":   true,
	})

	var messages struct {
		Messages []struct {
			SenderID   string `json:"sender_id"`
			SenderName string `json:"sender_name"`
			Body       string `json:"body"`
		} `json:"messages"`
	}
	decodeManagedMCPOutput(t, read, &messages)

	if len(messages.Messages) != 1 {
		t.Fatalf("read_messages returned %d messages, want 1", len(messages.Messages))
	}

	if got := messages.Messages[0].SenderID; got != managedMCPCallerID {
		t.Fatalf("message sender_id = %q, want authenticated caller %q", got, managedMCPCallerID)
	}

	if got := messages.Messages[0].SenderName; got != "canny" {
		t.Fatalf("message sender_name = %q, want daemon-forced session name", got)
	}

	createdResult := callManagedMCPTool(t, ctx, session, "create_session", map[string]any{
		"name":    "braw-mcp-child",
		"agent":   "echo",
		"no_repo": true,
	})

	var created struct {
		ID string `json:"id"`
	}
	decodeManagedMCPOutput(t, createdResult, &created)

	child, ok := env.sm.Get(created.ID)
	if !ok {
		t.Fatalf("MCP-created session %q not found", created.ID)
	}

	if child.ParentID != managedMCPCallerID {
		t.Fatalf("MCP-created parent = %q, want caller %q", child.ParentID, managedMCPCallerID)
	}

	todoResult := callManagedMCPTool(t, ctx, session, "todo_add", map[string]any{
		"title": "mend the caller's dyke",
	})

	var todo struct {
		Scope string `json:"scope"`
		Title string `json:"title"`
	}
	decodeManagedMCPOutput(t, todoResult, &todo)

	if todo.Scope != "session:"+managedMCPCallerID || todo.Title != "mend the caller's dyke" {
		t.Fatalf("todo_add result = %+v, want caller subtree scope", todo)
	}

	if _, err := env.messages.Publish(daemon.PublishOpts{
		Stream: "inbox:" + managedMCPCallerID,
		Body:   "caller inbox context",
	}); err != nil {
		t.Fatalf("seed caller inbox: %v", err)
	}

	inboxResult := callManagedMCPTool(t, ctx, session, "read_inbox", map[string]any{"all": true})

	var inbox struct {
		Messages []struct {
			Body string `json:"body"`
		} `json:"messages"`
	}
	decodeManagedMCPOutput(t, inboxResult, &inbox)

	if len(inbox.Messages) != 1 || inbox.Messages[0].Body != "caller inbox context" {
		t.Fatalf("read_inbox result = %+v, want caller-scoped message", inbox.Messages)
	}

	wrongProfileCmd := exec.Command(os.Args[0], proxyArgs...) //nolint:gosec // Re-executes this fixed integration test binary through the real CLI entrypoint.
	wrongProfileCmd.Env = replaceProcessEnv(os.Environ(), map[string]string{
		managedMCPHelperEnvName: "1",
		managedMCPAliasNames[0]: managedMCPCallerID,
		managedMCPAliasNames[1]: managedMCPCallerToken,
		managedMCPAliasNames[2]: "wrong",
		managedMCPAliasNames[3]: env.socket,
	})
	wrongProfileOutput, wrongProfileErr := wrongProfileCmd.CombinedOutput()
	if wrongProfileErr == nil {
		t.Fatal("wrong aliased profile unexpectedly connected")
	}
	if bytes.Contains(wrongProfileOutput, []byte(managedMCPCallerToken)) {
		t.Fatal("wrong-profile diagnostic exposed the token value")
	}
}

func setupManagedMCP(t *testing.T) *managedMCPTestEnv {
	t.Helper()

	root := t.TempDir()
	// Keep the socket independent of checkout depth and below Darwin's sun_path
	// limit. The integration suite already uses short /tmp roots for real Unix
	// listeners; enforced agent sandboxes that deny listeners cannot run it.
	runtimeRoot, err := os.MkdirTemp("/tmp", "graith-mcp-*")
	if err != nil {
		t.Fatalf("create short runtime root: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })

	t.Setenv(managedMCPHelperEnvName, "1")
	t.Setenv("GRAITH_PROFILE", fmt.Sprintf("mcpid-%d", os.Getpid()))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	// Simulate a daemon launched from an unrelated session. The manager must
	// remove this ambient token before injecting the authenticated proxy caller.
	t.Setenv("GRAITH_TOKEN", managedMCPAmbientToken)

	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatalf("resolve managed MCP paths: %v", err)
	}

	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("create managed MCP paths: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(paths.RuntimeDir) })

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Sandbox.Enabled = false
	cfg.Agents["echo"] = config.Agent{
		Command:    "sh",
		Args:       []string{"-c", "echo ready; exec cat"},
		ResumeArgs: []string{"-c", "echo resumed; exec cat"},
	}

	// EffectiveTOML is a display renderer that materializes optional tool
	// defaults. Preserve unset tools here so re-executing the CLI does not make
	// this hermetic test depend on developer-only binaries such as gcx.
	configData, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatalf("render managed MCP config: %v", err)
	}

	if err := os.WriteFile(paths.ConfigFile, configData, 0o600); err != nil {
		t.Fatalf("write managed MCP config: %v", err)
	}

	state := daemon.NewState()
	state.Sessions[managedMCPCallerID] = &daemon.SessionState{
		ID: managedMCPCallerID, Name: "canny", Agent: "claude",
		Status: daemon.StatusStopped, Token: managedMCPCallerToken, CreatedAt: time.Now().UTC(),
	}

	state.Sessions[managedMCPAmbientID] = &daemon.SessionState{
		ID: managedMCPAmbientID, Name: "dreich", Agent: "claude",
		Status: daemon.StatusStopped, Token: managedMCPAmbientToken, CreatedAt: time.Now().UTC(),
	}
	if err := daemon.SaveState(paths.StateFile, state); err != nil {
		t.Fatalf("seed managed MCP state: %v", err)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	sm := daemon.NewSessionManager(cfg, paths, log)

	if err := sm.LoadState(); err != nil {
		t.Fatalf("load managed MCP state: %v", err)
	}

	if err := sm.EnsureHumanToken(); err != nil {
		t.Fatalf("ensure managed MCP human token: %v", err)
	}

	msgStore, err := daemon.NewMsgStore(paths.MessagesDB)
	if err != nil {
		t.Fatalf("open managed MCP message store: %v", err)
	}

	t.Cleanup(func() { _ = msgStore.Close() })
	sm.SetMsgStore(msgStore)

	todoStore, err := daemon.NewTodoStore(paths.TodosDB)
	if err != nil {
		t.Fatalf("open managed MCP todo store: %v", err)
	}

	t.Cleanup(func() { _ = todoStore.Close() })
	sm.SetTodoStore(todoStore)

	mcpManager := daemon.NewManagedMCPManager(cfg, paths.LogDir, log)
	t.Cleanup(mcpManager.Shutdown)
	sm.SetMCPManager(mcpManager)

	listener, err := net.Listen("unix", paths.SocketPath)
	if err != nil {
		t.Fatalf("listen for managed MCP integration: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	srv := daemon.NewServer(listener, func(ctx context.Context, conn net.Conn) {
		daemon.HandleConnection(ctx, conn, daemon.ConnOrigin{}, sm, log)
	}, log)
	go func() { _ = srv.Serve(ctx) }()
	go sm.RunDetectionLoop(ctx)

	return &managedMCPTestEnv{
		testEnv:    &testEnv{sm: sm, srv: srv, cancel: cancel, socket: paths.SocketPath, tmpDir: root},
		mcpManager: mcpManager,
		messages:   msgStore,
		profile:    paths.Profile,
	}
}

func callManagedMCPTool(t *testing.T, ctx context.Context, session *gomcp.ClientSession, name string, arguments any) *gomcp.CallToolResult {
	t.Helper()

	result, err := session.CallTool(ctx, &gomcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call MCP tool %s: %v", name, err)
	}

	if err := result.GetError(); err != nil {
		t.Fatalf("MCP tool %s returned error: %v", name, err)
	}

	return result
}

func decodeManagedMCPOutput(t *testing.T, result *gomcp.CallToolResult, target any) {
	t.Helper()

	if result.StructuredContent == nil {
		t.Fatal("MCP result has no structured content")
	}

	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal MCP structured content: %v", err)
	}

	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode MCP structured content: %v", err)
	}
}

func replaceProcessEnv(base []string, replacements map[string]string) []string {
	env := make([]string, 0, len(base)+len(replacements))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replace := replacements[key]; replace {
				continue
			}
		}

		env = append(env, entry)
	}

	for key, value := range replacements {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}

	return env
}
