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
	_ = os.WriteFile(cfgPath, []byte(toml), 0o644)

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
	_ = os.WriteFile(cfgPath, []byte(toml), 0o644)

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
	_ = os.WriteFile(cfgPath, []byte(toml), 0o644)

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
	_ = os.WriteFile(cfgPath, []byte(toml), 0o644)

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
	_ = os.WriteFile(cfgPath, []byte(toml), 0o644)

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

		repo := filepath.Join(actual, "braw-croft")
		if err := os.Mkdir(repo, 0o755); err != nil {
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
	_ = os.WriteFile(cfgPath, []byte(toml), 0o644)

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

func TestLoadAgentExplicitEmptyArgs(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[agents.claude]
command = "claude"
args = []
`
	_ = os.WriteFile(cfgPath, []byte(toml), 0o644)

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
	_ = os.WriteFile(cfgPath, []byte(toml), 0o644)

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
	_ = os.WriteFile(cfgPath, []byte(toml), 0o644)

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
	_ = os.WriteFile(cfgPath, []byte(toml), 0o644)

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
	_ = os.WriteFile(cfgPath, []byte(toml), 0o644)

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
	_ = os.WriteFile(cfgPath, []byte(toml), 0o644)

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
`), 0o644)

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
`), 0o644)

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
`), 0o644)

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
`), 0o644)

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
