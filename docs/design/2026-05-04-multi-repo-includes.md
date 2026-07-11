---
authors: Dougal Matthews
created: 2026-06-11
status: Draft
reviewers: TBD
informed: TBD
---

# Multi-Repo Sessions ("includes")

## Background

graith creates isolated git worktrees for each agent session. An agent working
on `grafana` gets its own worktree and branch, sandboxed so it can only write
to that worktree. This model works well for single-repo tasks.

Some repositories are not standalone — they are **dev-env orchestrators** that
compose multiple repos together. For example:

- `~/Code/devenv` — Docker Compose-based local dev environment that mounts
  `~/Code/grafana`, `~/Code/examples`, and other repos
- `~/Code/examples` — references `~/Code/rrweb` and
  `~/Code/faro-web-sdk`

These orchestrators reference sibling repos by relative path (`../grafana`) or
absolute path (`~/Code/grafana`).

graith recently added `--in-place` mode to run agents directly in the repo
directory without creating a worktree. This was intended for orchestrators, but
it has two fundamental problems.

## Problem

### 1. Stale references

When a graith agent works on `~/Code/grafana`, it works in a worktree at
`~/.local/share/graith/worktrees/grafana/<hash>/<id>`. But an orchestrator
running in `~/Code/devenv` references `~/Code/grafana` — the main checkout,
not the worktree. The orchestrator sees unmodified code, not the agent's
in-progress changes.

### 2. Read-only sandbox

The sandbox gives agents read-only access to repos outside their own worktree.
Orchestrators need write access to referenced repos — they run builds, generate
files, modify configs, and start services in those directories.

### 3. Singleton runtime resources

Orchestrators like devenv manage Docker containers, networks, port bindings,
and running processes. These are global resources — two instances of devenv
cannot run simultaneously. Creating multiple worktrees for the orchestrator does
not create multiple isolated runtimes.

## Goals

- An agent working on an orchestrator repo can read and write to the correct
  versions of all referenced repos
- Relative path references between repos (`../grafana`) work without
  configuration in the orchestrated repos
- The sandbox enforces write access only to the session's own worktrees, not to
  the user's main checkouts
- Orchestrator repos with singleton constraints (Docker, ports) can be limited
  to one active session
- The feature composes with existing graith capabilities: resume, delete, fork,
  messaging, sandbox

### Non-Goals

- Transparent absolute path redirection (macOS has no unprivileged per-process
  bind-mount namespace — `sandbox-exec` resolves symlinks to real paths)
- Replacing `--in-place` mode (it remains useful for simpler cases)
- Building a verification queue or orchestrator-as-service protocol (this can
  be built on top using `gr msg`, but is out of scope for this design)
- Multi-agent concurrent access to the same orchestrator session
- Automatically consuming another session's in-progress changes (e.g., a
  devenv session seeing a standalone grafana agent's uncommitted work).
  Includes sessions create fresh worktrees from the default branch. Cross-
  session verification is handled by the orchestrator-as-service pattern
  (future work) where the orchestrator checks out requested branches on
  demand via `gr msg`.

## Proposals

### Proposal 0: Do Nothing

Keep `--in-place` as the only option for orchestrators. Users manually configure
sandbox `write_dirs` to include referenced repos.

**Consequences:**

- Agents always see the main checkout versions of referenced repos, not worktree
  versions — the stale reference problem persists
- Broad `write_dirs` (e.g., `~/Code`) breaks isolation between agents
- No guard against multiple orchestrator sessions conflicting on Docker/ports
- Users must manually coordinate which branch is checked out in each referenced
  repo

**Pros:**

- No implementation work
- No new concepts to learn

**Cons:**

- The core use case (orchestrator sees agent's changes) is unsolved
- Sandbox isolation is weakened or broken for orchestrator sessions
- No singleton enforcement — user errors cause Docker conflicts

### Proposal 1: Multi-repo worktree sessions with `includes`

Add an `includes` field to `[[repos]]` config. When creating a session for a
repo with `includes`, graith creates worktrees for the main repo AND all
included repos, arranged as siblings under a shared parent directory. Add a
`singleton` field to enforce at most one active session.

#### Configuration

```toml
[[repos]]
path = "~/Code/devenv"
singleton = true
includes = ["~/Code/grafana", "~/Code/examples"]

[[repos]]
path = "~/Code/faro-web-sdk"
includes = ["~/Code/rrweb"]
```

Both fields are optional. A repo with no `includes` behaves exactly as today.

The `singleton` field is independent of `includes` — it can be used alone to
limit any repo to one active session, though the primary use case is
orchestrators with global runtime resources.

**Config validation rules for `includes`:**

- Each path must point to an existing git repository
- Each path must pass `allowed_repo_paths` validation (same as regular repos)
- A repo cannot include itself
- Duplicate entries are rejected
- Paths are resolved via `config.ResolvePath` (symlink resolution, `~` expansion)
- All basenames (`filepath.Base`) must be unique across the main repo and all
  includes — collisions would create conflicting worktree paths in the session
  directory. This includes case-insensitive comparison on macOS (HFS+/APFS).
- All derived environment variable names must be unique — e.g., `foo-bar` and
  `foo.bar` both normalize to `GRAITH_INCLUDE_FOO_BAR_PATH`

**Config validation timing:** Validation runs at config load (daemon startup)
and on hot-reload. Invalid includes fail the reload and keep the previous
config — existing sessions are not disrupted. Session creation also validates
at runtime (the included repo must still exist and be a git repo).

**Concurrent access to included repos:** An included repo can have active
graith sessions in separate worktrees at the same time. Git supports multiple
worktrees per repo — each session gets its own branch and worktree directory.
There is no conflict. For example, an agent working on `grafana` in a standalone
session and a `devenv` includes session that also creates a `grafana` worktree
are fully independent — different branches, different directories.

Updated config struct:

```go
type RepoConfig struct {
    Path            string   `toml:"path"`
    AllowConcurrent bool     `toml:"allow_concurrent"`
    Singleton       bool     `toml:"singleton"`
    Includes        []string `toml:"includes"`
}
```

> **Note on `singleton` vs `allow_concurrent`:** `allow_concurrent` applies
> only to `--in-place` sessions. `singleton` applies to all session types
> (worktree, in-place, includes). Setting both `singleton = true` and
> `allow_concurrent = true` on the same repo is a config validation error.
>
> **Singleton enforcement mechanism:** When `singleton = true`, session
> creation iterates all sessions in state with a matching `RepoPath` and
> `Status == running` (regardless of session type — worktree, in-place, or
> includes). If any running session exists, creation fails with an error
> naming the existing session. Singleton enforcement only considers sessions
> where the repo is the main repo (`RepoPath`), not sessions where it appears
> as an include.
>
> **Note on stopped sessions:** A stopped singleton session may leave behind
> Docker containers, networks, or other external resources that graith does not
> manage. The singleton check only blocks concurrent running sessions — users
> must clean up external resources manually before starting a new session. This
> is consistent with graith's role as a session manager, not a container
> orchestrator.

#### Filesystem layout

Today, a regular worktree session creates:

```
<data_dir>/worktrees/<repoName>/<repoHash>/<sessionID>/
  (this IS the git worktree — files live directly here)
```

With `includes`, the session directory becomes a container for multiple
worktrees:

```
<data_dir>/worktrees/<repoName>/<repoHash>/<sessionID>/
  <repoName>/              # git worktree for the main repo
  <includedRepo1>/         # git worktree for first included repo
  <includedRepo2>/         # git worktree for second included repo
```

Concrete example for devenv:

```
~/.local/share/graith/worktrees/devenv/<hash>/<sessionID>/
  devenv/                        # git worktree for ~/Code/devenv
  grafana/                        # git worktree for ~/Code/grafana
  examples/        # git worktree for ~/Code/examples
```

The agent's working directory is set to `.../<sessionID>/devenv/`. From there,
`../grafana` resolves to the session's grafana worktree — the same relative
path structure as `~/Code/`.

This layout preserves relative path compatibility without symlinks, mount
tricks, or filesystem virtualization.

#### Session creation flow

When `Create()` is called for a repo with `includes`:

1. Resolve the main repo as today (validate path, discover base branch, etc.)
2. Look up `RepoConfig` — if it has `includes`, enter multi-repo path
3. If `singleton = true`, check no other running session exists for this repo
   (across all session types: worktree, in-place, includes)
4. Create the session directory: `<data_dir>/worktrees/<repoName>/<hash>/<id>/`
5. For the main repo:
   - Create branch: `<branchPrefix>/<sessionName>-<sessionID>`
   - Create worktree at: `<sessionDir>/<repoName>/`
6. For each included repo:
   - Validate it is a git repo and path passes `allowed_repo_paths`
   - Discover its default branch (the `--base` CLI flag applies only to the
     main repo; included repos use their discovered default branch, falling
     back to the repo's current HEAD branch if `origin/HEAD`, `origin/main`,
     and `origin/master` are all absent — e.g., for local-only repos)
   - Create branch: `<branchPrefix>/<sessionName>-<sessionID>/<includedRepoName>`
   - Create worktree at: `<sessionDir>/<includedRepoName>/`
7. Set `WorktreePath` to `<sessionDir>/<repoName>/` (agent cwd)
8. Build sandbox permissions (see below)
9. Start PTY, persist state

`fetch_on_create` applies to all repos (main + includes). Since the repos are
independent, fetches can be parallelized with a `sync.WaitGroup` to avoid
serializing the network cost.

If any step fails after partial worktree creation, roll back: track exactly
which branch/worktree pairs were created and remove them in reverse order,
collecting errors via `errors.Join`. Remove the session container directory
after all child worktrees are removed.

Branch naming uses a sub-path for included repos to keep them visually grouped:

```
dougal/graith/devenv-a1b2c3d4              # main repo branch
dougal/graith/devenv-a1b2c3d4/grafana      # included repo branch
dougal/graith/devenv-a1b2c3d4/examples
```

#### Sandbox permissions

The sandbox `WorktreeDir` (safehouse `--workdir`) is set to the main repo
worktree: `<sessionDir>/<repoName>/`. Safehouse auto-grants git metadata
access (`.git` common dir, linked worktree metadata) only for `--workdir`.

For each included worktree, graith must explicitly derive and grant git
metadata paths. After creating each included worktree, run:

```
git -C <includedWorktreePath> rev-parse --absolute-git-dir --git-common-dir
```

This returns the linked worktree git dir (e.g.,
`~/Code/grafana/.git/worktrees/<name>`) and the shared git dir (e.g.,
`~/Code/grafana/.git`). Both must be added to `WriteDirs` — git operations
like commit, status, and branch management write to these locations.

The full derived write dirs for an includes session are:

```
<sessionDir>/<includedRepo1>/                          # worktree files
<sourceRepo1>/.git/                                    # shared git metadata
<sourceRepo1>/.git/worktrees/<includedWorktreeName>/   # linked worktree metadata
<sessionDir>/<includedRepo2>/
<sourceRepo2>/.git/
<sourceRepo2>/.git/worktrees/<includedWorktreeName>/
...
```

These are added automatically — the user does not need to configure
`write_dirs` for included repos. Any user-configured `write_dirs` are merged
as today.

**Important:** The sandbox does NOT grant write access to source repo working
trees (`~/Code/grafana/*.go`, etc.). However, committing from a linked
worktree necessarily writes to the source repo's shared `.git` directory
(objects, refs, index). This is how git worktrees work — the working tree
files are isolated, but the git metadata is shared.

**Derived vs user sandbox config:** Session-derived grants (included worktree
paths, git metadata paths) must be kept separate from user-configured sandbox
config. At creation time, graith merges user config + agent config as today,
then appends derived grants. The derived grants are NOT persisted in
`SandboxConfig` — they are rebuilt from `SessionState.Includes` at
creation/resume/fork time. This prevents fork from inheriting write access to
the source session's worktree paths instead of its own.

Read dirs are unchanged — the existing config-driven `read_dirs` plus graith's
standard additions (config dir, runtime dir, hook dir, etc.).

#### State changes

Add an `Includes` field to `SessionState` and bump `CurrentStateVersion` to 2
with a no-op migration (the `omitempty` tag means old state files deserialize
cleanly, but the version bump documents the schema change):

```go
type SessionState struct {
    // ... existing fields ...
    Includes []IncludedRepoState `json:"includes,omitempty"`
}

type IncludedRepoState struct {
    RepoPath     string `json:"repo_path"`      // source repo (~/Code/grafana)
    RepoName     string `json:"repo_name"`       // basename (grafana)
    WorktreePath string `json:"worktree_path"`   // session worktree path
    Branch       string `json:"branch"`          // session branch name
    BaseBranch   string `json:"base_branch"`     // base branch (main)
}
```

The primary `SessionState` fields remain unchanged:

- `RepoPath`: source path of the main repo
- `WorktreePath`: main repo worktree (agent cwd)
- `Branch`: main repo branch

#### Protocol changes

`CreateSessionInput` already has all needed fields — `includes` is derived from
config, not passed from the client. No create request changes needed. The
list/info response payload is extended (backward-compatible addition).

**Status detection:** The `detectAgentStatuses` loop must be extended to compute
`GitDirty` and `GitUnpushed` for each included worktree, not just the main
worktree. This multiplies git subprocess calls per tick — for 3 includes, 6
extra calls (status + rev-list per repo). At the current 500ms interval this
is acceptable. The top-level `SessionInfo.Dirty` and `SessionInfo.UnpushedCount`
should be aggregates (dirty if ANY repo is dirty, unpushed = sum across repos),
with per-repo detail in `Includes`.

`SessionInfo` (returned by list/info) should expose included repo state so the
overlay and CLI can display it:

```go
type SessionInfo struct {
    // ... existing fields ...
    Includes []IncludedRepoInfo `json:"includes,omitempty"`
}

type IncludedRepoInfo struct {
    RepoName     string `json:"repo_name"`
    WorktreePath string `json:"worktree_path"`
    Branch       string `json:"branch"`
    BaseBranch   string `json:"base_branch"`
    Dirty        bool   `json:"dirty"`
    Unpushed     int    `json:"unpushed"`
}
```

#### Lifecycle: Delete

Deletion must clean up all worktrees and branches. Cleanup is **best-effort
across all repos** — all teardowns are attempted even if earlier ones fail,
and errors are collected via `errors.Join` and reported together. State is
kept until teardown succeeds (not deleted before teardown, as the current
code does), so that failed cleanups can be retried via `gr delete --force`
or repaired via `gr doctor`.

1. Kill the PTY process (as today)
2. For each included repo (in state):
   - Check for dirty files and unpushed commits
   - Call `git.TeardownSession(includedRepoPath, includedWorktreePath, includedBranch)`
3. Tear down the main repo worktree and branch
4. Remove the session directory
5. Clean up logs and hooks
6. Remove state entry only after all teardowns complete (or on `--force`)

The delete confirmation in `cli/delete.go` must check dirty/unpushed status
across ALL repos (main + included), not just the main worktree. Display should
group findings by repo:

```
Session "devenv" has uncommitted changes:

  devenv:
    M docker-compose.yml

  grafana:
    M pkg/api/handler.go
    A pkg/api/new_endpoint.go

  examples:
    (clean)

Delete anyway? [y/N]
```

#### Lifecycle: Resume

Resume for includes sessions:

1. Load persisted `Includes` state
2. Validate each included worktree still exists and is a git repo
3. If a worktree is missing, fail with a clear error (don't silently skip)
4. Rebuild sandbox permissions from persisted state
5. Restart PTY in the main repo worktree

#### Lifecycle: Fork

Forking an includes session creates a new session with fresh worktrees for all
repos:

1. Create new session ID
2. Create new session directory
3. For the main repo: create branch from source's branch, create worktree
4. For each included repo: create branch from source's included branch, create
   worktree
5. Persist new state with new `Includes` entries

The fork preserves the point-in-time state of all repos — the new session
branches from wherever the source session's branches are, not from the base
branches.

**Singleton and fork:** Forking a session whose repo has `singleton = true` is
blocked — the fork would create a second running session for the same repo,
violating the singleton constraint. The CLI should error with a message
explaining the singleton restriction and suggesting the user stop the source
session first or remove the singleton constraint.

#### Lifecycle: Shared worktree

`--share-worktree` with an includes session should share the entire session
directory (main + included worktrees), all read-only. The sharing session gets
a scratch directory for writes, same as today.

The sharing session's `SessionState` does NOT get its own `Includes` field —
it references the source session via `SharedWorktreeSourceID` (a new field
replacing the current `SharedWorktree` bool — storing the source session ID
enables reliable lookup after restarts and renames). The source session's
`Includes` state is the authoritative record. When displaying dirty/unpushed
status for the shared session (e.g., in the overlay), the daemon looks up the
source session's `Includes` to enumerate all worktrees.

The sandbox read-only grants for a shared includes session must cover the
entire source session directory (main + all included worktrees), plus git
metadata paths for each included repo.

#### Absolute path handling

This design solves relative paths (`../grafana`) but does NOT solve absolute
paths (`~/Code/grafana`). Absolute references in orchestrator configs still
point to the main checkout.

Mitigations included in this proposal:

- **Environment variables** (included in this proposal): graith sets
  `GRAITH_INCLUDE_<NAME>_PATH` for each included repo. Orchestrators that
  support env-based path configuration can use these.

Future mitigations (not part of this proposal):

- **Path rewriting**: a post-creation hook that rewrites known config files
  (e.g., `.env.local`, `docker-compose.override.yml`) to use worktree paths.
- **Documentation**: guide users to configure orchestrators to use relative
  paths where possible.

graith will set environment variables for each
included repo. The `<NAME>` is derived from `filepath.Base(repoPath)`,
uppercased, with hyphens and dots replaced by underscores:

```
GRAITH_INCLUDE_GRAFANA_PATH=/.../<sessionDir>/grafana
GRAITH_INCLUDE_EXAMPLES_PATH=/.../<sessionDir>/examples
```

#### Architecture diagram

```
┌─────────────────────────────────────────────────────────────────┐
│ Session directory: <data_dir>/worktrees/devenv/<hash>/<id>/    │
│                                                                 │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────────────────┐    │
│  │  devenv/    │  │  grafana/   │  │ session-replay-      │    │
│  │  (worktree)  │  │  (worktree) │  │ examples/ (worktree) │    │
│  │             ◄────►           ◄────►                      │    │
│  │  agent cwd   │  │  ../grafana │  │  ../session-replay-  │    │
│  │              │  │  works here │  │  examples works here  │    │
│  └─────────────┘  └─────────────┘  └──────────────────────┘    │
│                                                                 │
│  sandbox: write to all three worktrees                          │
│  sandbox: read-only to configured read_dirs                     │
└─────────────────────────────────────────────────────────────────┘

Source repos (untouched):
  ~/Code/devenv/
  ~/Code/grafana/
  ~/Code/examples/
```

**Pros:**

- Relative paths between repos work without configuration
- Full isolation — agents write to session-owned worktrees, never to main
  checkouts
- Singleton enforcement prevents Docker/port conflicts
- Composes with all existing lifecycle operations (resume, delete, fork, share)
- No symlinks, no mount tricks, no filesystem virtualization
- Incremental — repos without `includes` are completely unchanged
- Sandbox permissions are derived automatically from config

**Cons:**

- More disk usage — each included repo gets a full worktree
- Absolute path references are not solved (mitigated by env vars)
- Session creation is slower — each `git worktree add` takes under 1s for
  typical repos, so 3 includes adds ~3s. Acceptable for a creation-time cost.
- Delete/fork become more complex — must iterate all included repos
- Branch namespace gets busier — one branch per included repo per session

### Proposal 2: Broad write access for in-place sessions

Instead of creating worktrees, grant in-place sessions write access to
configured repos via sandbox `write_dirs`.

```toml
[[repos]]
path = "~/Code/devenv"
write_dirs = ["~/Code/grafana", "~/Code/examples"]
```

**Pros:**

- Simple to implement — just sandbox config derivation
- No extra worktrees or disk usage
- Fast session creation

**Cons:**

- Does NOT solve the stale reference problem — the orchestrator still sees
  main checkout versions, not agent worktree versions
- Breaks isolation — the agent can modify repos other agents are working on
- No singleton enforcement
- Race conditions between agents writing to the same repos

This solves problem 2 (write access) but not problem 1 (stale references) or
problem 3 (singleton enforcement). It is strictly worse than Proposal 1 for
the orchestrator use case.

## Consensus

Proposal 1 is recommended. It solves all three problems, composes with existing
features, and the complexity is proportional to the value.

## Other Notes

### References

- [PR #329: Externalize config defaults](https://github.com/d0ugal/graith/pull/329) —
  moves config defaults to embedded TOML. The `includes` and `singleton` fields
  layer cleanly on top since `RepoConfig` is user-configured, not defaulted.
- `internal/daemon/daemon.go:264-552` — current session creation flow
- `internal/config/config.go:31-34` — current `RepoConfig` struct
- `internal/git/worktree.go:18-47` — `SetupSession` and `TeardownSession`
- `internal/daemon/state.go:26-54` — current `SessionState` struct

### Implementation notes

**Ordering:** The implementation should land in this order:

1. Config changes (`RepoConfig` struct, validation, `singleton` enforcement)
2. Session creation (multi-repo worktree setup, rollback on failure)
3. Sandbox derivation (auto-add included worktrees to `WriteDirs`)
4. State persistence (`IncludedRepoState`, serialization)
5. Delete (multi-repo dirty check, multi-repo teardown)
6. Resume (validate all worktrees, rebuild sandbox)
7. Fork (create worktrees for all included repos)
8. SessionInfo (expose included repo status in list/info/overlay)
9. Environment variables (`GRAITH_INCLUDE_<NAME>_PATH`)

Each step can be a separate commit, tested independently.

**Backward compatibility:** Sessions created before this feature have no
`Includes` field in state. All lifecycle code must handle `len(s.Includes) == 0`
as a no-op — existing sessions are unaffected.

**Lock contention:** Current `Create` holds `sm.mu` for its entire duration
including git operations. Multi-repo setup multiplies the time under the lock,
blocking list/resume/delete/status during creation. For v1 this is acceptable
(~3s for 3 includes). If it becomes a bottleneck, the lock can be split: hold
`sm.mu` for state reservation/validation, release during git setup, re-acquire
for state persistence.

**Interaction with `--in-place`:** `includes` and `--in-place` are mutually
exclusive. An in-place session runs in the real repo directory; includes
sessions create worktrees. If a repo has `includes` configured and the user
passes `--in-place`, the CLI should error with a clear message suggesting they
drop `--in-place`.

**Interaction with `--share-worktree`:** Sharing an includes session shares the
full session directory. The sharing session sees all included worktrees
read-only.

**Interaction with `--no-repo`:** Mutually exclusive with `includes`.

**Future work — orchestrator-as-service:** Once includes sessions work, the
natural next step is an orchestrator-as-service pattern: the devenv session
runs long-lived, and other agents send verification requests via `gr msg`. The
orchestrator agent checks out requested branches in its included worktrees and
runs tests. This requires no graith changes beyond what this proposal adds —
it's a convention built on `includes` + `gr msg`.

**Future work — path rewriting:** A post-creation hook system that rewrites
config files (`.env.local`, `docker-compose.override.yml`) to replace source
paths with worktree paths. This would solve the absolute path problem for
orchestrators that can't use environment variables.
