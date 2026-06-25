---
authors: Dougal Matthews
created: 2026-06-18
status: Draft
reviewers: TBD
informed: TBD
---

# Configurable Orchestrator Sandbox Access

## Background

The graith orchestrator runs as a sandboxed agent session that coordinates other agent sessions. Sandboxing is mandatory for the orchestrator — it cannot be disabled. The sandbox configuration is derived by merging the global `[sandbox]` config with the agent-specific `[agents.claude.sandbox]` config, plus a few hardcoded extra write directories added during orchestrator creation (scratch dir, tmp dir, shared store).

The existing sandbox merge pattern (`SandboxConfig.Merge()`) already supports layered overrides: global config provides baseline dirs, agent config adds agent-specific dirs, and the merge deduplicates and combines them.

The orchestrator's effective sandbox is computed at four sites today: `createOrchestrator()`, `Resume()` (for orchestrator sessions), `CreationCfg` persistence, and `isConfigStale()` comparison. All four currently use only the two-layer `global.Merge(agent)` merge.

## Problem

There is no way to grant the orchestrator additional filesystem access beyond what the global and agent sandbox configs provide. The orchestrator's extra write access is hardcoded to three directories:

1. `tmpDir` — orchestrator temporary directory
2. `SharedStorePath` — the shared document store
3. `scratchDir` — the orchestrator's working directory

If a user wants the orchestrator to manage graith configuration (e.g. update `~/.config/graith/config.toml`), read documentation from specific paths, or access other resources outside its sandbox, there is no config-driven way to do so. The only current option is to add the paths to the global or agent-level sandbox config, which then applies to all sessions — not just the orchestrator.

## Goals

- Allow users to grant the orchestrator additional read/write filesystem access via config
- Apply consistently across all orchestrator lifecycle paths (create, resume, stale detection)
- Keep the change minimal and backward-compatible

### Non-Goals

- Changing the requirement that the orchestrator must be sandboxed
- Allowing the orchestrator to override the sandbox command, enable/disable sandboxing, or add sandbox features (e.g. network access) — these are structurally excluded by the config type
- Adding a UI or CLI command to modify orchestrator sandbox config at runtime

## Proposals

### Proposal 0: Do Nothing

Leave the orchestrator sandbox access as-is. Users who need the orchestrator to access additional paths must add them to the global `[sandbox]` config, which grants that access to all agent sessions.

**Pros:**
- No code changes required

**Cons:**
- Users cannot scope extra access to just the orchestrator — it's all-or-nothing via global config
- Violates principle of least privilege: granting the orchestrator config-write access means every agent gets it too

### Proposal 1: Add `[orchestrator.sandbox]` with a narrow config type (Recommended)

Add a dedicated `OrchestratorSandboxConfig` struct with only `ReadDirs` and `WriteDirs` fields to `OrchestratorConfig`. During orchestrator sandbox resolution, append these dirs to the merged global + agent config. Centralize this in a shared helper used by all lifecycle paths.

Using a narrow type (not the full `SandboxConfig`) structurally prevents users from setting `disabled`, `enabled`, `command`, or `features` at the orchestrator level — eliminating footguns without needing runtime validation.

#### Config example

```toml
[orchestrator]
enabled = true
agent = "claude"

[orchestrator.sandbox]
write_dirs = ["~/.config/graith"]
read_dirs = ["~/Documents/project-notes"]
```

Only `read_dirs` and `write_dirs` are accepted. Unrecognized TOML keys like `enabled`, `disabled`, `command`, or `features` under `[orchestrator.sandbox]` are silently ignored by the TOML parser (`pelletier/go-toml/v2` with default unmarshal settings), consistent with how all other config sections behave.

#### Implementation

**New type in `internal/config/config.go`:**

```go
type OrchestratorSandboxConfig struct {
    ReadDirs  []string `toml:"read_dirs"`
    WriteDirs []string `toml:"write_dirs"`
}
```

Added to `OrchestratorConfig`:

```go
type OrchestratorConfig struct {
    Enabled     bool                     `toml:"enabled"`
    Agent       string                   `toml:"agent"`
    Model       string                   `toml:"model"`
    IdleTimeout string                   `toml:"idle_timeout"`
    Prompt      string                   `toml:"prompt"`
    PromptFile  string                   `toml:"prompt_file"`
    Sandbox     OrchestratorSandboxConfig `toml:"sandbox"`
}
```

**Shared helper as a method on `*Config` in `internal/config/config.go`:**

```go
func (c *Config) OrchestratorSandboxMerged(agentName string) SandboxConfig {
    merged := c.Sandbox.Merge(c.Agents[agentName].Sandbox)
    orch := c.Orchestrator.Sandbox
    merged.ReadDirs = dedup(append(merged.ReadDirs, orch.ReadDirs...))
    merged.WriteDirs = dedup(append(merged.WriteDirs, orch.WriteDirs...))
    return merged
}
```

This method lives in the `config` package alongside `dedup()` (which is unexported) and `SandboxConfig.Merge()`. It is accessible from all call sites including the package-level `isConfigStale()` function in `handler.go`, which receives `*config.Config` but has no `SessionManager` receiver.

This helper is used in all four sites:

1. **`createOrchestrator()`** (`orchestrator.go:108`) — replace `sm.cfg.Sandbox.Merge(agent.Sandbox)` with `cfgSnapshot.OrchestratorSandboxMerged(agentName)`. Store the result as `sandboxMerged` and reuse it for both `sandbox.Wrap()` and `CreationCfg.SandboxConfig` to ensure they match the same config snapshot.
2. **`Resume()`** (`daemon.go:1412`) — when `sessState.SystemKind == SystemKindOrchestrator`, use `cfgSnapshot.OrchestratorSandboxMerged(sessState.Agent)` instead of the two-layer merge. Use the same config snapshot for both wrapping and `CreationCfg`.
3. **`CreationCfg`** (`orchestrator.go:200`, `daemon.go:1675`) — store the `sandboxMerged` value computed in steps 1/2 (from the config snapshot), not a re-computation against live `sm.cfg`. This prevents a config reload during creation from causing the persisted `CreationCfg` to diverge from the actual launch config.
4. **`isConfigStale()`** (`handler.go:925`) — when `s.SystemKind == SystemKindOrchestrator`, compare against `cfg.OrchestratorSandboxMerged(s.Agent)` instead of two-layer merge. Non-orchestrator sessions continue using `cfg.Sandbox.Merge(agent.Sandbox)`.

**Config change detection in `daemon.go`:**

The existing config reload handler (`daemon.go:2788-2801`) only checks `Orchestrator.Enabled` changes. Extend it to also compare `Orchestrator.Sandbox` and trigger an orchestrator restart when dirs change.

Comparison should use `reflect.DeepEqual` (slices are not comparable with `==`). Compare the full effective orchestrator sandbox: `old.OrchestratorSandboxMerged(old.Orchestrator.AgentName())` vs `newCfg.OrchestratorSandboxMerged(newCfg.Orchestrator.AgentName())`. This catches changes from any layer (global, agent, or orchestrator) that affect the orchestrator's effective sandbox.

**Restart mechanism:** When the effective sandbox changes and the orchestrator is currently running, call `go sm.Restart(orchID, 24, 80)`. If the orchestrator is stopped, errored, or absent, do nothing — the next start (manual or via `ensureOrchestrator`) will pick up the new config. Do NOT use `stopWithReason(orchID, StopReasonUser)` — that prevents the supervisor from auto-restarting. `Restart()` directly kills the PTY and resumes with fresh config, which is the correct primitive for an intentional config-driven restart.

**Default config (`default_config.toml`):**

Add commented-out example under `[orchestrator]`:

```toml
# [orchestrator.sandbox]
# read_dirs = []
# write_dirs = ["~/.config/graith"]
```

#### Security considerations

**Mandatory sandbox invariant is preserved.** The narrow type structurally prevents disabling the sandbox — there is no `Disabled` or `Enabled` field to set. The `resolveSandbox()` check (`daemon.go:2840`) continues to evaluate global + agent config only, which is correct: the orchestrator layer only adds dirs, it cannot influence whether sandboxing is active.

**Cannot weaken the sandbox.** The orchestrator layer can only append read/write dirs — it cannot remove dirs inherited from global or agent config (the merge is additive with dedup). It cannot change the sandbox command or add sandbox features.

**Cannot bypass sandbox command validation.** Because there is no `Command` field, `resolveSandbox()` validates the correct safehouse binary. No bypass vector exists.

**Path validation.** The existing `expandPaths()` function (`daemon.go:2881`) already skips non-existent directories with a daemon warning log. This applies to orchestrator sandbox paths too. A typo in `write_dirs` results in the orchestrator starting without that access, with a warning in `daemon.log`. This is consistent with how global/agent sandbox paths are handled.

**Config-write privilege.** Granting `write_dirs = ["~/.config/graith"]` gives the orchestrator the ability to modify graith configuration, which on reload could affect sandbox settings for other sessions. This is the intended use case and is explicitly user-opted-in, but operators should be aware this is an elevated grant. The default config example includes this path with a comment noting its implications.

**Pros:**
- Structurally prevents dangerous config (no Disabled/Enabled/Command/Features)
- Scoped to orchestrator only — other sessions are unaffected
- Backward-compatible: absent or empty `[orchestrator.sandbox]` changes nothing
- Consistent across all lifecycle paths via shared helper
- Config changes trigger auto-restart

**Cons:**
- Introduces a new type (`OrchestratorSandboxConfig`) rather than reusing `SandboxConfig`
- Debugging "which layer contributed which dir" requires checking the daemon log's `sandboxing orchestrator` line

## Consensus

Proposal 1 is recommended. The narrow config type eliminates security footguns structurally, and the shared helper prevents drift across lifecycle paths.

## Other Notes

### References

- `internal/config/config.go` — `SandboxConfig` struct (line 234), `Merge()` method (line 290), `OrchestratorConfig` (line 42)
- `internal/daemon/orchestrator.go` — `createOrchestrator()` (line 47), sandbox merge (line 108), `CreationCfg` (line 200)
- `internal/daemon/daemon.go` — `Resume()` sandbox merge (line 1412), `CreationCfg` on resume (line 1675), `sandboxOptsFromConfig()` (line 2855), config reload handler (line 2788)
- `internal/daemon/handler.go` — `isConfigStale()` (line 914)

### Backward Compatibility

An absent `[orchestrator.sandbox]` section produces a zero-value `OrchestratorSandboxConfig{}` with nil slices. When appended to the merged config, nil slices add nothing — the effective sandbox is identical to today's behavior. An empty `[orchestrator.sandbox]` table (present but with no keys) produces the same zero-value struct.

### Merge Layering

The effective orchestrator sandbox is computed as:

1. **Global** `[sandbox]` — baseline dirs for all sessions
2. **Agent** `[agents.<name>.sandbox]` — agent-specific dirs (e.g. `~/.claude`)
3. **Orchestrator** `[orchestrator.sandbox]` — orchestrator-only additional dirs
4. **Hardcoded** — tmpDir, shared store, scratch dir (added after config merge)

Dirs are additive and deduplicated (string-based dedup before path expansion). A path cannot be removed by a later layer. `~/foo` and `/Users/me/foo` are not canonicalized before dedup — both may appear, which is harmless (duplicate sandbox grants are tolerated by safehouse).

### Verification

1. `go build ./cmd/graith` — compiles
2. `go test ./internal/config/...` — config parsing tests pass (including new tests)
3. `go test ./internal/daemon/...` — daemon tests pass (including new tests)
4. `go test -race ./...` — no races
5. Manual: set `[orchestrator.sandbox] write_dirs = ["~/.config/graith"]`, start orchestrator, confirm it can write to the config directory
6. Manual: restart the daemon, verify orchestrator retains config access after resume
7. Manual: change `[orchestrator.sandbox]` while orchestrator is running, verify auto-restart picks up new dirs

### Test Plan

**Config layer:**
- `TestOrchestratorSandboxConfigParsing` — TOML loading for absent, empty, and populated `[orchestrator.sandbox]`
- `TestOrchestratorSandboxIgnoresDangerousKeys` — TOML with `disabled`, `enabled`, `command`, `features` under `[orchestrator.sandbox]` does not affect resolved sandbox or `resolveSandbox()` result
- `TestOrchestratorSandboxMerged` — `cfg.OrchestratorSandboxMerged()` produces correct config-derived dirs (global + agent + orchestrator, deduplicated). Does NOT include hardcoded runtime dirs (tmp, shared store, scratch)
- `TestOrchestratorSandboxBackwardCompat` — absent/empty section produces same effective sandbox as current two-layer merge

**Lifecycle:**
- `TestOrchestratorSandboxOnCreate` — `createOrchestrator` wraps with orchestrator dirs; `CreationCfg.SandboxConfig` matches launch sandbox; hardcoded dirs (tmp, shared store, scratch) appear in `WrapOpts` but not in `CreationCfg`
- `TestOrchestratorSandboxOnResume` — orchestrator retains config dirs after resume; `CreationCfg` reflects resumed config snapshot
- `TestOrchestratorSandboxStaleDetection` — `isConfigStale` detects changes to orchestrator sandbox for orchestrator sessions; non-orchestrator sessions are unaffected by orchestrator sandbox changes
- `TestOrchestratorSandboxAutoRestart` — config reload triggers `Restart()` on running orchestrator when effective sandbox changes; does NOT revive a stopped orchestrator
- `TestResolveSandboxIgnoresOrchestratorLayer` — `resolveSandbox()` result is unchanged regardless of `[orchestrator.sandbox]` content (security boundary test)
