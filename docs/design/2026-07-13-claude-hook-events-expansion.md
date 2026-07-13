---
title: "Design Doc: Wire the missing Claude Code hook events"
authors: Dougal Matthews
created: 2026-07-13
status: Draft
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1073
---

Claude Code emits 27 lifecycle hook events; graith registers 6 of them
(`internal/daemon/hooks.go:60`) and silently drops the rest. That leaves graith
blind to clean-shutdown reasons, context-window pressure, Claude's own
sub-agents, and a second idle signal — all data Claude hands us for free. This
doc argues for wiring a prioritised slice of the missing events through the
existing `command`-hook → `gr report-status` → `status_report` path (reusing
that one control message rather than inventing new ones), extending the status
model with a few runtime-only fields, and scoping the `PreToolUse` approval hook
so read-only tools skip the daemon round-trip. It rejects switching to `http`
hooks (the sandbox blocks the network egress they need) and rejects a big-bang
registration of all 21 events.

## Background

graith runs Claude Code as an interactive PTY and reconstructs agent state
externally. The authoritative half of that reconstruction is the hook pipeline
built in `docs/design/2026-06-08-agent-hooks.md`:

1. At session creation, `generateClaudeSettings` (`internal/daemon/hooks.go:45`)
   writes a per-session `settings.json` registering a fixed list of hook events.
   Claude's `--settings <file>` flag *merges* with the user's own settings
   (arrays concatenate), so we don't clobber user hooks.
2. Most events run `gr report-status --event <Event>`; `SessionStart` also runs
   `gr check-inbox`; `PreToolUse` runs `gr approve-request` (only when approval
   gating or yolo is on).
3. `gr report-status` (`internal/cli/report_status.go`) reads the hook JSON from
   stdin, extracts a couple of fields into `hookStdin`, and sends a
   `status_report` control frame over the daemon's Unix socket via the
   no-autostart `ConnectFast` path. It exits 0 silently on every outcome — a
   hook must never block the agent or write to its context.
4. The daemon's `status_report` handler calls
   `SessionManager.HandleHookReport` (`internal/daemon/daemon.go:163`), which
   maps the event name to an `AgentStatus` (`active` / `approval` / `ready`) with
   an event-specific staleness window, stores an in-memory `hookReport`, and
   fires `onAgentStatusChange` when the status flips.

The six registered events are `SessionStart`, `UserPromptSubmit`, `PreToolUse`,
`PostToolUse`, `Notification`, and `Stop`. Claude's full event set (from its
own source, cross-referenced in the `graith-cc-integration-gaps` analysis) is 27
events covering session lifecycle, tool use, sub-agents, compaction, and the
coordinator/teammate model.

Two facts about the plumbing matter for everything below:

- **`report_status.go` discards almost the whole payload.** `hookStdin`
  (`report_status.go:20`) parses only `tool_name` and `notification_type`.
  Everything else Claude sends — the shutdown reason, the compaction trigger,
  the final assistant message, sub-agent identity — is read off stdin and thrown
  away.
- **`Notification` is filtered to one subtype.** `report_status.go:69` returns
  early unless `notification_type == "permission_prompt"`, so `idle_prompt`,
  `auth_success`, and the `elicitation_*` subtypes never reach the daemon.

## Problem

graith is blind to session transitions Claude reports explicitly:

- **Clean vs. crash shutdown is guessed.** graith has no `SessionEnd` hook, so
  when the PTY process exits it defaults `StopReason` to `StopReasonCrash`
  unless some other path set a reason first (`daemon.go:1913`). A user who runs
  `/quit`, `/clear`, or logs out looks identical to a crash. Claude's
  `SessionEnd` carries a `reason` (`clear` / `resume` / `logout` /
  `prompt_input_exit` / `other`) that would make the stop summary honest.
- **Context pressure is invisible.** Claude fires `PreCompact` /`PostCompact`
  with a `trigger` (`manual` | `auto`) — a direct "this session is about to
  compact and lose context" signal. graith currently can't see it at all, so it
  can't warn the human, hand off, or gate a trigger on it.
- **Sub-agents are invisible.** Claude spawns its own sub-agents (the
  `agent-*.jsonl` sidechains) and fires `SubagentStart` / `SubagentStop` with
  `agent_id`, `agent_type`, and `agent_transcript_path`. graith shows a single
  flat status for a session that may be fanning out to several sub-agents.
- **A second idle signal is dropped.** `Notification(idle_prompt)` is Claude
  telling us it's waiting for input — exactly the "ready / needs attention"
  state graith cares about — but the `permission_prompt`-only filter discards it.
- **The final message is thrown away.** `Stop` carries
  `last_assistant_message`; graith maps `Stop` to `ready` and drops the text,
  even though it's the agent's final output with no transcript parsing required.

Each gap makes the overlay and the orchestrator less able to reason about what a
session is actually doing, and forces fallback heuristics (crash-labelling,
PTY-scraped idle) where Claude already has the ground truth.

## Goals

- Wire the high-value missing events so graith learns clean-shutdown reasons,
  context pressure, sub-agent activity, and the second idle signal.
- Reuse the existing `command`-hook → `report-status` → `status_report` path;
  add no new control-message type (and therefore no new `authmatrix` row).
- Extend the payload parsing and status model with the minimum fields needed to
  represent the new signals, surfaced where the UI can already show them.
- Keep hooks silent, non-blocking, and forward/backward compatible with Claude
  versions that predate a given event.
- Reduce, not increase, per-event daemon load — scope the approval hook so
  read-only tools don't round-trip.

### Non-Goals

- Wiring Claude's coordinator/teammate events (`TaskCreated`, `TaskCompleted`,
  `TeammateIdle`). graith orchestrates sessions itself; Claude's internal
  coordinator overlaps graith's core and is out of scope until there's a
  concrete need (see `graith-cc-integration-gaps`).
- Cost / token / context-percentage enrichment from the statusLine JSON. That's
  a separate, larger piece (phase 5 of the agent-hooks doc, and issue #644) and
  is tracked independently.
- Persisting sub-agent transcripts or reading the `agent-*.jsonl` sidechains.
  We track that sub-agents exist, not their contents.
- Codex / Cursor parity. This doc is Claude-specific; the equivalent Codex/Cursor
  events can follow the same pattern in a later pass.

## Proposals

### Proposal 0: Do Nothing

Leave the six events as they are. graith keeps guessing clean-vs-crash, stays
blind to compaction and sub-agents, and keeps discarding `idle_prompt` and the
final assistant message.

This is cheap but leaves real signal on the floor. The clean-shutdown
mislabelling in particular produces user-visible wrongness (`/quit` reported as
"crash"), and the context-pressure blindness blocks a class of useful triggers
("hand this session off before it compacts"). Rejected.

### Proposal 1: Incrementally wire a prioritised slice over the existing path (Recommended)

Keep the architecture exactly as it is — `command` hooks, `gr report-status`,
one `status_report` message — and extend it. Roll the events out in priority
tiers, each tier a self-contained PR with its own tests, rather than registering
all 21 at once.

#### Which events, and why (prioritised)

**Tier 1 — lifecycle & context truth (highest value, lowest risk):**

| Event | Why | Status-model effect |
|-------|-----|---------------------|
| `SessionEnd` | Clean shutdown with `reason`. Ends the crash-guessing. | Record the reason so the process-exit path labels the stop honestly instead of defaulting to `StopReasonCrash`. |
| `PreCompact` | Direct context-pressure signal (`trigger: manual\|auto`). | Set a runtime `ContextPressure` flag + timestamp; keep `AgentStatus` active. |
| `PostCompact` | Compaction finished. | Clear `ContextPressure`. |
| `Stop` (already registered) | Carries `last_assistant_message`. | Keep `ready`; additionally capture a truncated `LastMessage`. |

**Tier 2 — visibility into fan-out and idleness:**

| Event | Why | Status-model effect |
|-------|-----|---------------------|
| `SubagentStart` | Claude spawned a sub-agent (`agent_id`, `agent_type`). | Increment a runtime sub-agent count / append identity. |
| `SubagentStop` | Sub-agent finished. | Decrement. |
| `Notification(idle_prompt)` | Second idle signal — Claude awaiting input. | Map to `ready` (agent at rest, needs attention). |

**Tier 3 — informational subtypes (wire only if cheap):**
`Notification(auth_success)` and `Notification(elicitation_*)` are logged for
observability but don't change status. `TaskCreated` / `TaskCompleted` /
`TeammateIdle` are explicit non-goals.

#### How each maps to `HandleHookReport`

`HandleHookReport` (`daemon.go:163`) today is a pure event→status switch. Most
new events fit the same shape, but two need to reach *past* the status map:

- **`SessionEnd`** is not an `AgentStatus` — the process is about to exit and the
  existing exit path will set `stopped`. Instead of returning a status, it
  records the reason (e.g. on `hookReport` / a small pending-stop field on
  `SessionState`) so that when the PTY process exit fires, the `StopReason`
  defaults to the Claude-reported reason rather than `StopReasonCrash`. This is
  a race with process exit, so the reason must be *sticky*: written on the
  `SessionEnd` report and consumed by the exit handler if present (and ignored
  if the process crashed before ever emitting `SessionEnd`).
- **`PreCompact` / `PostCompact`** don't change `AgentStatus` either — a
  compacting agent is still active. They toggle a separate `ContextPressure`
  runtime signal. `onAgentStatusChange` isn't the right hook for this; a small
  dedicated notifier (or simply surfacing the field in `SessionInfo`) is enough
  for v1.
- **`SubagentStart/Stop`** keep `AgentStatus` active and adjust a counter. They
  should *not* clobber the parent's approval/ready status.
- **`idle_prompt`** slots straight into the existing switch as a `ready` mapping.

The switch's `default` branch already logs-and-ignores unknown events, so any
event we register but don't yet special-case degrades safely.

#### Schema changes

Follow the agent-hooks doc's discipline: raw hook payloads are **not** persisted,
and ephemeral signals live as runtime-only (`json:"-"`) fields alongside the
existing `HookToolName` (`state.go:74`), so there's no `state.json` migration and
a daemon restart cleanly forgets them (hooks re-establish the picture as they
fire).

`protocol.StatusReportMsg` (`messages.go:371`) — add optional fields, all
`omitempty`, so old clients/daemons that don't set/read them are unaffected:

```go
type StatusReportMsg struct {
    SessionID        string `json:"session_id"`
    Event            string `json:"event"`
    Status           string `json:"status,omitempty"`
    ToolName         string `json:"tool_name,omitempty"`
    // new:
    Reason           string `json:"reason,omitempty"`            // SessionEnd
    Trigger          string `json:"trigger,omitempty"`           // Pre/PostCompact
    NotificationType string `json:"notification_type,omitempty"` // Notification subtype
    LastMessage      string `json:"last_message,omitempty"`      // Stop (truncated)
    AgentID          string `json:"agent_id,omitempty"`          // Subagent*
    AgentType        string `json:"agent_type,omitempty"`        // Subagent*
}
```

`SessionState` (runtime-only, `json:"-"`, no migration):

```go
ContextPressure   bool       // set by PreCompact, cleared by PostCompact
ContextPressureAt time.Time  // when the last compaction signal arrived
SubAgentCount     int        // SubagentStart++/SubagentStop--
LastMessage       string     // truncated last_assistant_message from Stop
PendingStopReason string     // reason from SessionEnd, consumed by exit path
```

`SessionInfo` (`messages.go:392`) — mirror the display-worthy fields
(`ContextPressure`, `SubAgentCount`, and optionally a `LastMessage` snippet) so
the overlay and `gr list` can show them. These *are* JSON-serialised on the
wire but that's a protocol message, not persisted state — no migration.

#### `report_status.go` payload parsing

Extend `hookStdin` to cover the new fields and populate `StatusReportMsg`
per-event. The parse stays best-effort and non-blocking (the existing 100 ms
stdin read with a channel + timeout is kept):

```go
type hookStdin struct {
    ToolName            string `json:"tool_name"`
    NotificationType    string `json:"notification_type"`
    Reason              string `json:"reason"`                 // SessionEnd
    Trigger             string `json:"trigger"`                // Pre/PostCompact
    LastAssistantMsg    string `json:"last_assistant_message"` // Stop
    AgentID             string `json:"agent_id"`               // Subagent*
    AgentType           string `json:"agent_type"`             // Subagent*
}
```

Two rules keep the payload safe to carry: **truncate `last_assistant_message`**
to a bounded length in the CLI before sending (it's the agent's full final
message — we want a snippet, not an unbounded frame), and **drop the
`permission_prompt`-only early return**, replacing it with per-subtype routing so
`idle_prompt` is forwarded and `auth_success`/`elicitation_*` are forwarded as
informational (the daemon decides what to do with each subtype, not the CLI).

#### `PreToolUse` `if`-filter scoping

The `PreToolUse` hook runs `gr approve-request`, a daemon round-trip on *every*
tool call. Claude's hook matchers let us scope it with a regex on the tool name.
Read-only tools carry no approval risk, so they can skip the round-trip entirely:

- **Skip (no hook):** `Read`, `Glob`, `Grep`, `LS`, `NotebookRead`, `TodoWrite`,
  `TodoRead`.
- **Route through the daemon:** `Bash`, `Write`, `Edit`, `MultiEdit`,
  `NotebookEdit`, `WebFetch`, `WebSearch`, `Task`, and any MCP tool
  (`mcp__*`), i.e. everything that can mutate state or reach the network.

Implement it by giving the `PreToolUse` `matcherGroup` a `Matcher` regex that
matches the mutating set (rather than the current `""` = match-all). This cuts a
large fraction of approval round-trips (read-heavy exploration) with no loss of
safety, because the daemon's approval logic only ever *allows* read-only tools
anyway.

One caveat to record: `HandleHookReport` maps `PreToolUse` to `active`, but
today nothing sends a `PreToolUse` *report-status* — `PreToolUse` only runs
`approve-request`. So scoping the approval hook does not remove an `active`
signal; `PostToolUse` (unscoped) continues to drive the per-tool `active`
heartbeat. This is worth a comment in the code so a future reader doesn't
"restore" a match-all matcher thinking it's needed for status.

#### Registration: incremental, not big-bang

Register each event **in the same PR that adds its daemon handling**, not all 21
up front. Registering an event with a `report-status` handler means a `gr`
subprocess spawns on every fire; registering events we don't yet consume is pure
waste (extra forks, extra socket dials, no benefit). Tier 1 → Tier 2 → (maybe)
Tier 3, each a reviewable slice with tests. The generated `settings.json` is
rewritten wholesale each session, so there's no partial-registration state to
manage.

#### Trade-offs

- **Pro:** No new protocol surface, no `authmatrix` change, no state migration.
  Every new signal reuses a path that's already proven silent and non-blocking.
  Fully compatible with older Claude (see Backward compatibility).
- **Pro:** Net *reduction* in daemon round-trips once `PreToolUse` is scoped.
- **Con:** `SessionEnd`-vs-process-exit is a race; the reason is best-effort and
  a hard crash still lands as `StopReasonCrash`. Acceptable — that's the correct
  outcome for an actual crash.
- **Con:** Sub-agent tracking is a counter, not a tree; we know how many, not a
  full topology. Sufficient for the overlay; richer modelling can follow.

### Proposal 2: Switch the transport to `http` hooks

Claude supports `type: "http"` hooks that POST the event JSON to a URL instead
of spawning a command. This would eliminate the per-event `gr` subprocess
(fork/exec + `ConnectFast` dial + short-lived process) and deliver the full
payload directly to a daemon endpoint.

Rejected as the primary approach, for one decisive reason and several
supporting ones:

- **The sandbox blocks it.** graith's whole security posture is that agent
  processes are sandboxed and network egress is denied by default (nono /
  safehouse). An `http` hook is the Claude process making a network connection;
  under the sandbox that connection is blocked unless we specifically grant
  loopback egress — which widens the agent's network surface precisely where
  we've worked to keep it closed. The `command` hook sidesteps this: `gr`
  connects to the **Unix socket**, which the sandbox already grants, and the
  socket connection is a file-domain grant, not network egress
  (`sandbox-socket-connect-grant`).
- **New attack surface.** An HTTP listener needs a bound port, an auth token in
  the URL/headers, and its own request handling — more to secure than reusing
  the authenticated Unix-socket control protocol.
- **Marginal benefit today.** The subprocess cost is real but small, and
  Proposal 1's `PreToolUse` scoping removes more round-trips than `http` would
  save. If per-event overhead ever shows up in a profile, `http` can be
  revisited as an *opt-in* transport gated on a loopback-egress sandbox grant —
  but it should not be the default and is not needed for this issue.

### Proposal 3: Batch-register all 21 missing events now

Register every remaining event immediately (with a generic `report-status`
handler) and grow the daemon-side handling later.

Rejected: it spawns a `gr` subprocess for every fire of events we don't consume
yet — measurable waste for zero benefit — and it front-loads risk (a
mis-registered high-frequency event could spam the daemon) with none of the
incremental testing Proposal 1 gives. Registration is only "free" when the event
has no handler; with `report-status` it always costs a process.

## Other Notes

### References

- Issue: <https://github.com/d0ugal/graith/issues/1073>
- Prior art: `docs/design/2026-06-08-agent-hooks.md` (the hook pipeline this
  extends; phase 5 enrichment is the separate cost/token work).
- Analysis: `graith-cc-integration-gaps` memory + shared-store report
  `claude-code-analysis/graith-perspective-report.md` (graith wires 6 of 27
  events).
- Key code paths:
  - `internal/daemon/hooks.go:45` — `generateClaudeSettings`, the event list.
  - `internal/daemon/daemon.go:163` — `HandleHookReport`, the event→status map.
  - `internal/cli/report_status.go:20` — `hookStdin`, the payload parse (and the
    `permission_prompt` filter at `:69`).
  - `internal/protocol/messages.go:371` — `StatusReportMsg`.
  - `internal/daemon/state.go:74` — `SessionState.HookToolName`, the existing
    runtime-only hook field to model new ones on.
  - `internal/daemon/orchestrator.go:571` — `StopReason*` constants.

### Implementation Notes

- **Backward compatibility (Claude versions).** No version detection is needed
  in either direction. Registering a hook for an event a given Claude build
  doesn't know is inert — Claude only fires events it recognises — so an older
  Claude simply never emits `SessionEnd` / `SubagentStart` / `PreCompact` and
  graith falls back to today's behaviour. In the other direction,
  `HandleHookReport`'s `default` branch already logs-and-ignores unknown events,
  so a newer Claude emitting something we haven't wired is harmless.
- **No state migration.** All new signals are runtime-only (`json:"-"`) or live
  on the `SessionInfo` wire message, mirroring `HookToolName`. `state.json` is
  untouched, so `CurrentStateVersion` (17) does not move.
- **No authmatrix change.** Reusing `status_report` means no new handler case,
  so `TestRemoteMatrixCompleteness` (`daemon/authmatrix.go`) stays green — a
  deliberate benefit of not inventing new message types
  (`new-handler-case-needs-authmatrix`).
- **SessionEnd race.** Write the reason on the `SessionEnd` report and have the
  process-exit path prefer it over `StopReasonCrash` when present. Keep it
  best-effort: a genuine crash never emits `SessionEnd`, so it correctly stays
  `crash`.
- **Truncate `last_assistant_message`** in the CLI before it hits the wire —
  it's unbounded agent output and we only want a snippet.

### Testing

- Unit tests for `HandleHookReport` per new event: `SessionEnd` sets the pending
  reason without changing `AgentStatus`; `PreCompact`/`PostCompact` toggle
  `ContextPressure` and leave status active; `SubagentStart`/`Stop` move the
  counter and don't clobber `approval`/`ready`; `idle_prompt` maps to `ready`;
  unknown/other subtypes are ignored.
- `report_status.go` parse tests: each event's JSON shape decodes into the right
  `StatusReportMsg` fields; `last_assistant_message` is truncated; the removed
  `permission_prompt` early-return no longer drops `idle_prompt`.
- `generateClaudeSettings` test: the new events are registered with the right
  handlers, and the `PreToolUse` matcher regex matches the mutating set and not
  the read-only set. Use Scots fixture strings (`braw`, `dreich`, …) per the repo
  convention.
- A regression test locking the clean-shutdown labelling: a `SessionEnd(reason:
  clear)` followed by process exit yields a non-`crash` `StopReason` (fails
  against today's code, passes with the fix).
- All tests pass under `-race`; keep overall coverage ≥ 80%.

### Open questions

- **Where does `ContextPressure` surface, and does it fire anything?** v1 just
  exposes it on `SessionInfo` for the overlay. Should it also be a trigger source
  or a `gr notify` ("session about to compact — consider handing off")? Leaning
  toward field-only for v1, trigger/notify as a fast-follow.
- **Sub-agent granularity.** Counter (recommended for v1) vs. a list of
  `{agent_id, agent_type}` for a richer overlay. Start with the counter.
- **`idle_prompt` vs. `Stop`.** Both land on `ready`. Do we need to distinguish
  "finished a turn" from "prompting for input mid-turn" in the overlay, or is one
  `ready` state enough? Proposed: one `ready` for now.
