package config

import (
	"os"
	"path/filepath"
	"testing"
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
