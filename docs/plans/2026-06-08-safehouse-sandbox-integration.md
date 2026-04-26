# Safehouse Sandbox Integration Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wrap agent processes with safehouse (macOS kernel-level sandbox) so agents can run with `--dangerously-skip-permissions` safely — writes confined to the worktree, reads allowed from `~/Code`, no access to credentials or other sensitive home dirs.

**Architecture:** A new `internal/sandbox` package builds the safehouse command-line wrapper. The daemon calls it in Create/Resume/Fork before spawning the PTY. Config lives in `[sandbox]` (global) and `[agents.X.sandbox]` (per-agent overrides). Sandbox mode is persisted in session state so resume/fork preserves intent. The PTY layer remains unaware of sandboxing.

**Tech Stack:** Go, TOML config (go-toml/v2), macOS sandbox-exec via safehouse CLI

---

## File Map

| File | Action | Responsibility |
|------|--------|---------------|
| `internal/sandbox/sandbox.go` | Create | Pure arg wrapping: `Wrap()` builds safehouse argv, `Available()` checks runtime |
| `internal/sandbox/sandbox_test.go` | Create | Unit tests for Wrap/Available/merge logic |
| `internal/config/config.go` | Modify | Add `SandboxConfig` struct, wire into `Config` and `Agent` |
| `internal/config/config_test.go` | Modify | Add tests for sandbox config loading and merge |
| `internal/daemon/daemon.go` | Modify | Call `sandbox.Wrap()` before `grpty.NewSession` in Create/Resume/Fork |
| `internal/daemon/state.go` | Modify | Add `Sandboxed bool` to `SessionState` |
| `internal/protocol/messages.go` | Modify | Add `Sandbox *bool` to `CreateMsg`, `Sandboxed bool` to `SessionInfo` |
| `internal/cli/new.go` | Modify | Add `--sandbox` / `--no-sandbox` flags |
| `internal/cli/doctor.go` | Modify | Add safehouse health checks |

---

### Task 1: Sandbox Wrapper Package

**Files:**
- Create: `internal/sandbox/sandbox_test.go`
- Create: `internal/sandbox/sandbox.go`

The pure logic for building safehouse argv. No daemon, no PTY, no config — just takes structured input and returns `(command, args)`.

- [ ] **Step 1: Write the test file with all Wrap tests**

```go
// internal/sandbox/sandbox_test.go
package sandbox

import (
	"runtime"
	"testing"
)

func TestWrapBasic(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/home/user/worktree",
		EnvKeys:     []string{"GRAITH_SESSION_ID", "TERM"},
	}
	cmd, args := Wrap("claude", []string{"--session-id", "abc"}, opts)

	if cmd != "safehouse" {
		t.Fatalf("cmd = %q, want safehouse", cmd)
	}

	want := []string{
		"--workdir", "/home/user/worktree",
		"--env-pass", "GRAITH_SESSION_ID,TERM",
		"--", "claude", "--session-id", "abc",
	}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestWrapWithFeatures(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/wt",
		Features:    []string{"ssh", "process-control"},
		EnvKeys:     []string{"TERM"},
	}
	cmd, args := Wrap("codex", []string{}, opts)

	if cmd != "safehouse" {
		t.Fatalf("cmd = %q, want safehouse", cmd)
	}

	found := false
	for i, a := range args {
		if a == "--enable" && i+1 < len(args) {
			if args[i+1] != "ssh,process-control" {
				t.Errorf("--enable value = %q, want %q", args[i+1], "ssh,process-control")
			}
			found = true
			break
		}
	}
	if !found {
		t.Errorf("--enable not found in args: %v", args)
	}
}

func TestWrapWithReadDirs(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/wt",
		ReadDirs:    []string{"/home/user/Code", "/opt/shared"},
		EnvKeys:     []string{"TERM"},
	}
	_, args := Wrap("claude", nil, opts)

	found := false
	for i, a := range args {
		if a == "--add-dirs-ro" && i+1 < len(args) {
			if args[i+1] != "/home/user/Code:/opt/shared" {
				t.Errorf("--add-dirs-ro value = %q, want %q", args[i+1], "/home/user/Code:/opt/shared")
			}
			found = true
			break
		}
	}
	if !found {
		t.Errorf("--add-dirs-ro not found in args: %v", args)
	}
}

func TestWrapWithWriteDirs(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/wt",
		WriteDirs:   []string{"/tmp/extra"},
		EnvKeys:     []string{"TERM"},
	}
	_, args := Wrap("claude", nil, opts)

	found := false
	for i, a := range args {
		if a == "--add-dirs" && i+1 < len(args) {
			if args[i+1] != "/tmp/extra" {
				t.Errorf("--add-dirs value = %q, want %q", args[i+1], "/tmp/extra")
			}
			found = true
			break
		}
	}
	if !found {
		t.Errorf("--add-dirs not found in args: %v", args)
	}
}

func TestWrapNoEnvKeys(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/wt",
	}
	_, args := Wrap("claude", nil, opts)

	for _, a := range args {
		if a == "--env-pass" {
			t.Error("--env-pass should not be present when EnvKeys is empty")
		}
	}
}

func TestWrapCommandAndArgsAfterSeparator(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir: "/tmp/wt",
		EnvKeys:     []string{"TERM"},
	}
	_, args := Wrap("codex", []string{"resume", "--last"}, opts)

	sepIdx := -1
	for i, a := range args {
		if a == "--" {
			sepIdx = i
			break
		}
	}
	if sepIdx == -1 {
		t.Fatal("separator -- not found in args")
	}
	tail := args[sepIdx+1:]
	if len(tail) != 3 || tail[0] != "codex" || tail[1] != "resume" || tail[2] != "--last" {
		t.Errorf("args after -- = %v, want [codex resume --last]", tail)
	}
}

func TestWrapCustomCommand(t *testing.T) {
	opts := WrapOpts{
		WorktreeDir:      "/tmp/wt",
		SafehouseCommand: "/usr/local/bin/safehouse",
		EnvKeys:          []string{"TERM"},
	}
	cmd, _ := Wrap("claude", nil, opts)

	if cmd != "/usr/local/bin/safehouse" {
		t.Fatalf("cmd = %q, want /usr/local/bin/safehouse", cmd)
	}
}

func TestAvailableOnlyOnDarwin(t *testing.T) {
	result := Available()
	if runtime.GOOS != "darwin" && result {
		t.Error("Available() should be false on non-darwin")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/sandbox/ -v -count=1`
Expected: compilation failure — package doesn't exist yet.

- [ ] **Step 3: Write the sandbox package**

```go
// internal/sandbox/sandbox.go
package sandbox

import (
	"os/exec"
	"runtime"
	"strings"
)

type WrapOpts struct {
	WorktreeDir      string
	ReadDirs         []string
	WriteDirs        []string
	Features         []string
	EnvKeys          []string
	SafehouseCommand string
}

func Wrap(command string, args []string, opts WrapOpts) (string, []string) {
	safehouse := opts.SafehouseCommand
	if safehouse == "" {
		safehouse = "safehouse"
	}

	var wrapped []string

	wrapped = append(wrapped, "--workdir", opts.WorktreeDir)

	if len(opts.ReadDirs) > 0 {
		wrapped = append(wrapped, "--add-dirs-ro", strings.Join(opts.ReadDirs, ":"))
	}

	if len(opts.WriteDirs) > 0 {
		wrapped = append(wrapped, "--add-dirs", strings.Join(opts.WriteDirs, ":"))
	}

	if len(opts.Features) > 0 {
		wrapped = append(wrapped, "--enable", strings.Join(opts.Features, ","))
	}

	if len(opts.EnvKeys) > 0 {
		wrapped = append(wrapped, "--env-pass", strings.Join(opts.EnvKeys, ","))
	}

	wrapped = append(wrapped, "--", command)
	wrapped = append(wrapped, args...)

	return safehouse, wrapped
}

func Available() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath("safehouse")
	return err == nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/sandbox/ -v -count=1`
Expected: all PASS.

- [ ] **Step 5: Run gofmt**

Run: `gofmt -w internal/sandbox/`

- [ ] **Step 6: Commit**

```bash
git add internal/sandbox/sandbox.go internal/sandbox/sandbox_test.go
git commit -m "feat: add sandbox package for safehouse command wrapping"
```

---

### Task 2: Config Schema

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

Add `SandboxConfig` struct with global and per-agent settings, plus a `Merge` method that combines global + agent overrides.

- [ ] **Step 1: Write config loading tests**

Append to `internal/config/config_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run TestLoadConfigSandbox -v -count=1`
Expected: compilation error — `SandboxConfig` doesn't exist.

- [ ] **Step 3: Add SandboxConfig to config.go**

Add the struct and wire it into `Config` and `Agent`. In `internal/config/config.go`, add after the `Agent` struct:

```go
type SandboxConfig struct {
	Enabled  bool     `toml:"enabled"`
	Disabled *bool    `toml:"disabled,omitempty"`
	Command  string   `toml:"command"`
	Features []string `toml:"features"`
	ReadDirs []string `toml:"read_dirs"`
	WriteDirs []string `toml:"write_dirs"`
}

func (s SandboxConfig) Merge(agent SandboxConfig) SandboxConfig {
	merged := SandboxConfig{
		Enabled: s.Enabled,
		Command: s.Command,
	}

	if agent.Disabled != nil && *agent.Disabled {
		merged.Enabled = false
		return merged
	}

	merged.Features = dedup(append(s.Features, agent.Features...))
	merged.ReadDirs = dedup(append(s.ReadDirs, agent.ReadDirs...))
	merged.WriteDirs = dedup(append(s.WriteDirs, agent.WriteDirs...))

	if agent.Command != "" {
		merged.Command = agent.Command
	}

	return merged
}

func dedup(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
```

Add the `Sandbox` field to the `Config` struct:

```go
type Config struct {
	// ... existing fields ...
	Sandbox        SandboxConfig    `toml:"sandbox"`
	Agents         map[string]Agent `toml:"agents"`
}
```

Add the `Sandbox` field to the `Agent` struct:

```go
type Agent struct {
	// ... existing fields ...
	Sandbox     SandboxConfig     `toml:"sandbox"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v -count=1`
Expected: all PASS (both old and new tests).

- [ ] **Step 5: Run gofmt**

Run: `gofmt -w internal/config/config.go`

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add SandboxConfig to config schema with merge semantics"
```

---

### Task 3: Session State and Protocol

**Files:**
- Modify: `internal/daemon/state.go`
- Modify: `internal/protocol/messages.go`
- Modify: `internal/cli/new.go`

Add `Sandboxed` to session state (persisted across restarts), `Sandbox` to `CreateMsg` (so CLI can override), and `Sandboxed` to `SessionInfo` (so list/status shows it). Add CLI flags.

- [ ] **Step 1: Add Sandboxed field to SessionState**

In `internal/daemon/state.go`, add to `SessionState`:

```go
type SessionState struct {
	// ... existing fields ...
	Sandboxed      bool          `json:"sandboxed,omitempty"`
}
```

Add it after the `PID` field.

- [ ] **Step 2: Add Sandbox to CreateMsg and Sandboxed to SessionInfo**

In `internal/protocol/messages.go`, add to `CreateMsg`:

```go
type CreateMsg struct {
	Name     string `json:"name"`
	Agent    string `json:"agent"`
	RepoPath string `json:"repo_path"`
	Base     string `json:"base,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	NoRepo   bool   `json:"no_repo,omitempty"`
	Sandbox  *bool  `json:"sandbox,omitempty"`
}
```

Add `Sandboxed` to `SessionInfo`:

```go
type SessionInfo struct {
	// ... existing fields ...
	Sandboxed      bool   `json:"sandboxed,omitempty"`
}
```

- [ ] **Step 3: Update toSessionInfo in handler.go**

In `internal/daemon/handler.go`, in `toSessionInfo()`, add:

```go
info.Sandboxed = s.Sandboxed
```

- [ ] **Step 4: Add CLI flags to new.go**

In `internal/cli/new.go`, add variables and flags:

```go
var (
	// ... existing vars ...
	newSandbox   bool
	newNoSandbox bool
)
```

In the `init()` function, add:

```go
newCmd.Flags().BoolVar(&newSandbox, "sandbox", false, "run agent inside safehouse sandbox")
newCmd.Flags().BoolVar(&newNoSandbox, "no-sandbox", false, "disable safehouse sandbox for this session")
```

In the `RunE` function, before `c.SendControl("create", ...)`, build the sandbox override:

```go
var sandboxOverride *bool
if newSandbox {
	t := true
	sandboxOverride = &t
}
if newNoSandbox {
	f := false
	sandboxOverride = &f
}
```

Update the `SendControl` call to include the new field:

```go
c.SendControl("create", protocol.CreateMsg{
	Name:     name,
	Agent:    agent,
	RepoPath: repoPath,
	Base:     newBase,
	Prompt:   prompt,
	NoRepo:   newNoRepo,
	Sandbox:  sandboxOverride,
})
```

- [ ] **Step 5: Run full test suite**

Run: `go test ./... -count=1`
Expected: all PASS. These are additive changes — no behavior changed yet.

- [ ] **Step 6: Run gofmt**

Run: `gofmt -w internal/daemon/state.go internal/protocol/messages.go internal/cli/new.go internal/daemon/handler.go`

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/state.go internal/protocol/messages.go internal/cli/new.go internal/daemon/handler.go
git commit -m "feat: add sandbox fields to state, protocol, and CLI"
```

---

### Task 4: Wire Sandbox into Daemon Launch

**Files:**
- Modify: `internal/daemon/daemon.go`
- Modify: `internal/daemon/handler.go`

This is the core integration: resolve sandbox config, decide whether to wrap, call `sandbox.Wrap()` to transform command/args before `grpty.NewSession`.

- [ ] **Step 1: Add resolveSandbox helper to daemon.go**

Add this method to `SessionManager` in `internal/daemon/daemon.go`:

```go
func (sm *SessionManager) resolveSandbox(agentName string, override *bool) bool {
	merged := sm.cfg.Sandbox.Merge(sm.cfg.Agents[agentName].Sandbox)
	if override != nil {
		return *override
	}
	if !merged.Enabled {
		return false
	}
	return sandbox.Available()
}

func (sm *SessionManager) sandboxOpts(agentName, worktreePath string, envKeys []string) sandbox.WrapOpts {
	merged := sm.cfg.Sandbox.Merge(sm.cfg.Agents[agentName].Sandbox)
	return sandbox.WrapOpts{
		WorktreeDir:      worktreePath,
		ReadDirs:         merged.ReadDirs,
		WriteDirs:        merged.WriteDirs,
		Features:         merged.Features,
		EnvKeys:          envKeys,
		SafehouseCommand: merged.Command,
	}
}
```

Add the import for the sandbox package at the top of `daemon.go`:

```go
"github.com/d0ugal/graith/internal/sandbox"
```

- [ ] **Step 2: Wire into Create**

In `internal/daemon/daemon.go`, in the `Create` method, after building the env map and before calling `grpty.NewSession`, add the sandbox wrapping. Find the line:

```go
ptySess, err := grpty.NewSession(grpty.SessionOpts{
```

Insert before it:

```go
	sandboxed := sm.resolveSandbox(agentName, nil)
	command := agent.Command
	finalArgs := expandedArgs
	if sandboxed {
		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}
		opts := sm.sandboxOpts(agentName, worktreePath, envKeys)
		command, finalArgs = sandbox.Wrap(agent.Command, expandedArgs, opts)
		sm.log.Info("sandboxing session", "id", id, "agent", agentName)
	}
```

Then update the `grpty.NewSession` call to use `command` and `finalArgs`:

```go
	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID:         id,
		Command:    command,
		Args:       finalArgs,
		// ... rest unchanged ...
	})
```

And set `Sandboxed` on the session state:

```go
	sessState := &SessionState{
		// ... existing fields ...
		Sandboxed:      sandboxed,
	}
```

- [ ] **Step 3: Pass sandbox override from handler to Create**

The `Create` method needs to accept the sandbox override from `CreateMsg`. Update the `Create` method signature:

```go
func (sm *SessionManager) Create(name, agentName, repoPath, baseBranch, prompt string, noRepo bool, sandboxOverride *bool, rows, cols uint16) (SessionState, error) {
```

Update the `resolveSandbox` call inside `Create`:

```go
	sandboxed := sm.resolveSandbox(agentName, sandboxOverride)
```

In `internal/daemon/handler.go`, update the `Create` call at line ~92:

```go
sess, err := sm.Create(c.Name, agentName, c.RepoPath, c.Base, c.Prompt, c.NoRepo, c.Sandbox, clientRows, clientCols)
```

- [ ] **Step 4: Wire into Resume**

In `internal/daemon/daemon.go`, in the `Resume` method, after building the env map and before calling `grpty.NewSession`, add:

```go
	command := agent.Command
	finalArgs := expandedArgs
	if sessState.Sandboxed {
		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}
		opts := sm.sandboxOpts(sessState.Agent, sessState.WorktreePath, envKeys)
		command, finalArgs = sandbox.Wrap(agent.Command, expandedArgs, opts)
		sm.log.Info("sandboxing resumed session", "id", id)
	}
```

Update the `grpty.NewSession` call:

```go
	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID:         id,
		Command:    command,
		Args:       finalArgs,
		// ... rest unchanged ...
	})
```

- [ ] **Step 5: Wire into Fork**

In `internal/daemon/daemon.go`, in the `Fork` method, after building the env map and before calling `grpty.NewSession`, add:

```go
	sandboxed := source.Sandboxed
	command := agent.Command
	finalArgs := expandedArgs
	if sandboxed {
		envKeys := []string{"GRAITH_SESSION_ID", "GRAITH_SESSION_NAME", "GRAITH_WORKTREE_PATH", "TERM"}
		for k := range agent.Env {
			envKeys = append(envKeys, k)
		}
		opts := sm.sandboxOpts(agentName, worktreePath, envKeys)
		command, finalArgs = sandbox.Wrap(agent.Command, expandedArgs, opts)
		sm.log.Info("sandboxing forked session", "id", id)
	}
```

Update the `grpty.NewSession` call to use `command` and `finalArgs`, and set `Sandboxed` on the session state:

```go
	sessState := &SessionState{
		// ... existing fields ...
		Sandboxed:      sandboxed,
	}
```

- [ ] **Step 6: Build to verify compilation**

Run: `go build ./cmd/graith`
Expected: builds successfully.

- [ ] **Step 7: Run full test suite**

Run: `go test ./... -count=1`
Expected: all PASS. The daemon_test.go tests don't spawn real sessions, so they're unaffected.

- [ ] **Step 8: Run gofmt**

Run: `gofmt -w internal/daemon/daemon.go internal/daemon/handler.go`

- [ ] **Step 9: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/handler.go
git commit -m "feat: wire safehouse sandbox into Create, Resume, and Fork"
```

---

### Task 5: Doctor Checks

**Files:**
- Modify: `internal/cli/doctor.go`

Add safehouse diagnostics: is it installed, what version, is it macOS, show effective config.

- [ ] **Step 1: Add safehouse checks to doctor.go**

In `internal/cli/doctor.go`, inside the `RunE` function, after the update check block and before the final "All checks passed" message, add:

```go
		// Safehouse sandbox checks
		if cfg.Sandbox.Enabled {
			if runtime.GOOS != "darwin" {
				out.Print("  ✗ Sandbox enabled but not running macOS (safehouse requires macOS)\n")
				ok = false
			} else if !sandbox.Available() {
				out.Print("  ✗ Sandbox enabled but safehouse not found in PATH\n")
				out.Print("    → Install: brew install eugene1g/tools/agent-safehouse\n")
				ok = false
			} else {
				out.Print("  ✓ Sandbox enabled (safehouse available)\n")
			}
		} else {
			out.Print("  ○ Sandbox disabled\n")
		}
```

Add the imports at the top of doctor.go:

```go
	"runtime"

	"github.com/d0ugal/graith/internal/sandbox"
```

- [ ] **Step 2: Build to verify**

Run: `go build ./cmd/graith`
Expected: builds successfully.

- [ ] **Step 3: Run gofmt**

Run: `gofmt -w internal/cli/doctor.go`

- [ ] **Step 4: Commit**

```bash
git add internal/cli/doctor.go
git commit -m "feat: add safehouse checks to gr doctor"
```

---

### Task 6: Manual Verification

No new files — verify the integration works end-to-end by building and running.

- [ ] **Step 1: Build the binary**

Run: `go build -o ./graith ./cmd/graith`

- [ ] **Step 2: Run doctor to verify safehouse detection**

Run: `./graith doctor`
Expected: shows "Sandbox disabled" (since default config has it off).

- [ ] **Step 3: Run the full test suite with race detector**

Run: `go test -race ./... -count=1`
Expected: all PASS, no races.

- [ ] **Step 4: Run gofmt check**

Run: `gofmt -l ./...`
Expected: no output (all files formatted).

- [ ] **Step 5: Run go vet**

Run: `go vet ./...`
Expected: no issues.

- [ ] **Step 6: Commit any remaining fixes**

If any fixes were needed, commit them now.
