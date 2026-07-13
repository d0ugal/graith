---
title: "Design Doc: Per-Session Token Accounting"
authors: Dougal Matthews
created: 2026-07-13
status: Draft
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/644
---

# Per-Session Token Accounting

graith already opens each agent's on-disk transcript to power cross-agent
migration, and those transcripts already record how many tokens every turn
consumed — graith just streams past the numbers. This doc proposes extracting
the token usage graith is already reading, tallying it per session, and
surfacing per-session totals in `gr list`, the TUI picker, and a new `gr tokens`
command. USD cost (from a user-supplied price table) and budget caps are
designed here but deferred to follow-ups; the core deliverable is honest token
counts for the agents graith can already parse (Claude Code and Codex).

## Background

`internal/agent/transcript/` locates and stream-parses each supported agent's
JSONL conversation log. `transcript.Read` (`transcript.go:97`) is used today by
the cross-agent migration feature
(`docs/design/2026-06-24-cross-agent-conversation-migration-design.md`) to
reconstruct a neutral, readable conversation. The readers are deliberately
defensive: they tolerate undocumented, drifting formats and partially-written
live files by skipping and counting unparseable lines rather than failing.

Two agents have readers today (`transcript.Supported`, `transcript.go:87`):

- **Claude Code** — `~/.claude/projects/<encoded-cwd>/<agentSessionId>.jsonl`,
  located by globbing the session id across project dirs (`locateClaude`,
  `claude.go:32`). Each assistant record's `message` object carries a `usage`
  object; graith's `claudeMessage` struct (`claude.go:64`) only extracts
  `role`/`content` and drops `usage` and `model`.
- **Codex** — `~/.codex/sessions/.../rollout-*.jsonl`, located by scanning for
  the rollout whose `session_meta.cwd` matches the worktree, or by captured
  session id (`locateCodex`, `codex.go:52`). Codex writes explicit
  `token_count` records that graith's reader currently skips (`codex.go:386` —
  only `response_item` lines become conversation turns).

Session state lives on `SessionState` (`daemon/state.go:54`), projected to
clients as `SessionInfo` (`protocol/messages.go:392`). Several runtime-derived
fields — git dirtiness, unpushed count, linked PR, CI status — are marked
`json:"-"` and are **not** persisted: they are re-derived from external sources
by background loops (`RunPRWatchLoop`, `RunGitPullLoop`) and repopulate within
one tick after a daemon restart. `gr list` renders columns from a single
registry (`client/sessioncols.go`, `SessionColumns()`) shared by the CLI table
and the TUI overlay, so adding one column entry lights it up in both surfaces.

## Problem

There is no way to see how many tokens a session has spent. Someone running a
fleet of agents in parallel — graith's whole premise — has no per-session
visibility into which sessions are burning tokens, whether a runaway loop is
chewing through context, or how a day's work distributes across sessions. The
data exists on disk and graith is already reading the exact files that contain
it; it is thrown away line by line.

Issue #644 was originally closed as "not needed", then reopened and reframed
(2026-07-11): make **token counting** the deliverable, with USD cost and budgets
as optional extensions. Crucially, token accounting does **not** depend on the
observability stack that would eventually consume it — the OTel base (#599),
GenAI semconv (#655), and dashboard (#657) are all unbuilt. Token counting
stands alone as a post-hoc, file-read feature.

## Goals

- Extract per-turn token usage (input, output, cache-creation, cache-read) from
  the Claude and Codex transcripts graith already parses.
- Aggregate to a per-session total and expose it as a `SessionInfo` field,
  visible in `gr list` (and the TUI picker) and queryable via `gr tokens`.
- Be correct: no double-counting, and count all tokens actually spent (including
  subagent/sidechain turns), not just the tokens on the active conversation
  branch.
- Fail gracefully for agents graith can't parse (cursor, agy, …) — blank/`—`,
  never an error on the hot path.
- Add zero on-disk state migration and keep `gr list` fast (no synchronous file
  reads on the list path).
- Keep the door open for USD cost and budget caps without re-plumbing.

### Non-Goals

- **USD cost in v1.** Designed below, shipped as a follow-up behind an opt-in
  price table.
- **Budget caps / enforcement in v1.** Designed as a follow-up.
- **Real-time / per-turn live metering.** The read model is post-hoc polling;
  counts lag by up to one poll interval. Real-time paths (statusLine, OTel) are
  discussed as future work, not built.
- **Token counting for agents without a transcript reader** (cursor, agy). They
  degrade to "unknown", they don't block the feature.
- **Cross-session/day rollups and reporting UIs.** Per-session totals only; a
  reporting layer can be built on top later.

## Proposals

### Proposal 0: Do Nothing

Token usage stays invisible. Users who care resort to reading raw JSONL by hand
or to Claude's own `/cost`, which is per-Claude-session and doesn't exist for
Codex or in graith's fleet view. Given the data is already in files graith opens
and the reader plumbing already exists, "do nothing" leaves obvious value on the
table for what is a contained change. Rejected.

### Proposal 1: Post-hoc transcript aggregation via a polling loop (Recommended)

Extend the existing transcript readers to sum token usage, run a background loop
that tallies each supported session's transcript into runtime-only fields, and
render those fields in the existing surfaces.

#### Data flow

```
on-disk JSONL ── transcript.Usage() ──> Usage{Input,Output,CacheCreation,CacheRead}
     ▲                                              │
     │ (readers already locate + parse)             ▼
                                    RunTokenLoop tick ─> SessionState.Tokens (json:"-")
                                                          │
                                                          ▼
                                    toSessionInfo ─> SessionInfo.Tokens ─> gr list / TUI / gr tokens
```

#### The reader extension

Add a second method to the `reader` interface (`transcript.go:71`) alongside
`read`:

```go
type Usage struct {
    Input         int64
    Output        int64
    CacheCreation int64
    CacheRead     int64
    ByModel       []ModelUsage // populated by readers; used by the cost follow-up
    DroppedLines  int
}

func (u Usage) Total() int64 {
    return u.Input + u.Output + u.CacheCreation + u.CacheRead
}

type ModelUsage struct {
    Model string
    Usage // embedded totals for that model
}

type reader interface {
    read(path string) ([]Turn, int, error)
    usage(path string) (Usage, error)
}
```

A package-level `transcript.Usage(agent, agentSessionID, worktreePath) (Usage, error)`
dispatches through `readerFor` + `locate`, exactly like `Read`, so all the
existing location logic (glob by session id for Claude, cwd/id scan for Codex)
is reused unchanged.

**Token counting is a separate pass from `Read`, and this is deliberate.**
`Read` walks only the *active leaf chain* back through `parentUuid`
(`walkChain`, `claude.go:157`) because migration wants the conversation that is
actually live. Token counting must instead sum **every** usage-bearing record in
the file — including sidechains (subagent turns) and abandoned branches — because
those tokens were genuinely spent. So `usage()` iterates the file linearly and
never touches the chain-walking logic.

##### Claude reader (`claude.go`)

Extend the record/message structs to capture what's already on the line:

```go
type claudeMessage struct {
    Role    string          `json:"role"`
    Content json.RawMessage `json:"content"`
    ID      string          `json:"id"`     // NEW: "msg_..." — dedup key
    Model   string          `json:"model"`  // NEW: for per-model cost
    Usage   *claudeUsage    `json:"usage"`  // NEW
}

type claudeUsage struct {
    InputTokens              int64 `json:"input_tokens"`
    OutputTokens             int64 `json:"output_tokens"`
    CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
    CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}
```

**Deduplication by `message.id` is the single most important correctness
detail.** Verified against a live transcript (2026-07-13): Claude Code writes
**one JSONL line per content block**, and every line belonging to one assistant
response shares the same `message.id` *and carries an identical `usage` object*
(usage is per-API-response, repeated on each block-line). In one real sample, 38
assistant records collapsed to 21 distinct ids, 16 of them duplicated (some
3×). Summing `usage` across all lines inflates counts by ~2-3×. The reader keeps
a `seen map[string]bool` of message ids and counts each id's usage exactly once.
Records with no id (rare) are counted individually since they can't collide.

##### Codex reader (`codex.go`)

Codex `token_count` records are **cumulative running totals**, not per-turn
deltas — so the reader takes the **last** `token_count` record rather than
summing. It supports both observed shapes:

- Legacy: `{"type":"event_msg","payload":{"type":"token_count","total":42}}`
  (an aggregate int; already in the test fixtures at `codex_test.go:19`) →
  mapped to `Output` as a coarse total when no breakdown is present.
- Modern: `payload.info.total_token_usage.{input_tokens, cached_input_tokens,
  output_tokens, reasoning_output_tokens}` → `Input=input_tokens`,
  `CacheRead=cached_input_tokens`, `Output=output_tokens+reasoning_output_tokens`.
  Codex has no cache-creation concept, so `CacheCreation` stays 0.

The mapping is best-effort and documented as such; Codex's model id (from
`turn_context`/`session_meta`) fills `ModelUsage.Model` when available.

#### Where the counts live: runtime-only, no migration

Token stats are **runtime-derived**, following the exact convention already used
for PR/CI/git state (`json:"-"`, re-derived by a loop):

```go
// SessionState (daemon/state.go) — NOT persisted; re-derived each poll.
Tokens *TokenStats `json:"-"`

type TokenStats struct {
    Input, Output, CacheCreation, CacheRead int64
    Total     int64
    CountedAt time.Time
    // ByModel is kept off SessionInfo in v1; the cost follow-up promotes it.
}
```

Because the transcript file on disk is the source of truth and outlives the
daemon, there is **no state migration and no `CurrentStateVersion` bump**. After
a restart the loop repopulates every session's counts within one tick. Stopped
sessions keep showing their final counts because the loop polls stopped sessions
too (their transcripts remain on disk), and they simply stop changing. This is
the same trade-off PR/CI already make, and it keeps the change small.

#### The polling loop

A new `RunTokenLoop(ctx)` (`internal/daemon/tokens.go`), registered next to the
other loops in `daemon.go:6059`, ticks on a fixed interval (proposed default
**30s**, overridable via `[tokens] poll_interval`). Each tick:

1. Snapshot eligible sessions under `RLock`: status running **or** stopped, not
   soft-deleted, agent `transcript.Supported()`. (Same eligibility shape as
   `prWatchTargets`, `prwatch.go:168`.)
2. For each, off-lock, call `transcript.Usage(...)`. **mtime cache:** keep an
   in-memory `map[sessionID]struct{mtime, Usage}`; if the transcript's mtime is
   unchanged since the last read, reuse the cached result and skip re-parsing.
   This bounds cost — a quiet fleet does almost no work, and a large transcript
   is only re-read when it actually grows.
3. Write the result back to `SessionState.Tokens` under `Lock`.

Errors (missing transcript, unsupported agent, parse failure) are logged at
debug and leave `Tokens` nil — never surfaced on the hot path. `gr list` reads
only the cached field, so the list path never touches the filesystem.

#### CLI and TUI surface

**`gr list` / TUI** — one entry in the `SessionColumns()` registry
(`sessioncols.go:70`):

```go
{
    Key: "tokens", Header: "Tokens", ShowCLI: true, Wide: true,
    CLIValue: func(s protocol.SessionInfo, _ time.Time) string { return cliTokens(s) },
}
```

`cliTokens` renders the compact total, e.g. `1.2M`, `847k`, `3.4k`, or `""` when
unknown (using a humanising helper like the existing `ShortDuration`). It ships
as a `--wide` CLI column to avoid widening the default table; `ShowTUI` is off in
v1 and can be flipped on later. Because the registry is the single source of
truth, defining it once covers both surfaces.

**`gr tokens`** — a new read-only command (`internal/cli/tokens.go`) that renders
a per-session breakdown:

```
$ gr tokens
SESSION   AGENT   INPUT    OUTPUT   CACHE-R    CACHE-W   TOTAL
braw      claude  12,431   48,209   1,204,882  96,004    1,361,526
canny     codex   88,120   210,004  0          0         298,124
dreich    cursor  —        —        —          —         (unsupported)

$ gr tokens braw          # single-session detail
$ gr tokens --json        # agent-mode structured output
```

`gr tokens` reuses the **existing `ListSessions` RPC** and formats the token
fields already present on `SessionInfo` — so it needs **no new control-message
handler case** and therefore no `remoteMessagePolicy` / `authmatrix` row. It
inherits the poll cadence (counts lag by up to one tick); this is documented.
Agent-mode auto-JSON applies as for other commands.

#### `SessionInfo` field

Mirror the runtime stats into the protocol projection (`messages.go:392`), set
in `toSessionInfo` (`handler.go:2063`):

```go
Tokens *TokenInfo `json:"tokens,omitempty"`

type TokenInfo struct {
    Input, Output, CacheCreation, CacheRead, Total int64
}
```

`omitempty` keeps it absent for unsupported/unpolled sessions, so older clients
and JSON consumers see nothing new until a session has counts.

#### Trade-offs

- **Pro:** small, self-contained; reuses all existing location + parsing;
  no migration; hot path stays filesystem-free; matches established runtime-field
  conventions.
- **Pro:** correct-by-construction on the two known format hazards (Claude
  per-block dedup; Codex cumulative last-wins), locked down by regression tests.
- **Con:** counts lag by up to the poll interval, and vanish if the user deletes
  the underlying transcript. Acceptable — the transcript is the source of truth
  and the issue explicitly accepts post-hoc lag.
- **Con:** re-reads whole files (mitigated by the mtime cache). An incremental
  offset reader is possible but adds cross-restart bookkeeping for marginal gain
  (see Alternatives).

### Proposal 2: Real-time metering via Claude's statusLine hook

Claude Code pipes a per-turn JSON blob (cost, context, token counts) to the
command configured in `settings.statusLine.command`. graith could install a
shim that captures it, giving live, per-turn data with no file parsing.

Rejected for v1: it is **Claude-only** (no Codex, no parity with the transcript
path), requires graith to own/modify the user's Claude settings, and adds a live
ingestion path and its failure modes. It is genuinely complementary — a future
enhancement that could feed the *same* `TokenStats` field for lower latency — but
it doesn't replace the transcript reader, which is the portable path that also
covers Codex and works retroactively on existing sessions.

### Proposal 3: OpenTelemetry counters

Claude Code emits `claude_code.*` OTel counters when gated env vars are set.
graith could run a collector and read them.

Rejected: infrastructure-heavy for a contained feature, gated behind env
configuration, Claude-specific, and the entire telemetry stack that would make
this coherent (#599/#655/#657) is unbuilt. This is the long-term "real
observability" path, explicitly out of scope here.

## Other Notes

### References

- Issue: https://github.com/d0ugal/graith/issues/644
- Readers: `internal/agent/transcript/transcript.go` (`Read`, `Supported`,
  `reader`), `claude.go` (`claudeReader`, `claudeMessage`, `walkChain`),
  `codex.go` (`codexReader`, `token_count` skip at `codex.go:386`).
- State/projection: `internal/daemon/state.go` (`SessionState`),
  `internal/protocol/messages.go` (`SessionInfo`),
  `internal/daemon/handler.go` (`toSessionInfo`).
- Loops: `internal/daemon/prwatch.go` (`RunPRWatchLoop`, `prWatchTargets` — the
  model for eligibility + off-lock polling); registration at
  `internal/daemon/daemon.go:6059`.
- Columns: `internal/client/sessioncols.go` (`SessionColumns`), CLI table in
  `internal/cli/list.go`.
- Related memory: token accounting feasibility; Claude-integration gaps.

### Implementation Notes

- **Phasing.** (1) reader `usage()` for Claude+Codex + `transcript.Usage`;
  (2) `RunTokenLoop` + `SessionState.Tokens` + `SessionInfo.Tokens`; (3) the
  `tokens` column + `gr tokens` command. Ships as one PR (the core); cost and
  budgets are separate follow-up PRs.
- **No migration.** Runtime-only `json:"-"` fields; no `CurrentStateVersion`
  bump.
- **Defensive parsing carries over.** `usage()` uses the same
  skip-and-count-unparseable-lines discipline as `read()`; `Usage.DroppedLines`
  records drift for diagnostics.
- **mtime cache** keyed by session id; invalidated when the resolved transcript
  path changes (e.g. after a migration re-points the agent session id).

### USD cost (follow-up, designed)

Cost = Σ over models of (per-model tokens × per-model unit price), summed across
the four token classes (input/output/cache-read/cache-write each priced
separately, since cache reads are far cheaper). This needs:

- **A user-supplied price table** in config, e.g.
  `[[tokens.price]] model = "claude-opus-4-8"` with `input`/`output`/
  `cache_read`/`cache_write` USD-per-Mtok fields. **Opt-in and user-owned**
  because prices go stale and vary by plan/region — graith must not bundle a
  table that silently drifts.
- **Per-model breakdown** flowing through: the readers already populate
  `Usage.ByModel`; the follow-up promotes `ByModel` onto `TokenStats` and
  `TokenInfo`, and `gr tokens --cost` (and a `Cost` column) computes USD
  client-side from config × the per-model tokens. Models absent from the table
  render `—` for cost (never a guess).
- **Why not read `costUSD` from the transcript?** Verified 2026-07-13: current
  Claude Code writes **no** `costUSD` field (0 records across a full live
  transcript). Relying on it would produce silently-zero costs. The price-table
  path is the only reliable one and works for Codex too.

### Budget caps (follow-up, designed)

A `[tokens.budget]` block with per-session / per-scenario / per-day limits.
After each poll, `RunTokenLoop` compares totals to the limit and, on breach,
delivers a warning to the session's inbox (soft cap) or, at a hard cap, stops
the agent and notifies the orchestrator via `gr notify`. Deferred: enforcement
policy (warn vs stop), reset windows, and group accounting all warrant their own
design once the core counts exist and can be observed in practice.

### Alternatives considered

- **Persist token totals in `state.json`** (rather than runtime-only). Rejected:
  it adds a state migration and cross-restart dedup bookkeeping (which message
  ids were already counted) for the marginal benefit of surviving a transcript
  deletion — but runtime recompute repopulates in one tick and matches the
  PR/CI/git precedent.
- **Incremental offset reads** (remember the byte offset + counted ids, append
  only). Rejected for v1: needs durable per-session cursors that survive log
  rotation/compaction (Codex cold rollouts become `.jsonl.zst`) and restarts;
  the mtime cache captures most of the win with none of that complexity. Can be
  revisited if large transcripts prove costly.
- **Lazy compute on `gr tokens`/`gr list`** (no loop). Rejected: puts
  synchronous file reads on the interactive list path and gives the TUI nothing
  to poll for a live column.

### Testing

- **Reader unit tests** with fixtures (Scots-word session names/fixtures per
  house style):
  - Claude: the **dedup regression test** — a fixture with multiple block-lines
    sharing one `message.id` must count usage once (fails against a naive sum).
    Plus sidechain records counted, records without id counted individually,
    string vs array content, drifting/partial trailing lines skipped.
  - Codex: cumulative **last-wins** (a fixture with several `token_count` records
    returns the last, not the sum); legacy `total` int vs modern
    `info.total_token_usage` object; missing `token_count` → zero.
- **Aggregation/loop tests:** eligibility filter (running/stopped, not
  soft-deleted, supported agent only); unsupported agent → nil `Tokens`; mtime
  cache reuse; error paths (missing file) leave `Tokens` nil without erroring.
- **CLI/formatting tests:** `cliTokens` humanisation (0/blank, k, M rounding);
  `gr tokens` table + `--json`; unsupported-agent renders `—`. Follow the extract
  -pure-logic-and-test-it rule for anything touching the loop.
- Coverage bar (≥80%) and `-race` apply; new behaviour ships with its tests in
  the same PR.

### Open questions

- Default poll interval — 30s proposed; is a config knob warranted in v1 or
  should it be a constant until someone needs to tune it?
- Should the `tokens` column be TUI-visible by default, or stay CLI-`--wide`
  only until the display is proven un-cluttered?
- For Codex's legacy `total`-only records, is folding the aggregate into
  `Output` acceptable, or should it land in a separate "uncategorised" bucket so
  it isn't mistaken for pure output tokens?
