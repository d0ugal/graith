# Externalize Config Defaults Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move hardcoded Go struct defaults into an embedded TOML file and add `gr config reset/diff/show` CLI commands.

**Architecture:** An embedded `default_config.toml` replaces Go struct construction in `Default()`. Three new CLI subcommands under `gr config` let users inspect, diff, and reset their config. The `config` parent command overrides root `PersistentPreRunE` to skip config loading so commands work with malformed configs.

**Tech Stack:** Go, `go:embed`, `go-toml/v2`, `go-difflib` (new dep for unified diff), Cobra

---

### Task 1: Create the embedded default config TOML

**Files:**
- Create: `internal/config/default_config.toml`

- [ ] **Step 1: Create the TOML file**

Write `internal/config/default_config.toml` with all current defaults from `Default()`, organized by section with comments:

```toml
# graith default configuration
#
# This file defines the built-in defaults. To create your own config:
#   gr config reset
#
# To see what you've changed from defaults:
#   gr config diff
#
# To see the effective merged config:
#   gr config show
#
# Template variables available in agent args:
#   {username}                      - GitHub username or "user"
#   {agent_session_id}              - UUID for the agent session
#   {session_name}                  - Human-readable session name
#   {session_id}                    - Internal graith session ID
#   {worktree_path}                 - Absolute path to the session worktree
#   {fork_source_agent_session_id}  - Source session's agent ID (fork only)

default_agent = "claude"
branch_prefix = "{username}/graith"
fetch_on_create = true

[status_bar]
enabled = true
position = "bottom"

[notifications]
enabled = true
on_approval = true

[approvals]
mode = "prompt"
timeout = "10m"

[keybindings]
prefix = "ctrl+b"
new_session = "c"
fork_session = "f"
delete_session = "x"
detach = "d"
session_list = "w"
next_session = "n"
prev_session = "p"
last_session = "l"
resume_session = "R"
rename_session = ","
search = "/"
scroll_mode = "["
shell = "s"

# Agent definitions
# Each agent needs at minimum: command, args
# Optional: resume_args, fork_args, env, idle_timeout, sandbox

[agents.claude]
command = "claude"
args = ["--session-id", "{agent_session_id}"]
resume_args = ["--resume", "{agent_session_id}"]
fork_args = ["--resume", "{fork_source_agent_session_id}", "--fork-session", "--session-id", "{agent_session_id}"]

[agents.codex]
command = "codex"
args = []
resume_args = ["resume", "--last"]
fork_args = ["fork", "{fork_source_agent_session_id}"]

[agents.opencode]
command = "opencode"
args = []
resume_args = ["--session", "{agent_session_id}"]

[agents.agy]
command = "agy"
args = []
resume_args = ["--conversation", "{agent_session_id}"]
```

- [ ] **Step 2: Commit**

```bash
git add internal/config/default_config.toml
git commit -m "feat: add embedded default config TOML"
```

### Task 2: Replace Default() with embedded TOML parsing

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Write test for Default() parsing the embedded TOML**

Add to `internal/config/config_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it passes with current Default()**

Run: `go test ./internal/config/ -run TestDefaultParsesEmbeddedTOML -v`
Expected: PASS (the test validates the contract, not the implementation)

- [ ] **Step 3: Write test for DefaultTOML() defensive copy**

Add to `internal/config/config_test.go`:

```go
func TestDefaultTOMLDefensiveCopy(t *testing.T) {
	a := DefaultTOML()
	b := DefaultTOML()
	a[0] = 0xFF
	if b[0] == 0xFF {
		t.Error("DefaultTOML() returns shared backing array, want independent copies")
	}
}
```

- [ ] **Step 4: Write test for Default() mutation safety**

Add to `internal/config/config_test.go`:

```go
func TestDefaultMutationSafety(t *testing.T) {
	a := Default()
	a.Agents["claude"] = Agent{Command: "mutated"}
	b := Default()
	if b.Agents["claude"].Command != "claude" {
		t.Error("mutating Default() result affected subsequent Default() calls")
	}
}
```

- [ ] **Step 5: Replace Default() with embedded TOML, add DefaultTOML()**

In `internal/config/config.go`, add the embed import and directive, replace the `Default()` function body, and add `DefaultTOML()`:

```go
import (
	_ "embed"
	// ... existing imports
)

//go:embed default_config.toml
var defaultConfigTOML []byte

func Default() *Config {
	cfg := &Config{}
	if err := toml.Unmarshal(defaultConfigTOML, cfg); err != nil {
		panic("invalid embedded default config: " + err.Error())
	}
	return cfg
}

func DefaultTOML() []byte {
	out := make([]byte, len(defaultConfigTOML))
	copy(out, defaultConfigTOML)
	return out
}
```

Remove the old `Default()` function body that constructed the struct manually.

- [ ] **Step 6: Run all tests**

Run: `go test ./internal/config/ -v`
Expected: All PASS

- [ ] **Step 7: Run full test suite**

Run: `go test ./...`
Expected: All PASS

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: replace Default() with embedded TOML parsing"
```

### Task 3: Add unified diff dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add go-difflib**

Run: `go get github.com/pmezard/go-difflib@latest`

- [ ] **Step 2: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add go-difflib dependency for config diff"
```

### Task 4: Create gr config CLI commands

**Files:**
- Create: `internal/cli/config.go`

- [ ] **Step 1: Create the config parent command and subcommands**

Create `internal/cli/config.go` with:

```go
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/d0ugal/graith/internal/config"
)

var configForceReset bool

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage graith configuration",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		var err error
		paths, err = config.ResolvePaths()
		if err != nil {
			return err
		}
		return nil
	},
}

var configResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Write built-in defaults to config file",
	RunE: func(cmd *cobra.Command, args []string) error {
		target := cfgFile
		if target == "" {
			target = paths.ConfigFile
		}

		if _, err := os.Stat(target); err == nil {
			if !configForceReset {
				if !term.IsTerminal(int(os.Stdin.Fd())) {
					return fmt.Errorf("config file exists at %s; use --force to overwrite in non-interactive mode", target)
				}
				fmt.Fprintf(os.Stderr, "This will overwrite your config at %s. Continue? [y/N] ", target)
				var answer string
				fmt.Scanln(&answer)
				if answer != "y" && answer != "Y" {
					fmt.Fprintln(os.Stderr, "Aborted.")
					return nil
				}
			}
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}

		tmp := target + ".tmp"
		if err := os.WriteFile(tmp, config.DefaultTOML(), 0o600); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		if err := os.Rename(tmp, target); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("rename config: %w", err)
		}

		fmt.Fprintf(os.Stderr, "Wrote default config to %s\n", target)
		return nil
	},
}

var configDiffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Show changes from built-in defaults",
	RunE: func(cmd *cobra.Command, args []string) error {
		target := cfgFile
		if target == "" {
			target = paths.ConfigFile
		}

		if _, err := os.Stat(target); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "No config file found at %s. Using built-in defaults.\n", target)
			return nil
		}

		userCfg, err := config.Load(target)
		if err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
		defaultCfg := config.Default()

		defaultBytes, err := toml.Marshal(defaultCfg)
		if err != nil {
			return fmt.Errorf("marshal defaults: %w", err)
		}
		userBytes, err := toml.Marshal(userCfg)
		if err != nil {
			return fmt.Errorf("marshal user config: %w", err)
		}

		diff := difflib.UnifiedDiff{
			A:        difflib.SplitLines(string(defaultBytes)),
			B:        difflib.SplitLines(string(userBytes)),
			FromFile: "defaults",
			ToFile:   target,
			Context:  3,
		}
		text, err := difflib.GetUnifiedDiffString(diff)
		if err != nil {
			return fmt.Errorf("compute diff: %w", err)
		}
		if text == "" {
			return nil
		}
		fmt.Print(text)
		return nil
	},
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the effective (merged) configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		target := cfgFile
		if target == "" {
			target = paths.ConfigFile
		}

		effectiveCfg, err := config.LoadOrDefault(target)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		data, err := toml.Marshal(effectiveCfg)
		if err != nil {
			return fmt.Errorf("marshal config: %w", err)
		}
		fmt.Print(string(data))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configResetCmd)
	configCmd.AddCommand(configDiffCmd)
	configCmd.AddCommand(configShowCmd)
	configResetCmd.Flags().BoolVar(&configForceReset, "force", false, "overwrite without confirmation")
}
```

- [ ] **Step 2: Build and verify**

Run: `go build ./cmd/graith`
Expected: Builds successfully

- [ ] **Step 3: Commit**

```bash
git add internal/cli/config.go
git commit -m "feat: add gr config reset/diff/show commands"
```

### Task 5: Update gr doctor output

**Files:**
- Modify: `internal/cli/doctor.go`

- [ ] **Step 1: Update the no-config message in doctor**

Change the no-config output line to mention `gr config reset`:

```go
// Change from:
out.Print("  ○ No config file (using defaults): %s\n", paths.ConfigFile)
// To:
out.Print("  ○ No config file (using defaults): %s\n", paths.ConfigFile)
out.Print("    → Run: gr config reset (to create one)\n")
```

- [ ] **Step 2: Run build**

Run: `go build ./cmd/graith`
Expected: Builds successfully

- [ ] **Step 3: Commit**

```bash
git add internal/cli/doctor.go
git commit -m "feat: update doctor to suggest gr config reset"
```

### Task 6: Run full test suite and gofmt

- [ ] **Step 1: gofmt**

Run: `gofmt -w ./internal/config/ ./internal/cli/`

- [ ] **Step 2: go vet**

Run: `go vet ./...`
Expected: No issues

- [ ] **Step 3: Full test suite**

Run: `go test -race ./...`
Expected: All PASS

- [ ] **Step 4: Commit if needed**

```bash
git add -A
git commit -m "chore: gofmt and vet fixes"
```
