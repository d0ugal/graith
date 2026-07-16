package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/approvals/localmost"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
default_agent = "claude"
github_username = "braw-lad"
branch_prefix = "{username}/graith"
fetch_on_create = true

[keybindings]
prefix = "ctrl+b"
new_session = "c"
delete_session = "x"
detach = "d"
session_list = "w"
next_session = "n"
prev_session = "p"
resume_session = "R"
rename_session = ","
search = "/"
scroll_mode = "["

[agents.claude]
command = "claude"
args = ["--session-id", "{agent_session_id}"]
resume_args = ["--resume", "{agent_session_id}"]

[agents.codex]
command = "codex"
args = []
resume_args = ["resume", "--last"]
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude", cfg.DefaultAgent)
	}

	if cfg.GitHubUsername != "braw-lad" {
		t.Errorf("GitHubUsername = %q, want braw-lad", cfg.GitHubUsername)
	}

	if cfg.Keybindings.Prefix != "ctrl+b" {
		t.Errorf("Prefix = %q, want ctrl+b", cfg.Keybindings.Prefix)
	}

	claude, ok := cfg.Agents["claude"]
	if !ok {
		t.Fatal("missing claude agent")
	}

	if claude.Command != "claude" {
		t.Errorf("claude command = %q", claude.Command)
	}

	if len(claude.Args) != 2 || claude.Args[0] != "--session-id" {
		t.Errorf("claude args = %v", claude.Args)
	}
}

func TestLoadConfigDataDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `data_dir = "~/.graith"
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.DataDir != "~/.graith" {
		t.Errorf("DataDir = %q, want ~/.graith", cfg.DataDir)
	}
}

func TestDefaultConfigDataDirEmpty(t *testing.T) {
	cfg := Default()
	if cfg.DataDir != "" {
		t.Errorf("default DataDir = %q, want empty", cfg.DataDir)
	}
}

func TestDataDirValidation(t *testing.T) {
	tests := []struct {
		name    string
		dataDir string
		wantErr bool
	}{
		{"empty is valid", "", false},
		{"absolute path", "/tmp/graith", false},
		{"home relative", "~/graith", false},
		{"relative path rejected", "graith-data", true},
		{"dot relative rejected", "./graith-data", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.DataDir = tt.dataDir

			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfigMissing(t *testing.T) {
	_, err := Load("/nonexistent/config.toml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := Default()
	if cfg.DefaultAgent != "claude" {
		t.Errorf("default agent = %q, want claude", cfg.DefaultAgent)
	}

	if _, ok := cfg.Agents["claude"]; !ok {
		t.Error("default config missing claude agent")
	}
}

func TestIdleTimeoutDuration(t *testing.T) {
	tests := []struct {
		name  string
		agent Agent
		want  time.Duration
	}{
		{
			name:  "explicit duration",
			agent: Agent{IdleTimeout: "30m"},
			want:  30 * time.Minute,
		},
		{
			name:  "explicit zero disables",
			agent: Agent{IdleTimeout: "0", ResumeArgs: []string{"--resume"}},
			want:  0,
		},
		{
			name:  "default with resume args",
			agent: Agent{ResumeArgs: []string{"--resume"}},
			want:  time.Hour,
		},
		{
			name:  "default without resume args",
			agent: Agent{},
			want:  0,
		},
		{
			name:  "invalid duration",
			agent: Agent{IdleTimeout: "bogus"},
			want:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.agent.IdleTimeoutDuration()
			if got != tt.want {
				t.Errorf("IdleTimeoutDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApprovalTimeoutDuration(t *testing.T) {
	tests := []struct {
		name string
		a    Approvals
		want time.Duration
	}{
		{
			name: "default when empty",
			a:    Approvals{},
			want: 10 * time.Minute,
		},
		{
			name: "explicit duration",
			a:    Approvals{Timeout: "30m"},
			want: 30 * time.Minute,
		},
		{
			name: "days",
			a:    Approvals{Timeout: "1d"},
			want: 24 * time.Hour,
		},
		{
			name: "negative falls back to default",
			a:    Approvals{Timeout: "-7d"},
			want: 10 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.a.TimeoutDuration()
			if got != tt.want {
				t.Errorf("TimeoutDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApprovalsBackendExecTimeoutDuration(t *testing.T) {
	tests := []struct {
		name         string
		a            Approvals
		wantCommand  time.Duration
		wantLocalmst time.Duration
	}{
		{
			name:         "defaults when unset",
			a:            Approvals{},
			wantCommand:  defaultBackendExecTimeout,
			wantLocalmst: defaultBackendExecTimeout,
		},
		{
			name:         "explicit values",
			a:            Approvals{CommandTimeout: "8s", LocalmostTimeout: "12s"},
			wantCommand:  8 * time.Second,
			wantLocalmst: 12 * time.Second,
		},
		{
			name:         "invalid falls back to default",
			a:            Approvals{CommandTimeout: "dreich", LocalmostTimeout: "-1s"},
			wantCommand:  defaultBackendExecTimeout,
			wantLocalmst: defaultBackendExecTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.CommandTimeoutDuration(); got != tt.wantCommand {
				t.Errorf("CommandTimeoutDuration() = %v, want %v", got, tt.wantCommand)
			}

			if got := tt.a.LocalmostTimeoutDuration(); got != tt.wantLocalmst {
				t.Errorf("LocalmostTimeoutDuration() = %v, want %v", got, tt.wantLocalmst)
			}
		})
	}
}

func TestApprovalsBackendExecTimeout(t *testing.T) {
	if d, ok := (Approvals{CommandTimeout: "7s"}).BackendExecTimeout("command"); !ok || d != 7*time.Second {
		t.Errorf("BackendExecTimeout(command) = %v, %v; want 7s, true", d, ok)
	}

	if d, ok := (Approvals{CommandTimeout: "7s"}).BackendExecTimeout("external"); !ok || d != 7*time.Second {
		t.Errorf("BackendExecTimeout(external) = %v, %v; want 7s, true", d, ok)
	}

	if d, ok := (Approvals{LocalmostTimeout: "9s"}).BackendExecTimeout("localmost"); !ok || d != 9*time.Second {
		t.Errorf("BackendExecTimeout(localmost) = %v, %v; want 9s, true", d, ok)
	}

	for _, backend := range []string{"prompt", "builtin", "auto", ""} {
		if _, ok := (Approvals{}).BackendExecTimeout(backend); ok {
			t.Errorf("BackendExecTimeout(%q) reported a subprocess timeout; want none", backend)
		}
	}
}

func TestApprovalsResolveBackend(t *testing.T) {
	tests := []struct {
		name        string
		a           Approvals
		wantBackend string
		wantDeprec  bool
		wantErr     bool
	}{
		{"empty -> prompt", Approvals{}, "prompt", false, false},
		{"explicit prompt", Approvals{Backend: "prompt"}, "prompt", false, false},
		{"explicit command", Approvals{Backend: "command", Command: "x"}, "command", false, false},
		{"explicit builtin", Approvals{Backend: "builtin"}, "builtin", false, false},
		{"explicit native localmost", Approvals{Backend: "localmost"}, "localmost", false, false},
		{"explicit auto", Approvals{Backend: "auto"}, "auto", false, false},
		{"unknown backend errors", Approvals{Backend: "thrawn"}, "", false, true},

		// auto is a first-class value in the deprecated Mode field too: it maps
		// straight to the "auto" backend (not "command") with a deprecation nudge.
		{"legacy mode=auto -> auto (warn)", Approvals{Mode: "auto"}, "auto", true, false},
		{"both agree (auto)", Approvals{Backend: "auto", Mode: "auto"}, "auto", true, false},
		{"conflict: auto + mode=localmost", Approvals{Backend: "auto", Mode: "localmost"}, "", false, true},

		// Back-compat: legacy mode maps to the command backend with a warning.
		{"legacy mode=localmost -> command (warn)", Approvals{Mode: "localmost"}, "command", true, false},
		{"legacy mode=command -> command (warn)", Approvals{Mode: "command"}, "command", true, false},
		{"legacy mode=external -> command (warn)", Approvals{Mode: "external"}, "command", true, false},
		{"unknown mode ignored -> prompt", Approvals{Mode: "haar"}, "prompt", false, false},

		// mode-only stays a warning even for localmost; never an error.
		{"mode=localmost alone is not an error", Approvals{Mode: "localmost", Command: "x"}, "command", true, false},

		// both set: agree is fine (with a redundant-mode nudge), disagree errors.
		{"both agree (command)", Approvals{Backend: "command", Mode: "command"}, "command", true, false},
		{"external+command agree", Approvals{Backend: "external", Mode: "localmost"}, "external", true, false},
		{"conflict: builtin + mode=localmost", Approvals{Backend: "builtin", Mode: "localmost"}, "", false, true},
		{"conflict: native localmost + mode=localmost", Approvals{Backend: "localmost", Mode: "localmost"}, "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, deprec, err := tt.a.ResolveBackend()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantErr {
				return
			}

			if backend != tt.wantBackend {
				t.Errorf("backend = %q, want %q", backend, tt.wantBackend)
			}

			if (deprec != "") != tt.wantDeprec {
				t.Errorf("deprecation = %q, wantDeprec %v", deprec, tt.wantDeprec)
			}
		})
	}
}

func TestApprovalsResolveBackendValidatedByConfig(t *testing.T) {
	c := Default()
	c.Approvals = Approvals{Backend: "builtin", Mode: "localmost"}

	if err := c.Validate(); err == nil {
		t.Error("Validate() should reject a conflicting approvals backend+mode")
	}
}

func boolPtr(b bool) *bool { return &b }

func TestApprovalsValidate(t *testing.T) {
	tests := []struct {
		name    string
		a       Approvals
		wantErr bool
	}{
		{"empty is fine", Approvals{}, false},
		{"command backend with command", Approvals{Backend: "command", Command: "graith-approver"}, false},
		{"external backend with command", Approvals{Backend: "external", Command: "graith-approver"}, false},
		{"localmost backend with binary override", Approvals{Backend: "localmost", Command: "/opt/localmost"}, false},
		{"legacy mode=localmost with command", Approvals{Mode: "localmost", Command: "graith-approver"}, false},

		// The #740 case: command set for a backend that ignores it.
		{"command ignored by prompt", Approvals{Command: "graith-approver"}, true},
		{"command ignored by explicit prompt", Approvals{Backend: "prompt", Command: "graith-approver"}, true},
		{"command ignored by builtin", Approvals{Backend: "builtin", Command: "graith-approver"}, true},
		{"auto backend is fine", Approvals{Backend: "auto"}, false},
		{"command ignored by auto", Approvals{Backend: "auto", Command: "graith-approver"}, true},

		// Conflicts from ResolveBackend still surface.
		{"unknown backend", Approvals{Backend: "thrawn"}, true},
		{"conflicting backend+mode", Approvals{Backend: "builtin", Mode: "localmost"}, true},

		// Inline builtin ruleset (#737).
		{"inline allow rules ok", Approvals{Backend: "builtin", Builtin: ApprovalsBuiltin{Allow: []any{"echo @*"}}}, false},
		{"inline flag only ok", Approvals{Backend: "builtin", Builtin: ApprovalsBuiltin{AskNoninteractive: boolPtr(false)}}, false},
		{"inline invalid rule rejected", Approvals{Backend: "builtin", Builtin: ApprovalsBuiltin{Allow: []any{"foo @("}}}, true},
		{"inline + external file conflict", Approvals{Backend: "builtin", Builtin: ApprovalsBuiltin{Config: "/tmp/approvals.json", Allow: []any{"echo @*"}}}, true},

		// Per-rule table validation (#737 hardening).
		{"inline rule table ok", Approvals{Backend: "builtin", Builtin: ApprovalsBuiltin{Allow: []any{map[string]any{"rule": "find @*", "unless": []any{"-delete"}}}}}, false},
		{"inline rule table unknown key rejected", Approvals{Backend: "builtin", Builtin: ApprovalsBuiltin{Allow: []any{map[string]any{"rule": "find @*", "unles": []any{"-delete"}}}}}, true},
		// The localmost #781 guard rejects an unless term matching the empty
		// command; inline rules compile through localmost.Parse so they get it too.
		{"inline empty unless rejected", Approvals{Backend: "builtin", Builtin: ApprovalsBuiltin{Deny: []any{map[string]any{"rule": "rm @arg*", "unless": []any{""}}}}}, true},
		{"inline rule table missing rule rejected", Approvals{Backend: "builtin", Builtin: ApprovalsBuiltin{Allow: []any{map[string]any{"unless": []any{"-delete"}}}}}, true},
		{"inline rule table empty rule rejected", Approvals{Backend: "builtin", Builtin: ApprovalsBuiltin{Deny: []any{map[string]any{"rule": "  "}}}}, true},
		{"inline non-string non-table rejected", Approvals{Backend: "builtin", Builtin: ApprovalsBuiltin{Allow: []any{42}}}, true},
		// Empty inline array is not "inline" and does not conflict with a file.
		{"empty inline array not a conflict", Approvals{Backend: "builtin", Builtin: ApprovalsBuiltin{Config: "/tmp/approvals.json", Allow: []any{}}}, false},

		// Per-backend execution timeouts (#1251).
		{"command_timeout unset ok", Approvals{Backend: "command", Command: "x"}, false},
		{"command_timeout valid ok", Approvals{Backend: "command", Command: "x", CommandTimeout: "10s"}, false},
		{"localmost_timeout valid ok", Approvals{Backend: "localmost", LocalmostTimeout: "10s"}, false},
		{"command_timeout invalid duration", Approvals{Backend: "command", Command: "x", CommandTimeout: "soon"}, true},
		{"command_timeout non-positive", Approvals{Backend: "command", Command: "x", CommandTimeout: "0s"}, true},
		{"command_timeout negative", Approvals{Backend: "command", Command: "x", CommandTimeout: "-1s"}, true},
		{"command_timeout over max", Approvals{Backend: "command", Command: "x", CommandTimeout: "2m"}, true},
		{"localmost_timeout over max", Approvals{Backend: "localmost", LocalmostTimeout: "90s"}, true},
		// A backend timeout at or above the enclosing approval timeout is incoherent.
		{"command_timeout >= enclosing", Approvals{Backend: "command", Command: "x", Timeout: "5s", CommandTimeout: "5s"}, true},
		{"command_timeout above enclosing", Approvals{Backend: "command", Command: "x", Timeout: "3s", CommandTimeout: "10s"}, true},
		// Timeout fields are validated for their syntax even when the resolved
		// backend does not use them.
		{"localmost_timeout invalid under command backend", Approvals{Backend: "command", Command: "x", LocalmostTimeout: "soon"}, true},
		// A deliberately tiny enclosing timeout is caught against the default 5s
		// backend timeout even with no explicit per-backend field.
		{"tiny enclosing vs default backend timeout", Approvals{Backend: "command", Command: "x", Timeout: "2s"}, true},
		// A non-subprocess backend has no execution timeout, so a tiny enclosing
		// timeout is fine on its own.
		{"tiny enclosing ok for prompt backend", Approvals{Timeout: "1s"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.a.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestApprovalsCommandIgnoredRejectedByConfig(t *testing.T) {
	c := Default()
	c.Approvals = Approvals{Backend: "builtin", Command: "graith-approver"}

	if err := c.Validate(); err == nil {
		t.Error("Validate() should reject a command set for a backend that ignores it")
	}
}

func TestParseDurationWithDays(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"30d", 30 * 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"7d12h", 7*24*time.Hour + 12*time.Hour, false},
		{"1d30m", 24*time.Hour + 30*time.Minute, false},
		{"2d1h30m", 2*24*time.Hour + 1*time.Hour + 30*time.Minute, false},
		{"0d5h", 5 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"30m", 30 * time.Minute, false},
		{"", 0, false},
		{"bogus", 0, true},
		{"d5h", 0, true},
		{"-7d", 0, true},
		{"-1d", 0, true},
		{"-30m", 0, true},
		{"-7d12h", 0, true},
		{"1d-30h", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseDurationWithDays(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseDurationWithDays(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}

			if got != tt.want {
				t.Errorf("ParseDurationWithDays(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestMessagesMaxAgeDuration(t *testing.T) {
	m := Messages{MaxAge: "30d", MaxPerStream: 1000}
	got := m.MaxAgeDuration()

	want := 30 * 24 * time.Hour
	if got != want {
		t.Errorf("MaxAgeDuration() = %v, want %v", got, want)
	}

	empty := Messages{}
	if empty.MaxAgeDuration() != 0 {
		t.Errorf("empty MaxAgeDuration() = %v, want 0", empty.MaxAgeDuration())
	}
}

func TestLoadConfigMessages(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[messages]
max_age = "7d"
max_per_stream = 500
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Messages.MaxAge != "7d" {
		t.Errorf("MaxAge = %q, want 7d", cfg.Messages.MaxAge)
	}

	if cfg.Messages.MaxPerStream != 500 {
		t.Errorf("MaxPerStream = %d, want 500", cfg.Messages.MaxPerStream)
	}

	if got := cfg.Messages.MaxAgeDuration(); got != 7*24*time.Hour {
		t.Errorf("MaxAgeDuration() = %v, want 168h", got)
	}
}

func TestLoadConfigIdleTimeout(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[agents.claude]
command = "claude"
idle_timeout = "2h"

[agents.codex]
command = "codex"
idle_timeout = "0"
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Agents["claude"].IdleTimeoutDuration(); got != 2*time.Hour {
		t.Errorf("claude idle timeout = %v, want 2h", got)
	}

	if got := cfg.Agents["codex"].IdleTimeoutDuration(); got != 0 {
		t.Errorf("codex idle timeout = %v, want 0", got)
	}
}

// TestLoadConfigApprovalTimeouts loads a real TOML through Load() so the per-
// backend timeout keys are exercised end-to-end, and confirms the embedded
// default config leaves them unset so the 5s Go default still applies (guards
// the #1228-class trap where an embedded value would defeat the Go fallback).
func TestLoadConfigApprovalTimeouts(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	toml := `
[approvals]
backend = "command"
command = "graith-approver"
command_timeout = "8s"
localmost_timeout = "12s"
`

	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := cfg.Approvals.CommandTimeoutDuration(); got != 8*time.Second {
		t.Errorf("command timeout = %v, want 8s", got)
	}

	if got := cfg.Approvals.LocalmostTimeoutDuration(); got != 12*time.Second {
		t.Errorf("localmost timeout = %v, want 12s", got)
	}

	// The embedded default config must NOT set these keys, so an out-of-the-box
	// config still falls back to the 5s Go default.
	if got := Default().Approvals.CommandTimeoutDuration(); got != defaultBackendExecTimeout {
		t.Errorf("default command timeout = %v, want %v", got, defaultBackendExecTimeout)
	}
}

func TestLoadConfigSandbox(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[sandbox]
enabled = true
features = ["ssh", "process-control"]
read_dirs = ["~/Code"]

[agents.claude]
command = "claude"

[agents.claude.sandbox]
features = ["clipboard"]
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if !cfg.Sandbox.Enabled {
		t.Error("Sandbox.Enabled = false, want true")
	}

	if len(cfg.Sandbox.Features) != 2 || cfg.Sandbox.Features[0] != "ssh" {
		t.Errorf("Sandbox.Features = %v, want [ssh process-control]", cfg.Sandbox.Features)
	}

	if len(cfg.Sandbox.ReadDirs) != 1 || cfg.Sandbox.ReadDirs[0] != "~/Code" {
		t.Errorf("Sandbox.ReadDirs = %v, want [~/Code]", cfg.Sandbox.ReadDirs)
	}

	claude := cfg.Agents["claude"]
	if len(claude.Sandbox.Features) != 1 || claude.Sandbox.Features[0] != "clipboard" {
		t.Errorf("claude.Sandbox.Features = %v, want [clipboard]", claude.Sandbox.Features)
	}
}

func TestSandboxConfigMerge(t *testing.T) {
	global := SandboxConfig{
		Enabled:   true,
		Features:  []string{"ssh", "process-control"},
		ReadDirs:  []string{"~/Code"},
		ReadFiles: []string{"~/.gitconfig"},
	}
	agent := SandboxConfig{
		Features:   []string{"clipboard"},
		WriteDirs:  []string{"~/.claude"},
		WriteFiles: []string{"~/.claude.json"},
	}

	merged := global.Merge(agent)

	if !merged.Enabled {
		t.Error("merged.Enabled = false, want true")
	}

	wantFeatures := []string{"ssh", "process-control", "clipboard"}
	if len(merged.Features) != 3 {
		t.Fatalf("merged.Features = %v, want %v", merged.Features, wantFeatures)
	}

	for i, f := range wantFeatures {
		if merged.Features[i] != f {
			t.Errorf("merged.Features[%d] = %q, want %q", i, merged.Features[i], f)
		}
	}

	if len(merged.ReadDirs) != 1 || merged.ReadDirs[0] != "~/Code" {
		t.Errorf("merged.ReadDirs = %v, want [~/Code]", merged.ReadDirs)
	}

	if len(merged.WriteDirs) != 1 || merged.WriteDirs[0] != "~/.claude" {
		t.Errorf("merged.WriteDirs = %v, want [~/.claude]", merged.WriteDirs)
	}

	if len(merged.ReadFiles) != 1 || merged.ReadFiles[0] != "~/.gitconfig" {
		t.Errorf("merged.ReadFiles = %v, want [~/.gitconfig]", merged.ReadFiles)
	}

	if len(merged.WriteFiles) != 1 || merged.WriteFiles[0] != "~/.claude.json" {
		t.Errorf("merged.WriteFiles = %v, want [~/.claude.json]", merged.WriteFiles)
	}
}

func TestSandboxConfigMergeNetworkAndSignalMode(t *testing.T) {
	// Global network + signal_mode inherit when the agent sets neither.
	global := SandboxConfig{
		Enabled:    true,
		SignalMode: "isolated",
		Network:    &SandboxNetworkConfig{Block: true},
	}
	merged := global.Merge(SandboxConfig{})

	if merged.SignalMode != "isolated" {
		t.Errorf("signal_mode should inherit from global, got %q", merged.SignalMode)
	}

	if !merged.Network.IsSet() || !merged.Network.Block {
		t.Errorf("network should inherit from global, got %+v", merged.Network)
	}

	// Agent overrides both wholesale.
	agent := SandboxConfig{
		SignalMode: "allow_all",
		Network:    &SandboxNetworkConfig{AllowDomains: []string{"kirk.example"}},
	}
	merged = global.Merge(agent)

	if merged.SignalMode != "allow_all" {
		t.Errorf("agent signal_mode should win, got %q", merged.SignalMode)
	}

	if merged.Network.Block {
		t.Error("agent network policy should replace global wholesale (block should be false)")
	}

	if len(merged.Network.AllowDomains) != 1 || merged.Network.AllowDomains[0] != "kirk.example" {
		t.Errorf("agent allow_domains should win, got %v", merged.Network.AllowDomains)
	}
}

func TestSandboxConfigMergeProfile(t *testing.T) {
	// Global profile inherits when the agent sets none.
	global := SandboxConfig{Enabled: true, Backend: "nono", Profile: "always-further/base"}
	merged := global.Merge(SandboxConfig{})

	if merged.Profile != "always-further/base" {
		t.Errorf("profile should inherit from global, got %q", merged.Profile)
	}

	// Agent profile overrides the global one wholesale (like backend/command).
	agent := SandboxConfig{Profile: "always-further/claude"}
	merged = global.Merge(agent)

	if merged.Profile != "always-further/claude" {
		t.Errorf("agent profile should win, got %q", merged.Profile)
	}

	// A whitespace-only agent profile is treated as unset: it must NOT clobber
	// the global profile (a config typo shouldn't silently discard inheritance).
	merged = global.Merge(SandboxConfig{Profile: "   "})
	if merged.Profile != "always-further/base" {
		t.Errorf("whitespace agent profile should inherit global, got %q", merged.Profile)
	}

	// Surrounding whitespace on a real agent value is trimmed.
	merged = global.Merge(SandboxConfig{Profile: "  always-further/claude  "})
	if merged.Profile != "always-further/claude" {
		t.Errorf("agent profile should be trimmed, got %q", merged.Profile)
	}
}

func TestSandboxSignalModeValidation(t *testing.T) {
	valid := &Config{Sandbox: SandboxConfig{SignalMode: "isolated"}}
	if err := valid.Validate(); err != nil {
		t.Errorf("isolated is a valid signal_mode: %v", err)
	}

	empty := &Config{Sandbox: SandboxConfig{}}
	if err := empty.Validate(); err != nil {
		t.Errorf("empty signal_mode should be valid: %v", err)
	}

	bad := &Config{Sandbox: SandboxConfig{SignalMode: "thrawn"}}
	if err := bad.Validate(); err == nil {
		t.Error("an unknown signal_mode should be rejected by Validate")
	}
}

func TestNetworkConfigIsSet(t *testing.T) {
	if (*SandboxNetworkConfig)(nil).IsSet() {
		t.Error("nil network config should not be set")
	}

	if (&SandboxNetworkConfig{}).IsSet() {
		t.Error("empty network config should not be set")
	}

	if !(&SandboxNetworkConfig{Block: true}).IsSet() {
		t.Error("block=true should be set")
	}

	if !(&SandboxNetworkConfig{AllowDomains: []string{"kirk.example"}}).IsSet() {
		t.Error("allow_domains should be set")
	}
}

func TestSandboxConfigMergeAgentDisabled(t *testing.T) {
	global := SandboxConfig{Enabled: true}
	disabled := true
	agent := SandboxConfig{Disabled: &disabled}

	merged := global.Merge(agent)

	if merged.Enabled {
		t.Error("merged.Enabled = true, want false (agent disabled)")
	}
}

func TestSandboxConfigMergeAgentEnabled(t *testing.T) {
	global := SandboxConfig{Enabled: false}
	agent := SandboxConfig{Enabled: true, Features: []string{"ssh"}}

	merged := global.Merge(agent)

	if !merged.Enabled {
		t.Error("merged.Enabled = false, want true (agent enabled)")
	}

	if len(merged.Features) != 1 || merged.Features[0] != "ssh" {
		t.Errorf("merged.Features = %v, want [ssh]", merged.Features)
	}
}

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		input string
		want  string
	}{
		{"~/Code", filepath.Join(home, "Code")},
		{"/absolute/path", "/absolute/path"},
		{"~/", home},
	}
	for _, tt := range tests {
		got := ExpandPath(tt.input)
		if got != tt.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExpandPathRelative(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		name    string
		input   string
		baseDir string
		want    string
	}{
		{"empty stays empty", "", "/etc/graith", ""},
		{"whitespace-only stays empty", "   ", "/etc/graith", ""},
		{"tilde expanded ignoring baseDir", "~/glen.json", "/etc/graith", filepath.Join(home, "glen.json")},
		{"leading whitespace trimmed then expanded", "  ~/glen.json  ", "/etc/graith", filepath.Join(home, "glen.json")},
		{"relative joined against baseDir", "approvals.json", "/etc/graith", "/etc/graith/approvals.json"},
		{"nested relative joined and cleaned", "rules/../approvals.json", "/etc/graith", "/etc/graith/approvals.json"},
		{"absolute path untouched", "/opt/graith/approvals.json", "/etc/graith", "/opt/graith/approvals.json"},
		{"relative with empty baseDir left relative", "approvals.json", "", "approvals.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExpandPathRelative(tt.input, tt.baseDir); got != tt.want {
				t.Errorf("ExpandPathRelative(%q, %q) = %q, want %q", tt.input, tt.baseDir, got, tt.want)
			}
		})
	}
}

func TestRepoPathAllowed(t *testing.T) {
	home, _ := os.UserHomeDir()

	t.Run("empty allows all", func(t *testing.T) {
		cfg := &Config{}
		if !cfg.RepoPathAllowed("/any/path") {
			t.Error("empty AllowedRepoPaths should allow all")
		}
	})

	t.Run("exact match", func(t *testing.T) {
		cfg := &Config{AllowedRepoPaths: []string{"~/Code"}}
		if !cfg.RepoPathAllowed(filepath.Join(home, "Code")) {
			t.Error("exact match should be allowed")
		}
	})

	t.Run("subdir allowed", func(t *testing.T) {
		cfg := &Config{AllowedRepoPaths: []string{"~/Code"}}
		if !cfg.RepoPathAllowed(filepath.Join(home, "Code/graith")) {
			t.Error("subdir should be allowed")
		}
	})

	t.Run("outside denied", func(t *testing.T) {
		cfg := &Config{AllowedRepoPaths: []string{"~/Code"}}
		if cfg.RepoPathAllowed("/tmp/thrawn-repo") {
			t.Error("path outside allowed dirs should be denied")
		}
	})

	t.Run("prefix trick denied", func(t *testing.T) {
		cfg := &Config{AllowedRepoPaths: []string{"~/Code"}}
		if cfg.RepoPathAllowed(filepath.Join(home, "Code-thrawn")) {
			t.Error("prefix without separator should be denied")
		}
	})

	t.Run("symlink to outside denied", func(t *testing.T) {
		allowed := t.TempDir()
		outside := t.TempDir()

		link := filepath.Join(allowed, "escape")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}

		cfg := &Config{AllowedRepoPaths: []string{allowed}}
		if cfg.RepoPathAllowed(link) {
			t.Error("symlink pointing outside allowed dirs should be denied")
		}
	})

	t.Run("symlink within allowed dir permitted", func(t *testing.T) {
		allowed := t.TempDir()

		target := filepath.Join(allowed, "real")
		if err := os.Mkdir(target, 0o750); err != nil {
			t.Fatal(err)
		}

		link := filepath.Join(allowed, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}

		cfg := &Config{AllowedRepoPaths: []string{allowed}}
		if !cfg.RepoPathAllowed(link) {
			t.Error("symlink pointing within allowed dir should be permitted")
		}
	})

	t.Run("intermediate symlink component to outside denied", func(t *testing.T) {
		allowed := t.TempDir()
		outside := t.TempDir()

		outsideRepo := filepath.Join(outside, "repo")
		if err := os.Mkdir(outsideRepo, 0o750); err != nil {
			t.Fatal(err)
		}

		link := filepath.Join(allowed, "escape")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}

		cfg := &Config{AllowedRepoPaths: []string{allowed}}
		if cfg.RepoPathAllowed(filepath.Join(link, "repo")) {
			t.Error("path through symlink intermediate pointing outside should be denied")
		}
	})

	t.Run("allowed path itself is a symlink", func(t *testing.T) {
		actual := t.TempDir()

		repo := filepath.Join(actual, "braw-croft")
		if err := os.Mkdir(repo, 0o750); err != nil {
			t.Fatal(err)
		}

		link := filepath.Join(t.TempDir(), "link-to-actual")
		if err := os.Symlink(actual, link); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}

		cfg := &Config{AllowedRepoPaths: []string{link}}
		if !cfg.RepoPathAllowed(filepath.Join(actual, "braw-croft")) {
			t.Error("repo under resolved allowed symlink should be permitted")
		}
	})
}

func TestLoadPartialAgentPreservesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[agents.claude]
command = "auld-claude"
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	claude := cfg.Agents["claude"]
	if claude.Command != "auld-claude" {
		t.Errorf("claude.Command = %q, want auld-claude", claude.Command)
	}

	if len(claude.Args) != 2 || claude.Args[0] != "--session-id" {
		t.Errorf("claude.Args = %v, want default args preserved", claude.Args)
	}

	if len(claude.ResumeArgs) != 2 || claude.ResumeArgs[0] != "--resume" {
		t.Errorf("claude.ResumeArgs = %v, want default resume_args preserved", claude.ResumeArgs)
	}

	if _, ok := cfg.Agents["codex"]; !ok {
		t.Error("codex agent lost — unmentioned defaults should be preserved")
	}

	if _, ok := cfg.Agents["opencode"]; !ok {
		t.Error("opencode agent lost")
	}

	if _, ok := cfg.Agents["agy"]; !ok {
		t.Error("agy agent lost")
	}
}

// TestValidateDefaultAgent covers default_agent membership validation against
// the final merged agent map (issue #1288): a typo'd or removed default fails
// load with a field-specific error, while a valid default (built-in or
// user-defined) loads, and sparse/default merging is preserved.
func TestValidateDefaultAgent(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()

		p := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		return p
	}

	t.Run("unknown default_agent rejected at load", func(t *testing.T) {
		_, err := Load(write(t, `default_agent = "ghaist"`+"\n"))
		if err == nil {
			t.Fatal("expected load to fail for unknown default_agent")
		}

		if !strings.Contains(err.Error(), "default_agent") || !strings.Contains(err.Error(), "ghaist") {
			t.Errorf("error %q must name default_agent and the unknown value", err)
		}
	})

	t.Run("built-in default_agent loads", func(t *testing.T) {
		if _, err := Load(write(t, `default_agent = "codex"`+"\n")); err != nil {
			t.Fatalf("codex is a built-in agent; load should succeed: %v", err)
		}
	})

	t.Run("user-defined default_agent loads", func(t *testing.T) {
		body := "default_agent = \"bespoke\"\n\n[agents.bespoke]\ncommand = \"my-agent\"\n"
		if _, err := Load(write(t, body)); err != nil {
			t.Fatalf("user-defined default agent should load: %v", err)
		}
	})

	t.Run("removing the user-defined default fails on reload", func(t *testing.T) {
		// First generation: default points at a user-defined agent and loads.
		withAgent := "default_agent = \"bespoke\"\n\n[agents.bespoke]\ncommand = \"my-agent\"\n"
		if _, err := Load(write(t, withAgent)); err != nil {
			t.Fatalf("initial load should succeed: %v", err)
		}

		// Reload after the [agents.bespoke] block is removed: mergeAgents no longer
		// carries it (built-ins union back, but a user agent does not), so the now
		// dangling default must fail loudly.
		withoutAgent := "default_agent = \"bespoke\"\n"

		_, err := Load(write(t, withoutAgent))
		if err == nil {
			t.Fatal("expected reload to fail once the referenced agent was removed")
		}

		if !strings.Contains(err.Error(), "default_agent") || !strings.Contains(err.Error(), "bespoke") {
			t.Errorf("error %q must name default_agent and the missing value", err)
		}
	})

	t.Run("empty default_agent is accepted", func(t *testing.T) {
		if _, err := Load(write(t, `default_agent = ""`+"\n")); err != nil {
			t.Fatalf("empty default_agent must remain valid: %v", err)
		}
	})
}

// TestValidateOrchestratorAgent covers membership validation against the final
// merged agent map. It mirrors default_agent validation while preserving the
// empty override's inheritance semantics.
func TestValidateOrchestratorAgent(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()

		p := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		return p
	}

	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"empty inherits default", "[orchestrator]\nagent = \"\"\n", ""},
		{"built-in agent", "[orchestrator]\nagent = \"codex\"\n", ""},
		{"custom agent", "[orchestrator]\nagent = \"bespoke\"\n\n[agents.bespoke]\ncommand = \"my-agent\"\n", ""},
		{"unknown agent", "[orchestrator]\nagent = \"ghaist\"\n", "ghaist"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(write(t, tt.body))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Load() = %v, want nil", err)
				}
				return
			}

			if err == nil || !strings.Contains(err.Error(), "orchestrator.agent") || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load() = %v, want actionable orchestrator.agent error containing %q", err, tt.wantErr)
			}
		})
	}

	t.Run("removing custom agent fails on reload", func(t *testing.T) {
		path := write(t, "[orchestrator]\nagent = \"bespoke\"\n\n[agents.bespoke]\ncommand = \"my-agent\"\n")
		if _, err := Load(path); err != nil {
			t.Fatalf("initial Load() = %v", err)
		}

		if err := os.WriteFile(path, []byte("[orchestrator]\nagent = \"bespoke\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "orchestrator.agent") || !strings.Contains(err.Error(), "bespoke") {
			t.Fatalf("reload Load() = %v, want missing bespoke orchestrator.agent error", err)
		}
	})
}

func TestLoadAgentExplicitEmptyArgs(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[agents.claude]
command = "claude"
args = []
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	claude := cfg.Agents["claude"]
	if len(claude.Args) != 0 {
		t.Errorf("claude.Args = %v, want [] (explicit empty should override default)", claude.Args)
	}

	if len(claude.ResumeArgs) != 2 {
		t.Errorf("claude.ResumeArgs = %v, want default preserved when not specified", claude.ResumeArgs)
	}
}

func TestLoadExplicitEmptyResumeAndForkArgs(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[agents.claude]
resume_args = []
fork_args = []
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	claude := cfg.Agents["claude"]
	if len(claude.ResumeArgs) != 0 {
		t.Errorf("claude.ResumeArgs = %v, want [] (explicit empty overrides default)", claude.ResumeArgs)
	}

	if len(claude.ForkArgs) != 0 {
		t.Errorf("claude.ForkArgs = %v, want [] (explicit empty overrides default)", claude.ForkArgs)
	}

	if len(claude.Args) != 2 {
		t.Errorf("claude.Args = %v, want default preserved when not specified", claude.Args)
	}

	if claude.Command != "claude" {
		t.Errorf("claude.Command = %q, want default preserved", claude.Command)
	}
}

func TestLoadExplicitEmptyEnv(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[agents.claude]
env = {}
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	claude := cfg.Agents["claude"]
	if claude.Env == nil {
		t.Error("claude.Env = nil, want empty map (explicit empty should override)")
	}

	if len(claude.Args) != 2 {
		t.Errorf("claude.Args = %v, want default preserved", claude.Args)
	}
}

func TestLoadCustomAgentPreserved(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[agents.canny]
command = "canny-agent"
args = ["--flag"]
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	canny, ok := cfg.Agents["canny"]
	if !ok {
		t.Fatal("canny agent not found")
	}

	if canny.Command != "canny-agent" {
		t.Errorf("canny.Command = %q, want canny-agent", canny.Command)
	}

	if len(canny.Args) != 1 || canny.Args[0] != "--flag" {
		t.Errorf("canny.Args = %v, want [--flag]", canny.Args)
	}

	if _, ok := cfg.Agents["claude"]; !ok {
		t.Error("default claude agent lost when adding canny agent")
	}
}

func TestMergeAgent(t *testing.T) {
	def := Agent{
		Command:    "claude",
		Args:       []string{"--session-id", "{agent_session_id}"},
		ResumeArgs: []string{"--resume", "{agent_session_id}"},
		ForkArgs:   []string{"--fork", "{fork_source_agent_session_id}"},
	}

	t.Run("override command only", func(t *testing.T) {
		usr := Agent{Command: "auld-claude"}

		got := mergeAgent(def, usr)
		if got.Command != "auld-claude" {
			t.Errorf("Command = %q, want auld-claude", got.Command)
		}

		if len(got.Args) != 2 {
			t.Errorf("Args = %v, want defaults preserved", got.Args)
		}

		if len(got.ResumeArgs) != 2 {
			t.Errorf("ResumeArgs = %v, want defaults preserved", got.ResumeArgs)
		}

		if len(got.ForkArgs) != 2 {
			t.Errorf("ForkArgs = %v, want defaults preserved", got.ForkArgs)
		}
	})

	t.Run("override env", func(t *testing.T) {
		usr := Agent{Env: map[string]string{"FOO": "neep"}}

		got := mergeAgent(def, usr)
		if got.Env["FOO"] != "neep" {
			t.Errorf("Env = %v, want FOO=neep", got.Env)
		}

		if got.Command != "claude" {
			t.Errorf("Command = %q, want claude", got.Command)
		}
	})

	t.Run("override idle_timeout", func(t *testing.T) {
		usr := Agent{IdleTimeout: "30m"}

		got := mergeAgent(def, usr)
		if got.IdleTimeout != "30m" {
			t.Errorf("IdleTimeout = %q, want 30m", got.IdleTimeout)
		}
	})

	t.Run("sandbox override", func(t *testing.T) {
		usr := Agent{Sandbox: SandboxConfig{Enabled: true, Features: []string{"ssh"}}}

		got := mergeAgent(def, usr)
		if !got.Sandbox.Enabled {
			t.Error("Sandbox.Enabled = false, want true")
		}

		if len(got.Sandbox.Features) != 1 || got.Sandbox.Features[0] != "ssh" {
			t.Errorf("Sandbox.Features = %v, want [ssh]", got.Sandbox.Features)
		}
	})

	t.Run("sandbox profile-only override replaces default sandbox", func(t *testing.T) {
		defWithSbx := def
		defWithSbx.Sandbox = SandboxConfig{ReadDirs: []string{"~/.claude"}}
		usr := Agent{Sandbox: SandboxConfig{Profile: "always-further/claude"}}

		got := mergeAgent(defWithSbx, usr)
		if got.Sandbox.Profile != "always-further/claude" {
			t.Errorf("Sandbox.Profile = %q, want always-further/claude", got.Sandbox.Profile)
		}
	})

	t.Run("whitespace-only profile does not clobber default sandbox", func(t *testing.T) {
		// A whitespace typo in a user agent's profile must NOT trip the
		// sandbox-override predicate — otherwise the embedded default sandbox
		// grants (e.g. ~/.claude) would be dropped even though the profile is
		// later trimmed to unset. See PR #807.
		defWithSbx := def
		defWithSbx.Sandbox = SandboxConfig{ReadDirs: []string{"~/.claude"}}
		usr := Agent{Sandbox: SandboxConfig{Profile: "   "}}

		got := mergeAgent(defWithSbx, usr)
		if len(got.Sandbox.ReadDirs) != 1 || got.Sandbox.ReadDirs[0] != "~/.claude" {
			t.Errorf("Sandbox.ReadDirs = %v, want [~/.claude] preserved from default", got.Sandbox.ReadDirs)
		}
	})

	t.Run("override inject_prompt", func(t *testing.T) {
		f := false
		usr := Agent{InjectPrompt: &f}

		got := mergeAgent(def, usr)
		if got.InjectPrompt == nil || *got.InjectPrompt != false {
			t.Errorf("InjectPrompt = %v, want false", got.InjectPrompt)
		}
	})

	t.Run("nil inject_prompt preserves default", func(t *testing.T) {
		tr := true
		defWithPrompt := def
		defWithPrompt.InjectPrompt = &tr
		usr := Agent{Command: "auld-claude"}

		got := mergeAgent(defWithPrompt, usr)
		if got.InjectPrompt == nil || *got.InjectPrompt != true {
			t.Errorf("InjectPrompt = %v, want true (preserved from default)", got.InjectPrompt)
		}
	})

	t.Run("override interrupt fields", func(t *testing.T) {
		count, delay := 3, 150
		usr := Agent{InterruptCount: &count, InterruptDelayMs: &delay}

		got := mergeAgent(def, usr)
		if got.InterruptCount == nil || *got.InterruptCount != 3 {
			t.Errorf("InterruptCount = %v, want 3", got.InterruptCount)
		}

		if got.InterruptDelayMs == nil || *got.InterruptDelayMs != 150 {
			t.Errorf("InterruptDelayMs = %v, want 150", got.InterruptDelayMs)
		}
	})

	t.Run("nil interrupt fields preserve default", func(t *testing.T) {
		count, delay := 2, 200
		defWithInt := def
		defWithInt.InterruptCount = &count
		defWithInt.InterruptDelayMs = &delay
		usr := Agent{Command: "auld-claude"}

		got := mergeAgent(defWithInt, usr)
		if got.InterruptCount == nil || *got.InterruptCount != 2 {
			t.Errorf("InterruptCount = %v, want 2 (preserved from default)", got.InterruptCount)
		}

		if got.InterruptDelayMs == nil || *got.InterruptDelayMs != 200 {
			t.Errorf("InterruptDelayMs = %v, want 200 (preserved from default)", got.InterruptDelayMs)
		}
	})
}

func TestAgentInterruptAccessors(t *testing.T) {
	t.Run("unset defaults to count 1 delay 0", func(t *testing.T) {
		a := Agent{}
		if got := a.InterruptCountValue(); got != 1 {
			t.Errorf("InterruptCountValue() = %d, want 1", got)
		}

		if got := a.InterruptDelay(); got != 0 {
			t.Errorf("InterruptDelay() = %v, want 0", got)
		}
	})

	t.Run("configured values are honoured", func(t *testing.T) {
		count, delay := 2, 200
		a := Agent{InterruptCount: &count, InterruptDelayMs: &delay}

		if got := a.InterruptCountValue(); got != 2 {
			t.Errorf("InterruptCountValue() = %d, want 2", got)
		}

		if got := a.InterruptDelay(); got != 200*time.Millisecond {
			t.Errorf("InterruptDelay() = %v, want 200ms", got)
		}
	})

	t.Run("count below 1 is clamped to 1", func(t *testing.T) {
		zero := 0
		if got := (Agent{InterruptCount: &zero}).InterruptCountValue(); got != 1 {
			t.Errorf("InterruptCountValue() = %d, want 1", got)
		}

		neg := -5
		if got := (Agent{InterruptCount: &neg}).InterruptCountValue(); got != 1 {
			t.Errorf("InterruptCountValue() = %d, want 1", got)
		}
	})

	t.Run("negative delay is treated as 0", func(t *testing.T) {
		neg := -10
		if got := (Agent{InterruptDelayMs: &neg}).InterruptDelay(); got != 0 {
			t.Errorf("InterruptDelay() = %v, want 0", got)
		}
	})
}

func TestDefaultConfigClaudeInterrupt(t *testing.T) {
	cfg := Default()

	claude, ok := cfg.Agents["claude"]
	if !ok {
		t.Fatal("default config has no claude agent")
	}

	if got := claude.InterruptCountValue(); got != 2 {
		t.Errorf("claude InterruptCountValue() = %d, want 2", got)
	}

	if got := claude.InterruptDelay(); got != 200*time.Millisecond {
		t.Errorf("claude InterruptDelay() = %v, want 200ms", got)
	}

	// Other known agents keep the single-press default.
	for _, name := range []string{"codex", "opencode", "cursor", "agy"} {
		agent, ok := cfg.Agents[name]
		if !ok {
			continue
		}

		if got := agent.InterruptCountValue(); got != 1 {
			t.Errorf("%s InterruptCountValue() = %d, want 1", name, got)
		}

		if got := agent.InterruptDelay(); got != 0 {
			t.Errorf("%s InterruptDelay() = %v, want 0", name, got)
		}
	}
}

func TestLoadConfigRepos(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[[repos]]
path = "~/Code/croft"

[[repos]]
path = "~/Code/glen-scripts"
allow_concurrent = true
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Repos) != 2 {
		t.Fatalf("Repos = %d entries, want 2", len(cfg.Repos))
	}

	if cfg.Repos[0].Path != "~/Code/croft" {
		t.Errorf("Repos[0].Path = %q, want ~/Code/croft", cfg.Repos[0].Path)
	}

	if cfg.Repos[0].AllowConcurrent {
		t.Error("Repos[0].AllowConcurrent = true, want false (default)")
	}

	if !cfg.Repos[1].AllowConcurrent {
		t.Error("Repos[1].AllowConcurrent = false, want true")
	}
}

func TestFindRepo(t *testing.T) {
	home, _ := os.UserHomeDir()

	t.Run("exact match", func(t *testing.T) {
		cfg := &Config{Repos: []RepoConfig{{Path: "~/Code/croft"}}}

		rc, ok := cfg.FindRepo(filepath.Join(home, "Code", "croft"))
		if !ok {
			t.Fatal("expected to find repo")
		}

		if rc.Path != "~/Code/croft" {
			t.Errorf("Path = %q, want ~/Code/croft", rc.Path)
		}
	})

	t.Run("no match", func(t *testing.T) {
		cfg := &Config{Repos: []RepoConfig{{Path: "~/Code/croft"}}}

		_, ok := cfg.FindRepo("/tmp/thrawn")
		if ok {
			t.Error("expected no match for /tmp/thrawn")
		}
	})

	t.Run("symlink resolved", func(t *testing.T) {
		actual := t.TempDir()

		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(actual, link); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}

		cfg := &Config{Repos: []RepoConfig{{Path: actual}}}

		_, ok := cfg.FindRepo(link)
		if !ok {
			t.Error("expected symlink to resolve and match")
		}
	})
}

func TestAvailableRepoPaths(t *testing.T) {
	home, _ := os.UserHomeDir()

	t.Run("empty config", func(t *testing.T) {
		cfg := &Config{}
		if got := cfg.AvailableRepoPaths(); got != nil {
			t.Errorf("expected nil for empty config, got %v", got)
		}
	})

	t.Run("allowed paths and repos combined, deduped, ~ expanded", func(t *testing.T) {
		cfg := &Config{
			AllowedRepoPaths: []string{"~/Code/croft", "/glen/bothy"},
			Repos: []RepoConfig{
				{Path: "/glen/bothy"}, // duplicate of an allowed path
				{Path: "~/Code/clachan"},
			},
		}

		got := cfg.AvailableRepoPaths()
		want := []string{
			filepath.Join(home, "Code", "croft"),
			"/glen/bothy",
			filepath.Join(home, "Code", "clachan"),
		}

		if !reflect.DeepEqual(got, want) {
			t.Errorf("AvailableRepoPaths() = %v, want %v", got, want)
		}
	})

	t.Run("repos only", func(t *testing.T) {
		cfg := &Config{Repos: []RepoConfig{{Path: "/glen/croft"}}}
		if got := cfg.AvailableRepoPaths(); !reflect.DeepEqual(got, []string{"/glen/croft"}) {
			t.Errorf("AvailableRepoPaths() = %v, want [/glen/croft]", got)
		}
	})
}

func TestDefaultParsesEmbeddedTOML(t *testing.T) {
	cfg := Default()
	if cfg.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude", cfg.DefaultAgent)
	}

	if cfg.BranchPrefix != "{username}/graith" {
		t.Errorf("BranchPrefix = %q, want {username}/graith", cfg.BranchPrefix)
	}

	if !cfg.FetchOnCreate {
		t.Error("FetchOnCreate = false, want true")
	}

	claude, ok := cfg.Agents["claude"]
	if !ok {
		t.Fatal("claude agent not found in defaults")
	}

	if claude.Command != "claude" {
		t.Errorf("claude.Command = %q, want claude", claude.Command)
	}

	wantForkArgs := []string{"--resume", "{fork_source_agent_session_id}", "--fork-session", "--session-id", "{agent_session_id}"}
	if !reflect.DeepEqual(claude.ForkArgs, wantForkArgs) {
		t.Errorf("claude.ForkArgs = %v, want %v", claude.ForkArgs, wantForkArgs)
	}

	codex, ok := cfg.Agents["codex"]
	if !ok {
		t.Fatal("codex agent not found in defaults")
	}

	if codex.Command != "codex" {
		t.Errorf("codex.Command = %q, want codex", codex.Command)
	}

	if _, ok := cfg.Agents["opencode"]; !ok {
		t.Error("opencode agent not found in defaults")
	}

	if _, ok := cfg.Agents["agy"]; !ok {
		t.Error("agy agent not found in defaults")
	}
}

func TestDefaultTOMLDefensiveCopy(t *testing.T) {
	a := DefaultTOML()
	b := DefaultTOML()
	a[0] = 0xFF

	if b[0] == 0xFF {
		t.Error("DefaultTOML() returns shared backing array, want independent copies")
	}
}

func TestDefaultMutationSafety(t *testing.T) {
	a := Default()
	a.Agents["claude"] = Agent{Command: "thrawn"}

	b := Default()
	if b.Agents["claude"].Command != "claude" {
		t.Error("mutating Default() result affected subsequent Default() calls")
	}
}

func TestValidate(t *testing.T) {
	t.Run("no includes is valid", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/braw-croft"}
		if err := rc.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("self-include rejected", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/braw-croft", Includes: []string{"~/Code/braw-croft"}}
		if err := rc.Validate(); err == nil {
			t.Error("expected error for self-include")
		}
	})

	t.Run("duplicate basename rejected", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/braw-croft", Includes: []string{"~/Code/kirk", "~/work/kirk"}}
		if err := rc.Validate(); err == nil {
			t.Error("expected error for duplicate basename")
		}
	})

	t.Run("main repo basename collision rejected", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/kirk", Includes: []string{"~/work/kirk"}}
		if err := rc.Validate(); err == nil {
			t.Error("expected error for main/include basename collision")
		}
	})

	t.Run("env var collision rejected", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/croft-main", Includes: []string{"~/Code/braw-kirk", "~/Code/braw.kirk"}}
		if err := rc.Validate(); err == nil {
			t.Error("expected error for env var name collision")
		}
	})

	t.Run("singleton plus allow_concurrent rejected with includes", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/braw-croft", Singleton: true, AllowConcurrent: true, Includes: []string{"~/Code/kirk"}}
		if err := rc.Validate(); err == nil {
			t.Error("expected error for singleton + allow_concurrent")
		}
	})

	t.Run("singleton plus allow_concurrent rejected without includes", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/braw-croft", Singleton: true, AllowConcurrent: true}
		if err := rc.Validate(); err == nil {
			t.Error("expected error for singleton + allow_concurrent without includes")
		}
	})

	t.Run("valid includes pass", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/croft-mono", Includes: []string{"~/Code/glen-frontend", "~/Code/glen-utils"}}
		if err := rc.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestIncludeEnvVarName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"glen-frontend", "GRAITH_INCLUDE_GLEN_FRONTEND_PATH"},
		{"glen-utils", "GRAITH_INCLUDE_GLEN_UTILS_PATH"},
		{"braw.web.sdk", "GRAITH_INCLUDE_BRAW_WEB_SDK_PATH"},
		{"bonnie croft", "GRAITH_INCLUDE_BONNIECROFT_PATH"},
		{"auld@kirk!", "GRAITH_INCLUDE_AULDKIRK_PATH"},
	}
	for _, tt := range tests {
		got := IncludeEnvVarName(tt.input)
		if got != tt.want {
			t.Errorf("IncludeEnvVarName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSandboxConfigMergeDeduplicatesFeatures(t *testing.T) {
	global := SandboxConfig{
		Enabled:  true,
		Features: []string{"ssh", "docker"},
	}
	agent := SandboxConfig{
		Features: []string{"ssh", "clipboard"},
	}

	merged := global.Merge(agent)

	want := []string{"ssh", "docker", "clipboard"}
	if len(merged.Features) != 3 {
		t.Fatalf("merged.Features = %v, want %v", merged.Features, want)
	}

	for i, f := range want {
		if merged.Features[i] != f {
			t.Errorf("merged.Features[%d] = %q, want %q", i, merged.Features[i], f)
		}
	}
}

func TestDefaultAgentSandboxPaths(t *testing.T) {
	cfg := Default()

	tests := []struct {
		agent    string
		wantRead []string
	}{
		{"claude", []string{"~/.claude"}},
		{"codex", []string{"~/.codex"}},
		{"agy", []string{"~/.gemini"}},
	}

	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			agent, ok := cfg.Agents[tt.agent]
			if !ok {
				t.Fatalf("agent %q not found in defaults", tt.agent)
			}

			if !reflect.DeepEqual(agent.Sandbox.ReadDirs, tt.wantRead) {
				t.Errorf("%s.Sandbox.ReadDirs = %v, want %v", tt.agent, agent.Sandbox.ReadDirs, tt.wantRead)
			}
		})
	}
}

func TestMergeMCPServers(t *testing.T) {
	t.Run("no overrides", func(t *testing.T) {
		global := []MCPServerConfig{
			{Name: "graith", Command: "gr", Args: []string{"mcp"}},
			{Name: "chrome", Command: "npx", Args: []string{"chrome-mcp"}},
		}

		got := MergeMCPServers(global, nil)
		if len(got) != 2 {
			t.Fatalf("got %d servers, want 2", len(got))
		}

		if got[0].Name != "graith" || got[1].Name != "chrome" {
			t.Errorf("order = [%s, %s], want [graith, chrome]", got[0].Name, got[1].Name)
		}
	})

	t.Run("override args", func(t *testing.T) {
		global := []MCPServerConfig{
			{Name: "chrome", Command: "npx", Args: []string{"chrome-mcp", "--port", "9222"}},
		}
		overrides := map[string]MCPServerConfig{
			"chrome": {Args: []string{"chrome-mcp", "--port", "9333"}},
		}

		got := MergeMCPServers(global, overrides)
		if len(got) != 1 {
			t.Fatalf("got %d servers, want 1", len(got))
		}

		if got[0].Args[2] != "9333" {
			t.Errorf("args = %v, want port 9333", got[0].Args)
		}

		if got[0].Command != "npx" {
			t.Errorf("command = %q, want npx (preserved from global)", got[0].Command)
		}
	})

	t.Run("disable server", func(t *testing.T) {
		global := []MCPServerConfig{
			{Name: "graith", Command: "gr"},
			{Name: "chrome", Command: "npx"},
		}
		overrides := map[string]MCPServerConfig{
			"graith": {Disabled: true},
		}

		got := MergeMCPServers(global, overrides)
		if len(got) != 1 {
			t.Fatalf("got %d servers, want 1", len(got))
		}

		if got[0].Name != "chrome" {
			t.Errorf("remaining server = %q, want chrome", got[0].Name)
		}
	})

	t.Run("agent-specific addition", func(t *testing.T) {
		global := []MCPServerConfig{
			{Name: "graith", Command: "gr"},
		}
		overrides := map[string]MCPServerConfig{
			"canny": {Command: "canny-tool", Args: []string{"serve"}},
		}

		got := MergeMCPServers(global, overrides)
		if len(got) != 2 {
			t.Fatalf("got %d servers, want 2", len(got))
		}

		if got[1].Name != "canny" {
			t.Errorf("added server name = %q, want canny", got[1].Name)
		}

		if got[1].Command != "canny-tool" {
			t.Errorf("added server command = %q, want canny-tool", got[1].Command)
		}
	})

	t.Run("disabled addition is skipped", func(t *testing.T) {
		got := MergeMCPServers(nil, map[string]MCPServerConfig{
			"thrawn-server": {Disabled: true, Command: "thrawn-cmd"},
		})
		if len(got) != 0 {
			t.Errorf("got %d servers, want 0", len(got))
		}
	})

	t.Run("duplicate global names deduplicates", func(t *testing.T) {
		global := []MCPServerConfig{
			{Name: "graith", Command: "gr", Args: []string{"mcp"}},
			{Name: "graith", Disabled: true},
		}

		got := MergeMCPServers(global, nil)
		if len(got) != 0 {
			t.Errorf("got %d servers, want 0 (disabled wins)", len(got))
		}
	})

	t.Run("duplicate global names last wins", func(t *testing.T) {
		global := []MCPServerConfig{
			{Name: "graith", Command: "auld-gr"},
			{Name: "graith", Command: "braw-gr", Args: []string{"mcp", "--verbose"}},
		}

		got := MergeMCPServers(global, nil)
		if len(got) != 1 {
			t.Fatalf("got %d servers, want 1", len(got))
		}

		if got[0].Command != "braw-gr" {
			t.Errorf("command = %q, want braw-gr (last entry wins)", got[0].Command)
		}
	})

	t.Run("global disabled filtered", func(t *testing.T) {
		global := []MCPServerConfig{
			{Name: "a", Command: "a"},
			{Name: "b", Command: "b", Disabled: true},
			{Name: "c", Command: "c"},
		}

		got := MergeMCPServers(global, nil)
		if len(got) != 2 {
			t.Fatalf("got %d servers, want 2", len(got))
		}

		if got[0].Name != "a" || got[1].Name != "c" {
			t.Errorf("got [%s, %s], want [a, c]", got[0].Name, got[1].Name)
		}
	})

	t.Run("preserves order", func(t *testing.T) {
		global := []MCPServerConfig{
			{Name: "a", Command: "a"},
			{Name: "b", Command: "b"},
			{Name: "c", Command: "c"},
		}
		overrides := map[string]MCPServerConfig{
			"b": {Command: "b2"},
		}

		got := MergeMCPServers(global, overrides)
		if len(got) != 3 {
			t.Fatalf("got %d servers, want 3", len(got))
		}

		if got[0].Name != "a" || got[1].Name != "b" || got[2].Name != "c" {
			t.Errorf("order = [%s, %s, %s], want [a, b, c]", got[0].Name, got[1].Name, got[2].Name)
		}

		if got[1].Command != "b2" {
			t.Errorf("b command = %q, want b2", got[1].Command)
		}
	})
}

func TestMCPServerValidation(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		cfg := Default()

		cfg.MCPServers = []MCPServerConfig{
			{Name: "chrome", Command: "npx"},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("duplicate name", func(t *testing.T) {
		cfg := Default()

		cfg.MCPServers = []MCPServerConfig{
			{Name: "chrome", Command: "npx"},
			{Name: "chrome", Command: "other"},
		}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for duplicate MCP server name")
		}
	})

	t.Run("empty name", func(t *testing.T) {
		cfg := Default()

		cfg.MCPServers = []MCPServerConfig{
			{Name: "", Command: "npx"},
		}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for empty MCP server name")
		}
	})

	t.Run("empty command", func(t *testing.T) {
		cfg := Default()

		cfg.MCPServers = []MCPServerConfig{
			{Name: "chrome", Command: ""},
		}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for empty MCP server command")
		}
	})

	t.Run("disabled with empty command is ok", func(t *testing.T) {
		cfg := Default()

		cfg.MCPServers = []MCPServerConfig{
			{Name: "graith", Disabled: true},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("agent-specific addition without command", func(t *testing.T) {
		cfg := Default()

		cfg.Agents["claude"] = Agent{
			Command: "claude",
			MCPServers: map[string]MCPServerConfig{
				"thrawn": {Args: []string{"--flag"}},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for agent-specific MCP server without command")
		}
	})

	t.Run("agent override without command is ok", func(t *testing.T) {
		cfg := Default()
		cfg.MCPServers = []MCPServerConfig{
			{Name: "chrome", Command: "npx"},
		}

		cfg.Agents["claude"] = Agent{
			Command: "claude",
			MCPServers: map[string]MCPServerConfig{
				"chrome": {Args: []string{"--port", "9333"}},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("unexpected error for agent override without command: %v", err)
		}
	})
}

func TestLoadConfigMCPServers(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[[mcp_servers]]
name = "chrome"
command = "npx"
args = ["chrome-mcp", "--port", "9222"]

[[mcp_servers]]
name = "canny"
command = "canny-tool"

[agents.claude.mcp_servers.chrome]
args = ["chrome-mcp", "--port", "9333"]

[agents.claude.mcp_servers.agent-only]
command = "bonnie"
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.MCPServers) != 2 {
		t.Fatalf("MCPServers = %d, want 2", len(cfg.MCPServers))
	}

	if cfg.MCPServers[0].Name != "chrome" {
		t.Errorf("MCPServers[0].Name = %q, want chrome", cfg.MCPServers[0].Name)
	}

	if cfg.MCPServers[0].Args[2] != "9222" {
		t.Errorf("MCPServers[0].Args = %v, want port 9222", cfg.MCPServers[0].Args)
	}

	claude := cfg.Agents["claude"]
	if len(claude.MCPServers) != 2 {
		t.Fatalf("claude.MCPServers = %d entries, want 2", len(claude.MCPServers))
	}

	chromeOvr, ok := claude.MCPServers["chrome"]
	if !ok {
		t.Fatal("claude.MCPServers missing chrome override")
	}

	if chromeOvr.Args[2] != "9333" {
		t.Errorf("claude chrome override args = %v, want port 9333", chromeOvr.Args)
	}

	agentOnly, ok := claude.MCPServers["agent-only"]
	if !ok {
		t.Fatal("claude.MCPServers missing agent-only")
	}

	if agentOnly.Command != "bonnie" {
		t.Errorf("agent-only command = %q, want bonnie", agentOnly.Command)
	}
}

func TestMergeAgentPreservesMCPServers(t *testing.T) {
	def := Agent{
		Command: "claude",
		Args:    []string{"--session-id"},
	}
	usr := Agent{
		MCPServers: map[string]MCPServerConfig{
			"chrome": {Command: "npx"},
		},
	}

	got := mergeAgent(def, usr)
	if len(got.MCPServers) != 1 {
		t.Fatalf("MCPServers = %d, want 1", len(got.MCPServers))
	}

	if got.MCPServers["chrome"].Command != "npx" {
		t.Errorf("chrome command = %q, want npx", got.MCPServers["chrome"].Command)
	}

	if got.Command != "claude" {
		t.Errorf("Command = %q, want claude (preserved)", got.Command)
	}
}

func TestStatusConfig_TTLDuration(t *testing.T) {
	tests := []struct {
		name string
		ttl  string
		want time.Duration
	}{
		{"default empty", "", 5 * time.Minute},
		{"explicit 10m", "10m", 10 * time.Minute},
		{"with days", "1d", 24 * time.Hour},
		{"30 seconds", "30s", 30 * time.Second},
		{"invalid falls back", "thrawn", 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := StatusConfig{TTL: tt.ttl}
			if got := sc.TTLDuration(); got != tt.want {
				t.Errorf("TTLDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAgySandboxPathsMergedWithGlobal(t *testing.T) {
	global := SandboxConfig{
		Enabled:  true,
		ReadDirs: []string{"~/Code"},
	}
	cfg := Default()
	agy := cfg.Agents["agy"]

	merged := global.Merge(agy.Sandbox)

	if !merged.Enabled {
		t.Error("merged.Enabled = false, want true")
	}

	foundGemini := false

	for _, d := range merged.ReadDirs {
		if d == "~/.gemini" {
			foundGemini = true
		}
	}

	if !foundGemini {
		t.Errorf("merged.ReadDirs = %v, want ~/.gemini included", merged.ReadDirs)
	}
}

func TestAgentPromptInjectionEnabled(t *testing.T) {
	t.Run("nil defaults to true", func(t *testing.T) {
		a := Agent{}
		if !a.PromptInjectionEnabled() {
			t.Error("nil InjectPrompt should default to true")
		}
	})
	t.Run("explicit true", func(t *testing.T) {
		v := true

		a := Agent{InjectPrompt: &v}
		if !a.PromptInjectionEnabled() {
			t.Error("explicit true should return true")
		}
	})
	t.Run("explicit false", func(t *testing.T) {
		v := false

		a := Agent{InjectPrompt: &v}
		if a.PromptInjectionEnabled() {
			t.Error("explicit false should return false")
		}
	})
}

func TestValidPromptInjection(t *testing.T) {
	valid := []string{
		"", // empty = name-based fallback
		PromptInjectionAppendSystemPrompt,
		PromptInjectionCursorRules,
		PromptInjectionDeveloperInstructions,
		PromptInjectionNone,
	}
	for _, v := range valid {
		if !ValidPromptInjection(v) {
			t.Errorf("ValidPromptInjection(%q) = false, want true", v)
		}
	}

	invalid := []string{"append", "system_prompt", "cursor", "developer", "haar", "NONE"}
	for _, v := range invalid {
		if ValidPromptInjection(v) {
			t.Errorf("ValidPromptInjection(%q) = true, want false", v)
		}
	}
}

// TestValidateRejectsUnknownPromptInjection guards that an unknown
// prompt_injection value fails config validation loudly rather than silently
// becoming "no injection" — graith owns this enum (#1232).
func TestValidateRejectsUnknownPromptInjection(t *testing.T) {
	cfg := &Config{
		Agents: map[string]Agent{
			"thrawn": {Command: "thrawn", PromptInjection: "haar-nonsense"},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for unknown prompt_injection value")
	}

	if !strings.Contains(err.Error(), "prompt_injection") || !strings.Contains(err.Error(), "haar-nonsense") {
		t.Errorf("error should name the field and bad value, got: %v", err)
	}

	// A valid value passes.
	cfg.Agents["thrawn"] = Agent{Command: "thrawn", PromptInjection: PromptInjectionCursorRules}
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid prompt_injection should pass validation, got: %v", err)
	}
}

func TestLoadAgentPromptInjection(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	toml := `
[agents.thrawn]
command = "thrawn"
prompt_injection = "developer_instructions"
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Agents["thrawn"].PromptInjection; got != PromptInjectionDeveloperInstructions {
		t.Errorf("prompt_injection = %q, want %q", got, PromptInjectionDeveloperInstructions)
	}
}

func TestLoadAgentInjectPrompt(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	toml := `
[agents.claude]
inject_prompt = false
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	claude := cfg.Agents["claude"]
	if claude.PromptInjectionEnabled() {
		t.Error("inject_prompt = false should disable prompt injection")
	}
}

func TestLoadAgentPreTrustWorkspace(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	toml := `
[agents.cursor]
pre_trust_workspace = false
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	cursor := cfg.Agents["cursor"]
	if cursor.PreTrustWorkspaceEnabled() {
		t.Error("pre_trust_workspace = false should disable pre-trust")
	}
}

func TestGitPullConfig_IntervalDuration(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		want     time.Duration
	}{
		{"default empty", "", time.Hour},
		{"explicit 30m", "30m", 30 * time.Minute},
		{"with days", "1d", 24 * time.Hour},
		{"invalid falls back", "thrawn", time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := GitPullConfig{Interval: tt.interval}
			if got := g.IntervalDuration(); got != tt.want {
				t.Errorf("IntervalDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGitPullConfig_Validate(t *testing.T) {
	t.Run("valid interval passes", func(t *testing.T) {
		cfg := Default()

		cfg.GitPull = GitPullConfig{Enabled: true, Interval: "1h"}
		if err := cfg.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid interval rejected", func(t *testing.T) {
		cfg := Default()

		cfg.GitPull = GitPullConfig{Enabled: true, Interval: "thrawn"}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for invalid interval")
		}
	})

	t.Run("interval under 1 minute rejected", func(t *testing.T) {
		cfg := Default()

		cfg.GitPull = GitPullConfig{Enabled: true, Interval: "30s"}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for sub-minute interval")
		}
	})

	t.Run("empty interval passes validation", func(t *testing.T) {
		cfg := Default()

		cfg.GitPull = GitPullConfig{Enabled: true, Interval: ""}
		if err := cfg.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestOrchestratorSandboxConfigParsing(t *testing.T) {
	t.Run("absent section produces zero value", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		_ = os.WriteFile(cfgPath, []byte(`
[orchestrator]
enabled = true
agent = "claude"
`), 0o600)

		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatal(err)
		}

		if cfg.Orchestrator.Sandbox.ReadDirs != nil {
			t.Errorf("ReadDirs = %v, want nil", cfg.Orchestrator.Sandbox.ReadDirs)
		}

		if cfg.Orchestrator.Sandbox.WriteDirs != nil {
			t.Errorf("WriteDirs = %v, want nil", cfg.Orchestrator.Sandbox.WriteDirs)
		}
	})

	t.Run("empty section produces zero value", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		_ = os.WriteFile(cfgPath, []byte(`
[orchestrator]
enabled = true
[orchestrator.sandbox]
`), 0o600)

		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatal(err)
		}

		if cfg.Orchestrator.Sandbox.ReadDirs != nil {
			t.Errorf("ReadDirs = %v, want nil", cfg.Orchestrator.Sandbox.ReadDirs)
		}
	})

	t.Run("populated section parsed correctly", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		_ = os.WriteFile(cfgPath, []byte(`
[orchestrator]
enabled = true
[orchestrator.sandbox]
read_dirs = ["~/docs"]
write_dirs = ["~/.config/graith", "/tmp/extra"]
`), 0o600)

		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatal(err)
		}

		if len(cfg.Orchestrator.Sandbox.ReadDirs) != 1 || cfg.Orchestrator.Sandbox.ReadDirs[0] != "~/docs" {
			t.Errorf("ReadDirs = %v, want [~/docs]", cfg.Orchestrator.Sandbox.ReadDirs)
		}

		if len(cfg.Orchestrator.Sandbox.WriteDirs) != 2 {
			t.Errorf("WriteDirs len = %d, want 2", len(cfg.Orchestrator.Sandbox.WriteDirs))
		}
	})
}

func TestOrchestratorRestartDelayForLevel(t *testing.T) {
	t.Run("zero config preserves the historical schedule", func(t *testing.T) {
		var r OrchestratorRestartConfig

		want := []time.Duration{
			2 * time.Second,
			4 * time.Second,
			8 * time.Second,
			16 * time.Second,
			32 * time.Second,
			60 * time.Second,
			300 * time.Second,
		}
		for level, w := range want {
			if got := r.DelayForLevel(level); got != w {
				t.Errorf("level %d: DelayForLevel = %v, want %v", level, got, w)
			}
		}
	})

	t.Run("levels past the schedule repeat the last entry", func(t *testing.T) {
		var r OrchestratorRestartConfig
		if got := r.DelayForLevel(99); got != 300*time.Second {
			t.Errorf("DelayForLevel(99) = %v, want 300s", got)
		}
	})

	t.Run("negative level is clamped to zero", func(t *testing.T) {
		var r OrchestratorRestartConfig
		if got := r.DelayForLevel(-5); got != 2*time.Second {
			t.Errorf("DelayForLevel(-5) = %v, want 2s", got)
		}
	})

	t.Run("explicit schedule wins over geometric knobs", func(t *testing.T) {
		r := OrchestratorRestartConfig{
			Schedule:       []string{"1s", "5s", "10s"},
			InitialBackoff: "2s",
			Multiplier:     3,
		}

		want := []time.Duration{time.Second, 5 * time.Second, 10 * time.Second, 10 * time.Second}
		for level, w := range want {
			if got := r.DelayForLevel(level); got != w {
				t.Errorf("level %d: DelayForLevel = %v, want %v", level, got, w)
			}
		}
	})

	t.Run("unparseable schedule entries are skipped", func(t *testing.T) {
		r := OrchestratorRestartConfig{Schedule: []string{"nope", "5s", "bad"}}
		if got := r.DelayForLevel(0); got != 5*time.Second {
			t.Errorf("DelayForLevel(0) = %v, want 5s (only valid entry)", got)
		}
	})

	t.Run("non-positive direct values keep a positive restart floor", func(t *testing.T) {
		cases := []OrchestratorRestartConfig{
			{Schedule: []string{"0s"}},
			{Schedule: []string{"-1s"}},
			{Schedule: []string{"0s", "-1s"}, InitialBackoff: "0s", MaxBackoff: "-1s"},
		}
		for _, r := range cases {
			if got := r.DelayForLevel(0); got <= 0 {
				t.Errorf("DelayForLevel(0) = %v, want a positive defensive floor", got)
			}
		}
	})

	t.Run("geometric mode computes and caps at max", func(t *testing.T) {
		r := OrchestratorRestartConfig{
			InitialBackoff: "1s",
			MaxBackoff:     "10s",
			Multiplier:     2,
		}

		want := []time.Duration{
			1 * time.Second,
			2 * time.Second,
			4 * time.Second,
			8 * time.Second,
			10 * time.Second, // 16s capped to 10s
			10 * time.Second,
		}
		for level, w := range want {
			if got := r.DelayForLevel(level); got != w {
				t.Errorf("level %d: DelayForLevel = %v, want %v", level, got, w)
			}
		}
	})

	t.Run("setting only one geometric knob switches off the historical schedule", func(t *testing.T) {
		// Multiplier set, initial/max default: pure doubling from 2s capped at 300s.
		r := OrchestratorRestartConfig{Multiplier: 2}
		if got := r.DelayForLevel(5); got != 64*time.Second {
			t.Errorf("DelayForLevel(5) = %v, want 64s (geometric, not historical 60s)", got)
		}
	})

	t.Run("non-positive multiplier falls back to default", func(t *testing.T) {
		r := OrchestratorRestartConfig{InitialBackoff: "1s", Multiplier: 0.5}
		if got := r.DelayForLevel(1); got != 2*time.Second {
			t.Errorf("DelayForLevel(1) = %v, want 2s (default multiplier 2)", got)
		}
	})
}

// TestDefaultConfigRestartPreservesHistoricalBehaviour locks in that the shipped
// embedded default config reproduces graith's pre-config restart behaviour
// exactly (issue #1239 "sensible defaults preserved").
func TestDefaultConfigRestartPreservesHistoricalBehaviour(t *testing.T) {
	rc := Default().Orchestrator.Restart

	wantSecs := []int{2, 4, 8, 16, 32, 60, 300}
	for level, secs := range wantSecs {
		want := time.Duration(secs) * time.Second
		if got := rc.DelayForLevel(level); got != want {
			t.Errorf("level %d: DelayForLevel = %v, want %v", level, got, want)
		}
	}

	if got := rc.StableResetDuration(); got != 60*time.Second {
		t.Errorf("default StableResetDuration = %v, want 60s", got)
	}

	if got := rc.FreshStartThresholdOrDefault(); got != 3 {
		t.Errorf("default FreshStartThreshold = %d, want 3", got)
	}
}

func TestOrchestratorRestartStableReset(t *testing.T) {
	t.Run("empty uses default", func(t *testing.T) {
		var r OrchestratorRestartConfig
		if got := r.StableResetDuration(); got != OrchestratorStableResetDefault {
			t.Errorf("StableResetDuration = %v, want %v", got, OrchestratorStableResetDefault)
		}
	})

	t.Run("configured value is used", func(t *testing.T) {
		r := OrchestratorRestartConfig{StableReset: "5m"}
		if got := r.StableResetDuration(); got != 5*time.Minute {
			t.Errorf("StableResetDuration = %v, want 5m", got)
		}
	})

	t.Run("unparseable falls back to default", func(t *testing.T) {
		r := OrchestratorRestartConfig{StableReset: "notaduration"}
		if got := r.StableResetDuration(); got != OrchestratorStableResetDefault {
			t.Errorf("StableResetDuration = %v, want default", got)
		}
	})

	for _, bad := range []string{"0", "0s", "-1s"} {
		t.Run("non-positive "+bad+" falls back to default", func(t *testing.T) {
			r := OrchestratorRestartConfig{StableReset: bad}
			if got := r.StableResetDuration(); got != OrchestratorStableResetDefault {
				t.Errorf("StableResetDuration(%q) = %v, want default", bad, got)
			}
		})
	}
}

func TestOrchestratorRestartFreshStartThreshold(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero uses default", 0, OrchestratorFreshStartThresholdDefault},
		{"negative uses default", -1, OrchestratorFreshStartThresholdDefault},
		{"positive is used", 7, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := OrchestratorRestartConfig{FreshStartThreshold: tc.in}
			if got := r.FreshStartThresholdOrDefault(); got != tc.want {
				t.Errorf("FreshStartThresholdOrDefault = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestOrchestratorRestartValidate(t *testing.T) {
	t.Run("bad durations and schedule entries are reported", func(t *testing.T) {
		c := &Config{Orchestrator: OrchestratorConfig{Restart: OrchestratorRestartConfig{
			InitialBackoff: "notaduration",
			MaxBackoff:     "alsobad",
			StableReset:    "nope",
			Schedule:       []string{"2s", "wut"},
		}}}

		err := c.Validate()
		if err == nil {
			t.Fatal("expected validation error for bad restart config")
		}

		for _, want := range []string{
			"orchestrator.restart.initial_backoff",
			"orchestrator.restart.max_backoff",
			"orchestrator.restart.stable_reset",
			"orchestrator.restart.schedule[1]",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q missing mention of %q", err.Error(), want)
			}
		}
	})

	t.Run("valid restart config passes", func(t *testing.T) {
		c := &Config{Orchestrator: OrchestratorConfig{Restart: OrchestratorRestartConfig{
			InitialBackoff:      "2s",
			MaxBackoff:          "300s",
			Multiplier:          2,
			Schedule:            []string{"2s", "4s"},
			StableReset:         "60s",
			FreshStartThreshold: 3,
		}}}
		if err := c.Validate(); err != nil {
			t.Errorf("unexpected validation error: %v", err)
		}
	})

	t.Run("non-positive and incoherent policies are rejected", func(t *testing.T) {
		tests := []struct {
			name    string
			mutate  func(*OrchestratorRestartConfig)
			wantErr string
		}{
			{"initial zero", func(r *OrchestratorRestartConfig) { r.InitialBackoff = "0s" }, "initial_backoff"},
			{"initial negative", func(r *OrchestratorRestartConfig) { r.InitialBackoff = "-1s" }, "initial_backoff"},
			{"max zero", func(r *OrchestratorRestartConfig) { r.MaxBackoff = "0s" }, "max_backoff"},
			{"max negative", func(r *OrchestratorRestartConfig) { r.MaxBackoff = "-1s" }, "max_backoff"},
			{"stable zero", func(r *OrchestratorRestartConfig) { r.StableReset = "0s" }, "stable_reset"},
			{"stable negative", func(r *OrchestratorRestartConfig) { r.StableReset = "-1s" }, "stable_reset"},
			{"schedule zero", func(r *OrchestratorRestartConfig) { r.Schedule = []string{"0s"} }, "schedule[0]"},
			{"schedule negative", func(r *OrchestratorRestartConfig) { r.Schedule = []string{"-1s"} }, "schedule[0]"},
			{"schedule decreases", func(r *OrchestratorRestartConfig) { r.Schedule = []string{"2s", "1s"} }, "previous delay"},
			{"initial exceeds max", func(r *OrchestratorRestartConfig) { r.InitialBackoff, r.MaxBackoff = "3s", "2s" }, "must not exceed max_backoff"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cfg := Default()
				tt.mutate(&cfg.Orchestrator.Restart)
				err := cfg.Validate()
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
				}
			})
		}
	})

	t.Run("coherent equality boundaries pass", func(t *testing.T) {
		cfg := Default()
		cfg.Orchestrator.Restart.InitialBackoff = "2s"
		cfg.Orchestrator.Restart.MaxBackoff = "2s"
		cfg.Orchestrator.Restart.Schedule = []string{"1s", "1s"}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil at equality boundaries", err)
		}
	})
}

func TestOrchestratorAgentName(t *testing.T) {
	t.Run("explicit orchestrator agent wins over default_agent", func(t *testing.T) {
		o := OrchestratorConfig{Agent: "codex"}
		if got := o.AgentName("cursor"); got != "codex" {
			t.Errorf("AgentName = %q, want codex", got)
		}
	})

	t.Run("inherits default_agent when orchestrator agent is unset", func(t *testing.T) {
		o := OrchestratorConfig{}
		if got := o.AgentName("codex"); got != "codex" {
			t.Errorf("AgentName = %q, want codex", got)
		}
	})

	t.Run("falls back to claude when neither is set", func(t *testing.T) {
		o := OrchestratorConfig{}
		if got := o.AgentName(""); got != "claude" {
			t.Errorf("AgentName = %q, want claude", got)
		}
	})
}

// TestOrchestratorInheritsDefaultAgent is a regression test for the orchestrator
// hardcoding "claude" instead of inheriting the top-level default_agent: a config
// that sets default_agent but leaves [orchestrator] agent unset must resolve the
// orchestrator to the default agent, not claude.
func TestOrchestratorInheritsDefaultAgent(t *testing.T) {
	t.Run("default_agent inherited when orchestrator agent absent", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		_ = os.WriteFile(cfgPath, []byte(`
default_agent = "codex"
[orchestrator]
enabled = true
`), 0o600)

		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatal(err)
		}

		if got := cfg.Orchestrator.AgentName(cfg.DefaultAgent); got != "codex" {
			t.Errorf("orchestrator AgentName = %q, want codex", got)
		}
	})

	t.Run("explicit orchestrator agent overrides default_agent", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		_ = os.WriteFile(cfgPath, []byte(`
default_agent = "codex"
[orchestrator]
enabled = true
agent = "cursor"
`), 0o600)

		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatal(err)
		}

		if got := cfg.Orchestrator.AgentName(cfg.DefaultAgent); got != "cursor" {
			t.Errorf("orchestrator AgentName = %q, want cursor", got)
		}
	})

	t.Run("bare default config resolves orchestrator to claude", func(t *testing.T) {
		cfg := Default()
		if got := cfg.Orchestrator.AgentName(cfg.DefaultAgent); got != "claude" {
			t.Errorf("orchestrator AgentName = %q, want claude", got)
		}
	})
}

func TestOrchestratorSandboxIgnoresDangerousKeys(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(cfgPath, []byte(`
[orchestrator]
enabled = true
agent = "claude"
[orchestrator.sandbox]
disabled = true
enabled = true
command = "thrawn"
features = ["network"]
write_dirs = ["~/.config/graith"]
`), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Orchestrator.Sandbox.WriteDirs) != 1 {
		t.Errorf("WriteDirs = %v, want [~/.config/graith]", cfg.Orchestrator.Sandbox.WriteDirs)
	}

	merged := cfg.OrchestratorSandboxMerged("claude")

	baseline := cfg.Sandbox.Merge(cfg.Agents["claude"].Sandbox)
	if merged.Command != baseline.Command {
		t.Errorf("merged.Command = %q, want %q (dangerous key should not alter command)", merged.Command, baseline.Command)
	}

	if merged.Enabled != baseline.Enabled {
		t.Errorf("merged.Enabled = %v, want %v (dangerous key should not alter enabled)", merged.Enabled, baseline.Enabled)
	}

	if !reflect.DeepEqual(merged.Features, baseline.Features) {
		t.Errorf("merged.Features = %v, want %v (dangerous key should not add features)", merged.Features, baseline.Features)
	}
}

func TestOrchestratorSandboxMerged(t *testing.T) {
	cfg := &Config{
		Sandbox: SandboxConfig{
			Enabled:   true,
			ReadDirs:  []string{"/glen/read"},
			WriteDirs: []string{"/glen/write"},
		},
		Agents: map[string]Agent{
			"claude": {
				Sandbox: SandboxConfig{
					ReadDirs:  []string{"/croft/read"},
					WriteDirs: []string{"/croft/write"},
				},
			},
		},
		Orchestrator: OrchestratorConfig{
			Sandbox: OrchestratorSandboxConfig{
				ReadDirs:  []string{"/kirk/read"},
				WriteDirs: []string{"~/.config/graith"},
			},
		},
	}

	merged := cfg.OrchestratorSandboxMerged("claude")

	wantRead := []string{"/glen/read", "/croft/read", "/kirk/read"}
	if !reflect.DeepEqual(merged.ReadDirs, wantRead) {
		t.Errorf("ReadDirs = %v, want %v", merged.ReadDirs, wantRead)
	}

	wantWrite := []string{"/glen/write", "/croft/write", "~/.config/graith"}
	if !reflect.DeepEqual(merged.WriteDirs, wantWrite) {
		t.Errorf("WriteDirs = %v, want %v", merged.WriteDirs, wantWrite)
	}

	if !merged.Enabled {
		t.Error("merged should be enabled")
	}
}

func TestOrchestratorSandboxMergedDedup(t *testing.T) {
	cfg := &Config{
		Sandbox: SandboxConfig{
			ReadDirs: []string{"/glen"},
		},
		Agents: map[string]Agent{
			"claude": {
				Sandbox: SandboxConfig{
					ReadDirs: []string{"/glen"},
				},
			},
		},
		Orchestrator: OrchestratorConfig{
			Sandbox: OrchestratorSandboxConfig{
				ReadDirs: []string{"/glen", "/kirk-only"},
			},
		},
	}

	merged := cfg.OrchestratorSandboxMerged("claude")

	wantRead := []string{"/glen", "/kirk-only"}
	if !reflect.DeepEqual(merged.ReadDirs, wantRead) {
		t.Errorf("ReadDirs = %v, want %v (should dedup)", merged.ReadDirs, wantRead)
	}
}

func TestOrchestratorSandboxBackwardCompat(t *testing.T) {
	cfg := &Config{
		Sandbox: SandboxConfig{
			Enabled:   true,
			ReadDirs:  []string{"/glen"},
			WriteDirs: []string{"/glen-w"},
		},
		Agents: map[string]Agent{
			"claude": {
				Sandbox: SandboxConfig{
					ReadDirs:  []string{"/croft"},
					WriteDirs: []string{"/croft-w"},
				},
			},
		},
		Orchestrator: OrchestratorConfig{},
	}

	twoLayer := cfg.Sandbox.Merge(cfg.Agents["claude"].Sandbox)
	threeLayer := cfg.OrchestratorSandboxMerged("claude")

	if !reflect.DeepEqual(twoLayer, threeLayer) {
		t.Errorf("empty orchestrator sandbox should produce same result as two-layer merge\ntwo-layer: %+v\nthree-layer: %+v", twoLayer, threeLayer)
	}
}

func TestApprovalsHookEnabled(t *testing.T) {
	no := false
	yes := true

	tests := []struct {
		name    string
		enabled *bool
		want    bool
	}{
		{"unset defaults to disabled", nil, false},
		{"explicitly enabled", &yes, true},
		{"explicitly disabled", &no, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := Approvals{Enabled: tt.enabled}
			if got := a.HookEnabled(); got != tt.want {
				t.Errorf("HookEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApprovalsDefaultConfigDisablesHook(t *testing.T) {
	// The shipped default must leave approval gating off so unattended agents
	// aren't gated on a human who may never answer.
	if Default().Approvals.HookEnabled() {
		t.Error("Default() approvals hook is enabled, want disabled by default")
	}
}

func TestApprovalsEnabledParsedFromTOML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[approvals]
enabled = false
`

	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Approvals.Enabled == nil {
		t.Fatal("Approvals.Enabled is nil, want explicit false")
	}

	if *cfg.Approvals.Enabled {
		t.Error("Approvals.Enabled = true, want false")
	}

	if cfg.Approvals.HookEnabled() {
		t.Error("HookEnabled() = true, want false when disabled in TOML")
	}
}

// TestLoadConfigInlineApprovalsBareStrings verifies the bare-string array form
// of inline builtin rules decodes and compiles (#737).
func TestLoadConfigInlineApprovalsBareStrings(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[approvals]
backend = "builtin"

[approvals.builtin]
allow = ["echo @*", "ls @*"]
deny = ["rm @arg*"]
allowSafeXargs = true
askNoninteractive = false
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	b := cfg.Approvals.Builtin
	if !b.HasInline() {
		t.Fatal("HasInline() = false, want true")
	}

	if len(b.Allow) != 2 || len(b.Deny) != 1 {
		t.Errorf("allow=%v deny=%v", b.Allow, b.Deny)
	}

	if b.AskNoninteractive == nil || *b.AskNoninteractive {
		t.Errorf("askNoninteractive = %v, want false", b.AskNoninteractive)
	}

	data, err := b.InlineJSON()
	if err != nil {
		t.Fatalf("InlineJSON: %v", err)
	}

	if _, err := localmost.Parse(data); err != nil {
		t.Fatalf("inline rules should compile: %v", err)
	}
}

// TestLoadConfigInlineApprovalsTables verifies the array-of-tables form (with
// per-rule keys like unless) decodes and compiles (#737).
func TestLoadConfigInlineApprovalsTables(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[approvals]
backend = "builtin"

[[approvals.builtin.allow]]
rule = "find @*"
unless = ["-exec", "-delete"]

[[approvals.builtin.deny]]
rule = "rm @arg*"
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	b := cfg.Approvals.Builtin
	if len(b.Allow) != 1 || len(b.Deny) != 1 {
		t.Fatalf("allow=%v deny=%v", b.Allow, b.Deny)
	}

	data, err := b.InlineJSON()
	if err != nil {
		t.Fatalf("InlineJSON: %v", err)
	}

	engine, err := localmost.Parse(data)
	if err != nil {
		t.Fatalf("inline rules should compile: %v", err)
	}

	// The unless clause should exempt find -delete from the allow rule.
	if pol, _ := engine.Evaluate("find . -name x"); pol != localmost.PolicyAllow {
		t.Errorf("find without unless term: policy = %q, want allow", pol)
	}

	if pol, _ := engine.Evaluate("find . -delete"); pol == localmost.PolicyAllow {
		t.Errorf("find -delete should not be allowed (unless clause), got %q", pol)
	}
}

// TestLoadConfigInlineApprovalsConflict verifies the external file + inline
// conflict is rejected at load (#737).
func TestLoadConfigInlineApprovalsConflict(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[approvals]
backend = "builtin"

[approvals.builtin]
config = "~/.config/graith/approvals.json"
allow = ["echo @*"]
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	if _, err := Load(cfgPath); err == nil {
		t.Fatal("Load should reject both config path and inline rules being set")
	}
}

// TestLoadConfigInlineApprovalsUnknownTopKey verifies a misspelled top-level key
// under [approvals.builtin] is rejected at load rather than silently dropped —
// a fail-open guard, since a typo'd "deny" would otherwise leave an allow-all
// base rule in force (#737 hardening).
func TestLoadConfigInlineApprovalsUnknownTopKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[approvals]
backend = "builtin"

[approvals.builtin]
allow = ["@arg @*"]
dney  = ["rm @arg*"]
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	if _, err := Load(cfgPath); err == nil {
		t.Fatal("Load should reject an unknown [approvals.builtin] key (dney)")
	}
}

// TestLoadConfigInlineApprovalsUnknownRuleKey verifies a misspelled per-rule key
// (e.g. "unles" for "unless") is rejected at load. Without this, localmost's
// rule decoder silently drops the unknown JSON field and the intended
// constraint vanishes, broadening the rule (#737 hardening).
func TestLoadConfigInlineApprovalsUnknownRuleKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[approvals]
backend = "builtin"

[[approvals.builtin.allow]]
rule  = "find @*"
unles = ["-delete"]
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o600)

	if _, err := Load(cfgPath); err == nil {
		t.Fatal("Load should reject an unknown per-rule key (unles)")
	}
}

func TestParsePairRequestRate(t *testing.T) {
	tests := []struct {
		in        string
		wantCount int
		wantPer   time.Duration
		wantErr   bool
	}{
		{"5/min", 5, time.Minute, false},
		{"10/sec", 10, time.Second, false},
		{"2/hour", 2, time.Hour, false},
		{"1/minute", 1, time.Minute, false},
		{" 3 / min ", 3, time.Minute, false},
		{"5/wheesht", 0, 0, true},
		{"haar", 0, 0, true},
		{"0/min", 0, 0, true},
		{"-1/min", 0, 0, true},
		{"/min", 0, 0, true},
		{"5/", 0, 0, true},
		{"", 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			r, err := ParsePairRequestRate(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParsePairRequestRate(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}

			if tt.wantErr {
				return
			}

			if r.Count != tt.wantCount || r.Per != tt.wantPer {
				t.Errorf("ParsePairRequestRate(%q) = {%d, %v}, want {%d, %v}", tt.in, r.Count, r.Per, tt.wantCount, tt.wantPer)
			}
		})
	}
}

func TestRemoteConfigValidation(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg := Default()
		r := cfg.Remote

		if r.Enabled {
			t.Error("default remote.enabled should be false")
		}

		if r.Mode != "tsnet" {
			t.Errorf("default remote.mode = %q, want tsnet", r.Mode)
		}

		// The embedded default_config.toml must stay in lockstep with the
		// DefaultRemotePort constant — that constant is the single source of
		// truth the CLI flag and Swift clients also mirror.
		if r.Port != DefaultRemotePort {
			t.Errorf("default remote.port = %d, want DefaultRemotePort (%d)", r.Port, DefaultRemotePort)
		}

		if !r.RequirePairing {
			t.Error("default remote.require_pairing should be true")
		}

		if r.MaxPendingPairings != RemoteMaxPendingPairingsDefault {
			t.Errorf("default remote.max_pending_pairings = %d, want %d", r.MaxPendingPairings, RemoteMaxPendingPairingsDefault)
		}
		if r.PendingPairingTTL != "10m" {
			t.Errorf("default remote.pending_pairing_ttl = %q, want 10m", r.PendingPairingTTL)
		}
		if r.PairFallbackCount != RemotePairFallbackCountDefault {
			t.Errorf("default remote.pair_fallback_count = %d, want %d", r.PairFallbackCount, RemotePairFallbackCountDefault)
		}
		if r.PairFallbackWindow != "1m" {
			t.Errorf("default remote.pair_fallback_window = %q, want 1m", r.PairFallbackWindow)
		}

		if err := r.Validate(); err != nil {
			t.Errorf("default (disabled) remote config should validate: %v", err)
		}
	})

	tests := []struct {
		name    string
		remote  RemoteConfig
		wantErr bool
	}{
		{
			name:    "disabled block with garbage still loads",
			remote:  RemoteConfig{Enabled: false, Mode: "wheesht", AuthKeyFile: "~/ts-authkey"},
			wantErr: false,
		},
		{
			name:    "enabled tsnet defaults ok",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Hostname: "ben", Port: 4823, RequirePairing: true},
			wantErr: false,
		},
		{
			name:    "enabled interface ok",
			remote:  RemoteConfig{Enabled: true, Mode: "interface", Hostname: "brae", Port: 4823},
			wantErr: false,
		},
		{
			name:    "unknown mode rejected",
			remote:  RemoteConfig{Enabled: true, Mode: "wheesht"},
			wantErr: true,
		},
		{
			name:    "interface with auth_key_file rejected",
			remote:  RemoteConfig{Enabled: true, Mode: "interface", AuthKeyFile: "~/.config/graith/ts-authkey"},
			wantErr: true,
		},
		{
			name:    "interface with tags rejected",
			remote:  RemoteConfig{Enabled: true, Mode: "interface", Tags: []string{"tag:graith"}},
			wantErr: true,
		},
		{
			name:    "tsnet with auth_key_file and tags ok",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, AuthKeyFile: "~/.config/graith/ts-authkey", Tags: []string{"tag:graith"}},
			wantErr: false,
		},
		{
			name:    "valid pair_request_rate ok",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, PairRequestRate: "5/min"},
			wantErr: false,
		},
		{
			name:    "garbage pair_request_rate rejected",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", PairRequestRate: "haar"},
			wantErr: true,
		},
		{
			name:    "allow_tailnet_users emails and tag entries ok",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, AllowTailnetUsers: []string{"speir@example.com", "tag:graith"}},
			wantErr: false,
		},
		{
			name:    "in-bounds pairing policy ok",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, MaxPendingPairings: 32, PendingPairingTTL: "5m", PairFallbackCount: 3, PairFallbackWindow: "30s"},
			wantErr: false,
		},
		{
			name:    "zero pairing policy fields use defaults, ok",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, MaxPendingPairings: 0, PairFallbackCount: 0},
			wantErr: false,
		},
		{
			name:    "negative max_pending_pairings rejected",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, MaxPendingPairings: -1},
			wantErr: true,
		},
		{
			name:    "over-ceiling max_pending_pairings rejected",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, MaxPendingPairings: RemoteMaxPendingPairingsMax + 1},
			wantErr: true,
		},
		{
			name:    "unparseable pending_pairing_ttl rejected",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, PendingPairingTTL: "haar"},
			wantErr: true,
		},
		{
			name:    "below-floor pending_pairing_ttl rejected",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, PendingPairingTTL: "1s"},
			wantErr: true,
		},
		{
			name:    "above-ceiling pending_pairing_ttl rejected",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, PendingPairingTTL: "48h"},
			wantErr: true,
		},
		{
			name:    "negative pair_fallback_count rejected",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, PairFallbackCount: -5},
			wantErr: true,
		},
		{
			name:    "over-ceiling pair_fallback_count rejected",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, PairFallbackCount: RemotePairFallbackCountMax + 1},
			wantErr: true,
		},
		{
			name:    "below-floor pair_fallback_window rejected",
			remote:  RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, PairFallbackWindow: "100ms"},
			wantErr: true,
		},
		{
			name:    "disabled block ignores out-of-bounds pairing policy",
			remote:  RemoteConfig{Enabled: false, MaxPendingPairings: -9, PendingPairingTTL: "haar", PairFallbackCount: 99999},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.remote.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRemotePairingPolicyAccessors(t *testing.T) {
	t.Run("max pending defaults and clamps", func(t *testing.T) {
		cases := []struct {
			in, want int
		}{
			{0, RemoteMaxPendingPairingsDefault},
			{-3, RemoteMaxPendingPairingsDefault},
			{32, 32},
			{RemoteMaxPendingPairingsMax + 100, RemoteMaxPendingPairingsMax},
		}
		for _, c := range cases {
			if got := (RemoteConfig{MaxPendingPairings: c.in}).MaxPendingPairingsOrDefault(); got != c.want {
				t.Errorf("MaxPendingPairingsOrDefault(%d) = %d, want %d", c.in, got, c.want)
			}
		}
	})

	t.Run("ttl defaults and clamps", func(t *testing.T) {
		cases := []struct {
			in   string
			want time.Duration
		}{
			{"", RemotePendingPairingTTLDefault},
			{"haar", RemotePendingPairingTTLDefault},
			{"5m", 5 * time.Minute},
			{"1s", RemotePendingPairingTTLMin},
			{"48h", RemotePendingPairingTTLMax},
		}
		for _, c := range cases {
			if got := (RemoteConfig{PendingPairingTTL: c.in}).PendingPairingTTLDuration(); got != c.want {
				t.Errorf("PendingPairingTTLDuration(%q) = %v, want %v", c.in, got, c.want)
			}
		}
	})

	t.Run("fallback rate defaults and clamps", func(t *testing.T) {
		def := (RemoteConfig{}).PairFallbackRate()
		if def.Count != RemotePairFallbackCountDefault || def.Per != RemotePairFallbackWindowDefault {
			t.Errorf("default fallback = %+v, want {%d, %v}", def, RemotePairFallbackCountDefault, RemotePairFallbackWindowDefault)
		}

		set := (RemoteConfig{PairFallbackCount: 3, PairFallbackWindow: "30s"}).PairFallbackRate()
		if set.Count != 3 || set.Per != 30*time.Second {
			t.Errorf("configured fallback = %+v, want {3, 30s}", set)
		}

		clamped := (RemoteConfig{PairFallbackCount: RemotePairFallbackCountMax + 50, PairFallbackWindow: "100ms"}).PairFallbackRate()
		if clamped.Count != RemotePairFallbackCountMax || clamped.Per != RemotePairFallbackWindowMin {
			t.Errorf("clamped fallback = %+v, want {%d, %v}", clamped, RemotePairFallbackCountMax, RemotePairFallbackWindowMin)
		}
	})
}

func TestRemoteConfigAllowsTaggedNodes(t *testing.T) {
	no := RemoteConfig{AllowTailnetUsers: []string{"speir@example.com"}}
	if no.AllowsTaggedNodes() {
		t.Error("no tag: entry should mean tagged nodes are disallowed")
	}

	yes := RemoteConfig{AllowTailnetUsers: []string{"speir@example.com", "tag:graith"}}
	if !yes.AllowsTaggedNodes() {
		t.Error("a tag: entry should opt tagged nodes in")
	}
}

func TestLoadConfigRemote(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	toml := `
[remote]
enabled = true
mode = "tsnet"
hostname = "ben"
port = 4823
auth_key_file = "~/.config/graith/ts-authkey"
tags = ["tag:graith"]
allow_tailnet_users = ["speir@example.com"]
require_pairing = true
pair_request_rate = "5/min"
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !cfg.Remote.Enabled || cfg.Remote.Hostname != "ben" || cfg.Remote.Port != 4823 {
		t.Errorf("remote block did not load: %+v", cfg.Remote)
	}
}

func TestLoadConfigRemoteInvalid(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	toml := `
[remote]
enabled = true
mode = "interface"
auth_key_file = "~/.config/graith/ts-authkey"
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(cfgPath); err == nil {
		t.Fatal("Load should reject a tsnet-only field in interface mode")
	}
}

func TestLoadConfigRemoteDisabledLoads(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	// A disabled block with otherwise-invalid values must still load fine.
	toml := `
[remote]
enabled = false
mode = "interface"
auth_key_file = "~/.config/graith/ts-authkey"
tags = ["tag:graith"]
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(cfgPath); err != nil {
		t.Fatalf("disabled remote block should load: %v", err)
	}
}

func TestRemoteConfigPortValidation(t *testing.T) {
	base := func() RemoteConfig {
		return RemoteConfig{Enabled: true, Mode: "tsnet", Port: 4823, RequirePairing: true}
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("valid port rejected: %v", err)
	}

	bad := base()

	bad.Port = 0
	if err := bad.Validate(); err == nil {
		t.Error("port 0 should be rejected")
	}

	bad = base()

	bad.Port = 70000
	if err := bad.Validate(); err == nil {
		t.Error("port > 65535 should be rejected")
	}

	// A disabled block with a nonsense port still loads (validation is skipped).
	off := base()
	off.Enabled = false

	off.Port = 0
	if err := off.Validate(); err != nil {
		t.Errorf("disabled block should not validate port: %v", err)
	}
}
