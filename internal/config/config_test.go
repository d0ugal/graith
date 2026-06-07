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
