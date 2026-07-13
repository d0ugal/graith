---
title: "Design Doc: Per-Session Token Accounting"
authors: Dougal Matthews
created: 2026-07-13
status: Draft (revised after an independent two-model review — see Consensus)
reviewers: Claude (Opus 4.8), Codex (o3)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/644
---

# Per-Session Token Accounting

graith already opens each agent's on-disk transcript to power cross-agent
migration, and those transcripts already record how many tokens every turn
consumed — graith just streams past the numbers. This doc proposes extracting
the token usage graith is already reading, tallying it per session, and
surfacing per-session totals in `gr list` and a new `gr tokens` command. USD
cost (from a user-supplied price table) and budget caps are designed here but
deferred to follow-ups; the core deliverable is honest token counts for the
current agent of each session graith can already parse (Claude Code and Codex).

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
  only `response_item` lines become conversation turns). Cold rollouts are
  compressed to `.jsonl.zst` and are skipped by the locator (`codex.go:46`).

Session state lives on `SessionState` (`daemon/state.go:54`), projected to
clients as `SessionInfo` (`protocol/messages.go:392`). Several runtime-derived
fields — git dirtiness, unpushed count, linked PR, CI status — are marked
`json:"-"` and are **not** persisted: they are re-derived from external sources
by background loops (`RunPRWatchLoop`, `RunGitPullLoop`) and repopulate within
one tick after a daemon restart. Crucially, those loops never mutate the runtime
field in place — `PRStatus` documents that it is always assigned a freshly-built
value so an off-lock `cloneSessionState` (`state.go:196`, shallow except for
named slices) can't race the writer (`state.go:177`). `RunGitPullLoop` also runs
a short startup sweep so fields aren't blank for a full interval
(`gitpull.go:20`), and `RunPRWatchLoop` caps work per tick (`prwatch.go:129`).

`gr list` renders columns from a single registry (`client/sessioncols.go`,
`SessionColumns()`) shared by the CLI table and the TUI overlay. A column is
filtered independently by `ShowCLI`/`ShowTUI`; a TUI column must supply both
`TUIValue` and `TUIStyle` (enforced by `sessioncols_test.go`), so lighting up
the TUI is not free — it needs formatters and a width/layout decision.

## Problem

There is no way to see how many tokens a session has spent. Someone running a
fleet of agents in parallel — graith's whole premise — has no per-session
visibility into which sessions are burning tokens, whether a runaway loop is
chewing through context, or how a day's work distributes across sessions. The
data exists on disk and graith is already reading the exact files that contain
it; it is thrown away line by line.

Issue #644 was originally closed as "not needed", then reopened and reframed
(2026-07-11): make **token counting** the deliverable, with USD cost and budgets
as optional extensions. Token accounting does **not** depend on the
observability stack that would eventually consume it — the OTel base (#599),
GenAI semconv (#655), and dashboard (#657) are all unbuilt. Token counting
stands alone as a post-hoc, file-read feature.

## Goals

- Extract per-turn token usage (input, output, cache-creation, cache-read) from
  the Claude and Codex transcripts graith already parses, as **mutually
  exclusive** categories so a total is meaningful.
- Aggregate to a per-session total for the session's **current agent** and
  expose it as a `SessionInfo` field, visible in `gr list --wide` and queryable
  via a new `gr tokens` command.
- Be correct: no double-counting (across duplicate Claude records, across the
  overlapping Codex cache/reasoning sub-fields, or across a Codex agent's
  multiple rollout files), and count all tokens the current agent actually spent
  (including in-file subagent/sidechain turns), not just the active branch.
- Distinguish **unknown** (never parsed / unsupported agent / read error) from a
  genuine **zero** — never report a confident zero for a session we couldn't
  read.
- Fail gracefully for agents graith can't parse (cursor, agy, …) — blank/`—`,
  never an error on the hot path.
- Keep `gr list` fast (no synchronous file reads on the list path) and bound the
  background work so a large fleet with big transcripts stays cheap.
- Keep the door open for USD cost and budget caps without re-plumbing.

### Non-Goals

- **Lifetime usage across a cross-agent migration.** v1 counts the session's
  *current* agent's transcript. When a session migrates (Claude→Codex etc.), the
  displayed count reflects the new agent from that point; the prior agent's spend
  is not carried forward. A persisted per-source ledger that preserves lifetime
  totals across migration is a designed follow-up (see below). This is called out
  because it is a deliberate scope decision, not an oversight.
- **USD cost in v1.** Designed below, shipped as a follow-up behind an opt-in
  price table (and it needs accurate per-model attribution, which v1 does not
  build).
- **Budget caps / enforcement in v1.** Designed as a follow-up.
- **TUI picker column in v1.** Shipped CLI-first (`gr list --wide` + `gr
  tokens`); the shared registry makes adding the TUI column a fast-follow once a
  width/layout decision is made. The v1 goals therefore do **not** promise TUI.
- **Real-time / per-turn live metering.** The read model is post-hoc polling;
  counts lag by up to one poll interval. Real-time paths (statusLine, OTel) are
  future work, not built.
- **Reading cold, compressed Codex rollouts (`.jsonl.zst`).** The locator skips
  them today; v1 inherits that and may undercount a long-idle Codex session
  whose early rollouts have been compressed. Documented limitation.

## Proposals

### Proposal 0: Do Nothing

Token usage stays invisible. Users who care resort to reading raw JSONL by hand
or to Claude's own `/cost`, which is per-Claude-session and doesn't exist for
Codex or in graith's fleet view. Given the data is already in files graith opens
and the reader plumbing already exists, "do nothing" leaves obvious value on the
table for a contained change. Rejected.

### Proposal 1: Post-hoc transcript aggregation via a polling loop (Recommended)

Extend the existing transcript readers to sum token usage into mutually
exclusive categories, run a bounded background loop that tallies each supported
session's current-agent transcript(s) into runtime-only fields, and render those
fields in the CLI.

#### Data flow

```
on-disk JSONL ── Locate() ─> []Source{Path,Size,ModTime}
     │                            │  (loop fingerprints → skip if unchanged)
     │                            ▼
     └──────────── UsageFrom() ─> Usage{Input,Output,CacheCreation,CacheRead,
                                         Unclassified, Found, Dropped}
                                     │
                RunTokenLoop tick ─> SessionState.Tokens (json:"-", replaced whole)
                                     │
                     toSessionInfo ─> SessionInfo.Tokens ─> gr list --wide / gr tokens
```

#### The reader API: resolve, fingerprint, then parse

The daemon must be able to *stat before it parses* so an unchanged transcript is
skipped without reading it. A single `Usage(agent,id,worktree)` call that
resolves-and-parses atomically can't support that (the review flagged this: the
loop can't fingerprint a path it never sees). So the package exposes two steps:

```go
// Source is one on-disk transcript file contributing to a session's usage.
type Source struct {
    Path    string
    Size    int64
    ModTime time.Time
}

// Locate returns every readable transcript file for the current agent of a
// session. One file for Claude; possibly several for Codex (see below).
func Locate(agent, agentSessionID, worktreePath string) ([]Source, error)

// UsageFrom parses the given sources and returns aggregated usage. It never
// returns ErrNoTurns — a transcript with zero usage records yields Found=false,
// not an error (unlike Read, which treats "no turns" as an error).
func UsageFrom(agent string, sources []Source) (Usage, error)
```

The daemon fingerprints the `[]Source` (path + size + mtime of each) and, when
the fingerprint is unchanged since the last successful read, reuses the cached
`Usage` and skips `UsageFrom` entirely. The cache lives in the daemon loop
keyed by session id; a changed fingerprint (grew, rotated, re-pointed after a
migration) forces a re-read. Fingerprinting on size+mtime (not mtime alone), and
re-statting *after* the read completes, guards against an append racing the read
being cached under a newer mtime.

The `Usage` value type is flat (diagnostics kept separate from a future
per-model grouping, per review):

```go
type Usage struct {
    Input         int64 // fresh, uncached input tokens
    Output        int64 // completion tokens
    CacheCreation int64 // cache-write tokens (Claude only)
    CacheRead     int64 // cache-hit input tokens
    Unclassified  int64 // provider aggregate we can't break down (legacy Codex)
    Found         bool  // at least one valid usage record was observed
    Dropped       int   // unparseable / conflicting records, for diagnostics
}

func (u Usage) Total() int64 {
    return u.Input + u.Output + u.CacheCreation + u.CacheRead + u.Unclassified
}
```

**The four categories plus `Unclassified` are mutually exclusive by
construction**, so `Total()` is a real total, not a sum of overlapping buckets.
Each reader is responsible for producing an exclusive breakdown; where a provider
reports its own grand total we validate against it.

**Token counting is a separate pass from `Read`, and this is deliberate.** `Read`
walks only the *active leaf chain* back through `parentUuid` (`walkChain`,
`claude.go:157`) because migration wants the live conversation. Token counting
instead sums **every** usage-bearing record in the file — including sidechains
(subagent turns) and abandoned branches — because those tokens were genuinely
spent. So the usage pass iterates linearly and never touches the chain walk.

##### Claude reader (`claude.go`)

Extend the structs to capture what's already on the line:

```go
type claudeMessage struct {
    Role    string          `json:"role"`
    Content json.RawMessage `json:"content"`
    ID      string          `json:"id"`     // NEW: "msg_..." — dedup key
    Model   string          `json:"model"`  // NEW: kept for the cost follow-up
    Usage   *claudeUsage    `json:"usage"`  // NEW; nil on user records
}

type claudeUsage struct {
    InputTokens              int64 `json:"input_tokens"`
    OutputTokens             int64 `json:"output_tokens"`
    CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
    CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}
```

Claude's four fields **are** already mutually exclusive — verified on a live
transcript, `input_tokens` is the fresh (uncached) input, with cache-read and
cache-creation counted separately (sample: `input_tokens:2,
cache_creation:36352, cache_read:16884, output:461`). So the mapping is direct:
`Input=input_tokens, Output=output_tokens, CacheCreation=cache_creation_input_tokens,
CacheRead=cache_read_input_tokens`. Only assistant records carry `usage`; user
lines have `Usage==nil` and are skipped cleanly (a user line is in the
regression fixture to lock that in).

**Deduplication by `message.id` is the single most important correctness
detail.** Verified against a live transcript (2026-07-13): Claude Code writes
**one JSONL line per content block**, and every line of one assistant response
shares the same `message.id` and carries an identical `usage` object. In one
real sample, 38 assistant records collapsed to 21 distinct ids, 16 duplicated
(some 3×). Naive summing inflates counts ~2-3×.

Dedup policy (made deterministic per review, not just "first wins"): keep one
usage per `message.id`; **on a duplicate, verify the usage is identical to the
occurrence already recorded** and, if it differs (drift / partial live write),
keep the numerically larger per-field value and increment `Dropped` as a
conflict signal rather than silently trusting either. Records with **no** id
can't be deduped: they are counted, but a nonzero no-id count sets a
`degraded` marker (surfaced via `Dropped`) so the total is flagged as uncertain
rather than presented as exact.

**Subagent storage caveat.** Summing every record in the located
`<agentSessionId>.jsonl` captures in-file sidechain (subagent) usage. It has
**not** been verified that Claude stores *all* subagent usage in that one file
versus separate child transcripts. Phase 2 must confirm the on-disk layout with
a real fixture; if subagents write separate files, `Locate` for Claude gains
multi-file discovery. Until verified, the doc claims only "in-file sidechains",
not "all subagents".

##### Codex reader (`codex.go`)

Codex `token_count` records are **cumulative running totals** within a rollout,
so the reader takes the **last** valid `token_count` per rollout file (a
backward tail scan suffices — no full parse). Two shapes:

- **Modern:** `payload.info.total_token_usage.{input_tokens, cached_input_tokens,
  output_tokens, reasoning_output_tokens, total_tokens}`. **These fields
  overlap** — verified on a real rollout, `cached_input_tokens` is a *subset* of
  `input_tokens`, `reasoning_output_tokens` a subset of `output_tokens`, and
  `total_tokens == input_tokens + output_tokens`. The naive additive mapping in
  the first draft double-counted (reporting 1,585,673 vs the true 828,872). The
  correct **exclusive** mapping is:

  ```text
  CacheRead     = cached_input_tokens
  Input         = input_tokens - cached_input_tokens   // fresh input only
  Output        = output_tokens                        // reasoning is a subset; NOT added
  CacheCreation = 0                                     // Codex has no cache-write concept
  ```

  We then **validate** `Input+CacheRead+Output == total_tokens`; on mismatch
  (format drift, negative subset) we fall back to `Unclassified = total_tokens`
  and increment `Dropped`, so a drifting record degrades to an honest total
  rather than a wrong breakdown. `reasoning_output_tokens` is retained only as
  optional metadata for a future reasoning breakdown, never folded into `Output`.

- **Legacy:** `payload.total` (an int; the `codex_test.go:19` fixture). This is
  an opaque aggregate with no breakdown, so it lands in **`Unclassified`**, never
  `Output`. (Folding it into `Output` would make the `gr tokens` OUTPUT column
  lie — the open question in the first draft, now resolved.)

**Multi-rollout per session.** A single Codex agent's cwd routinely accumulates
several rollout files — the codebase already disambiguates this
(`CodexSessionIDSince`, `codex.go:217`, treats 2+ ids in a window as ambiguous).
If `codex resume` forks a fresh rollout, its cumulative counter restarts near
zero, so "last `token_count` of the newest file" would drop everything before
the last resume. Therefore `Locate` for Codex returns **all** uncompressed
rollouts belonging to the current agent's lifetime for the worktree, and
`UsageFrom` sums each file's final cumulative `token_count`. The exact
enumeration (by captured id lineage vs cwd+since) and whether resume appends or
forks **must be verified with a real fixture in Phase 2**; the mapping above is
correct either way (one file → one final; N files → sum of finals).

#### Where the counts live: runtime-only, no migration

Token stats are **runtime-derived**, following the verified PR/CI/git convention
(`json:"-"`, re-derived by a loop):

```go
// SessionState (daemon/state.go) — NOT persisted; re-derived each poll.
Tokens *TokenStats `json:"-"`

type TokenStats struct {
    Input, Output, CacheCreation, CacheRead, Unclassified int64
    Total     int64     // stored once, computed from the exclusive categories
    Known     bool      // false = never observed / unsupported / unreadable
    Degraded  bool      // parsed, but with conflicts / no-id records
    CountedAt time.Time // last successful observation, for staleness
}
```

Because the transcript file on disk is the source of truth for the *current*
agent and outlives the daemon, there is **no state migration and no
`CurrentStateVersion` bump**. After a restart the loop repopulates within one
tick (plus a short startup sweep, below). This matches PR/CI/git; the one place
the precedent is weaker is that historical spend is an accounting record whose
source can be rotated/replaced — which is exactly why lifetime-across-migration
is an explicit non-goal here and a persisted-ledger follow-up.

**Concurrency:** `Tokens` is always **replaced with a freshly-built pointer**
under the lock, never mutated in place, so an off-lock `cloneSessionState`
(shallow copy of the pointer) is race-free — the same rule `PRStatus` follows
(`state.go:177`). `cloneSessionState` needs no change (the pointer is immutable
once published).

**Error/staleness policy (made explicit per review):** on a transient read
error the loop **retains** the last-good `TokenStats` (does not clear it to nil,
which would erase a known total on a blip) and leaves `CountedAt` unchanged, so
staleness is detectable client-side. A session that has genuinely never been
read has `Tokens == nil` → rendered as unknown (`—`), distinct from a real zero
(`Known=true, Total=0`).

#### The polling loop

A new `RunTokenLoop(ctx)` (`internal/daemon/tokens.go`), registered next to the
other loops (`daemon.go:6059`):

1. **Startup sweep:** a short initial timer (as `RunGitPullLoop` does,
   `gitpull.go:20`) so `gr tokens` isn't blank for a full interval after daemon
   start.
2. **Eligibility:** status running, stopped, **or errored** (errored sessions can
   have billable usage), agent `transcript.Supported()`. **Unlike
   `prWatchTargets`, mirror and in-place sessions are *not* excluded** — a mirror
   runs its own agent with its own transcript, so its tokens are real. (Edge:
   two mirror sessions sharing a worktree where Codex still falls back to the cwd
   scan — no captured id yet — could resolve the same rollout; once each id is
   captured `findCodexRolloutByID` disambiguates. Noted as a low-probability
   transient.) Soft-deleted sessions are excluded from polling but their
   last-known `Tokens` is retained (not cleared) so `gr list --deleted` still
   shows a value; purged sessions have their cache entry pruned.
3. **Bounded work:** a per-tick batch cap and a small concurrency limit (like
   `prWatch`'s cap, `prwatch.go:129`) so a large fleet can't stall the loop;
   `ctx` is checked between sessions for prompt shutdown. Per session, off-lock:
   `Locate` → fingerprint → skip if unchanged → else `UsageFrom` → replace
   `SessionState.Tokens` under `Lock`.

Interval is a **constant** in v1 (proposed **30s**) — no config knob, no config
struct or docs burden, until a tuning need is demonstrated (resolves an earlier
open question). Errors (missing transcript, unsupported agent, parse failure)
are logged at debug and leave the retained value in place — never surfaced on the
hot path. `gr list` reads only the cached field, so the list path never touches
the filesystem.

**Scaling honesty:** the fingerprint cache makes an *idle* fleet nearly free,
but an *active* session's transcript changes every tick, so it is re-read in
full each interval — precisely the sessions a token view most wants to watch.
For v1 this is bounded by the batch cap and accepted; incremental reading
(Codex: tail to the last `token_count`; Claude: a **runtime** byte-offset +
per-file seen-id cursor, rebuilt after restart, invalidated on size/inode
regression) is the clear next optimization and does **not** require durable
cursors (correcting the first draft). See Alternatives.

#### CLI surface

**`gr list --wide`** — one entry in `SessionColumns()` (`sessioncols.go:70`),
CLI-only in v1:

```go
{
    Key: "tokens", Header: "Tokens", ShowCLI: true, Wide: true,
    CLIValue: func(s protocol.SessionInfo, _ time.Time) string { return cliTokens(s) },
}
```

`cliTokens` renders the compact total (`1.2M`, `847k`, `3.4k`), `0` for a known
zero, and `""`/`—` for unknown. A trailing `~` marks a degraded count. It ships
`--wide` to avoid widening the default table. `ShowTUI` stays off in v1 (adding
it requires `TUIValue`+`TUIStyle` and a width decision — a fast-follow, not a v1
claim).

**`gr tokens`** — a new read-only command (`internal/cli/tokens.go`, registered
in `root.go`):

```
$ gr tokens
SESSION   AGENT   INPUT    OUTPUT   CACHE-R    CACHE-W   OTHER   TOTAL
braw      claude  12,431   48,209   1,204,882  96,004    0       1,361,526
canny     codex   69,131   3,517    756,224    0         0       828,872
dreich    cursor  —        —        —          —         —       (unsupported)

$ gr tokens braw          # single-session detail
$ gr tokens --json        # agent-mode structured output
```

- Reuses the **existing `ListSessions` RPC** and formats the token fields on
  `SessionInfo` — no new control-message handler case, so no
  `remoteMessagePolicy`/`authmatrix` row (which `TestRemoteMatrixCompleteness`
  would otherwise require).
- Single-session lookup uses the **ambiguity-safe** `resolveByNameOrID`
  (`delete.go:272`), not `findSession` (`list.go:442`, first-match), since names
  aren't globally unique.
- Unknown/unsupported render `—`; degraded totals show `~`; the `--json` schema
  is a token-focused projection (documented in Phase 2).

#### `SessionInfo` field

Mirror the runtime stats into the protocol projection (`messages.go:392`), set in
`toSessionInfo` (`handler.go:2063`):

```go
Tokens *TokenInfo `json:"tokens,omitempty"`

type TokenInfo struct {
    Input, Output, CacheCreation, CacheRead, Unclassified, Total int64
    Degraded  bool   `json:"degraded,omitempty"`
    CountedAt string `json:"counted_at,omitempty"` // RFC3339; staleness signal
}
```

`omitempty` keeps it absent for unknown sessions, so older clients and JSON
consumers see nothing new until a session has a count.

**Visibility decision (made explicit per review):** because token data rides on
`SessionInfo`, it becomes visible to every `SessionInfo` consumer, including
read-only remote guests — not just `gr tokens`. For raw token *counts* this is
acceptable (they're low-sensitivity). It is called out here so the follow-up that
adds USD **cost** revisits whether cost should be gated more tightly.

#### Trade-offs

- **Pro:** small, self-contained; reuses all existing location + parsing; no
  migration; hot path stays filesystem-free; matches established runtime-field
  and immutable-replacement conventions.
- **Pro:** correct-by-construction on the three counting hazards (Claude
  per-block dedup; Codex overlapping sub-fields; Codex multi-rollout), each with
  provider-total validation and a degraded fallback, locked by regression tests.
- **Con:** v1 counts the current agent only — a migrated session loses prior
  spend (explicit non-goal; ledger follow-up). Cold compressed Codex rollouts
  are skipped (documented limitation).
- **Con:** active sessions are re-read in full each interval (bounded by the
  batch cap; incremental reading is the next optimization).

### Proposal 2: Real-time metering via Claude's statusLine hook

Claude Code pipes a per-turn JSON blob (cost, context, token counts) to the
command in `settings.statusLine.command`. graith could install a shim that
captures it, giving live per-turn data with no file parsing.

Rejected for v1: **Claude-only** (no Codex parity), requires graith to own/modify
the user's Claude settings, and adds a live ingestion path and its failure modes.
Genuinely complementary — a future enhancement that could feed the *same*
`TokenStats` for lower latency — but it doesn't replace the transcript reader,
which is the portable path that also covers Codex and works retroactively.

### Proposal 3: OpenTelemetry counters

Claude Code emits `claude_code.*` OTel counters when gated env vars are set;
graith could run a collector and read them. Rejected: infrastructure-heavy for a
contained feature, gated behind env config, Claude-specific, and the telemetry
stack that would make it coherent (#599/#655/#657) is unbuilt. This is the
long-term "real observability" path, explicitly out of scope.

## Consensus

The draft was reviewed independently by two models (Claude Opus 4.8 and Codex
o3) against the actual codebase. Both endorsed the overall architecture —
usage parsing in `internal/agent/transcript`, a daemon-cached projection off the
request path, an optional `SessionInfo` field, and reuse of the `list` RPC +
shared column registry. Both independently confirmed the Claude-dedup and
Codex-cumulative hazards. The following findings changed the design:

- **Codex sub-field double-count (critical, Codex).** The draft's additive Codex
  mapping summed overlapping fields (`cached_input_tokens ⊂ input_tokens`,
  `reasoning_output_tokens ⊂ output_tokens`, `total = input + output`), nearly
  doubling the count on a real rollout. **Fixed:** exclusive mapping
  (`Input = input−cached`, `CacheRead = cached`, `Output = output`), validated
  against provider `total_tokens`, with an `Unclassified` fallback. This drove a
  general rule: `Total()` only ever sums mutually exclusive categories.
- **Accounting identity (critical, Codex; echoed by Claude).** "Per-session" in
  the draft meant "current native transcript", which silently drops prior spend
  after a cross-agent migration and can't be reconstructed from the one-deep
  `MigratedFrom`. **Resolved:** v1 is explicitly scoped to *current-agent* usage
  (a stated non-goal + a designed persisted-ledger follow-up), rather than
  over-claiming lifetime totals it can't deliver runtime-only.
- **Codex multi-rollout undercount (Claude).** A resumed Codex session can span
  several rollout files, each with its own cumulative counter. **Fixed:** `Locate`
  returns all of the current agent's rollouts and `UsageFrom` sums their finals;
  exact enumeration flagged for Phase 2 fixture verification.
- **mtime-cache API boundary + scaling (both).** The single resolve-and-parse
  API made the cache impossible (nothing to stat first) and full rescans scale
  poorly for active sessions. **Fixed:** split into `Locate` (returns sources to
  fingerprint) + `UsageFrom`; fingerprint on size+mtime with a post-read
  re-stat; batch cap + concurrency limit + startup sweep; incremental reading
  reframed as a non-durable runtime optimization (the draft wrongly claimed it
  needed durable cursors).
- **Unknown vs zero vs stale (both).** The draft conflated them. **Fixed:**
  `Usage.Found`, `TokenStats.Known`/`Degraded`/`CountedAt`, retain-on-error, and
  `—` vs `0` vs `~` rendering.
- **TUI contradiction (Codex).** Goals promised the picker but the column set
  `ShowCLI` only. **Resolved:** TUI moved to an explicit non-goal/fast-follow;
  goals no longer claim it.
- **Smaller fixes adopted:** immutable `Tokens` replacement; drop the recursive
  `Usage.ByModel` and **omit per-model breakdown from v1** (accurate Codex
  attribution needs cumulative-delta logic — deferred to the cost follow-up);
  `resolveByNameOrID` for `gr tokens`; include `StatusErrored`; prune purged
  cache entries; constant interval (no config knob) in v1; explicit remote
  visibility note; Claude `usage()` must not inherit `Read`'s `ErrNoTurns`.

Remaining items intentionally left to Phase 2 (verified with real fixtures, not
guessed in the design): the exact Codex multi-rollout enumeration and resume
append-vs-fork behaviour, and whether Claude stores all subagent usage in the
one located file. Both are called out inline above.

## Other Notes

### References

- Issue: https://github.com/d0ugal/graith/issues/644
- Readers: `internal/agent/transcript/transcript.go` (`Read`, `Supported`,
  `reader`), `claude.go` (`claudeReader`, `claudeMessage`, `walkChain`,
  `locateClaude`), `codex.go` (`codexReader`, `token_count` skip at `:386`,
  `CodexSessionIDSince` at `:217`, `.zst` skip at `:46`).
- State/projection: `internal/daemon/state.go` (`SessionState`, `PRStatus`
  immutable-replacement note at `:177`, `cloneSessionState` at `:196`),
  `internal/protocol/messages.go` (`SessionInfo`), `internal/daemon/handler.go`
  (`toSessionInfo` at `:2063`, list projection at `:376`).
- Loops: `internal/daemon/prwatch.go` (`RunPRWatchLoop`, `prWatchTargets`,
  per-tick cap at `:129`), `internal/daemon/gitpull.go` (startup sweep at `:20`);
  registration at `internal/daemon/daemon.go:6059`.
- CLI: `internal/client/sessioncols.go` (`SessionColumns`),
  `internal/cli/list.go` (`findSession`), `internal/cli/delete.go`
  (`resolveByNameOrID`), `internal/cli/root.go` (command registration).
- Migration continuity: `internal/daemon/migrate.go` (`MigratedFrom` at `:149`).
- Related memory: token-accounting feasibility; #644 transcript-format gotchas.

### Implementation Notes

- **Phasing.** (1) reader `Locate`/`UsageFrom` for Claude+Codex with the
  exclusive mappings + dedup + validation, driven by real fixtures; (2)
  `RunTokenLoop` + `SessionState.Tokens` + `SessionInfo.Tokens`; (3) the
  `tokens` `--wide` column + `gr tokens` command + `website/content/docs/`
  updates (`commands.md`; `configuration.md` only if a knob is later added).
  Ships as one core PR; cost and budgets are separate follow-ups.
- **Fixture-first for the format-sensitive bits.** The Codex enumeration/resume
  behaviour and the Claude subagent storage layout are verified with captured
  real transcripts before the mapping code is trusted.
- **No migration.** Runtime-only `json:"-"`; no `CurrentStateVersion` bump.
- **Defensive parsing carries over.** `UsageFrom` uses the same
  skip-and-count-unparseable-lines discipline as `read()`; `Usage.Dropped`
  records drift/conflicts for diagnostics.

### USD cost (follow-up, designed)

Cost = Σ over models of (per-model tokens × per-model unit price), with input,
output, cache-read, and cache-write priced separately (cache reads are far
cheaper). Prerequisites:

- **A user-supplied price table** in config (`[[tokens.price]] model = "…"` with
  `input`/`output`/`cache_read`/`cache_write` USD-per-Mtok). **Opt-in and
  user-owned** because prices go stale and vary by plan/region — graith must not
  bundle a silently-drifting table.
- **Accurate per-model attribution**, which v1 deliberately does *not* build:
  Claude carries `message.model` per record (easy), but Codex's cumulative
  `token_count` can't be attributed to a model by reading one `turn_context` —
  a rollout may span models, so correct attribution needs deltas between
  successive cumulative records keyed to the active model, with reset handling.
  The follow-up designs that algorithm and adds a per-model breakdown to
  `TokenStats`/`TokenInfo`; models absent from the table render `—` for cost,
  never a guess.
- **Why not read `costUSD` from the transcript?** Verified 2026-07-13: current
  Claude Code writes **no** `costUSD` field (0 records across a full live
  transcript). Relying on it would produce silently-zero costs; the price-table
  path is the only reliable one and works for Codex too.

### Budget caps (follow-up, designed)

A `[tokens.budget]` block with per-session / per-scenario / per-day limits.
After each poll, `RunTokenLoop` compares totals to the limit and, on breach,
delivers a warning to the session inbox (soft cap) or stops the agent and
notifies the orchestrator via `gr notify` (hard cap). Deferred: enforcement
policy, reset windows, and group accounting each warrant their own design once
core counts exist and can be observed in practice.

### Lifetime-across-migration ledger (follow-up, designed)

To count a graith session's *lifetime* spend across cross-agent migration:
persist a small per-session ledger keyed by native-transcript identity
(agent + agentSessionID). Before a migration swaps `Agent`/`AgentSessionID`,
freeze the current source's last full total into the ledger as a baseline; the
displayed total is then `Σ frozen baselines + current-source recompute`. This
needs a persisted field (an optional `TokenLedger` on `SessionState`, possibly a
state-version bump) but **not** per-message-id persistence — dedup still happens
within each full scan. Deferred so v1 stays migration-free.

### Alternatives considered

- **Persist token totals in `state.json`** for all sessions (not just the
  migration ledger). Rejected for v1: adds a migration for the common case where
  runtime recompute already repopulates in one tick (PR/CI/git precedent). The
  targeted ledger above is the narrower persisted piece, reserved for the
  lifetime follow-up.
- **Incremental offset reads.** Deferred, not rejected: a *runtime* cursor
  (byte offset + per-file seen-id set for Claude; tail-to-last-`token_count` for
  Codex) rebuilt after restart and invalidated on size/inode regression captures
  most of the active-session cost with no durable state. v1 ships full-rescan +
  fingerprint cache + batch cap; this is the first optimization if it proves
  costly.
- **Lazy compute on `gr tokens`/`gr list`** (no loop). Rejected: puts synchronous
  file reads on the interactive path and gives a future TUI column nothing to
  poll.
- **Additive Codex mapping** (first draft). Rejected: double-counts overlapping
  sub-fields (see Consensus).

### Testing

- **Reader unit tests** with real-format fixtures (Scots-word session
  names/fixtures per house style):
  - Claude: **dedup regression** — multiple block-lines sharing one `message.id`
    counted once (fails a naive sum); duplicate with *differing* usage →
    larger-per-field + `Dropped` conflict; no-id record → counted + `degraded`;
    user line (`Usage==nil`) skipped; in-file sidechain counted; string vs array
    content; drifting/partial trailing line skipped; empty-but-parsed → `Found=false`
    (not `ErrNoTurns`).
  - Codex: **exclusive-mapping arithmetic** against provider `total_tokens`
    (the 828,872 sample); overlapping-subset validation + `Unclassified`
    fallback on mismatch; **cumulative last-wins** within a file; **multi-rollout
    sum** across files; legacy `total` → `Unclassified` (not `Output`); missing
    `token_count` → `Found=false`.
- **Loop tests:** eligibility (running/stopped/errored, supported only, mirror
  *included*, soft-deleted excluded-but-retained, purged pruned); fingerprint
  cache reuse + invalidation on size/mtime change; retain-on-error (transient
  error doesn't clear a known total); startup sweep; batch cap; ctx cancellation.
- **CLI/formatting tests:** `cliTokens` humanisation (unknown `—` vs zero `0` vs
  `k`/`M`, degraded `~`); `gr tokens` table + `--json`; `resolveByNameOrID`
  ambiguity rejection; unsupported → `—`.
- Coverage bar (≥80%) and `-race` apply; new behaviour ships with its tests in
  the same PR.

### Open questions

- Poll interval: 30s constant proposed; revisit a config knob only if a tuning
  need appears (would pull in `[tokens]` config + `configuration.md`).
- Degraded-count presentation: is a trailing `~` in `gr list --wide` clear
  enough, or should degraded totals be suppressed to `—`?
