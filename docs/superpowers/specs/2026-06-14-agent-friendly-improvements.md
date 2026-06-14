# Agent-Friendly Improvements

Three features to make graith work better when agents invoke `gr` commands:
auto-JSON detection, `gr stop --children`, and `gr msg send --children/--parent`.

## Feature 1: Auto-JSON Agent Detection

### Problem

Agents calling `gr list`, `gr new`, `gr msg send`, etc. must pass `--json`
every time. Since graith already sets `GRAITH_SESSION_ID` in agent processes,
it knows when it's running inside an agent — it should default to JSON
automatically.

### Design

New package `internal/agent/agent.go` with init-time detection.

Detection priority (first match wins):

1. `GR_AGENT_MODE` — explicit override. `1`/`true`/`yes` enables,
   `0`/`false`/`no` disables. Case-insensitive.
2. `GRAITH_SESSION_ID` — set by graith in every agent process.
3. `CLAUDECODE` or `CLAUDE_CODE` — Claude Code IDE extension.
4. `CURSOR_AGENT` — Cursor IDE.
5. `GITHUB_COPILOT` — GitHub Copilot.
6. `AMAZON_Q` — Amazon Q.
7. `OPENCODE` — OpenCode agent.

Exposes `agent.Detected() bool`.

CLI changes in `root.go`:

- Add `--agent` persistent bool flag (force-enables agent mode regardless of
  env detection).
- In `PersistentPreRunE`: if `agent.Detected()` or `--agent` flag is set, and
  `--json` was not explicitly passed by the user, set `jsonOutput = true`.
- The `--json` flag continues to work as before — `--agent` is additive.

Only JSON output changes. No color/style library changes needed — graith
doesn't use one.

### Files to change

| File | Change |
|------|--------|
| `internal/agent/agent.go` | New file. Detection logic + `Detected()` |
| `internal/cli/root.go` | Add `--agent` flag, auto-enable JSON in `PersistentPreRunE` |

## Feature 2: `gr stop --children` + Env Auto-Resolve

### Problem

`gr delete --children` exists but `gr stop --children` does not. Agents that
spawn child sessions need to stop them without deleting worktrees. Additionally,
both `stop --children` and `delete --children` should auto-resolve the current
session from `GRAITH_SESSION_ID` when no positional arg is given.

### Design

#### Protocol

Add `Children bool` field to `StopMsg`:

```go
type StopMsg struct {
    SessionID string `json:"session_id"`
    Children  bool   `json:"children,omitempty"`
}
```

#### Daemon

Add `StopWithChildren(rootID string) ([]string, error)` to `SessionManager`.
Reuses existing `collectDescendants()`. Sends SIGTERM to each descendant in
leaf-first order. Returns the list of stopped session IDs.

#### Handler

When `StopMsg.Children` is true, call `StopWithChildren` instead of `Stop`.
Return `{"stopped": ["id1", "id2"]}` on success.

#### CLI (`stop.go`)

- Add `--children` flag.
- When `--children` is set and no positional arg: read `GRAITH_SESSION_ID`
  from env. Stop children only (not self).
- Error if `--children` with no arg and `GRAITH_SESSION_ID` is not set.
- Args validation: `cobra.MaximumNArgs(1)` when `--children`, otherwise
  `cobra.ExactArgs(1)`.

#### CLI (`delete.go`)

Apply the same env auto-resolve: when `--children` is set and no positional
arg, read `GRAITH_SESSION_ID`. Delete children only (not self).

### Self-exclusion

When auto-resolved from env (no positional arg), `--children` operates on
descendants only — the calling session is never stopped/deleted. This prevents
agents from killing their own process mid-command.

When a positional arg is given, the named session IS included in the
stop/delete (existing behavior for delete, new for stop).

### Files to change

| File | Change |
|------|--------|
| `internal/protocol/messages.go` | Add `Children` to `StopMsg` |
| `internal/daemon/daemon.go` | Add `StopWithChildren()` method |
| `internal/daemon/handler.go` | Handle `Children` flag on stop message |
| `internal/cli/stop.go` | Add `--children` flag, env auto-resolve |
| `internal/cli/delete.go` | Add env auto-resolve for `--children` |

## Feature 3: `gr msg send --children` / `--parent`

### Problem

Agents communicating with children or parent must know session names/IDs.
Since parent-child relationships are tracked via `ParentID`, the CLI can
resolve targets automatically.

### Design

#### `gr msg send --children <body>`

1. Resolve current session from `GRAITH_SESSION_ID` env var.
2. Fetch session list from daemon.
3. Find all sessions where `ParentID == currentSessionID` (direct children
   only — not transitive descendants; broadcasting to grandchildren is a
   pub/sub use case via `gr msg pub`).
4. Send the message body to each child's inbox (`inbox:<childID>`).
5. Type the notification hint into each child session (unless `--quiet`).
6. Error if no children found.

#### `gr msg send --parent <body>`

1. Resolve current session from `GRAITH_SESSION_ID` env var.
2. Fetch session list, find current session's `ParentID`.
3. Send to parent's inbox.
4. Error if current session has no parent (orphan/root session).

#### Mutual exclusion

`--children`, `--parent`, and a positional session arg are mutually exclusive.
Exactly one target mode must be specified.

#### JSON output

In JSON mode, emit structured result:

```json
{
  "sent_to": ["child-session-1", "child-session-2"],
  "count": 2,
  "stream": "inbox:..."
}
```

#### Args validation

With `--children` or `--parent`: `cobra.RangeArgs(0, 1)` — body is the only
positional arg (session target is resolved from env).

Without either flag: existing behavior — `cobra.RangeArgs(1, 2)` with session
as first arg.

### Files to change

| File | Change |
|------|--------|
| `internal/cli/msg.go` | Add `--children` and `--parent` flags to `msgSendCmd`, target resolution logic |
