---
title: "Design Doc: Headless stream-json mode for fire-and-forget sessions"
authors: Dougal Matthews
created: 2026-07-13
status: Draft
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1075
---

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
launch path — `Create` (`daemon.go:1292`), `Resume`, `Fork`, and `AdoptSession`
on daemon restart — builds a `*pty.Session` (`internal/pty/session.go`). The PTY
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
- `--include-partial-messages` streams token deltas; hook events can be embedded
  in the stream.

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
today — the methods called from `daemon.go`, `handler.go`, `wait.go`, and the
watcher. Roughly:

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

`Resize`/`Poke` are meaningful only for the PTY; the headless driver implements
them as no-ops. `Snapshot()` is the new capability: it returns the structured
status and the most recent cost/token/context data the headless reader has seen
(the PTY driver returns a zero snapshot, so status still flows through the
existing scrape/hook path for it).

Crucially, **`Scrollback` is reused unchanged.** The headless driver renders the
structured stream into human-readable text and writes it to the *same*
append-only scrollback file the PTY uses. That means `gr logs`, the overlay
preview, and `gr wait --contains` all work against a headless session with no
changes — they read scrollback, and scrollback is populated either way.

#### The headless session

`internal/headless/session.go` — mechanically close to `pty.Session` but backed
by pipes instead of a ptmx:

```
claude -p --output-format stream-json --input-format stream-json --verbose
  ├─ stdin   ← control_request + user messages   (graith writes)
  ├─ stdout  → stream-json events                 (graith reads → readLoop)
  └─ stderr  → diagnostics                        (→ scrollback, tagged)
```

- **readLoop**: scans stdout line-by-line, decodes each JSON envelope, updates
  in-memory status, appends a rendered line to scrollback, and — on the terminal
  `result` message — stores the cost/usage/`session_id` snapshot. Malformed lines
  are logged and skipped (never fatal); a non-JSON line (e.g. an early crash
  banner) is written to scrollback verbatim so failures are visible.
- **writer**: `WriteInput` wraps text in a stream-json `user` message.
  `Interrupt` sends a `control_request` of type `interrupt` (with a monotonically
  increasing request id) rather than `Ctrl-C` bytes — clean and acknowledged.
- **control**: a `control(req)` helper matches `control_response` to
  `control_request` by id, so `get_context_usage`, `set_model`, etc. can be
  issued synchronously with a timeout.

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

This replaces both the scraper *and* the hook path for headless sessions:
`approval` becomes authoritative from `can_use_tool` with no injected hook
script, and `ready` is a real terminal event, not an inferred idle.

#### Config surface

A `headless` flag, defaulting off, resolvable at four levels (later overrides
earlier):

1. **Global default** — `[headless] default = false` (also a per-agent
   `[agents.claude] headless_capable = true` so non-supporting agents can't be
   asked to go headless).
2. **`gr new --headless`** — a bool flag on `internal/cli/new.go`, plumbed through
   `CreateOpts` to `SessionState.Headless`.
3. **Trigger config** — `[trigger.action] headless = true` for `type = "session"`
   actions (morning briefing, cleanup reactors).
4. **Auto-inferred for `--mirror`** — mirror sessions are read-only by
   construction; they are strong candidates to default to headless (see Open
   questions — this is opt-in in v1, not forced).

`SessionState` gains `Headless bool json:"headless,omitempty"` — a
state-version bump with a no-op forward migration (absent = false).

Launch selection: at the four launch sites, if `state.Headless &&
agentSupportsHeadless(agent)`, build a `headless.Session`; otherwise the existing
`pty.Session`. The command construction reuses the agent's configured `command`
and appends the stream-json flags in front of the templated `args` (so
`--session-id {agent_session_id}` is still passed and graith still owns the id).

#### Convert-to-interactive on attach

`gr attach <headless-session>` cannot stream a PTY — there is no ptmx. So attach
**converts**:

1. Daemon sees the target is a headless driver.
2. It prompts the client for confirmation (unless `--yes`): *"`braw` is a headless
   session. Attaching will restart it as an interactive session (conversation is
   preserved). Continue?"*
3. On confirmation: send a graceful `stop_task` + close stdin, wait for the
   process to exit, then relaunch **exactly like `Resume`** — `claude --resume
   {agent_session_id}` in a real `pty.Session`. Because graith already owns the
   session id and `resume_args` already exist, this is the existing resume path
   with the driver flipped to PTY.
4. Flip `SessionState.Headless = false` (the conversion is permanent for that
   session) and attach normally.

**Preserved:** full conversation history (Claude reloads it from its transcript
on `--resume`), the worktree, the branch, the graith session id, and all env.
**Lost:** the in-flight turn (an interrupt/stop is issued first — a tool call
mid-flight is cancelled, not resumed) and the live control channel. This is the
same "stop and resume" seam graith already uses for restart, so the loss profile
is understood.

Edge cases:

- **Attach during an active tool call** — `stop_task` first; the resumed PTY
  session starts fresh at the prompt with history intact. The tool call does not
  silently continue.
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
- **Headless:** the agent sends an inbound `can_use_tool` control request on
  stdout. graith answers with a `control_response` (allow/deny) **inline on
  stdin** — no injected hook, no process spawn. graith routes the decision
  through the *same* `[approvals]` backend it already uses (auto / human /
  external), so policy is unified; only the transport differs. `yolo` sessions
  auto-allow every `can_use_tool` without consulting a backend.

This makes the `PreToolUse` approval hook **redundant for headless sessions** —
one of the concrete simplifications headless buys us (see Risks → hooks).

#### Data extraction → #644 and #1073

- **#644 (token accounting):** the `result` envelope is captured per turn into
  the driver snapshot (`total_cost_usd`, `usage`, `num_turns`). The daemon
  already has a place to put this — the enrichment fields the agent-hooks doc
  deferred to its "Phase 5" (`UsageReport` / `ContextReport`). Headless populates
  them *natively* instead of via a statusline hook. #644's transcript reader
  stays the source of truth for PTY sessions; headless simply feeds the same
  fields from a live source. The `internal/agent/transcript` decoders and the
  headless readLoop should share the envelope struct so there is one definition
  of "a Claude result".
- **#1073 (structured events replacing hooks):** for headless sessions the typed
  stream *is* the hook feed — `SessionStart` ≈ `system/init`, `Stop` ≈ `result`,
  `PreToolUse` ≈ `can_use_tool`. graith consumes them directly, so hook injection
  is skipped for headless (see below). For PTY sessions, #1073's hooks remain the
  structured path. Headless and hooks are two transports for the same event
  vocabulary; the daemon should map both into one internal status/enrichment
  type.

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
  - `internal/daemon/daemon.go:1292`, `Resume`, `Fork`, `AdoptSession` — the four
    launch sites that choose a driver.
  - `internal/pty/session.go` — the surface the driver interface generalises;
    `Scrollback` is reused verbatim.
  - `internal/daemon/handler.go:83,489,795` — attach / logs read through
    `GetPTY`; these become `getDriver` and branch on `Kind()`.
  - `internal/agent/transcript/claude.go` — shares the Claude result/message
    struct with the headless reader.
  - `internal/config/default_config.toml:328` — agent `args` / `resume_args`
    graith reuses for the convert-to-interactive path.
  - Claude Code source cited in the issue: `src/cli/structuredIO.ts`,
    `src/cli/print.ts`, `src/entrypoints/sdk/controlSchemas.ts:57-658`.

### Implementation Notes

**Phasing** (each phase is a shippable PR; effort is rough dev-days):

1. **Driver interface + PTY adapter (no behaviour change) — ~2d.** Extract
   `sessionDriver` from the methods the daemon calls on `*pty.Session`; wrap the
   existing PTY session so `map[string]sessionDriver` holds it. Pure refactor,
   proven by the existing test suite staying green. *This is the riskiest phase
   for regressions and must land alone.*
2. **Headless driver + reader — ~4d.** `internal/headless/session.go`: launch
   with stream-json flags, readLoop, status mapping, scrollback rendering, result
   snapshot. Unit-tested against captured stream-json fixtures (see Testing).
3. **Config + launch selection — ~2d.** `SessionState.Headless`, state migration,
   `gr new --headless`, `[headless] default`, `[agents.*] headless_capable`,
   driver selection at the four launch sites.
4. **Interrupt + approvals over control protocol — ~3d.** `interrupt` control
   request; inbound `can_use_tool` routed through the existing approvals backend;
   `yolo` auto-allow.
5. **Convert-to-interactive on attach — ~3d.** Detect headless target in the
   attach handler, confirmation prompt, `stop_task` → `--resume` in PTY, flip
   `Headless=false`. New handler-case authorization row in `authmatrix.go`.
6. **Data extraction / enrichment surface — ~2d.** Feed the result snapshot into
   the #644 fields and `gr list` / overlay; `get_context_usage`.
7. **Trigger + mirror wiring — ~1d.** `[trigger.action] headless`; opt-in headless
   for `--mirror`.

Phases 1–3 are the minimum that lets a trigger/tribunal session run headless with
structured status. 4–5 make it fully usable; 6–7 are enrichment.

**Backward compatibility.** `headless` defaults off; every existing session and
config stays on the PTY driver. The state migration is additive (absent field =
`false`). On daemon restart, a running headless process is a plain child process —
`AdoptSession` needs a headless-aware variant (re-attach to a running headless
child by pid, or, simpler for v1, treat an interrupted headless session as
`stopped` and let it resume). *Adoption of a live headless process across daemon
restart is an explicit v1 simplification — see Open questions.*

**MCP injection.** graith injects MCP servers via `--mcp-config` / per-session
server processes (`docs/design/2026-06-11-mcp-server-injection-design.md`); those
flags are agent-args and apply identically in headless mode. The one difference:
`mcp_reconnect` lets a headless session repair a dropped MCP server *without a
restart* — a future win the PTY path can't match.

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
- **Convert-to-interactive:** integration test (spawns a real daemon) — create a
  headless session against a fake agent, attach, assert it stops the headless
  process and relaunches with `resume_args`, and that `Headless` flips to false.
- **Regression:** any bug found while building gets a test that fails on the old
  code first (repo rule). The convert-during-active-tool-call path in particular
  needs a locked-in test that the in-flight turn is cancelled, not resumed.
- Coverage bar ≥80%; the pure logic (reader, status mapping, control matching,
  render-to-scrollback) is all unit-testable off the process — the process
  spawning is the only part that needs the integration harness.

### Open questions

1. **Adopting a live headless process across daemon restart.** PTY sessions are
   re-adopted by fd/pid on restart. A headless child's stdout pipe is owned by the
   (now-dead) daemon, so its stream can't be re-read. v1 proposal: on restart, a
   previously-running headless session is marked `stopped` and resumes on demand
   (its transcript preserves state). Is that acceptable, or must headless survive
   restart transparently in v1?
2. **Codex headless.** Codex has an experimental JSON mode with a different
   protocol shape. Worth a follow-up doc, or fold into v1.1 once the Claude driver
   is proven?
3. **Forcing headless for `--mirror`.** Opt-in in v1. Do we want a global
   `[headless] mirror_default = true` once convert-on-attach is trusted?
4. **Control-protocol stability.** The stream-json *output* is documented; the
   *control protocol* on stdin is an SDK-internal contract (the public CLI docs do
   not describe `control_request`). graith would be coupling to an undocumented
   interface that Anthropic can change without notice. Mitigation: version-gate
   the headless path behind a detected Claude Code version, and fall back to PTY
   if `initialize` fails or returns an unexpected shape. Is that guard sufficient,
   or is the coupling risk reason to keep headless behind an experimental flag
   until the protocol is documented?
