---
weight: 410
title: "Session management"
description: "Create, attach, stop, fork, migrate, and delete sessions."
icon: "account_tree"
toc: true
draft: false
---

## `gr new <name>` (alias: `n`)

Create a new agent session.

| Flag | Description |
|------|-------------|
| `--agent <name>` | Agent to run (default: `default_agent` from config) |
| `--base <branch>` | Base branch to fork the worktree from (default: repo default branch) |
| `-C, --repo <path>` | Path to git repo (default: current directory) |
| `--no-repo` | Create session without a git repo or worktree |
| `--in-place` | Run agent directly in the repo without creating a worktree |
| `--allow-concurrent` | Allow multiple in-place sessions on the same repo (requires `--in-place`) |
| `--mirror <session>` | Mount another session's worktree read-only (requires sandbox) |
| `--background` | Create without attaching |
| `-p, --prompt <text>` | Initial prompt for the agent |
| `--prompt-file <path>` | Read initial prompt from file |
| `-m, --model <name>` | Model for the agent (expands `{model}` in agent args) |
| `--headless` | Run the agent headless (stream-json) instead of an interactive PTY, for fire-and-forget sessions (experimental; Claude only) |
| `--no-fetch` | Skip `git fetch origin` and create the worktree from local repo state |

The `--no-fetch` flag skips the `git fetch origin` step that normally runs before the worktree is created, overriding `fetch_on_create` for that one session. Use it when SSH auth is unavailable (e.g. a biometric/Secretive agent that can't sign non-interactively) or when you're offline — the worktree is then created from whatever the local repo already has.

The `--headless` flag runs the agent in Claude Code's stream-json mode rather than an interactive terminal — suited to fire-and-forget sessions no human will attach to (tribunal judges, trigger briefings). graith parses the typed event stream, so `gr logs -f` shows rendered output and the run's cost/token usage is captured from the result envelope. It is **experimental** and inert unless `[headless] experimental = true` is set in config; it is Claude-only in v1, requires a prompt (`-p`), runs one-shot (one prompt, run to completion, exit), and is **incompatible with the sandbox** in v1 (a `--headless` request with the sandbox enabled is an error). Asking for `--headless` on an agent that can't do it is an error, not a silent downgrade to PTY. Because a headless session can't be attached, `--headless` implies `--background`. See [Configuration → Headless sessions]({{< relref "/docs/configuration/sessions.md#headless-sessions" >}}) and [Session Lifecycle → Headless sessions]({{< relref "/docs/sessions.md#headless-sessions" >}}).

When a session is created:

1. Fetches origin (if `fetch_on_create` is true and `--no-fetch` was not given)
2. Creates branch `<branch_prefix>/<session-name>-<session-id>` from the base branch
3. Creates a worktree at `<data_dir>/worktrees/<repo-name>/<repo-hash>/<session-id>/`
4. Starts the agent process in the worktree
5. Attaches (unless `--background`)

## `gr attach [name-or-id]` (alias: `a`)

Attach to a session. If no name is given, opens the session picker overlay.

A **headless** session has no PTY to stream, so `gr attach` on one is not
supported yet — graith directs you to `gr logs -f <name>`, which streams its
rendered output read-only. Convert-to-interactive on attach (relaunching the
agent in a PTY via `claude --resume <session-id>`, preserving history) is a
planned follow-up (issue #1075).

## `gr stop <name-or-id>`

Stop a running session. The agent process is killed but the worktree and branch are preserved for later resumption.

| Flag | Description |
|------|-------------|
| `--self` | Stop the current session (resolved from `GRAITH_SESSION_ID`/`GRAITH_SESSION_NAME`) |
| `--children` | Also stop all descendant sessions |
| `--repo <name>` | Filter by repo name (batch mode) |
| `--stopped` | Match stopped and errored sessions (batch mode) |
| `--stale <duration>` | Match sessions not attached for this duration, e.g. `7d`, `24h` (batch mode) |
| `-f, --force` | Skip confirmation prompt (batch mode) |

When `--children` is used without a positional argument inside a graith session, it auto-resolves the current session from `GRAITH_SESSION_ID` and excludes it from the stop.

`--self` targets the session it is run from — handy for an agent that wants to stop itself without knowing its own name (`gr stop --self`). It takes no positional argument and cannot be combined with `--children` or the batch filters; outside a graith session (no `GRAITH_SESSION_ID`/`GRAITH_SESSION_NAME`) it errors.

## `gr restart <name-or-id>`

Restart a stopped session. The agent process is restarted in the existing worktree using the agent's `resume_args`.

| Flag | Description |
|------|-------------|
| `--background` | Restart without attaching |

## `gr delete <name-or-id>` (alias: `rm`)

Delete a session. Kills the agent process, removes the worktree, and deletes the branch. Prompts for confirmation if there are uncommitted changes or unpushed commits.

| Flag | Description |
|------|-------------|
| `--self` | Soft-delete the current session (resolved from `GRAITH_SESSION_ID`/`GRAITH_SESSION_NAME`) |
| `--children` | Also delete all descendant sessions |
| `-f, --force` | Skip confirmation prompt |
| `--repo <name>` | Filter by repo name (batch mode) |
| `--stopped` | Match stopped and errored sessions (batch mode) |
| `--stale <duration>` | Match sessions not attached for this duration (batch mode) |

`--self` targets the session it is run from, so an agent can clean itself up after its work is merged with `gr delete --self` — no need to interpolate `$GRAITH_SESSION_NAME`. It takes no positional argument and cannot be combined with `--children` or the batch filters; outside a graith session it errors. `gr purge --self` does the same for an immediate, irrecoverable purge.

## `gr fork <source-session> <new-name>`

Fork a session. Creates a new worktree, a new branch, and a new agent process while the original session keeps running. If the agent has `fork_args` configured, the new agent inherits the source agent's conversation history.

With `--agent <target>` this becomes a **cross-agent fork**: the source's conversation is rendered to a neutral context file (reusing the migration reader/renderer) and the new agent — a *different* agent type — is seeded with it. Unlike `gr migrate` (which swaps the agent in place, keeping the worktree), a fork creates a new worktree branched from the base branch, so the source's changes — **uncommitted edits and any commits on its branch — are not carried over**. Claude and Codex are supported as fork *sources*; any configured agent can be a *target*.

| Flag | Description |
|------|-------------|
| `--agent <name>` | Fork into a different agent, seeding it with the source's conversation history (cross-agent fork) |
| `--model <model>` | Model for the target agent (cross-agent fork only; default: the target's default) |
| `--background` | Fork without attaching |

## `gr migrate <name-or-id>`

Migrate a session to a different agent **in place** — for example, switch from Claude to Codex during a provider outage without losing your work. The current agent's conversation is rendered to a neutral context file, the agent is stopped, and the target agent is started **in the same worktree** seeded with that history. The session keeps its id, name, worktree, and branch, so all code state (commits and uncommitted edits) carries over with no branching.

This is a lossy reseed, not a native resume: reasoning/thinking and exact tool-call replay are not carried over, and the agent process is restarted (attached clients re-attach to the new agent). If the target agent fails to start, the original agent is restored. Claude and Codex are supported as migration *sources*; any configured agent can be a *target*.

| Flag | Description |
|------|-------------|
| `--agent <name>` | Target agent to migrate to (required) |
| `--model <model>` | Model for the target agent (default: the target's default) |
| `--background` | Migrate without attaching |

## `gr rename <name-or-id> <new-name>`

Rename a session.

## `gr star <name-or-id>`

Star a session. Starred sessions are protected from accidental deletion and appear in the Starred view.

## `gr unstar <name-or-id>`

Remove the star from a session.
