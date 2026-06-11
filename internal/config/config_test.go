package config

import (
	"os"
	"path/filepath"
	"reflect"
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
		if err := os.Mkdir(target, 0o755); err != nil {
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
		if err := os.Mkdir(outsideRepo, 0o755); err != nil {
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
		repo := filepath.Join(actual, "myrepo")
		if err := os.Mkdir(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "link-to-actual")
		if err := os.Symlink(actual, link); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}
		cfg := &Config{AllowedRepoPaths: []string{link}}
		if !cfg.RepoPathAllowed(filepath.Join(actual, "myrepo")) {
			t.Error("repo under resolved allowed symlink should be permitted")
		}
	})
}

func TestLoadPartialAgentPreservesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[agents.claude]
command = "my-claude"
`
	os.WriteFile(cfgPath, []byte(toml), 0o644)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	claude := cfg.Agents["claude"]
	if claude.Command != "my-claude" {
		t.Errorf("claude.Command = %q, want my-claude", claude.Command)
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

func TestLoadAgentExplicitEmptyArgs(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[agents.claude]
command = "claude"
args = []
`
	os.WriteFile(cfgPath, []byte(toml), 0o644)
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
	os.WriteFile(cfgPath, []byte(toml), 0o644)
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
	os.WriteFile(cfgPath, []byte(toml), 0o644)
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
[agents.custom]
command = "my-agent"
args = ["--flag"]
`
	os.WriteFile(cfgPath, []byte(toml), 0o644)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	custom, ok := cfg.Agents["custom"]
	if !ok {
		t.Fatal("custom agent not found")
	}
	if custom.Command != "my-agent" {
		t.Errorf("custom.Command = %q, want my-agent", custom.Command)
	}
	if len(custom.Args) != 1 || custom.Args[0] != "--flag" {
		t.Errorf("custom.Args = %v, want [--flag]", custom.Args)
	}

	if _, ok := cfg.Agents["claude"]; !ok {
		t.Error("default claude agent lost when adding custom agent")
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
		usr := Agent{Command: "my-claude"}
		got := mergeAgent(def, usr)
		if got.Command != "my-claude" {
			t.Errorf("Command = %q, want my-claude", got.Command)
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
		usr := Agent{Env: map[string]string{"FOO": "bar"}}
		got := mergeAgent(def, usr)
		if got.Env["FOO"] != "bar" {
			t.Errorf("Env = %v, want FOO=bar", got.Env)
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
}

func TestLoadConfigRepos(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[[repos]]
path = "~/Code/devenv"

[[repos]]
path = "~/Code/scripts"
allow_concurrent = true
`
	os.WriteFile(cfgPath, []byte(toml), 0o644)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 2 {
		t.Fatalf("Repos = %d entries, want 2", len(cfg.Repos))
	}
	if cfg.Repos[0].Path != "~/Code/devenv" {
		t.Errorf("Repos[0].Path = %q, want ~/Code/devenv", cfg.Repos[0].Path)
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
		cfg := &Config{Repos: []RepoConfig{{Path: "~/Code/devenv"}}}
		rc, ok := cfg.FindRepo(filepath.Join(home, "Code", "devenv"))
		if !ok {
			t.Fatal("expected to find repo")
		}
		if rc.Path != "~/Code/devenv" {
			t.Errorf("Path = %q, want ~/Code/devenv", rc.Path)
		}
	})

	t.Run("no match", func(t *testing.T) {
		cfg := &Config{Repos: []RepoConfig{{Path: "~/Code/devenv"}}}
		_, ok := cfg.FindRepo("/tmp/random")
		if ok {
			t.Error("expected no match for /tmp/random")
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
	a.Agents["claude"] = Agent{Command: "mutated"}
	b := Default()
	if b.Agents["claude"].Command != "claude" {
		t.Error("mutating Default() result affected subsequent Default() calls")
	}
}

func TestValidate(t *testing.T) {
	t.Run("no includes is valid", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/foo"}
		if err := rc.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("self-include rejected", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/foo", Includes: []string{"~/Code/foo"}}
		if err := rc.Validate(); err == nil {
			t.Error("expected error for self-include")
		}
	})

	t.Run("duplicate basename rejected", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/foo", Includes: []string{"~/Code/bar", "~/work/bar"}}
		if err := rc.Validate(); err == nil {
			t.Error("expected error for duplicate basename")
		}
	})

	t.Run("main repo basename collision rejected", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/bar", Includes: []string{"~/work/bar"}}
		if err := rc.Validate(); err == nil {
			t.Error("expected error for main/include basename collision")
		}
	})

	t.Run("env var collision rejected", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/main", Includes: []string{"~/Code/foo-bar", "~/Code/foo.bar"}}
		if err := rc.Validate(); err == nil {
			t.Error("expected error for env var name collision")
		}
	})

	t.Run("singleton plus allow_concurrent rejected with includes", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/foo", Singleton: true, AllowConcurrent: true, Includes: []string{"~/Code/bar"}}
		if err := rc.Validate(); err == nil {
			t.Error("expected error for singleton + allow_concurrent")
		}
	})

	t.Run("singleton plus allow_concurrent rejected without includes", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/foo", Singleton: true, AllowConcurrent: true}
		if err := rc.Validate(); err == nil {
			t.Error("expected error for singleton + allow_concurrent without includes")
		}
	})

	t.Run("valid includes pass", func(t *testing.T) {
		rc := RepoConfig{Path: "~/Code/dem-dev", Includes: []string{"~/Code/grafana", "~/Code/session-replay-examples"}}
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
		{"grafana", "GRAITH_INCLUDE_GRAFANA_PATH"},
		{"session-replay-examples", "GRAITH_INCLUDE_SESSION_REPLAY_EXAMPLES_PATH"},
		{"faro.web.sdk", "GRAITH_INCLUDE_FARO_WEB_SDK_PATH"},
		{"my repo", "GRAITH_INCLUDE_MYREPO_PATH"},
		{"my@repo!", "GRAITH_INCLUDE_MYREPO_PATH"},
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
