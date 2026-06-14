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

- Add `--agent-mode` persistent bool flag (force-enables agent mode regardless
  of env detection). Named `--agent-mode` to avoid conflicting with
  `gr new --agent <type>` which is already a string flag.
- In `PersistentPreRunE`: if `agent.Detected()` or `--agent-mode` flag is set,
  and `--json` was not explicitly passed by the user, set `jsonOutput = true`.
- The `--json` flag continues to work as before — `--agent-mode` is additive.
- In `executeWithArgs` error fallback (root.go:56): also check
  `agent.Detected()` so that Cobra errors (unknown subcommand, arg validation)
  are emitted as JSON when running inside an agent.

JSON output also affects behavior: `gr new` and `gr fork` print JSON and
exit without attaching when `jsonOutput` is true (existing behavior). This
is desirable for agents — they create sessions programmatically and don't
need interactive attachment. No change needed, but callers should be aware.

### Files to change

| File | Change |
|------|--------|
| `internal/agent/agent.go` | New file. Detection logic + `Detected()` |
| `internal/cli/root.go` | Add `--agent-mode` flag, auto-enable JSON in `PersistentPreRunE` and error fallback |

## Feature 2: `gr stop --children` + Env Auto-Resolve

### Problem

`gr delete --children` exists but `gr stop --children` does not. Agents that
spawn child sessions need to stop them without deleting worktrees. Additionally,
both `stop --children` and `delete --children` should auto-resolve the current
session from `GRAITH_SESSION_ID` when no positional arg is given.

### Design

#### Protocol

Add `Children bool` and `ExcludeRoot bool` fields to `StopMsg`:

```go
type StopMsg struct {
    SessionID  string `json:"session_id"`
    Children   bool   `json:"children,omitempty"`
    ExcludeRoot bool  `json:"exclude_root,omitempty"`
}
```

Add `ExcludeRoot bool` to `DeleteMsg` as well, for consistency with the
env auto-resolve behavior.

#### Daemon

Add `StopWithChildren(rootID string, excludeRoot bool) ([]string, error)` to
`SessionManager`. Reuses existing `collectDescendants()`. When `excludeRoot`
is true, filters out `rootID` from the result before processing. Sends
SIGTERM to each descendant in leaf-first order. **Skips already-stopped
descendants** (do not error — log and continue). Returns the list of
actually-stopped session IDs.

Note: `collectDescendants()` (daemon.go:1375) includes the root ID in its
result. The `excludeRoot` parameter handles this without changing the
shared helper.

#### Handler

When `StopMsg.Children` is true, call `StopWithChildren` instead of `Stop`.
Return `{"stopped": ["id1", "id2"]}` on success.

#### CLI (`stop.go`)

- Add `--children` flag.
- When `--children` is set and no positional arg: read `GRAITH_SESSION_ID`
  from env. Stop children only (not self) — sets `ExcludeRoot: true` in the
  protocol message.
- When `--children` is set with a positional arg: stop the named session and
  its children (existing delete behavior) — `ExcludeRoot: false`.
- Error if `--children` with no arg and `GRAITH_SESSION_ID` is not set.
- `--children` cannot be combined with batch filters (`--repo`, `--stopped`,
  `--stale`), matching existing delete behavior (delete.go:33).
- Args validation: custom `Args` function — `NoArgs` when batch active,
  `MaximumNArgs(1)` when `--children`, `ExactArgs(1)` otherwise.

#### CLI (`delete.go`)

Apply the same env auto-resolve: when `--children` is set and no positional
arg, read `GRAITH_SESSION_ID`. Delete children only (not self).

### Self-exclusion

When auto-resolved from env (no positional arg), `--children` sets
`ExcludeRoot: true` in the protocol message — the calling session is never
stopped/deleted. This prevents agents from killing their own process
mid-command.

When a positional arg is given, the named session IS included in the
stop/delete (`ExcludeRoot: false` — existing behavior for delete, new for
stop).

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
   If a child is stopped, skip the notification for that child (message is
   still delivered to the inbox stream — it will be readable when the
   session resumes). Continue on notification failure.
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
  "count": 2
}
```

For single-target sends (positional arg or `--parent`), continue returning
the daemon's `msg_published` payload directly (existing behavior).

#### Mutual exclusion

`--children`, `--parent`, and a positional session arg are mutually exclusive.
Use `cmd.MarkFlagsMutuallyExclusive("children", "parent")` for the flag pair.
Check for positional arg + flag conflict in `RunE`.

#### Args validation

Custom `Args` function:
- With `--children` or `--parent`: `MaximumNArgs(1)` — the one positional arg
  is the body (session target is resolved from env).
- Without either flag: existing `RangeArgs(1, 2)` — session as first arg.

Update `ValidArgsFunction` to suppress session name completion when
`--children` or `--parent` is set.

### Files to change

| File | Change |
|------|--------|
| `internal/cli/msg.go` | Add `--children` and `--parent` flags to `msgSendCmd`, target resolution logic |
