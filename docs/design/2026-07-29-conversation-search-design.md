---
title: "Design Doc: Conversation Search"
authors: Dougal Matthews
created: 2026-07-29
status: Implemented (v1)
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1455
---

# Conversation Search

Graith should let a user find the session that contains a remembered prompt,
error, file name, command output, or decision. V1 adds local, provider-neutral
literal search over the Claude Code and Codex transcript readers that graith
already trusts for migration and token accounting, exposed through a daemon RPC
and `gr search`.

## Background

Graith already reads provider transcripts in `internal/agent/transcript/` for
cross-agent migration, forks, resume checks, and token accounting. Those readers
normalize Claude Code and Codex JSONL into a provider-neutral conversation model
and intentionally skip records that should not become portable user-visible
context, including hidden reasoning. They are defensive against malformed,
oversized, and partially-written transcript lines.

The existing session picker filter is metadata-only: session name, repo, branch,
labels, and status. Scrollback search is attached-session only. Neither helps
when the user remembers a phrase from a prior conversation but not the session
that produced it.

## Problem

Without global conversation search, users inspect transcripts manually or attach
to likely sessions one at a time. That is slow, error-prone, and gets worse as a
graith fleet grows. A naive filesystem grep would solve the speed problem by
breaking the safety model: it would parse provider formats independently,
include fields the canonical readers deliberately ignore, and risk turning the
daemon into an arbitrary file search endpoint.

## Goals

- Reuse `internal/agent/transcript` as the only provider parser in v1.
- Support Claude Code and Codex transcripts in a provider-neutral result model.
- Keep search local to the daemon and never send content to an external service.
- Support filters for session/subtree, repo, agent, time, message kind, live vs
  soft-deleted sessions, state, result limit, and cursor pagination.
- Return enough context to act: session metadata, message kind/timestamp when
  known, UTF-8-safe snippets with match ranges, and an opaque locator.
- Avoid reparsing unchanged transcript files on every query.
- Keep file I/O off the session-manager lock and bound reads, snippets, result
  counts, and cached memory.
- Report unsupported agents explicitly.

### Non-Goals

- Semantic, fuzzy, vector, or embedding search.
- Searching worktree files or arbitrary paths.
- Indexing hidden reasoning or transcript fields excluded by the canonical
  readers.
- Persisted SQLite FTS in v1.
- Full native interactive search UI in the first implementation slice.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted in v1 | `gr search` is the first user-facing surface, supports JSON, and is easy to compose with shell tools. |
| Daemon/wire model | Targeted in v1 | The search contract must be shared and implementation-neutral before native UI work. |
| Session picker TUI | Planned | The picker needs a distinct global-conversation mode and navigation affordance, separate from `/` metadata filtering. |
| iOS | Planned | Native search should use the same daemon query/result model, but the first PR only adds the wire contract and shared model hooks needed for a later UI. |
| macOS | Planned | Same as iOS: use the shared daemon model, then add platform-appropriate presentation and navigation. |

## Proposals

### Proposal 0: Do Nothing

Keep search scoped to metadata filters and attached-session scrollback. This
preserves the current safety boundary but leaves a core fleet workflow unsolved.
Rejected.

### Proposal 1: Bounded on-demand scanning with an in-memory cache (Recommended)

The daemon snapshots eligible sessions under the session-manager lock, releases
the lock, resolves each session's transcript through `transcript.LocateWithRoot`,
and parses changed files through the canonical reader. Codex sessions persist
the agent-native transcript root used at launch/capture so captured native ids
continue to resolve after daemon restart or after `NativeStateRoot` recovery
metadata is cleared. Parsed search entries are cached by session id plus a
fingerprint of agent, native session id, native transcript root, worktree, source
path, size, and mtime. Unchanged transcripts are reused across queries, so
repeated search does not reread idle sessions.

Search remains an implementation detail behind the wire contract:

- Request: literal query, optional filters, limit, and cursor.
- Response: ordered results, next cursor, truncation flag, and unsupported-agent
  counts.
- Result: session id/name, repo path/name, agent, native agent session id,
  message kind, timestamp when parseable, snippet, match ranges, and opaque
  locator.

Ordering is deterministic: newest message timestamp first when available, then
session creation time, session id, migrated/current generation, agent, native
agent session id, and turn index. One transcript turn yields at most one search
result; match ranges point into the bounded, terminal-control-sanitized snippet
rather than the full message body. Pagination uses an opaque offset cursor over
that ordered result set.
Sessions with `MigratedFrom` provenance search the current transcript and the
persisted source transcript as separate generations, including the source
generation's native transcript root when it is known. V1 does not coalesce
duplicates across those generations; the result `agent`, `agent_session_id`, and
locator disambiguate them for clients.

Soft-deleted sessions are excluded unless explicitly requested. Purged sessions
are absent from state and their cache entries are pruned during each search, so
no persisted index data survives purge. Unsupported agents are skipped with a
count in the response rather than being treated as zero-result sessions.
Cold parsing is cancellable between bounded line reads and capped per source at
the `[search]` `max_source_bytes` and `max_source_turns` defaults (16 MiB and
10,000 turns); responses set `truncated` when these bounds or result window
limits are hit. When a cold parse hits those source bounds, v1 keeps the oldest
records it read and omits later transcript content.

Trade-offs:

- Good: no new storage dependency, no provider parser duplication, safe first
  slice, restart naturally rebuilds cache on demand.
- Good: append/replacement/truncation are detected by source size+mtime and
  become searchable on the next query.
- Cost: a broad cold query still scans eligible transcript files once.
- Cost: cursor pagination is not a durable snapshot if transcripts change
  between pages.

### Proposal 2: SQLite FTS

Persist a normalized per-turn table and index it with SQLite FTS. This gives
fast queries and durable pagination for large fleets, and is likely the right
answer if search becomes a hot workflow over thousands of sessions.

Rejected for v1 because lifecycle correctness becomes much larger: append,
replacement, truncation, resume, migration, restore, purge, and daemon restart
all need durable index maintenance before the first useful CLI surface ships.
The v1 wire contract deliberately avoids exposing the backend so SQLite FTS can
replace the scanner later.

### Proposal 3: External grep over transcript roots

Run ripgrep over known provider transcript roots and then map files back to
sessions. This is fast but violates the parser boundary, risks matching hidden
or unsupported fields, and has weaker authorization and path scoping. Rejected.

## Other Notes

### References

- `internal/agent/transcript/` canonical Claude/Codex readers.
- `docs/design/2026-06-24-cross-agent-conversation-migration-design.md`.
- `docs/design/2026-07-13-per-session-token-accounting.md`.
- `internal/daemon/authmatrix.go` remote message policy.
- `website/content/docs/commands/` user command docs.

### Implementation Notes

V1 search is literal and case-insensitive. It lowercases runes one-to-one for
matching, so snippets and match ranges stay valid UTF-8 and do not depend on
byte offsets in transformed text.

The daemon never logs query text or matched body content at normal log levels.
File reads only occur through transcript sources resolved from existing session
metadata. The search cache is process-local, capped, and discarded on restart.

### Testing

Focused coverage should include:

- Claude and Codex reader timestamps and hidden reasoning exclusion.
- Result filtering by repo, session subtree, agent, kind, time, state, and
  deleted-session inclusion.
- Unicode snippets and match ranges.
- Malformed/oversized transcript records not crashing search.
- Cache reuse for unchanged transcripts and invalidation on append/truncation.
- Migrated-source generation inclusion and deterministic duplicate ordering.
- Handler authorization and remote policy completeness.
- CLI JSON and human rendering.
