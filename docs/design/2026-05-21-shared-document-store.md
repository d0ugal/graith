# Shared Document Store for Graith

---
authors: Dougal Matthews
created: 2026-06-15
status: Draft
reviewers: TBD
informed: TBD
---

## Background

Graith manages AI coding agent sessions in isolated git worktrees. Each session gets its own
worktree and branch, which is cleaned up when the session is deleted. This isolation is a
strength — agents can work in parallel without stepping on each other — but it means any files
an agent writes that aren't committed to git are lost when the worktree is removed.

Agents frequently produce artifacts that are valuable but shouldn't be committed: design
documents, research notes, review reports, investigation findings, build analysis.
Today, these artifacts are either committed to the repo (polluting git history) or lost when
the session ends.

The inter-agent messaging system (`gr msg`) provides ephemeral communication, but messages are
transient and not designed for durable document storage. There is no mechanism for agents to
persist structured or unstructured artifacts that survive the session lifecycle.

## Problem

When a graith session is deleted, its worktree is removed. Any files the agent created that
weren't pushed to a remote branch are permanently lost. This creates several problems:

1. **Artifact loss**: Design docs, research findings, and analysis written by agents disappear
   with the worktree unless manually extracted first.

2. **No cross-session sharing**: Agent A cannot easily share a document with Agent B. The
   messaging system works for short text but isn't suited for multi-paragraph documents or
   structured data.

3. **No historical record**: Repeated operations (like review runs) produce results
   that would be valuable to compare over time, but there's no persistent store for them.

4. **Git pollution**: The workaround — committing artifacts to the repo — clutters git history
   with files that don't belong in the codebase.

## Goals

- Provide a durable, repo-scoped store for documents and structured data that persists beyond
  session lifetimes.
- Expose the store via CLI commands (`gr store`) that agents can call without special setup.
- Support both human-readable documents (markdown) and machine-readable data (JSON).
- Keep the design simple: flat key-value store, no hierarchies, no versioning, no access
  control.

### Non-Goals

- **Version history**: The store does not track revisions. Overwriting a key replaces the
  previous value. If you need history, use timestamped keys (e.g.
  `reviews/2026-06-15T14:30`).
- **Binary storage**: The store is text-only. Binary artifacts (images, compiled binaries)
  should use other mechanisms.
- **Cross-repo sharing**: Documents are scoped to a single repo. There is no global namespace
  or cross-repo query capability.
- **Full-text search**: No search beyond key prefix filtering. Build search as a separate tool
  that reads from the store if needed.
- **Automatic cleanup / TTL**: Documents persist until explicitly deleted. No expiration.

## Proposals

### Proposal 0: Do Nothing

Continue with the current state. Agents commit artifacts to git or lose them with the
worktree. Cross-session sharing happens via `gr msg` with body text pasted inline.

**Pros:**
- No implementation effort.
- No new surface area to maintain.

**Cons:**
- Artifacts continue to be lost or committed inappropriately.
- The independent review and similar tools cannot build historical records.
- Agents working in parallel have no shared document space.

### Proposal 1: SQLite-Backed Document Store via Daemon Protocol

Add a new SQLite database (`docstore.sqlite`) in the graith data directory, accessed through
the existing daemon control protocol. Documents are key-value pairs scoped by canonical repo
path.

#### Data Model

Each document has:

| Field | Type | Description |
|-------|------|-------------|
| `repo` | TEXT, PK part | Canonical repo path (e.g. `/Users/dev/Code/graith`) |
| `key` | TEXT, PK part | Slash-separated identifier (e.g. `reviews/2026-06-15T14:30`) |
| `body` | TEXT | Document content (markdown or JSON) |
| `content_type` | TEXT, required | MIME type — must be non-empty. `text/markdown` or `application/json` in practice |
| `author_id` | TEXT | Session ID of the writer |
| `author_name` | TEXT | Session name of the writer |
| `created_at` | TEXT | RFC3339Nano timestamp, set on first write |
| `updated_at` | TEXT | RFC3339Nano timestamp, set on every write |

Primary key is `(repo, key)`. Writes are upserts — a second write to the same key replaces
the body but preserves `created_at`.

`content_type` is required on every put. The CLI expands shorthands: `md` →
`text/markdown`, `json` → `application/json`, `text` → `text/plain`. Full MIME types are
also accepted. The daemon rejects puts with an empty content type.

#### Architecture

```
┌──────────────┐     ┌──────────────┐     ┌─────────────────┐
│  gr store    │────▶│   graithd    │────▶│ docstore.sqlite │
│  (CLI)       │◀────│  (handler)   │◀────│  (SQLite WAL)   │
└──────────────┘     └──────────────┘     └─────────────────┘
   control msg          DocStore
   over Unix socket     methods
```

The store is a new subsystem alongside the existing MsgStore. It follows the same patterns:

- SQLite with WAL mode and busy timeout (same as `messages.sqlite`)
- Mutex-protected operations in a `DocStore` struct
- Initialized during daemon startup in `Run()`, wired to `SessionManager`
- New control message types dispatched in `handler.go`

No new wire channels — store operations use channel 0x00 (JSON control messages), the same as
every other control operation.

#### CLI Interface

```bash
# Store a document (--type is required)
gr store put design/api.md --type md --file ./api-design.md
gr store put design/api.md --type md "# API Design\n\n..."
echo '{"score": 85}' | gr store put reviews/2026-06-15 --type json

# Retrieve a document (prints body to stdout)
gr store get design/api.md

# List documents (optional prefix filter)
gr store list
gr store list reviews/
gr store ls design/

# Remove a document
gr store rm design/api.md

# Explicit repo scoping (when not in a session or overriding)
gr store list --repo ~/Code/graith
```

Repo resolution order for scoping:
1. `--repo` flag (explicit)
2. `GRAITH_SESSION_ID` → look up session's `RepoPath` from daemon state
3. CWD's git root (`git rev-parse --show-toplevel`)

Agent mode auto-detection applies: when `gr` detects it's running inside an agent session, it
auto-enables JSON output for `gr store list`. `gr store get` always outputs the raw document
body regardless of mode, so it can be piped directly into files or other commands.

#### Protocol Messages

Four new control message types, following the existing envelope pattern:

| Message Type | Direction | Purpose |
|-------------|-----------|---------|
| `store_put` | client → daemon | Upsert a document |
| `store_get` | client → daemon | Retrieve a document by key |
| `store_list` | client → daemon | List documents (with optional prefix) |
| `store_delete` | client → daemon | Remove a document |
| `store_ok` | daemon → client | Acknowledgment for put/delete |
| `store_get_response` | daemon → client | Document (or not-found) response |
| `store_list_response` | daemon → client | List of document metadata |

#### Conventions

Two content types cover the expected use cases:

- **`text/markdown`** — human-readable documents: design docs, research notes, plans.
  Key convention: descriptive names like `design/api.md`, `notes/findings.md`.

- **`application/json`** — machine-readable structured data: review reports, build results,
  metrics. Key convention: timestamped entries like `reviews/2026-06-15T14:30`,
  `builds/2026-06-15-abc123`. Tools that produce repeated output use a common prefix with
  timestamp suffixes so `gr store list <prefix>/` shows history.

The convention is unenforced — the store doesn't validate that markdown keys end in `.md` or
that JSON bodies parse. It stores what you give it.

**Pros:**
- Follows established graith patterns (MsgStore, protocol, handler dispatch).
- No new infrastructure — SQLite, same driver, same data directory.
- Daemon-mediated access means no symlinks or filesystem sharing to manage.
- Works naturally with the worktree model — no per-session setup needed.
- Required content type makes it unambiguous what a document contains.

**Cons:**
- Documents can't be accessed via plain file I/O (must use `gr store` CLI).
- Large documents go through the Unix socket as JSON-encoded strings (base64 for binary would
  be needed if binary support were added later, but it's a non-goal).
- No indexing or search beyond prefix matching on keys.

#### Alternative Considered: Filesystem with Symlinks (Hive's Approach)

Hive uses a shared filesystem directory (`~/.local/share/hive/context/{owner}/{repo}/`) with a
`.hive` symlink in each worktree pointing to it. Agents read and write documents as regular
files.

This was considered but rejected for graith because:

- **Symlink management**: Graith would need to create and maintain symlinks in every worktree,
  adding a new failure mode. Hive requires an explicit `hive ctx init` step; graith's
  worktrees are created automatically and we'd need to wire symlink creation into the session
  lifecycle.
- **Sandbox complications**: Graith supports sandboxed sessions via `safehouse`. File-based
  access would require adding the shared directory to every sandbox's allowed paths, and the
  sandbox config is baked at session creation time.
- **Consistency**: All other graith operations go through the daemon protocol. A filesystem
  side-channel would be architecturally inconsistent.
- **Concurrency**: The daemon's mutex-protected SQLite operations handle concurrent writes
  safely. Filesystem writes from multiple agents to the same file would need separate locking.

## Consensus

Proposal 1 (SQLite-backed store via daemon protocol) is the recommended approach. It fits
graith's architecture, requires no new infrastructure, and addresses all stated goals.

## Other Notes

### References

- [Graith CLAUDE.md](../../CLAUDE.md) — project conventions and architecture
- [Implementation plan](../superpowers/plans/2026-06-15-shared-document-store.md) — task-level
  breakdown
- Hive shared context (`~/Code/hive`) — alternative approach studied for comparison

### Implementation Notes

Implementation is broken into 7 tasks in the implementation plan:

1. DocStore SQLite backend (`internal/daemon/docstore.go`)
2. Protocol message types (`internal/protocol/messages.go`)
3. Daemon handler integration (`internal/daemon/handler.go`, `daemon.go`, `config/paths.go`)
4. CLI commands (`internal/cli/store.go`)
5. Integration smoke test
6. Documentation (CLAUDE.md)
7. Format and final verification

The work is self-contained — no breaking changes to existing functionality.
