package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
default_agent = "claude"
github_username = "d0ugal"
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
	os.WriteFile(cfgPath, []byte(toml), 0o644)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude", cfg.DefaultAgent)
	}
	if cfg.GitHubUsername != "d0ugal" {
		t.Errorf("GitHubUsername = %q, want d0ugal", cfg.GitHubUsername)
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

func TestParseDurationWithDays(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"30d", 30 * 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"1d", 24 * time.Hour},
		{"24h", 24 * time.Hour},
		{"30m", 30 * time.Minute},
		{"", 0},
		{"bogus", 0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseDurationWithDays(tt.input)
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
	os.WriteFile(cfgPath, []byte(toml), 0o644)
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
	os.WriteFile(cfgPath, []byte(toml), 0o644)
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
	os.WriteFile(cfgPath, []byte(toml), 0o644)
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
		Enabled:  true,
		Features: []string{"ssh", "process-control"},
		ReadDirs: []string{"~/Code"},
	}
	agent := SandboxConfig{
		Features:  []string{"clipboard"},
		WriteDirs: []string{"~/.claude"},
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
		if cfg.RepoPathAllowed("/tmp/evil-repo") {
			t.Error("path outside allowed dirs should be denied")
		}
	})

	t.Run("prefix trick denied", func(t *testing.T) {
		cfg := &Config{AllowedRepoPaths: []string{"~/Code"}}
		if cfg.RepoPathAllowed(filepath.Join(home, "Code-evil")) {
			t.Error("prefix without separator should be denied")
		}
	})
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
