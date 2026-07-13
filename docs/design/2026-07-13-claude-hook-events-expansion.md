---
title: "Design Doc: Wire the missing Claude Code hook events"
authors: Dougal Matthews
created: 2026-07-13
status: Draft (revised after an independent multi-model review — see Consensus)
reviewers: independent multi-model design review (3 reviewers)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1073
---

# Wire the missing Claude Code hook events

Claude Code emits on the order of two-dozen lifecycle hook events (the issue's
source snapshot counts 27); graith registers 6 of them
(`internal/daemon/hooks.go:60`) and never wires the rest. That leaves graith
blind to clean-shutdown reasons, context-window pressure, Claude's own
sub-agents, and a second idle signal — all data Claude hands us for free.

This doc argues for wiring a prioritised slice of the missing events through the
existing `command`-hook → `gr report-status` → `status_report` path — reusing
that one control message rather than inventing new ones — and extending the
status model with a few runtime-only fields. It rejects switching to `http`
hooks (they'd need a new authenticated TCP listener and would break under an
opt-in network-block sandbox policy, for marginal benefit) and rejects a
big-bang registration of all the missing events. It also proposes scoping the
`PreToolUse` approval hook so read-only tools skip the daemon round-trip —
a deliberate, and documented, change to when the approval backend is consulted.

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
`PostToolUse`, `Notification`, and `Stop`. Claude's full event set (from its own
source, cross-referenced in the `graith-cc-integration-gaps` analysis — the
issue's snapshot counted 27, though the exact number moves between Claude
releases) covers session lifecycle, tool use, sub-agents, compaction, and the
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
  represent the new signals, and make them available to the overlay / `gr list`
  (the exact v1 rendering contract is an open question, below).
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

`HandleHookReport` (`daemon.go:163`) today has a *classification* portion that is
a pure event→status switch (`daemon.go:169-185`), wrapped in side effects: it
writes `hookReports`, mutates `SessionState`, updates timestamps and tool name,
logs, and fires `onAgentStatusChange` (`daemon.go:187-231`). The new events
extend the classification switch, but several must reach *past* the
event→status map — and two must not touch `AgentStatus` at all.

- **`SessionEnd` is logical-session metadata, not a process-exit reason — this
  is the subtle one.** Claude fires `SessionEnd` on `/clear` and on switching
  sessions via interactive `/resume` *without* terminating the PTY process, as
  well as on a genuine exit. So a naive "sticky `PendingStopReason` consumed by
  the process-exit handler" is unsafe: a `SessionEnd(reason: clear)` followed by
  a continuing session and a *later* real crash would mislabel the crash as
  clean. The design must therefore:
  1. Keep a runtime `SessionEndReason` (Claude's raw value) distinct from
     graith's `StopReason`.
  2. **Clear it on the next `SessionStart`** (and on resume/restart), and bind
     it to the current agent/process generation, so a stale reason can't outlive
     the turn that produced it.
  3. Map only the *process-ending* reasons onto graith's four `StopReason`
     constants (`crash`/`idle`/`user`/`shutdown`, `orchestrator.go:571`). A
     logout / `prompt_input_exit` reason maps to `user`; `clear` / `resume` are
     explicitly *not* process exits and set no `StopReason`; `other` is treated
     as "not proof of a clean exit" and left to fall back to `crash`. Because
     `StopReason` also drives restart suppression (`orchestrator.go:464-536`
     suppresses restart only for `user`/`idle`/`shutdown`), stop-summary text
     (`lifecycle.go:25-48`), and trigger auto-cleanup
     (`trigger_actions.go:681-699`), a Claude reason may **only** become a
     `StopReason` through this explicit mapping — never by assigning the raw
     string.
  The process-exit seam it fills is `daemon.go:1913`'s
  `if s.StopReason == "" { … = StopReasonCrash }`, so the precedence is
  *already-set → pending (mapped) → crash*; an explicit `gr stop`
  (`StopReasonUser`) that ran first still wins.
- **`PreCompact` / `PostCompact`** don't change `AgentStatus` — a compacting
  agent is still active. They toggle a separate `ContextPressure` runtime
  signal. `onAgentStatusChange` isn't the right hook for this; surfacing the
  field on `SessionInfo` (see the privacy note under Schema) is enough for v1.
- **`SubagentStart/Stop`** keep `AgentStatus` active and update a runtime
  `map[agent_id]agent_type` (see Schema) — *not* a bare counter, so duplicate or
  missing events can't underflow or leak. They must not clobber the parent's
  `approval`/`ready` status.
- **`Notification` becomes subtype-aware — and this is a coupled change.** Today
  the switch maps *every* `Notification` to `approval` (`daemon.go:176`), which
  is only correct because the CLI pre-filters to `permission_prompt`
  (`report_status.go:69`). Removing that CLI filter (so `idle_prompt` can get
  through) and leaving the daemon's wholesale `Notification → approval` case
  intact would flip sessions to `approval` on every `auth_success` /
  `elicitation_*` — a regression. So the CLI-filter removal and the daemon
  sub-switch on `NotificationType` **must ship in the same PR**: `idle_prompt` →
  `ready`, `permission_prompt` → `approval`, everything else (including an empty
  or unparsed subtype) → *no status change* and log-only. The switch keys on
  `(Event, NotificationType)`, not `Event` alone.

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
ContextPressure   bool              // set by PreCompact, cleared by PostCompact
ContextPressureAt time.Time         // when the last compaction signal arrived
SubAgents         map[string]string // agent_id → agent_type; len() is the count
LastMessage       string            // truncated last_assistant_message from Stop
SessionEndReason  string            // Claude's raw SessionEnd reason (see mapping above)
```

`SubAgents` is a map, not a counter, so a duplicate `SubagentStop`, a missed
`SubagentStop`, or out-of-order delivery can't underflow or strand a count — the
count is always `len(SubAgents)` and a stop is an idempotent `delete`.

**Runtime-field lifecycle must be defined explicitly** — `json:"-"` gets us out
of a `state.json` migration, but it does *not* define when these fields reset,
and `Resume` already resets a hand-picked set (`AgentStatus`, `IdleSince`,
`StopReason`, `daemon.go:2625-2640`). So: `SessionStart` clears
`SessionEndReason`, `ContextPressure`, and `SubAgents` (a fresh turn starts
clean); resume/restart clears all four; and a daemon restart naturally empties
them (hooks re-establish the live picture as they fire, and any pressure/subagent
state that began before the daemon could observe it is simply unknown until the
next event — an accepted limitation, stated rather than glossed).

`SessionInfo` (`messages.go:392`) — mirror `ContextPressure` and the sub-agent
count so the overlay and `gr list` can show them. **`LastMessage` needs a
privacy decision before it goes on `SessionInfo`.** `list` is `remoteReadOnly`
(`authmatrix.go:50`) and `toSessionInfo` feeds list/status/attach responses
(`handler.go:2063`), so anything on `SessionInfo` is visible to a remote *guest*
— and a final assistant message can contain source, credentials, or incident
detail. For v1, `LastMessage` is captured into `SessionState` (runtime-only) but
**not** placed on `SessionInfo` unredacted; if it's surfaced at all it reuses the
existing sanitised, ~100-byte lifecycle-summary machinery (`lifecycle.go:9-23`)
rather than inventing a second raw-output snippet. These `SessionInfo` fields are
JSON-serialised on the wire but that's a protocol message, not persisted state —
no migration.

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
`permission_prompt`-only early return**, forwarding the raw `NotificationType`
(including empty, when stdin didn't parse within the 100 ms budget) and letting
the daemon route it. The CLI no longer decides what a subtype *means* — but note
the failure mode this fixes: today an unparsed or timed-out `Notification`
already slips past the CLI filter and reaches the daemon as a generic
`approval`. Under the new subtype-aware daemon switch, an empty/unknown/unparsed
`NotificationType` must map to *no status change*, not to `approval` or `ready`
(see the coupled change above), so a parse timeout can no longer spuriously flag
a session as needing attention.

#### `PreToolUse` scoping

The `PreToolUse` hook runs `gr approve-request`, a daemon round-trip on *every*
tool call. The issue asks whether an `if` filter can pre-approve some tools to
skip that round-trip. It can — but the correct design here is more careful than
"allowlist the read-only tools," and getting it wrong changes the approval
system's security semantics.

**First, correct the tempting-but-false rationale.** It is *not* true that "the
daemon only ever allows read-only tools anyway." `approve-request` forwards the
tool name/input to `SubmitApproval` (`approvals.go:25`), which applies the
configured backend. The `builtin` and `localmost` backends **defer every
non-`Bash` tool to the human approval queue** (`builtin.go:110`,
`localmost.go:75`) — so with gating on and one of those backends, a `Read` /
`Glob` / `Grep` today produces a *human prompt*, not an auto-allow. Only the
`auto` backend (and per-session yolo) allows unconditionally (`auto.go`). So
scoping read-only tools out of the hook is a **behaviour change** for gating-on
users, not a no-op.

That change is still *defensible* — a read-only tool can't mutate state or reach
the network, so gating it is prompt noise without protection — but the doc must
say that, and two constraints keep it safe:

- **Fail closed, not open.** A positive allowlist ("hook fires only for this
  mutating set") silently skips approval for any *future or renamed* Claude tool
  not in the regex — a fail-open regression that also defeats the yolo
  dangerous-command-blocklist rationale the `PreToolUse` hook exists for
  (`hooks.go:55`). So the scoping is an **exclusion** of a small, explicit set of
  known-safe read-only tools (`Read`, `Glob`, `Grep`, `LS`, `NotebookRead`),
  with the default remaining *route to the daemon*. Unknown/new tools, all
  mutating tools (`Bash`, `Write`, `Edit`, `MultiEdit`, `NotebookEdit`,
  `WebFetch`, `WebSearch`, `Task`), and every MCP tool (`mcp__*`) still
  round-trip. `TodoWrite` is **not** exempt — it mutates state; the earlier
  instinct to skip it was wrong.
- **`if` (handler-level) vs `matcher` (group-level).** Claude distinguishes a
  matcher-group `matcher` (a tool-name regex on the whole group,
  `hooks.go:74`/`112`) from a handler-level `if` (permission-rule syntax on a
  single handler; `hookHandler` today has only `Type`/`Command`,
  `hooks.go:69`). Group-`matcher` exclusion is the simpler, sufficient mechanism
  for a name-based skip and is what this proposes; handler-`if` would only be
  needed for richer per-request predicates we don't have a use for yet.

**The forward-compat assumption to name explicitly:** this scoping is safe *only
while approval policy is tool-name-based*. If a backend ever grows path-aware
rules (e.g. "deny `Read` of `~/.ssh/**`" or audit reads of sensitive paths),
excluding `Read` from the hook would silently bypass it. That assumption gets a
comment in both the code and the doc: revisit the exclusion set if approval
policy becomes path-aware.

One status caveat to record in the code: `HandleHookReport` maps `PreToolUse` to
`active`, and no *Claude* hook sends a `PreToolUse` report-status —
Claude's `PreToolUse` only runs `approve-request` (`hooks.go:91`). (Codex *does*
register a `pre-tool-use → report-status` hook, `hooks.go:208`, and
`HandleHookReport` is agent-agnostic, so the mapping is live for Codex — hence
"no *Claude* hook," not "nothing.") So scoping the Claude approval hook removes
no Claude `active` signal; `PostToolUse` (unscoped) drives the per-tool `active`
heartbeat. Comment it so a future reader doesn't "restore" a match-all matcher
thinking status needs it.

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
- **Con:** `SessionEnd` is best-effort. It's a logical-session event, not a
  process death, so its reason only labels a stop when it's a process-ending
  reason *and* no later `SessionStart` cleared it *and* the daemon didn't restart
  in the window between `SessionEnd` and process exit (the reason is `json:"-"`,
  so a daemon restart in that gap loses it and the stop falls back to `crash`).
  A hard crash never emits `SessionEnd` and correctly stays `crash`. All three
  fallbacks land on the safe/honest outcome.
- **Con:** Sub-agent tracking is a `map[agent_id]agent_type`, not a full tree;
  we know which sub-agents and how many, not their nesting or transcripts.
  Sufficient for the overlay; richer modelling can follow.

### Proposal 2: Switch the transport to `http` hooks

Claude supports `type: "http"` hooks that POST the event JSON to a URL instead
of spawning a command. This would eliminate the per-event `gr` subprocess
(fork/exec + `ConnectFast` dial + short-lived process) and deliver the full
payload directly to a daemon endpoint.

Rejected as the primary approach — but *not*, as an earlier draft of this doc
wrongly claimed, because the sandbox denies network egress by default. It does
not: nono is **allow-by-default** for network, and a `[sandbox.network] block`
policy is opt-in (`sandbox.go:70` — "Nil means no network restriction (nono is
allow-by-default)"; `config.go:1151`; the nono design doc `:427`). safehouse has
no network primitive at all and merely *warns* that it can't enforce one. Claude
itself needs outbound network to function, so a blanket default-deny was never
plausible. The real reasons `command` wins:

- **A new authenticated listener is real cost and real surface.** An `http` hook
  is Claude POSTing to a URL, which means graith must run a bound TCP listener
  with its own auth token, port lifecycle, and request parsing — a second
  authenticated entry point to secure, alongside the existing Unix-socket control
  protocol. The `command` hook reuses that existing protocol: `gr` connects to
  the daemon **Unix socket**, which is already granted to sandboxed agents via
  `WrapOpts.UnixSockets` (`sandbox.go:41`). Note the socket connect *is*
  classified as network egress by Seatbelt/Landlock — it works not because Unix
  sockets are exempt, but because graith explicitly whitelists that one socket
  path (`sandbox-socket-connect-grant`); an `http` hook to a TCP port is *not*
  whitelisted.
- **`http` breaks under an opt-in network-block policy.** Precisely because an
  operator *can* set `[sandbox.network] block = true` (with an `allow_domains`
  allowlist), an `http` hook would need `127.0.0.1` explicitly allowlisted per
  session to survive that config — widening the profile exactly for the operators
  who chose to tighten it. The whitelisted Unix socket keeps working under
  `block = true` with no extra grant.
- **Marginal benefit today.** The subprocess cost is real but small, and
  Proposal 1's `PreToolUse` scoping removes more round-trips than `http` would
  save. If per-event overhead ever shows up in a profile, `http` can be
  revisited as an *opt-in* transport gated on an explicit loopback grant — but it
  should not be the default and is not needed for this issue.

### Proposal 3: Batch-register all 21 missing events now

Register every remaining event immediately (with a generic `report-status`
handler) and grow the daemon-side handling later.

Rejected: it spawns a `gr` subprocess for every fire of events we don't consume
yet — measurable waste for zero benefit — and it front-loads risk (a
mis-registered high-frequency event could spam the daemon) with none of the
incremental testing Proposal 1 gives. Registration is only "free" when the event
has no handler; with `report-status` it always costs a process.

## Consensus

An independent review by three reviewers (run in parallel, verdicts reconciled
here) unanimously endorsed the direction — Proposal 1's incremental
command-hook architecture, reusing `status_report`, with the `PreToolUse`
optimisation — and every code citation was independently verified as accurate.
The review changed the *arguments and safety details*, not the recommendation.
Four substantive corrections, all now folded into the doc above:

1. **The `http`-vs-`command` argument was factually wrong** and has been
   rewritten. An earlier draft claimed the sandbox denies network egress by
   default; it does not (nono is allow-by-default, `sandbox.go:70`; network-block
   is opt-in). Two reviewers accepted the wrong premise; one refuted it with
   code citations, which held up on direct inspection. `command` still wins, but
   now for the right reasons: reusing the already-whitelisted Unix socket (itself
   *network egress* that graith explicitly grants) vs. standing up a new
   authenticated TCP listener that breaks under an opt-in `block = true` policy.
2. **The `PreToolUse` scoping justification was wrong and the mechanism was
   fail-open.** "The daemon only ever allows read-only tools" is false —
   `builtin`/`localmost` *defer* non-`Bash` tools to the human queue
   (`builtin.go:110`, verified). Scoping is now framed honestly as a behaviour
   change, made **fail-closed** (exclude a known read-only set; unknown/new tools
   still route), `TodoWrite` removed from the skip list, and the
   `matcher`-vs-`if` distinction spelled out.
3. **`SessionEnd` is a logical-session event, not process death.** It fires on
   `/clear` and `/resume` without exiting. The naive sticky-reason model could
   mislabel a later crash as clean. The design now separates `SessionEndReason`
   from `StopReason`, clears it on `SessionStart`/resume, and maps only
   process-ending reasons into graith's four `StopReason` constants.
4. **Runtime-field lifecycle, sub-agent robustness, and `LastMessage` privacy**
   were underspecified. Sub-agents are now a `map[agent_id]agent_type` (no
   underflow), field reset on `SessionStart`/resume is defined, and
   `LastMessage` is kept off the guest-visible `SessionInfo` unredacted.

Smaller fixes (Notification subtype coupling shipping as one PR, backward-compat
softened to a verifiable assumption, "27 events" marked version-sensitive, the
agent-agnostic note, the missing H1) were also applied. No open disagreement
remains on the recommendation; the remaining decisions are captured under Open
questions.

## Other Notes

### References

- Issue: <https://github.com/d0ugal/graith/issues/1073>
- Prior art: `docs/design/2026-06-08-agent-hooks.md` (the hook pipeline this
  extends; phase 5 enrichment is the separate cost/token work).
- Analysis: the `graith-cc-integration-gaps` finding (an internal analysis
  captured as an agent memory) + shared-store report
  `claude-code-analysis/graith-perspective-report.md` (graith wires 6 of the
  ~27 events in that Claude snapshot).
- Claude Code hooks reference (event names, `SessionEnd` reasons, matcher/`if`
  semantics): <https://code.claude.com/docs/en/hooks>. The exact event *count*
  is version-sensitive — treat the issue's "27" as a snapshot, not an invariant.
- Approval backends this design must not bypass: `internal/approvals/builtin.go`,
  `internal/approvals/localmost.go`, `internal/approvals/auto.go`,
  `internal/daemon/approvals.go` (`SubmitApproval`).
- Sandbox network model (for the `http`-vs-`command` argument):
  `internal/sandbox/sandbox.go:41` (`UnixSockets`), `:70` (`Network`),
  `docs/design/2026-07-02-nono-sandbox-design.md:427`.
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

- **Backward compatibility (Claude versions).** The runtime-degradation
  direction is solid: `HandleHookReport`'s `default` branch already
  logs-and-ignores unknown events (`daemon.go:182`), and an older Claude that
  never emits `SessionEnd` / `SubagentStart` / `PreCompact` simply leaves graith
  on today's behaviour. The one claim to *verify rather than assert* is that an
  older Claude's `settings.json` schema validator accepts an event key it
  doesn't recognise (rather than rejecting the whole generated settings file).
  This is expected but not proven by this repo — the existing settings tests
  (`hooks_test.go`) only check graith's JSON shape. Mitigation: an integration
  test against a pinned older Claude, or, if a version is found that rejects
  unknown keys, gate the extra event registrations behind a detected-version
  check. Until then the claim is stated as an assumption, not a fact.
- **The daemon-side handling is agent-agnostic.** `HandleHookReport` keys on the
  event name, not the agent, so once these events are wired the same handling
  applies to any agent that emits them. This change touches only Claude's
  `generateClaudeSettings` registration; Codex/Cursor can wire the same events
  later (via `injectCodexHooks` / `injectCursorHooks`) with no further daemon
  work — which is why Codex parity is a Non-Goal, not a blocker.
- **No state migration.** All new signals are runtime-only (`json:"-"`) or live
  on the `SessionInfo` wire message, mirroring `HookToolName`. `state.json` is
  untouched, so `CurrentStateVersion` (17) does not move.
- **No authmatrix change.** Reusing `status_report` means no new handler case,
  so `TestRemoteMatrixCompleteness` (`daemon/authmatrix.go`) stays green — a
  deliberate benefit of not inventing new message types
  (`new-handler-case-needs-authmatrix`).
- **SessionEnd lifecycle** (the correctness-critical piece, expanded from the
  mapping section). Store Claude's raw reason as a runtime `SessionEndReason`;
  clear it on the next `SessionStart` and on resume/restart; bind it to the
  current process generation; and only *map* process-ending reasons
  (`logout`/`prompt_input_exit` → `user`) into `StopReason` at the exit seam
  (`daemon.go:1913`, precedence *already-set → mapped → crash*). `clear`/`resume`
  set no `StopReason`; `other` falls back to `crash`. A raw Claude string must
  never be assigned to `StopReason` directly, because that field also drives
  restart suppression, stop summaries, and trigger auto-cleanup.
- **Truncate `last_assistant_message`** in the CLI before it hits the wire (bound
  in runes/bytes) — it's unbounded agent output and we only want a snippet — and
  keep it off `SessionInfo` unredacted (see the privacy note under Schema).

### Testing

- Unit tests for `HandleHookReport` per new event: `SessionEnd(logout)` records
  a reason that maps to `StopReasonUser` without changing `AgentStatus`;
  `PreCompact`/`PostCompact` toggle `ContextPressure` and leave status active;
  `SubagentStart`/`Stop` update the `SubAgents` map and don't clobber
  `approval`/`ready`; `idle_prompt` → `ready`; `permission_prompt` → `approval`;
  `auth_success`/`elicitation_*`/empty subtype → *no status change*.
- **Adverse SessionEnd lifecycle** (the correctness-critical cases): `SessionEnd(clear)
  → SessionStart(clear) → later crash` yields `crash`, *not* clean (the pending
  reason was cleared); `SessionEnd(resume) → SessionStart(resume)` doesn't stop
  the session; `SessionEnd(other)` falls back to `crash`; an explicit `gr stop`
  in flight (`StopReasonUser`) takes precedence over a pending reason; a
  duplicate/missing `SubagentStop` neither underflows nor strands the count.
- `report_status.go` parse tests: each event's JSON shape decodes into the right
  `StatusReportMsg` fields; `last_assistant_message` is truncated; a timed-out /
  unparsed `Notification` forwards an empty subtype that the daemon maps to *no
  status change* (regression for the "unparsed notification became `approval`"
  bug).
- **PreToolUse scoping**: the exclusion covers exactly the known-safe read-only
  set; an *unknown/new* tool name and every MCP tool still route to the daemon
  (fail-closed); `TodoWrite` is not exempt; yolo mode and each configured backend
  (`auto`/`builtin`/`localmost`) still see the mutating tools.
- **Privacy**: `LastMessage` is not exposed unredacted on a guest `list`
  response.
- `generateClaudeSettings` test: the new events register with the right handlers
  and the `PreToolUse` group `matcher` excludes the read-only set. Use Scots
  fixture strings (`braw`, `dreich`, …) per the repo convention.
- A regression test locking the clean-shutdown labelling: `SessionEnd(reason:
  logout)` followed by process exit yields `StopReasonUser`, not `crash` (fails
  against today's code, passes with the fix).
- All tests pass under `-race`; keep overall coverage ≥ 80%.

### Open questions

- **The v1 rendering contract.** Adding fields to `SessionInfo` makes them
  *available*, not *shown* — `toSessionInfo` (`handler.go:2063`) is only the
  conversion point; `gr list` and the overlay must be changed to render them.
  Does v1 include the overlay/`gr list` display of `ContextPressure` and the
  sub-agent count, or does it stop at populating the fields and defer rendering?
  Proposed: populate + a minimal overlay indicator for context pressure in v1;
  richer display deferred. This needs pinning before Accepted so the Goals'
  "surface the signals" promise isn't left half-met.
- **Does `ContextPressure` fire anything beyond display?** Should it be a trigger
  source or a `gr notify` ("session about to compact — consider handing off")?
  Leaning toward field + overlay indicator for v1, trigger/notify as a
  fast-follow.
- **`LastMessage` exposure.** Resolved toward "captured runtime-only, not on the
  guest-visible `SessionInfo` unredacted" (see Schema) — but if a use case wants
  it shown, the redaction/truncation contract (reuse `lifecycle.go`'s sanitiser?)
  needs signing off.
- **`idle_prompt` vs. `Stop`.** Both land on `ready`. Do we need to distinguish
  "finished a turn" from "prompting for input mid-turn" in the overlay, or is one
  `ready` state enough? Proposed: one `ready` for now.
