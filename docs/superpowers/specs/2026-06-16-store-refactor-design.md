# Store Refactor: SQLite to Flat Files with Git

Replace the SQLite-backed document store with a flat-file git-backed store.
Documents become plain files on disk, managed in per-repo git repositories.

## Motivation

The current store uses SQLite, which makes documents invisible to IDEs,
editors, grep, and standard filesystem tools. The store's operations
(put/get/list/rm) map directly to filesystem operations — the database layer
adds indirection without adding capability. A flat-file approach gives us IDE
browsability, git history for free, and works even when the daemon is down.

## Store location and layout

Each project repo gets its own git-managed store directory under the graith
data directory:

```
~/.local/share/graith/store/<reponame>-<hash>/
├── .git/
├── design/
│   └── api.md
├── research/
│   └── findings.json
└── tribunal/
    └── 2026-06-15.md
```

The `<reponame>-<hash>` naming follows the same pattern as the existing share
directory (using `repoHash`). On first write, the CLI runs `git init` if the
directory doesn't exist yet.

Keys map directly to file paths — `design/api.md` becomes
`<store-root>/design/api.md`. Parent directories are created automatically on
put and cleaned up on rm.

## CLI commands

All four commands remain. They operate directly on the filesystem instead of
going through the daemon. No daemon connection is needed for store operations.

### Write protection

Write operations (`put`, `rm`) are restricted to the current repo's store.
The "current repo" is resolved from `GRAITH_SESSION_ID` (session's RepoPath)
or the CWD git root. If `--repo` is explicitly set and doesn't match the
auto-detected current repo, write commands reject with an error. Read commands
(`get`, `list`) work on any repo via `--repo`.

When there is no auto-detected current repo (e.g. running outside a git repo
without `GRAITH_SESSION_ID`), `--repo` is required and write operations are
allowed since the user is explicitly choosing the target.

### `gr store put <key> [body]`

- Resolves store path for the current repo
- Enforces write protection
- Creates parent directories as needed
- Writes file content from: positional arg, `--file` flag, or stdin
- Runs `git add <key> && git commit`

### `gr store get <key>`

- Reads the file and prints the body to stdout
- Works on any repo via `--repo`

### `gr store list [prefix]`

- Lists files under the prefix using recursive directory walk, skipping `.git/`
- Human output: tabwriter with KEY and UPDATED columns (mod time from filesystem)
- JSON output: array of objects with `key` and `updated_at` fields

### `gr store rm <key>`

- Enforces write protection
- Runs `git rm <key> && git commit`
- Cleans up empty parent directories after removal

### Dropped flags and fields

- `--type` flag — removed; content type is implicit from the file extension
- Author fields in list output — removed; authorship is in git history
- Content type in list output — removed; visible from the file extension

### Kept flags

- `--file` / `-f` — read body from a file path
- `--repo` — explicit repo path override (read-only unless no current repo)

## Git commit messages

Every `put` and `rm` auto-commits. The commit message includes agent context
when available via environment variables:

```
store: update design/api.md

session: fix-overlay (abc123)
agent: claude
```

The trailer lines (`session:` and `agent:`) are only included when
`GRAITH_SESSION_ID` and `GRAITH_SESSION_NAME` are set. `agent:` uses
`GRAITH_AGENT_TYPE`. When running outside a graith session, the commit
message is just the first line.

## New package: `internal/store`

A new package encapsulates all store logic, keeping CLI commands thin:

- `StorePath(dataDir, repoRoot string) string` — resolves the store directory
  path: `<datadir>/store/<reponame>-<hash>/`
- `Init(storePath string) error` — runs `git init` if the `.git` directory
  doesn't exist
- `Put(storePath, key, body string) error` — writes file, creates parent
  dirs, `git add`, `git commit`
- `Get(storePath, key string) (string, error)` — reads and returns file
  contents
- `List(storePath, prefix string) ([]Entry, error)` — recursive directory
  walk, returns key + mod time
- `Remove(storePath, key string) error` — `git rm`, commit, clean up empty
  parent dirs
- `CommitMessage(action, key string) string` — builds the commit message
  with optional agent trailer from environment variables

Git operations shell out to `git` via `exec.Command`, consistent with how
graith handles git elsewhere (worktree creation, branch management).

## Code removed

- `internal/daemon/docstore.go` and `internal/daemon/docstore_test.go` —
  entire SQLite store implementation
- Four handler cases in `daemon/handler.go` — `store_put`, `store_get`,
  `store_list`, `store_delete` (~120 lines)
- Protocol message types in `protocol/messages.go` — `StorePutMsg`,
  `StoreGetMsg`, `StoreListMsg`, `StoreDeleteMsg`, `StoreGetResponseMsg`,
  `StoreListResponseMsg`, `StoreDocument`
- `DocStoreDB` path from `config/paths.go`
- `SetDocStore` / `docStore` field from `daemon/daemon.go`
- SQLite dependency for the store (the `modernc.org/sqlite` import remains if
  `msgstore.go` still needs it)

## CLI rewrite

`internal/cli/store.go` is rewritten to:

1. Resolve the store path (using the new `store` package)
2. Check write permission for mutating commands
3. Call into the `store` package directly
4. No daemon connection needed

The `resolveRepoPath` function is rewritten to no longer need a daemon
connection. The current implementation queries the daemon's session list to
find the session's RepoPath; the new version resolves the repo root from
`GRAITH_WORKTREE_PATH` (always set in agent sessions) or CWD via
`git rev-parse --show-toplevel`. The `expandContentType` function is removed.

## Concurrent access

Git handles concurrent access natively. If two agents write to different keys
simultaneously, both commits succeed. If they write to the same key, one
`git commit` will succeed and the other will see a dirty index — the worst
case is a failed commit that the agent can retry. In practice, agents work on
different keys so this is a non-issue.
