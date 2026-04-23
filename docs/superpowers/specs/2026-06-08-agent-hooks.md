# Agent Hooks — Structured Status Detection

**Date**: 2026-06-08
**Status**: Draft
**Consensus**: Claude (Opus) + Codex (GPT-5.5) — 2 rounds of independent review

## Problem

graith detects agent status (active, approval, ready, unknown) by scraping PTY
scrollback text every 500ms (`internal/detector/detector.go`). This approach has
three structural problems:

1. **Fragile** — hardcoded string literals (`"Yes, allow once"`, `"esc to
   interrupt"`, ~90 thinking words, braille spinner glyphs) break when agents
   update their TUI. Anthropic rotates the thinking-word list between releases.

2. **Blind** — scraping can only infer status from rendered text. It cannot see
   structured data: which tool is running, cost, token usage, context window
   pressure, model name, permission details.

3. **Racy** — the 500ms poll races against screen redraws. A half-rendered
   permission dialog can be missed or misclassified until the next tick.

## Solution

Replace scraping with structured hook events from agents that support them
(Claude Code, Codex), while keeping the scraper as a fallback for agents that
don't (opencode, agy).

Both Claude Code and Codex emit JSON lifecycle events through configurable hooks:
`SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Stop`,
`Notification`/`PermissionRequest`. graith already controls the agent launch and
injects env vars (`GRAITH_SESSION_ID`, `GRAITH_SESSION_NAME`), so hook callbacks
can attribute themselves to the exact session.

## Design

### Architecture

```
Agent process (claude/codex)
    │
    │ hook fires → runs hook script
    │
    ▼
gr report-status          ← new CLI command, reads GRAITH_SESSION_ID from env
    │
    │ fast-path Unix socket connect (no daemon autostart, short timeout)
    │
    ▼
graithd handler
    │
    │ new "status_report" control message
    │
    ▼
SessionManager.hookReports[sessionID]   ← in-memory only, not persisted
    │
    │ detectAgentStatuses() checks hookReports first
    │
    ▼
AgentStatus update + onAgentStatusChange()
```

### Event-to-Status Mapping

| Agent Hook Event                      | graith Status | Staleness          |
|---------------------------------------|---------------|--------------------|
| `SessionStart`                        | `active`      | 5s                 |
| `UserPromptSubmit`                    | `active`      | 30s                |
| `PreToolUse`                          | `active`      | 30s                |
| `PostToolUse`                         | `active`      | 30s                |
| `Notification(permission_prompt)` [C] | `approval`    | sticky until next  |
| `PermissionRequest` [Codex]           | `approval`    | sticky until next  |
| `Stop`                                | `ready`       | sticky until next  |
| Process exit                          | `stopped`     | (existing path)    |

"Sticky until next" means the status holds until a contradicting event arrives.
`active` events expire after their TTL and fall back to scraping if no new hook
event arrives.

### Phase 1: Protocol + Ingestion

**New control message** in `protocol/messages.go`:

```go
type StatusReportMsg struct {
    SessionID string `json:"session_id"`
    Event     string `json:"event"`
    Status    string `json:"status,omitempty"`
    ToolName  string `json:"tool_name,omitempty"`
}
```

Deliberately minimal — 4 fields. Enrichment fields (cost, tokens, context %,
model) are deferred to phase 5. The current UI only consumes `AgentStatus` (via
`SessionInfo` in `handler.go:toSessionInfo` and fleet counts in
`daemon.go:fleetSummary`), so there's nothing to display richer data against
until the UI is extended.

**New CLI command** `gr report-status`:

- Reads `GRAITH_SESSION_ID` from environment
- Reads hook JSON from stdin (for future enrichment parsing)
- Maps the hook event name to a graith status string
- Connects to the daemon Unix socket with a **fast path**: short timeout
  (500ms), no daemon autostart, no handshake beyond the minimum
- Sends a `status_report` control frame
- Exits 0 silently on success AND failure (hooks must never block the agent or
  produce stdout that could affect agent context)

**Daemon handler** — new case in `handler.go` switch:

```go
case "status_report":
    var sr protocol.StatusReportMsg
    if err := protocol.DecodePayload(msg, &sr); err != nil {
        sendControl("error", protocol.ErrorMsg{Message: "invalid status_report"})
        continue
    }
    sm.HandleHookReport(sr)
    sendControl("status_reported", struct{}{})
```

**In-memory hook state** on `SessionManager` (not on persisted `SessionState`):

```go
type hookReport struct {
    Status             string
    Event              string
    ToolName           string
    ReportedAt         time.Time
    AuthoritativeUntil time.Time
}

// In SessionManager struct:
hookReports map[string]hookReport  // keyed by session ID
```

On daemon restart, `hookReports` is empty, so the scraper naturally resumes
authority until hooks start firing again. No state migration needed.

### Phase 2: Claude Code Hook Injection

**Hook script** — a stable wrapper generated by graith:

```sh
#!/bin/sh
if [ -n "${GRAITH_BIN:-}" ] && [ -x "$GRAITH_BIN" ]; then
  exec "$GRAITH_BIN" report-status "$@" >/dev/null 2>&1
fi
if command -v gr >/dev/null 2>&1; then
  exec gr report-status "$@" >/dev/null 2>&1
fi
exit 0
```

The script uses `GRAITH_BIN` (resolved via `exec.LookPath("gr")` at session
creation, falling back to `os.Executable()`) for upgrade resilience. If neither
path works, it exits silently — hooks must never fail loudly.

**Settings file** — per-session JSON generated at launch time. Claude Code's
`--settings <file>` flag merges with existing settings, so user config is not
overridden:

```json
{
  "hooks": {
    "SessionStart":     [{"type": "command", "command": "/path/to/graith-hook.sh"}],
    "UserPromptSubmit": [{"type": "command", "command": "/path/to/graith-hook.sh"}],
    "PreToolUse":       [{"type": "command", "command": "/path/to/graith-hook.sh"}],
    "PostToolUse":      [{"type": "command", "command": "/path/to/graith-hook.sh"}],
    "Notification":     [{"type": "command", "command": "/path/to/graith-hook.sh"}],
    "Stop":             [{"type": "command", "command": "/path/to/graith-hook.sh"}]
  }
}
```

**Injection points** — must cover all launch paths:

- `SessionManager.Create()` — `daemon.go:152`
- `SessionManager.Fork()` — `daemon.go:303`
- `SessionManager.Resume()` — `daemon.go:443`

Each generates the hook script + settings JSON in a session-specific temp
directory, appends `--settings <path>` to the agent args, and adds `GRAITH_BIN`
to the agent env alongside the existing `GRAITH_SESSION_ID` etc.

**Cleanup** — when a session is deleted (`SessionManager.Delete()`), remove the
generated hook script and settings file.

### Phase 3: Authority Layer

Modify `detectAgentStatuses()` in `daemon.go` to check hook reports before
scraping:

```go
func (sm *SessionManager) detectAgentStatuses() {
    // ... existing target gathering ...

    for _, t := range targets {
        // Check hook authority first
        sm.mu.RLock()
        hr, hasHook := sm.hookReports[t.id]
        sm.mu.RUnlock()

        var status string
        if hasHook && time.Now().Before(hr.AuthoritativeUntil) {
            status = hr.Status
        } else {
            // Fall back to PTY scraping
            content := t.pty.ScreenPreview()
            if content == "" {
                continue
            }
            outputAge := detector.OutputAgeUnknown
            if lastOut := t.pty.LastOutputAt(); !lastOut.IsZero() {
                outputAge = time.Since(lastOut)
            }
            d := detector.New(t.agent)
            status = string(d.Detect(content, outputAge))
        }

        // ... rest of existing logic (git state, status change, idle check) ...
    }
}
```

The detection loop still runs at 500ms for git dirty/unpushed checks and idle
timeout tracking. For hook-authoritative sessions, it simply skips the
`ScreenPreview()` + `Detect()` calls — saving the vt10x render overhead.

### Phase 4: Codex Hook Injection

Codex supports the same lifecycle hook model (`SessionStart`, `PreToolUse`,
`PermissionRequest`, `PostToolUse`, `UserPromptSubmit`, `Stop`). Injection
strategy:

- Use session-level config overrides or profile injection — do NOT write into
  the repo
- Use the same hook script as Claude
- Include `PermissionRequest` — this is the deterministic approval signal for
  Codex
- Do NOT add `--dangerously-bypass-hook-trust` by default — it can bypass trust
  for user/project hooks, which is a security concern
- Keep Codex `notify` as a legacy fallback for turn-completion only

### Phase 5: Enrichment

Extend `StatusReportMsg` with optional fields:

```go
type StatusReportMsg struct {
    SessionID string       `json:"session_id"`
    Event     string       `json:"event"`
    Status    string       `json:"status,omitempty"`
    ToolName  string       `json:"tool_name,omitempty"`
    // Phase 5 additions:
    Usage     *UsageReport   `json:"usage,omitempty"`
    Context   *ContextReport `json:"context,omitempty"`
    Model     string         `json:"model,omitempty"`
}

type UsageReport struct {
    InputTokens  *int64   `json:"input_tokens,omitempty"`
    OutputTokens *int64   `json:"output_tokens,omitempty"`
    CostUSD      *float64 `json:"cost_usd,omitempty"`
}

type ContextReport struct {
    UsedTokens *int64   `json:"used_tokens,omitempty"`
    MaxTokens  *int64   `json:"max_tokens,omitempty"`
    Percent    *float64 `json:"percent,omitempty"`
}
```

Source: Claude Code's statusline command emits a JSON blob after each message
with `cost.total_cost_usd`, `context_window.used_percentage`, `tokens`,
`model.display_name`. Parse this in `gr report-status` and include in the
status report.

Surface in:
- `gr list` — add cost/token columns
- Overlay session picker — show context % and current tool
- Status bar — show live cost/model

## Failure Modes and Mitigations

| Failure | Impact | Mitigation |
|---------|--------|------------|
| Hook script can't connect to daemon | No status report | Exit 0 silently; scraper takes over after authority expires |
| Daemon restarts | hookReports map is empty | Scraper resumes until hooks fire again |
| Agent updates hook event names | Events don't map to status | Unknown events are logged and ignored; scraper fallback |
| `gr` binary not found by hook | Hook is a no-op | GRAITH_BIN env + PATH fallback + silent exit |
| Hook fires between detection ticks | Status update delayed up to 500ms | Acceptable — still far better than scraping race |
| Claude `--settings` merge conflict | User hooks overwritten | Claude merges settings files; user hooks preserved |
| Codex hook trust verification | Hook rejected | Document opt-in; don't bypass trust silently |
| Long tool execution (>30s) | Active status expires, scraper takes over | Scraper would also detect active state; graceful degradation |

## What This Does NOT Change

- `detector.go` remains as-is — it's the fallback for hookless agents and for
  when hooks fail
- The 500ms detection loop continues running — it still drives git state checks,
  idle timeout, and scraper fallback
- `state.json` schema is unchanged — no state migration
- opencode and agy continue to work via scraping only

## Files Changed

| Phase | Files | Changes |
|-------|-------|---------|
| 1 | `protocol/messages.go` | Add `StatusReportMsg` |
| 1 | `daemon/handler.go` | Add `status_report` case |
| 1 | `daemon/daemon.go` | Add `hookReports` map, `HandleHookReport()` |
| 1 | `cli/report_status.go` | New `gr report-status` command |
| 1 | `client/client.go` | Fast-path connect variant |
| 2 | `daemon/hooks.go` | New file: hook script + settings generation |
| 2 | `daemon/daemon.go` | Inject hooks in Create/Fork/Resume |
| 2 | `config/template.go` | Add `GRAITH_BIN` to env vars |
| 3 | `daemon/daemon.go` | Authority check in `detectAgentStatuses()` |
| 4 | `daemon/hooks.go` | Codex hook injection |
| 5 | `protocol/messages.go` | Extend `StatusReportMsg` with enrichment |
| 5 | `cli/list.go`, `client/overlay.go` | Display enrichment data |

## Implementation Order

1. **PR 1**: Protocol + `gr report-status` + daemon ingestion + tests
2. **PR 2**: Claude Code hook injection for Create/Fork/Resume
3. **PR 3**: Authority layer in detection loop with scraper fallback
4. **PR 4**: Codex lifecycle hook injection
5. **PR 5**: Enrichment (extended model, UI changes)

PRs 1-3 are the minimum viable feature for Claude Code. PR 4 extends to Codex.
PR 5 is additive enrichment that can land independently.

## Review Notes

**Round 1 consensus** (both agents independently agreed):
- Hook-first with scraper fallback is the right architecture
- `gr report-status` via Unix socket is the correct ingestion path
- Single generic hook script, not per-event scripts
- Hook script must be silent (no stdout)
- Fast-path connection — hooks must not autostart daemon
- Must inject for Create, Fork, AND Resume
- Event-specific staleness over blanket timeout
- Don't persist raw hook payloads

**Round 2 resolution** (4 disputed points, all resolved):
- Data model: start minimal (4 fields), extend in phase 5 — the UI can't
  display richer data until it's built
- Authority state: in-memory `hookReports` map on SessionManager, NOT on
  persisted SessionState — daemon restart naturally falls back to scraping
- Implementation order: protocol → injection → authority (not authority before
  injection) — real hook data validates assumptions before building authority
- Binary path: `GRAITH_BIN` env var with `exec.LookPath` + `os.Executable()`
  fallback, plus PATH fallback in the wrapper script
