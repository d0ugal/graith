package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestCheckCommandPolicyIsSynchronousAndFailClosed(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.CommandPolicy = config.CommandPolicy{
		Backend: "builtin",
		Builtin: config.CommandPolicyBuiltin{
			Allow: []any{"echo @*"},
			Deny:  []any{"rm @*"},
		},
	}
	sm := NewSessionManager(cfg, config.Paths{DataDir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sm.state.Sessions["canny"] = &SessionState{ID: "canny", Name: "canny", Agent: "claude"}

	tests := []struct {
		name, tool, input, want string
	}{
		{name: "allow", tool: "Bash", input: `{"command":"echo braw"}`, want: "allow"},
		{name: "deny", tool: "Bash", input: `{"command":"rm dreich"}`, want: "deny"},
		{name: "ask", tool: "Bash", input: `{"command":"curl example.invalid"}`, want: "deny"},
		{name: "malformed", tool: "Bash", input: `{`, want: "deny"},
		{name: "outside scope", tool: "Read", input: `{"file_path":"bothy"}`, want: "allow"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sm.CheckCommandPolicy(context.Background(), protocol.CommandPolicyCheckMsg{
				SessionID: "canny", ToolName: tt.tool, ToolInput: tt.input,
			})
			if got.Decision != tt.want {
				t.Fatalf("decision = %q (%s), want %q", got.Decision, got.Reason, tt.want)
			}

			if tt.want == "deny" && strings.TrimSpace(got.Reason) == "" {
				t.Fatal("deny must include a useful reason")
			}
		})
	}
}

func TestCheckCommandPolicyDisabledContinuesToSandbox(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.CommandPolicy = config.CommandPolicy{}
	sm := NewSessionManager(cfg, config.Paths{DataDir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	got := sm.CheckCommandPolicy(context.Background(), protocol.CommandPolicyCheckMsg{
		SessionID: "croft", ToolName: "Bash", ToolInput: `{"command":"anything"}`,
	})
	if got.Decision != "allow" {
		t.Fatalf("decision = %q, want allow", got.Decision)
	}
}

func TestCheckCommandPolicyTimeoutDeniesImmediately(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "localmost")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 2\n"), 0o755); err != nil { //nolint:gosec // executable test fixture
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.CommandPolicy = config.CommandPolicy{Backend: "localmost", Command: path, Timeout: "50ms"}
	sm := NewSessionManager(cfg, config.Paths{DataDir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sm.state.Sessions["thrawn"] = &SessionState{ID: "thrawn", Name: "thrawn", Agent: "codex"}

	start := time.Now()

	got := sm.CheckCommandPolicy(context.Background(), protocol.CommandPolicyCheckMsg{
		SessionID: "thrawn", ToolName: "exec_command", ToolInput: `{"cmd":"echo strath"}`,
	})
	if got.Decision != "deny" || !strings.Contains(got.Reason, "timed out") {
		t.Fatalf("decision = %+v, want immediate timeout deny", got)
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("policy timeout took %v", elapsed)
	}
}

func TestCreateAndResumeRejectUnavailableCommandPolicy(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.CommandPolicy = config.CommandPolicy{
		Backend: "localmost", Command: filepath.Join(t.TempDir(), "missing-localmost"),
	}
	dataDir := t.TempDir()
	sm := NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(dataDir, "state.json"), DataDir: dataDir, LogDir: dataDir,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sm.sandboxResolver = func(string) (bool, error) { return true, nil }

	_, err := sm.Create(CreateOpts{
		Name: "policy-create", AgentName: "claude", NoRepo: true, Rows: 24, Cols: 80,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot enforce") {
		t.Fatalf("Create error = %v, want unavailable-policy failure", err)
	}

	if len(sm.state.Sessions) != 0 {
		t.Fatalf("failed Create left reserved sessions: %+v", sm.state.Sessions)
	}

	sm.state.Sessions["policy-resume"] = &SessionState{
		ID: "policy-resume", Name: "policy-resume", Agent: "claude", Status: StatusStopped,
		WorktreePath: t.TempDir(),
	}

	_, err = sm.Resume("policy-resume", 24, 80)
	if err == nil || !strings.Contains(err.Error(), "cannot enforce") {
		t.Fatalf("Resume error = %v, want unavailable-policy failure", err)
	}
}

func TestConfiguredCommandPolicyRejectsAgentWithoutBlockingHook(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.CommandPolicy = config.CommandPolicy{
		Backend: "builtin", Builtin: config.CommandPolicyBuiltin{Allow: []any{"echo @*"}},
	}
	cfg.Agents["agy"] = config.Agent{Command: "agy", NonInteractiveArgs: []string{"--dangerously-skip-permissions"}}

	sm := NewSessionManager(cfg, config.Paths{DataDir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := sm.validateCommandPolicy("agy"); err == nil || !strings.Contains(err.Error(), "no synchronous shell hook") {
		t.Fatalf("validateCommandPolicy = %v, want unsupported-agent error", err)
	}

	if err := sm.validateCommandPolicy("cursor"); err == nil || !strings.Contains(err.Error(), "can drop synchronous deny output") {
		t.Fatalf("validateCommandPolicy(cursor) = %v, want fail-closed hook-runner error", err)
	}

	cfg.Agents["claude"] = config.Agent{Command: "not-claude", NonInteractiveArgs: []string{"--dangerously-skip-permissions"}}

	if err := sm.validateCommandPolicy("claude"); err == nil || !strings.Contains(err.Error(), "not the verified claude CLI") {
		t.Fatalf("validateCommandPolicy(custom claude) = %v, want verified-command error", err)
	}
}
