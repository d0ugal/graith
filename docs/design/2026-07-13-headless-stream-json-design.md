---
title: "Design Doc: Headless stream-json mode for fire-and-forget sessions"
authors: Dougal Matthews
created: 2026-07-13
status: Draft
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1075
---

# Headless stream-json mode for fire-and-forget sessions

graith runs **every** Claude Code session as an interactive PTY, even sessions
no human will ever attach to — tribunal judges, trigger-spawned briefings,
model pickers, evaluators, mirror sessions. For those, the PTY is pure overhead:
graith scrapes rendered terminal text to guess status and has no clean access to
cost, tokens, or a reliable interrupt. Claude Code offers a headless mode
(`-p --output-format stream-json`) that emits typed JSON events plus a
cost/token/usage envelope, and — driven the way the Agent SDK drives it —
exposes a bidirectional control protocol on stdio. This doc proposes a second
**session driver** alongside the PTY driver: `headless = true` sessions run the
agent in stream-json mode, so graith gets structured status, cost/tokens, and
clean interrupts for free, and converts a headless session to an interactive PTY
on demand via `claude --resume`.

## Background

**How a session runs today.** `SessionManager` owns a map of live PTY sessions,
`sessions map[string]*grpty.Session` (`internal/daemon/daemon.go:66`). Every
launch path — `Create` (`daemon.go:1292`), `Resume`, `Fork`, orchestrator
creation (`orchestrator.go:206`), and `AdoptSessions` on daemon restart
(`daemon.go:382`) — builds a `*pty.Session` (`internal/pty/session.go`). The PTY
session runs a `readLoop` that fans the child's raw output to (a) an append-only
`Scrollback` file, (b) an in-memory vt10x screen for previews, and (c) any
attached client writers. Input is written straight to the ptmx; interrupts are
`Ctrl-C` bytes (`Session.Interrupt`, tuned per-agent because Claude's TUI
needs two presses — issue #620).

**How status is detected today.** Two mechanisms, layered:

1. **Scraping** (`internal/detector/detector.go`): every 500ms the daemon renders
   the vt10x screen to text and matches hardcoded string literals ("esc to
   interrupt", ~90 thinking words, spinner glyphs) to guess `active` / `approval`
   / `ready`. Fragile — Anthropic rotates these strings between releases.
2. **Agent hooks** (`docs/design/2026-06-08-agent-hooks.md`, issue #1073): Claude
   Code fires lifecycle hooks (`SessionStart`, `PreToolUse`, `Stop`, …) that shell
   out to `gr report-status`, which sends a structured `status_report` control
   message. Hook reports are authoritative for `approval`; scraping is the
   fallback.

**How the agent session id works.** graith *generates* the Claude session id
itself and passes it in: `args = ["--session-id", "{agent_session_id}"]`, and
`resume_args = ["--resume", "{agent_session_id}"]`
(`internal/config/default_config.toml:328-333`). This matters enormously for this
design — because graith already knows the id up front, resuming a headless
session in an interactive PTY is a launch that already exists.

**What Claude Code's headless mode offers.** Per the issue and Claude Code's
source (`src/cli/structuredIO.ts`, `src/cli/print.ts`):

- `claude -p --output-format stream-json --verbose` emits newline-delimited JSON:
  a `system`/`init` message, then `assistant` / `user` / tool messages, then a
  terminal `result` message.
- The `result` envelope carries `total_cost_usd`, `usage` (input/output/cache
  tokens), `num_turns`, `duration_ms`, `duration_api_ms`, `session_id`,
  `subtype`, and `is_error`.
- Adding `--input-format stream-json` turns stdin into a message channel and — the
  way the Agent SDK launches the binary — enables a **control protocol**
  (`control_request` / `control_response`, schemas in
  `src/entrypoints/sdk/controlSchemas.ts:57-658`): `initialize`, `interrupt`,
  `get_context_usage`, `set_model`, `set_permission_mode`, `mcp_reconnect`,
  `stop_task`, `get_settings`, and inbound `can_use_tool` permission requests.
- `--include-partial-messages` streams token deltas and `--include-hook-events`
  embeds hook events in the stream — two *separate* flags. Neither is needed for
  v1's status/cost goals (they only add event volume), so v1 omits both.

**Data readers that already exist.** `internal/agent/transcript/` parses Claude
and Codex JSONL transcripts (for cross-agent conversation migration). These are
the same message shapes headless mode emits live — so a headless reader is close
kin to code that already ships.

## Problem

Running a never-attached session as a PTY is the wrong tool, and it costs us:

1. **Fragile status.** Scraping is a guess against a moving target. For a
   tribunal judge or a trigger briefing — where graith's *only* interest is "is
   it working, is it done, did it error" — we scrape a rendered TUI to infer what
   the agent already knows and would happily tell us as JSON.
2. **No structured data.** Cost, token usage, and context-window pressure exist in
   Claude Code's `result` envelope but are invisible behind the PTY. Issue #644
   (token accounting) has to reconstruct them from transcript files after the
   fact; a headless session would hand them over live, per turn.
3. **Dirty interrupts.** Interrupting a PTY agent means firing `Ctrl-C` bytes and
   hoping the TUI honours them (issue #620's two-press tuning exists precisely
   because this is unreliable). The control protocol has a first-class
   `interrupt` request.
4. **Wasted machinery.** vt10x rendering, screen diffing, and 500ms scrape ticks
   run for sessions that produce no rendered UI anyone will read.

The sessions that hurt most are exactly the fire-and-forget ones: tribunal judges
(spawned by `/ship-it`), trigger actions (morning briefing, cleanup), short-lived
helpers (model pickers, evaluators), scenario workers meant to run in the
background, and `--mirror` sessions (read-only by definition).

## Goals

- A `headless = true` session option that launches the agent in stream-json mode
  instead of a PTY, for sessions that will not be attached.
- Structured status (`active` / `approval` / `ready` / `stopped`) derived directly
  from typed stream messages — no scraping, no per-release string tuning.
- Cost / token / usage captured live from the `result` envelope and fed to the
  existing metadata surfaces and to #644.
- Clean `interrupt` and `get_context_usage` via the control protocol.
- A one-way **convert-to-interactive** path: `gr attach` on a headless session
  stops the stream-json process and relaunches it as an interactive PTY via
  `claude --resume <session-id>`, preserving conversation state.
- A read-only alternative: `gr logs -f` renders the structured stream without
  converting.
- No regression for the default interactive PTY path — headless is opt-in and
  additive.

### Non-Goals

- **Replacing the PTY driver.** Interactive attach stays PTY-based. This adds a
  driver; it does not remove one.
- **Persistent multi-turn headless sessions in v1.** v1 headless is **one-shot**:
  one initial message, run to the terminal `result`, exit. Keeping stdin open for
  follow-up traffic (inbox delivery, ensure-reactor prompts) — with its
  turn-idle-vs-done and background-task semantics — is deferred until those are
  designed. See the Lifecycle subsection for why conflating the two breaks
  trigger cleanup.
- **Headless for agents other than Claude Code in v1.** Codex has a JSON
  experimental mode but a different (and less stable) protocol; it is a follow-up
  (see Open questions). opencode / cursor / agy stay PTY-only.
- **Bidirectional live typing into a headless session from a human.** Sending
  input to a headless session is for graith's own orchestration (prompts,
  interrupts). A human who wants to type converts to interactive.
- **A general-purpose Agent-SDK client.** graith speaks the subset of the control
  protocol it needs, not the whole SDK surface.
- **Persisting the full structured stream.** We render it to the existing
  scrollback file and keep the last `result` envelope; we do not build a new
  event store (the transcript file on disk already is one).

## Proposals

### Proposal 0: Do Nothing

Keep every session on the PTY driver and keep improving status detection through
the agent-hooks work (#1073). Hooks already give us authoritative `approval`
status without scraping, and #644 can mine transcript files for cost/tokens
after the fact.

**Why it's not enough.** Hooks are a bolt-on: they require injecting a settings
file and a wrapper script, they shell out to `gr report-status` on every event
(a process spawn per hook), and they still ride on top of a PTY whose rendered
output nobody reads. They give us *status* but not a clean *interrupt*, not
*context-usage on demand*, and not a live cost envelope — those need the control
protocol or the result message, which only exist in stream-json mode. And the
PTY overhead (vt10x, scraping) remains pure waste for fire-and-forget sessions.
Do-nothing leaves the structural mismatch — interactive transport for
non-interactive work — in place.

### Proposal 1: A `headless` session driver behind a driver interface (Recommended)

Introduce a second driver. `SessionManager` stops holding
`map[string]*grpty.Session` directly and instead holds
`map[string]sessionDriver`, an interface both drivers satisfy. The PTY driver is
today's `*pty.Session` behind a thin adapter; the headless driver is a new
`internal/headless.Session` that speaks stream-json.

#### The driver interface

The interface is defined by what the daemon *actually uses* on `*pty.Session`
today — the methods called from `daemon.go`, `handler.go`, `wait.go`, `notify.go`,
`upgrade.go`, and the watcher. The illustrative shape below is **not exhaustive**:
the full call surface also includes `Fd()` (daemon upgrade FD handoff,
`upgrade.go`), `PeakRSSBytes()` (exit reporting), `RecentlyAdopted()`,
`ScreenSnapshot()` (attach screen restore), and `NotifyUserInput()` /
`WaitForUserIdle()` (idle tracking). Several of these are PTY-shaped, which
motivates a **capability split** (see below) rather than one fat interface full of
no-ops. Note also that `Scrollback` is today an exported *field* on `*pty.Session`,
not a method, so the adapter either exposes an accessor or the field is renamed —
a real, if small, cost. Roughly:

```go
// internal/daemon/driver.go
type sessionDriver interface {
    // Lifecycle
    ProcessPID() int
    Done() <-chan struct{}
    Exited() bool
    ExitCode() int
    ExitSignal() syscall.Signal
    Kill() error
    ForceKill() error
    Close()

    // I/O the daemon issues on the session's behalf
    WriteInput(data []byte) error
    WriteInputAndSubmit(data []byte) error
    Interrupt(count int, delay time.Duration) error

    // Output surfaces (attach, logs, preview, idle/status detection)
    Scrollback() *pty.Scrollback
    ScreenPreview() string
    LastOutputAt() time.Time
    Attach(w io.Writer)
    Detach()
    DetachWriter(w io.Writer)

    // PTY-only; headless returns nil / no-op
    Resize(rows, cols uint16) error
    Poke()

    // Driver identity + structured extras
    Kind() DriverKind          // "pty" | "headless"
    Snapshot() DriverSnapshot  // status, last result envelope, context usage
}
```

**Capability split, not no-ops.** A single interface stuffed with `Resize`/`Poke`
no-ops makes unsupported operations *look* successful. Better: a core
lifecycle/output `sessionDriver`, plus an optional `interactiveDriver`
(`Resize`, `Poke`, `ScreenSnapshot`, `Fd`, user-idle) that only the PTY driver
implements, and an optional `structuredDriver` (`control(req)`, `Snapshot()`,
raw-event subscription) that only the headless driver implements. Call sites
type-assert for the capability they need, so "headless can't resize" is a
compile-visible fact, not a silent success. `Snapshot()` is the new structured
capability: it returns the structured status and the most recent
cost/token/context data the headless reader has seen. `ScreenPreview()` on a
headless driver returns a rendered tail of the scrollback (there is no vt10x
screen), so the overlay preview and the `screen_preview` control message degrade
gracefully instead of requiring a live PTY — the `screen_preview` handler
(`handler.go:1196`) must branch on driver kind accordingly.

Crucially, **`Scrollback` is reused unchanged.** The headless driver renders the
structured stream into human-readable text and writes it to the *same*
append-only scrollback file the PTY uses. That means `gr logs`, the overlay
preview, and `gr wait --contains` all work against a headless session with no
changes — they read scrollback, and scrollback is populated either way.

#### The headless session

`internal/headless/session.go` — mechanically close to `pty.Session` but backed
by pipes instead of a ptmx. The **launch contract** matches how the Agent SDK
launches the CLI, because that is the only place the control protocol is
exercised in the wild:

```
claude -p --output-format stream-json --input-format stream-json --verbose \
       --permission-prompt-tool stdio        # only when graith owns approvals
  ├─ stdin   ← initialize, control_request, user messages   (graith writes)
  ├─ stdout  → system/init, stream-json events, control_response  (graith reads)
  └─ stderr  → diagnostics                                   (→ scrollback, tagged)
```

Two flags are load-bearing beyond the obvious ones: `--input-format stream-json`
turns stdin into the control/message channel, and `--permission-prompt-tool
stdio` is what routes ordinary permission prompts to graith as inbound
`can_use_tool` requests. Without the latter the CLI will *not* ask graith for
tool approval, so the approvals design below depends on it.

- **initialize handshake (mandatory).** Before the session is usable, graith
  sends an `initialize` control request and waits for the response. The response
  (and the `system/init` message's `capabilities`) is how graith
  **feature-detects** what the control protocol supports on the installed CLI —
  preferred over version-sniffing (Claude Code describes capabilities as an open
  set specifically to avoid version pins). If `initialize` fails, times out, or
  returns an unexpected shape, session creation **fails closed** (or, by explicit
  config, converts to PTY) rather than silently running without approval routing.
- **readLoop**: reads stdout with a growable buffer (a plain `bufio.Scanner` caps
  tokens at ~64KB and stream-json lines carrying large tool outputs or base64
  images exceed that — the transcript reader already raises its limit to 16MiB,
  `internal/agent/transcript/claude.go:97`). Decodes each JSON envelope, updates
  in-memory status, appends a rendered line to scrollback, and — on the terminal
  `result` message — stores the cost/usage/`session_id` snapshot. A malformed
  *data* line is logged and skipped; a malformed *control* frame or repeated
  decode failures mark the driver **degraded** and fail any pending control
  requests, so approvals never block forever. A non-JSON line (e.g. an early
  crash banner) is written to scrollback verbatim so failures are visible.
- **writer (single mutex).** `WriteInput`, `Interrupt`, `initialize`, and
  `can_use_tool` responses all write NDJSON lines to the *one* stdin pipe, so a
  write mutex serialises them — concurrent writers would otherwise interleave
  partial lines and corrupt the stream. `WriteInput` sends an `SDKUserMessage`
  (`{type, message, parent_tool_use_id}`), not a bare string. `Interrupt` sends a
  `control_request` of type `interrupt` (monotonic request id) rather than
  `Ctrl-C` bytes — clean and acknowledged.
- **control**: a `control(req)` helper matches `control_response` to
  `control_request` by id, so `interrupt`, `get_context_usage`, `set_model`, etc.
  can be issued synchronously with a timeout; unmatched/duplicate ids and
  process-exit-with-pending-controls are error paths, not hangs.

#### Message → status mapping

Directly from typed events — no string matching:

| stream-json event                         | graith status | Notes                          |
|-------------------------------------------|---------------|--------------------------------|
| `system`/`init`                           | `active`      | session started                |
| `assistant` (text / tool_use)             | `active`      | agent is working               |
| `user` (tool_result)                      | `active`      | tool finished, loop continues  |
| inbound `can_use_tool` control request    | `approval`    | needs a permission decision    |
| `result` (`is_error: false`)              | `ready`       | turn complete, awaiting input  |
| `result` (`is_error: true`)               | `ready`+error | turn errored; surface it       |
| stdout EOF / process exit                 | `stopped`     | existing exit path             |

This replaces the scraper *and* graith's generated status hook for headless
sessions: `approval` becomes authoritative from `can_use_tool` with no injected
status hook, and `ready` is a real turn-boundary event, not an inferred idle.

#### Lifecycle: v1 is one-shot

A subtlety the status table alone hides: a `result` message is a **turn
boundary**, not necessarily process completion, and a persistent streaming
session (stdin held open) emits a `result` per turn and then *waits* for more
input. There are two lifecycle models, and conflating them breaks graith's
cleanup:

1. **one-shot headless (v1):** graith sends one initial `SDKUserMessage`, reads
   until the terminal `result`, closes stdin, waits for process exit, and records
   success/failure. The process *exits* — which is exactly what tribunal judges,
   trigger briefings, and evaluators want, and what graith's teardown assumes.
2. **persistent headless (deferred):** stdin stays open for follow-up traffic
   (inbox delivery, ensure-reactor prompts); `result` means turn-idle, not done.

This distinction is not cosmetic — it collides with trigger auto-cleanup, which
only fires on `StatusStopped`: `auto_cleanup = "on_success"` never idle-stops
(`trigger.go:227`) and only reaps a clean self-exit, so a *persistent* headless
briefing that emits a successful `result` and waits on stdin would **leak
indefinitely**; `auto_cleanup = "always"` would idle-kill it after 1m and record
it as an idle-stop rather than natural success. **v1 is therefore one-shot only**
(see Non-Goals) — the process completes and exits, so `on_success`/`always`
cleanup, tribunal collection, and `gr wait --status stopped` all behave exactly
as they do for PTY sessions. Persistent multi-turn headless (and its background-
task lifecycle) is a follow-up once inbox delivery and turn-idle semantics are
designed.

#### Config surface

A `headless` flag, defaulting off, resolvable at four levels (later overrides
earlier):

1. **Global** — `[headless] experimental = false` (the master gate; headless is
   inert unless this is on in v1) and `[headless] default = false` (once enabled,
   whether new sessions go headless by default). A per-agent
   `[agents.claude] headless_capable = true` marks which agents support it, so
   non-supporting agents can't be asked to go headless.
2. **`gr new --headless`** — a bool flag on `internal/cli/new.go`, plumbed through
   `CreateOpts` and validated at `Create` (resolving to `DriverKind`).
3. **Trigger config** — `[trigger.action] headless = true` for `type = "session"`
   actions (morning briefing, cleanup reactors).
4. **Auto-inferred for `--mirror`** — mirror sessions are read-only by
   construction; they are strong candidates to default to headless (see Open
   questions — this is opt-in in v1, not forced).

`SessionState` gains a persisted **`DriverKind`** (`"pty"` | `"headless"`),
resolved *once* at creation and never re-derived from live config — so a config
reload can't flip a running session's transport. This is cleaner than a bare
`Headless bool`: the four config *inputs* above are each tri-state-ish (a bool
can't distinguish "unset" from "explicit false" when layering overrides), but
they collapse to a single resolved `DriverKind` at `Create` time. It's a
state-version bump (`CurrentStateVersion` in `state.go`) with a forward migration
that defaults absent → `"pty"`; the bump is needed because the field is
authoritative, not merely additive.

**Validation, not silent fallback.** If `--headless` is requested for an agent
that can't do it (`!agentSupportsHeadless(agent)`), `Create` returns an *error* —
a silent downgrade to PTY hides a config mistake. Launch selection then branches
on the resolved `DriverKind` at every launch site: `Create` (`daemon.go:1292`),
`Resume`, `Fork`, `AdoptSessions` (`daemon.go:382`), **and** orchestrator
creation (`orchestrator.go:206`, a fifth direct `grpty.NewSession` the first
pass of this doc missed — the orchestrator is declared permanently PTY-only, so
it stays on the PTY driver by construction). The headless command construction
reuses the agent's configured `command`, prepends the stream-json launch flags,
and keeps the templated `args` (so `--session-id {agent_session_id}` is still
passed and graith still owns the id).

#### Convert-to-interactive on attach

`gr attach <headless-session>` cannot stream a PTY — there is no ptmx. So attach
**converts**:

1. Daemon sees the target is a headless driver.
2. It prompts the client for confirmation (unless `--yes`): *"`braw` is a headless
   session. Attaching will restart it as an interactive session (conversation is
   preserved). Continue?"*
3. On confirmation: send an **`interrupt`** control request (not `stop_task` —
   `stop_task` targets a background task by `task_id`; `interrupt` is the
   foreground-turn operation in the control schema), wait for its
   `control_response` *and* the settling `result`/turn boundary, then close stdin
   and wait for process exit with a bounded TERM/KILL fallback. Then relaunch
   like `Resume` — `claude --resume {agent_session_id}` in a real `pty.Session`.
   Because graith already owns the session id and `resume_args` already exist, the
   *destination* is the existing resume path with the driver flipped to PTY.
4. Commit the transition atomically and attach normally.

**This needs a dedicated transactional swap, not a bare `Resume` call.** `Resume`
returns immediately when a session is already running (`daemon.go:2143`), whereas
conversion must *replace a live driver* under the manager lock: the persisted
`DriverKind = "pty"`, the new PID, and the new PTY driver install as one
transition with rollback, guarded so concurrent attach, interrupt, inbox
injection, stop/delete, and the old driver's exit watcher can't race the swap.
The pointer-based watcher staleness check (`daemon.go:1878`) helps but isn't
sufficient on its own.

**Preserved:** conversation history (Claude reloads it from its transcript on
`--resume`), the worktree, the branch, the graith session id, and all env.
**Lost:** the in-flight turn (the `interrupt` is issued first — a tool call
mid-flight is cancelled, not resumed) and the live control channel.

The claim that the resumed PTY "starts fresh at the prompt with history intact"
must be **validated against a real Claude CLI version**, not just a fake agent:
an interrupted transcript may contain a dangling tool turn, and how `--resume`
renders that is version-dependent. This is a required integration test (see
Testing), not an assumption.

Edge cases:

- **Attach during an active tool call** — `interrupt` first; the resumed PTY
  session starts at the prompt with history intact (subject to the interrupted-
  transcript validation above). The tool call is cancelled, not silently
  continued.
- **Attach during an API wait** — clean; no tool state to cancel.
- **Attach after completion (`result` seen, awaiting input)** — cleanest case;
  just relaunch and attach.

#### `gr logs -f` as read-only alternative

For inspection *without* converting, `gr logs -f` on a headless session streams
the rendered scrollback (already populated by the readLoop). A `--structured`
flag can additionally emit the raw JSON events for tooling. This is the
recommended way to watch a fire-and-forget session; convert only when you need to
take the wheel.

#### Control-protocol usage, phased

- **Immediately (v1):** `interrupt` (replaces Ctrl-C for headless), the terminal
  `result` envelope (cost/tokens), and `can_use_tool` handling (approvals).
- **Soon (v1.1):** `get_context_usage` — surfaced in the overlay/`gr list` as
  context-window pressure, and usable by idle/rollover logic.
- **Future / optional:** `set_model`, `set_permission_mode`, `mcp_reconnect`,
  `get_settings` — these enable live model switches and MCP repair without a
  restart, but are not required for the core feature.

#### Approvals in headless mode

Two mechanisms coexist, and headless prefers the structured one:

- **PTY today:** approvals are detected via the agent-hooks `PreToolUse` hook (or
  scraped) and, under `[approvals]`, routed to a backend. The hook shells out.
- **Headless:** with `--permission-prompt-tool stdio` set, the agent sends an
  inbound `can_use_tool` control request on stdout. graith answers with a
  `control_response` (allow/deny) **inline on stdin** — no injected hook, no
  process spawn, correlated by request id. graith reuses the *decision logic* of
  the existing `[approvals]` backends and its `SubmitApproval` queue; only the
  transport into the agent differs. `yolo` sessions auto-allow every
  `can_use_tool` (`approvals.go:33` already routes yolo through the auto backend).

**The `human` backend would hang a headless session forever.** `can_use_tool`
*blocks the agent* on stdout until graith answers on stdin, but a headless session
is by definition one no human will attach to — so a policy resolving to the
`human` backend has nobody to answer. v1 rule: a headless session's approval
policy **must be non-blocking** (`auto` / `external` / `yolo`); a `human`
resolution either fails at creation or applies a bounded control-response timeout
that **denies** (and escalates once to the orchestrator inbox) rather than
blocking. This is a stated rule, not left implicit.

This makes graith's generated `PreToolUse` approval hook **redundant for headless
sessions** — one of the concrete simplifications headless buys us. Custom/user
Claude hooks are untouched; only graith's own generated status/approval hooks are
skipped (see the MCP note below — skipping hook *generation* must not also drop
MCP config).

#### Data extraction → #644 and #1073

- **#644 (token accounting):** the `result` envelope carries `total_cost_usd`,
  `usage`, and `num_turns`, captured into the driver snapshot. Two honest caveats
  the first draft glossed:
  - **This is net-new code, not a struct to "share."** The `internal/agent/
    transcript` decoders parse *on-disk transcript* records (`claudeRecord` /
    `claudeMessage` / `claudeBlock`, `claude.go:56`) — **message shapes only**.
    There is no existing "Claude result" struct, no cost/usage/duration types:
    the `result` summary is a stream-json-only construct that never appears in the
    transcript JSONL. The *message-shape* decoders are close kin and their nested
    types can be extracted and shared (they're currently unexported); the
    **`result` envelope is a new struct** the headless reader introduces.
  - **The daemon protocol doesn't carry cost/usage yet.** `StatusReportMsg`
    (`protocol/messages.go`) is the minimal 4-field struct; the `UsageReport` /
    `ContextReport` enrichment fields are *designed* in the agent-hooks doc's
    Phase 5 but **not implemented**. So Phase 6 here includes that protocol/state
    extension — it is not something headless can "feed" into today.

  The live envelope is actually *cleaner* than the transcript path #644 must use
  for PTY sessions, which has to dedup Claude usage by `message.id` and has lost
  `costUSD` — the envelope sidesteps both. Because a persistent session emits a
  `result` per turn, the snapshot must define whether it keeps the **latest**
  (cumulative) or **accumulates deltas**, so a resume/retry can't double-count;
  v1's one-shot model has exactly one terminal `result`, which sidesteps the
  ambiguity for the sessions we care about first.
- **#1073 (structured events replacing hooks):** for headless sessions the typed
  stream *is* the status/approval feed — `SessionStart` ≈ `system/init`,
  `Stop` ≈ `result`, `PreToolUse` ≈ `can_use_tool`. graith consumes them directly,
  so graith's *generated status/approval hooks* are skipped for headless. For PTY
  sessions, #1073's hooks remain the structured path. Both are transports for one
  event vocabulary; the daemon maps them into one internal status/enrichment type.

  **Prerequisite: decouple MCP-config injection from lifecycle-hook injection.**
  This is a real code coupling, not a wording nuance. Today `injectClaudeHooks`
  (`hooks.go:174`) generates *both* the Claude settings file *and* the
  `--mcp-config` argument, and `Create` only calls it under the `agentHooks`
  branch (`daemon.go:1188`); MCP servers are likewise resolved only when
  `agentHooks` is true (`daemon.go:928`). The same setup installs the
  `SessionStart → gr check-inbox` hook (`hooks.go:99`). So naively "skipping hook
  injection" for headless would also drop the auto-injected graith MCP server, the
  per-session MCP proxies, **and** the startup inbox check — none of which the
  typed stream replaces (observing `system/init` is not the same as running
  `gr check-inbox`). v1 must therefore first split MCP-config generation from
  lifecycle-hook generation, enumerate each hook responsibility (status,
  permission gating, inbox check) and replace it deliberately, and untangle the
  `agentHooks = agentHooks || yolo` coupling (`daemon.go:920`) so yolo can request
  stdio approvals without silently governing MCP availability. This decoupling is
  its own phase (see Phasing) and gates the headless launch path.

#### Risks and trade-offs

Honestly, in rough order of severity:

1. **Undocumented, SDK-internal control protocol (the headline risk).** The
   stream-json *output* is documented; the *control protocol* on stdin
   (`control_request`, `initialize`, `interrupt`, `can_use_tool`,
   `--permission-prompt-tool stdio`) is not — it is the contract the Agent SDK
   speaks to the CLI, and Anthropic can change it within a release without notice.
   The entire v1 value beyond status-mapping (interrupt, approvals, context usage)
   rides on it. **Mitigations:** (a) gate the whole headless path behind
   `[headless] experimental = true` in v1 — a version pin protects only against
   *known* skew, not a silent shape change inside an allow-listed version; (b)
   feature-detect from the `initialize` response + `system/init.capabilities`
   rather than version-sniffing; (c) define which failures fall back to PTY
   (startup `initialize` failure) and which are fatal (mid-session control-frame
   drift → mark degraded, fail pending controls); (d) a real-Claude compatibility
   test pinned to the minimum supported version — captured fixtures alone can't
   prove continued compatibility of an undocumented interface.
2. **Two code paths.** PTY and headless drivers are a lasting maintenance cost.
   The capability-split interface and verbatim scrollback reuse keep the shared
   surface honest, but every session-lifecycle change now has two shapes to
   consider. Accepted deliberately: the alternative (Proposal 3) is worse.
3. **Security / fail-closed approvals.** Getting `--permission-prompt-tool stdio`
   or the `can_use_tool` routing wrong could silently run a session with *no*
   approval gating. Creation must fail closed if stdio approvals can't be
   established; the `human`-backend hang rule above is part of this.
4. **Sensitive tool input in logs.** The rendered scrollback (and
   `--structured`) may contain tool arguments/outputs — the same exposure as PTY
   scrollback, but worth noting since headless makes structured capture easy.
5. **Feature skew between the installed CLI and the SDK version used as protocol
   evidence.** graith reads the SDK's behaviour to know the protocol; the user's
   `claude` binary may be older/newer. Feature detection (risk 1b) is the guard.
6. **Interactive-only surfaces can't be emulated headless.** Tools/dialogs
   requiring user interaction (`requires_user_interaction`-style requests) have no
   headless answer; policy is deny-or-convert, stated per case.
7. **Output volume / backpressure.** `--include-partial-messages` and large tool
   outputs can flood stdout; v1 omits partial messages (not needed for
   status/cost) and bounds the reader buffer + drains stderr to avoid deadlock.

### Proposal 2: stream-json as a PTY sidecar (no second driver)

Keep the PTY, but *also* run the agent in a way that surfaces stream-json for
data extraction only — e.g. lean entirely on #1073 hooks plus a statusline
command for cost, and never add a headless transport. Status stays hook-driven;
cost comes from the statusline JSON blob the agent-hooks doc's Phase 5 describes.

**Why it loses.** It gets us *data* but none of the transport wins: no clean
control-protocol `interrupt`, no `get_context_usage`, no elimination of vt10x /
scraping overhead, and no structural fix for "interactive transport for
non-interactive work". It also keeps the hook machinery (settings injection,
wrapper script, process-per-event) that headless removes. It is strictly less
than Proposal 1 while still requiring most of the stream-json parsing work — so
we'd build the reader and not reap the transport benefits.

### Proposal 3: Replace the PTY entirely with a stream-json client

Make *all* sessions headless and render an interactive TUI ourselves from the
structured stream (graith becomes a thin client of the control protocol, à la the
Agent SDK).

**Why it loses.** Enormous scope and a huge regression surface: we'd be
reimplementing Claude Code's TUI (input editing, slash commands, permission
dialogs, images, `/`-menus) from the event stream, and we'd be doing it for every
agent, not just Claude. The control protocol is an *undocumented, SDK-internal*
contract (the public CLI docs describe stream-json output but not the control
protocol — see Risks); betting the entire interactive experience on it is
reckless. Interactive attach is graith's core; it must stay on the transport
Anthropic ships and tests. Rejected.

## Other Notes

### References

- Issue: <https://github.com/d0ugal/graith/issues/1075>
- Related issues: #644 (token accounting), #1073 (structured events / hooks),
  #620 (interrupt tuning), #1021 (`--mirror` rename).
- Design docs: `docs/design/2026-06-08-agent-hooks.md` (the status-detection
  layer this builds on and partly supersedes for headless),
  `docs/design/2026-07-03-pluggable-approvals-backends-design.md` (the approvals
  backend `can_use_tool` routes into),
  `docs/design/2026-06-24-cross-agent-conversation-migration-design.md` (the
  transcript readers that share the result-envelope shape).
- Key code paths:
  - `internal/daemon/daemon.go:66` — `sessions map[string]*grpty.Session` (becomes
    `map[string]sessionDriver`).
  - `internal/daemon/daemon.go:1292`, `Resume`, `Fork`, `AdoptSessions`
    (`daemon.go:382`), and `orchestrator.go:206` — the five launch sites that
    choose a driver (the orchestrator stays PTY-only). Plus the many lifecycle
    paths that type against `*grpty.Session` directly — `watchSession`
    (`daemon.go:1878`), `upgrade.go` FD handoff, `notify.go`, `wait.go` follow,
    screen snapshots, migration — all part of the Phase 1 refactor inventory.
  - `internal/pty/session.go` — the surface the driver interface generalises;
    `Scrollback` is reused verbatim.
  - `internal/daemon/handler.go:83,489,795` — attach / logs read through
    `GetPTY`; these become `getDriver` and branch on `Kind()`.
  - `internal/agent/transcript/claude.go` — shares the *message-shape* decoders
    with the headless reader (the `result` envelope is net-new; not present here).
  - `internal/config/default_config.toml:328` — agent `args` / `resume_args`
    graith reuses for the convert-to-interactive path.
  - Claude Code source cited in the issue: `src/cli/structuredIO.ts`,
    `src/cli/print.ts`, `src/entrypoints/sdk/controlSchemas.ts:57-658`.

### Implementation Notes

**Phasing** (each phase is a shippable PR; effort is rough dev-days). v1 is
**Claude-only, one-shot headless**, gated behind an experimental flag:

0. **Decouple MCP-config injection from lifecycle-hook injection — ~2d.
   ✅ Implemented (issue #1135).** Split `injectClaudeHooks` so MCP `--mcp-config`
   generation is now `injectMCPConfig`, a path independent of the hook path, and
   split the `agentHooks = agentHooks || yolo` reassignment into distinct
   `hooksEnabled` / `mcpEnabled` gates at all three launch sites (Create, Fork,
   Resume) so yolo no longer silently governs MCP availability. The sandbox
   hook-dir read grant now follows either gate (the dir holds both the settings
   and MCP config files). PTY behaviour is unchanged (`mcpEnabled == hooksEnabled`
   for PTY); the split is what lets a future headless launch inject MCP without
   generated hooks. Headless MCP injection itself stays gated off pending the
   later phases. Prerequisite: without it, a headless session that skips hook
   generation silently loses MCP + the inbox check.
1. **Driver interface + PTY adapter (no behaviour change) — ~4d** (bumped from the
   first draft's optimistic 2d). Extract the capability-split `sessionDriver` /
   `interactiveDriver` / `structuredDriver` from the *full* call surface (incl.
   `Fd`, `PeakRSSBytes`, `RecentlyAdopted`, `ScreenSnapshot`, user-idle, the
   `Scrollback` field accessor) across `daemon.go`, `handler.go`, `wait.go`,
   `notify.go`, `upgrade.go`, `orchestrator.go`, migration; wrap the PTY session
   so `map[string]sessionDriver` holds it. Pure refactor, gated by the existing
   suite under `-race`. *Riskiest phase for regressions; must land alone.*
2. **Headless driver + reader — ~5d.** `internal/headless/session.go`: launch with
   the full stream-json contract (incl. `--permission-prompt-tool stdio`), the
   `initialize` handshake, growable-buffer readLoop, single stdin write mutex,
   status mapping, scrollback rendering, one-shot result snapshot. Unit-tested
   against captured stream-json fixtures (see Testing).
3. **Config + launch selection — ~2d.** Resolved `DriverKind` + state migration,
   `gr new --headless` (validated against Claude at creation, no silent fallback),
   `[headless] experimental`/`default`, `[agents.*] headless_capable`, selection
   at the five launch sites.
4. **Interrupt + approvals over control protocol — ~3d.** `interrupt`; inbound
   `can_use_tool` → existing approval decision logic; `yolo` auto-allow; the
   non-blocking-backend rule (deny/timeout instead of a `human` hang).
5. **Convert-to-interactive on attach — ~4d.** Detect headless target,
   confirmation prompt, transactional driver swap (`interrupt` → settle → close →
   `--resume` in PTY, atomic under lock with rollback), flip `DriverKind`. New
   handler-case row in `authmatrix.go`. Includes the real-Claude interrupted-
   transcript integration test.
6. **Data extraction / enrichment surface — ~3d.** Extend `StatusReportMsg` (or a
   sibling) + daemon state with the `UsageReport`/`ContextReport` fields (net-new,
   not yet in the protocol); feed the result snapshot into `gr list` / overlay;
   `get_context_usage`.
7. **Trigger + mirror wiring — ~1d.** `[trigger.action] headless`; opt-in headless
   for `--mirror`.

Phases 0–3 are the minimum that lets a one-shot trigger/tribunal session run
headless with structured status. 4–5 make it fully usable; 6–7 are enrichment.
Persistent multi-turn headless is explicitly out of v1 (see Non-Goals).

**Backward compatibility.** Headless defaults off and behind
`[headless] experimental`; every existing session and config stays on the PTY
driver. The migration defaults absent `DriverKind` → `"pty"`.

**Daemon restart is a decided v1 behaviour, not an open question.** A live
headless child's stdout pipe is owned by the (now-dead) daemon and can't be
re-read, so `AdoptSessions` (`daemon.go:382`) cannot re-adopt it the way it
re-adopts a PTY by FD, and daemon *upgrade* (`upgrade.go:55`, which hands off PTY
FDs) can't hand off pipes. Because the headline use case is fire-and-forget work
with no human watching, silently abandoning an in-flight headless job on restart
is unacceptable. v1 rule: on startup the daemon **auto-resumes** each previously-
running one-shot `Headless` session via `--resume {agent_session_id}` (they resume
cleanly — the whole convert-on-attach design already leans on this), recording a
`stop_reason` that explains the restart. This is wired into the tribunal/trigger
flows so the orchestrator doesn't have to poll and re-kick.

**MCP injection.** After Phase 0's decoupling, MCP `--mcp-config` args apply
identically in headless mode (they're agent-args). Bonus: `mcp_reconnect` lets a
headless session repair a dropped MCP server *without* a restart — a future win
the PTY path can't match. **Sandbox:** a headless child is still a spawned
process, so `safehouse wrap` / `nono run` wrap it unchanged; the only new wrinkle
is that stdin/stdout are message channels rather than a ptmx, which the sandbox
backends don't care about.

**New handler cases** (attach-convert, and any headless-specific control message)
each need a `remoteMessagePolicy` row in `daemon/authmatrix.go` or
`TestRemoteMatrixCompleteness` fails.

**Docs.** User-facing pages to update in the same PR: `website/content/docs/`
commands (`gr new --headless`, attach-convert behaviour), configuration
(`[headless]`, `[agents.*] headless_capable`, `[trigger.action] headless`), and a
note in the sessions/lifecycle page. `AGENTS.md` gets a headless section.

### Alternatives considered

- **Force `--mirror` sessions headless.** Mirror sessions are read-only, so
  headless is a natural fit and would save the most overhead. But forcing it
  changes attach semantics for an existing feature (attach would suddenly
  convert), so v1 makes it opt-in and revisits forcing once the convert UX is
  proven.
- **A separate `map[string]*headless.Session`** parallel to the PTY map, with
  `if`-branches at every call site instead of an interface. Rejected: it scatters
  driver-kind checks across `daemon.go`/`handler.go` and rots fast. One interface,
  one map.
- **`Ctrl-C` interrupt even in headless** (skip the control protocol). Rejected:
  the whole point is the clean `interrupt` request; falling back to bytes would
  re-import the #620 unreliability.

### Testing

- **Driver interface refactor (Phase 1):** the existing daemon/pty test suite is
  the regression gate — it must stay green with zero behaviour change under
  `-race`. No new assertions beyond "the adapter satisfies the interface".
- **Headless reader:** table-driven tests over captured stream-json fixtures —
  a clean run (init → assistant → tool → result), an errored `result`
  (`is_error: true`), a malformed line mid-stream (skipped, not fatal), a
  non-JSON banner line (written to scrollback verbatim), and an EOF without a
  `result` (→ `stopped`). Assert status transitions and the captured envelope.
  Fixtures live under `internal/headless/testdata/`; fixture strings use Scots
  words (`braw`, `dreich`, …).
- **Status mapping:** each event → expected graith status, including
  `can_use_tool` → `approval`.
- **Interrupt / control protocol:** a fake agent (a small Go binary or a scripted
  pipe) that echoes `control_response` for a given `control_request` id; assert
  request/response matching and timeout behaviour.
- **Approvals:** `can_use_tool` routed through auto / deny backends; `yolo`
  auto-allow; assert the decision goes back as a `control_response` on stdin.
- **Reader limits / failure:** a single stream-json line >64KB (must not truncate
  — see the growable-buffer note), EOF with a partial trailing line, a malformed
  *control* frame (→ driver degraded, pending controls failed, not hung),
  duplicate/unmatched control ids, and pending-control cleanup on process exit.
- **Convert-to-interactive:** integration test (spawns a real daemon) — create a
  headless session against a fake agent, attach, assert it `interrupt`s + settles,
  stops the headless process, relaunches with `resume_args`, and that `DriverKind`
  flips to `"pty"` atomically (no window where two drivers exist).
- **Real-Claude compatibility:** because the control protocol is undocumented,
  fake-agent fixtures can't prove continued compatibility — a test pinned to the
  minimum supported Claude CLI version must exercise the `initialize` handshake,
  a `can_use_tool` round-trip, and the interrupted-transcript `--resume` render.
- **Regression:** any bug found while building gets a test that fails on the old
  code first (repo rule). The convert-during-active-tool-call path in particular
  needs a locked-in test that the in-flight turn is cancelled, not resumed.
- Coverage bar ≥80%; the pure logic (reader, status mapping, control matching,
  render-to-scrollback) is all unit-testable off the process — the process
  spawning is the only part that needs the integration harness.

### Open questions

Two earlier open questions were **resolved into the design** after the first
review round: daemon-restart adoption (now a decided auto-resume behaviour — see
Implementation Notes) and control-protocol stability (now the headline Risk, gated
behind `[headless] experimental` with capability-detection — see Risks). What
remains genuinely open:

1. **Codex headless.** Codex has an experimental JSON mode with a different
   protocol shape. Worth a follow-up doc, or fold into v1.1 once the Claude driver
   is proven?
2. **Forcing headless for `--mirror`.** Opt-in in v1. Do we want a global
   `[headless] mirror_default = true` once convert-on-attach is trusted, given
   mirror sessions are read-only and attach already has a defined convert path?
3. **Experimental → stable criteria.** What graduates headless out of
   `experimental`? A pinned-version compatibility test matrix that stays green
   across N Claude releases is the obvious bar — is that sufficient, or do we hold
   until Anthropic documents the control protocol?
