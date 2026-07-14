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

The `--headless` flag runs the agent in Claude Code's stream-json mode rather than an interactive terminal — suited to fire-and-forget sessions no human will attach to (tribunal judges, trigger briefings). graith parses the typed event stream, so `gr logs -f` shows rendered output and the run's cost/token usage is captured from the result envelope. It is **experimental** and inert unless `[headless] experimental = true` is set in config; it is Claude-only in v1, requires a prompt (`-p`), runs one-shot (one prompt, run to completion, exit), and is **incompatible with the sandbox** in v1 (a `--headless` request with the sandbox enabled is an error). Asking for `--headless` on an agent that can't do it is an error, not a silent downgrade to PTY. Because a headless session can't be attached, `--headless` implies `--background`. See [Configuration → Headless sessions]({{< relref "/docs/configuration/sessions.md#headless-sessions" >}}) and [Session Lifecycle → Headless sessions]({{< relref "/docs/sessions.md#headless-sessions" >}}).

When a session is created:

1. Fetches origin (if `fetch_on_create` is true)
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
| `--children` | Also stop all descendant sessions |
| `--repo <name>` | Filter by repo name (batch mode) |
| `--stopped` | Match stopped and errored sessions (batch mode) |
| `--stale <duration>` | Match sessions not attached for this duration, e.g. `7d`, `24h` (batch mode) |
| `-f, --force` | Skip confirmation prompt (batch mode) |

When `--children` is used without a positional argument inside a graith session, it auto-resolves the current session from `GRAITH_SESSION_ID` and excludes it from the stop.

## `gr restart <name-or-id>`

Restart a stopped session. The agent process is restarted in the existing worktree using the agent's `resume_args`.

| Flag | Description |
|------|-------------|
| `--background` | Restart without attaching |

## `gr delete <name-or-id>` (alias: `rm`)

Delete a session. Kills the agent process, removes the worktree, and deletes the branch. Prompts for confirmation if there are uncommitted changes or unpushed commits.

| Flag | Description |
|------|-------------|
| `--children` | Also delete all descendant sessions |
| `-f, --force` | Skip confirmation prompt |
| `--repo <name>` | Filter by repo name (batch mode) |
| `--stopped` | Match stopped and errored sessions (batch mode) |
| `--stale <duration>` | Match sessions not attached for this duration (batch mode) |

## `gr fork <source-session> <new-name>`

Fork a session. Creates a new worktree (from the source session's branch), a new branch, and a new agent process. If the agent has `fork_args` configured, the new agent inherits the source agent's conversation history.

| Flag | Description |
|------|-------------|
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
