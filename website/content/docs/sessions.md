---
weight: 600
title: "Session Lifecycle"
description: "How sessions are created, attached, detached, and destroyed."
icon: "account_tree"
toc: true
draft: false
---

## States

A session is always in one of these lifecycle states:

| State | Meaning |
|-------|---------|
| `running` | Agent process is alive |
| `stopped` | Agent process was stopped; worktree and branch are preserved |
| `errored` | Agent process exited with a non-zero exit code |
| `creating` | Session is being set up (transient) |
| `deleting` | Session is being torn down (transient) |

Running sessions also have an **agent status** that reflects the agent's current activity (e.g. `approval` when waiting for an approval decision, tool names from hook reports). This is separate from the session lifecycle state and is not persisted.

## Creation

```bash
gr new fix-auth-bug
```

Steps:

1. **Fetch** -- runs `git fetch origin` (when `fetch_on_create = true`)
2. **Branch** -- creates `<branch_prefix>/<session-name>-<session-id>` from the base branch (default: repo's default branch, override with `--base`)
3. **Worktree** -- creates a git worktree at `<data_dir>/worktrees/<repo-name>/<repo-hash>/<session-id>/`
4. **Environment** -- sets `GRAITH_SESSION_ID`, `GRAITH_SESSION_NAME`, `GRAITH_AGENT_TYPE`, `GRAITH_WORKTREE_PATH`, `GRAITH_REPO_PATH`, `GRAITH_TMPDIR`, `TMPDIR`
5. **Sandbox** (if enabled) -- wraps the command with the configured backend (`safehouse wrap` or `nono run --profile`); fails closed if no `backend` is set or it can't enforce
6. **Agent** -- starts the agent process with configured `command` and `args`
7. **Prompt** (if `--prompt` or `--prompt-file`) -- types the prompt into the agent's stdin after startup
8. **Attach** (unless `--background`) -- enters passthrough mode

### Variants

**No repo:** `gr new scratch --no-repo` creates a session in a scratch directory without a git repo or worktree.

**In-place:** `gr new quick --in-place` runs the agent directly in the repo without creating a worktree. No branch is created. Use `--allow-concurrent` to permit multiple in-place sessions on the same repo.

**Mirror:** `gr new observer --mirror my-session` creates a session that mounts another session's worktree read-only. Useful for observation or review. Requires sandbox to be enabled (`sandbox.enabled = true` in config).

### Headless sessions

**Experimental.** `gr new watcher --headless -p "…"` runs the agent in Claude Code's stream-json mode instead of an interactive PTY. Headless sessions are **non-interactive**: they are meant for fire-and-forget work no human will attach to (tribunal judges, trigger briefings). graith parses the typed event stream (so `gr logs -f` renders it and the run's cost/token usage is captured from the result envelope). v1 is Claude-only, one-shot (one prompt, run to completion, exit), requires a prompt, is **incompatible with the sandbox**, and implies `--background`; the whole path is inert unless `[headless] experimental = true` is set. A headless session is one-shot, so once it exits it cannot be resumed — create a new one. See [Configuration → Headless sessions](configuration.md#headless-sessions).

Because there is no PTY to stream, `gr attach` on a headless session is not
supported yet — use `gr logs -f` to watch it read-only. Convert-to-interactive
on attach (relaunching in a PTY via `claude --resume <session-id>`, preserving
history) is a planned follow-up (issue #1075).

## Attachment

```bash
gr attach fix-auth-bug
```

Attaching connects your terminal's stdin/stdout to the session's PTY through the daemon. Multiple clients cannot attach to the same session simultaneously; the previous client is kicked when a new one attaches.

The attach loop handles transitions between passthrough mode (raw terminal I/O) and the overlay (session picker). Detaching (`ctrl+b d`) or switching sessions (`ctrl+b n/p/l`) cycles through these states without dropping the daemon connection.

## Detachment

Press `ctrl+b d` to detach. The agent process continues running. The daemon maintains the PTY and buffers output to a scrollback file.

## Stop and resume

```bash
gr stop fix-auth-bug       # kill agent process, keep worktree
gr restart fix-auth-bug    # restart with resume_args in existing worktree
```

Stopping sends SIGTERM to the agent process. The worktree and branch are preserved. Restarting uses the agent's `resume_args` to continue the previous conversation.

Agents with `resume_args` configured default to a 1-hour idle timeout. After the timeout, the daemon stops the session automatically. It can be resumed later.

## Fork

```bash
gr fork fix-auth-bug auth-approach-2
```

Forking:

1. Creates a new worktree from the source session's current branch
2. Creates a new branch
3. Starts a new agent process using `fork_args` (if configured for the agent), which typically passes the source agent's session ID so the new agent can inherit the conversation history. If the agent has no `fork_args`, graith forks only the git/worktree state and starts the agent with its regular `args`
4. The source session is unaffected

Use forking to explore alternative approaches from the same git state.

## Migrate to a different agent

```bash
gr migrate fix-auth-bug --agent codex
```

Migration swaps the agent on an existing session **in place** — most useful during a provider outage (e.g. the Claude API is down and you want to keep working in Codex). Unlike fork, it does **not** create a new worktree or branch: the session keeps its id, name, worktree, and branch.

Migrating:

1. Renders the current agent's conversation to a neutral Markdown context file (fail-fast: if the transcript is missing or empty, nothing is changed)
2. Stops the current agent
3. Switches the session's agent type (and model, via `--model`)
4. Starts the target agent **in the same worktree**, seeded with the rendered context so it can continue the work
5. Runs a short health check; if the target agent fails to start, the **original agent is restored**

Because the worktree is retained, all code state — commits and uncommitted edits — carries over with no branching. The handoff is a lossy reseed, not a native resume: hidden reasoning/thinking and exact tool-call replay are not transferred, and the agent process restarts (attached clients re-attach to the new agent).

Claude and Codex are supported as migration *sources* (the transcript formats graith can read); any configured agent can be a *target*. System sessions such as the orchestrator can be migrated too. The migration is soft-reversible — `gr list` records the agent it was migrated from, so you can `gr migrate ... --agent <original>` to hand the work back.

## Deletion

```bash
gr delete fix-auth-bug
```

Deletion:

1. Kills the agent process (if running)
2. Removes the git worktree
3. Deletes the branch
4. Removes the session from state

If the worktree has uncommitted changes or unpushed commits, graith prompts for confirmation. Use `-f` to skip.

## Parent-child relationships

When a session creates child sessions (e.g. an orchestrator spawning workers), graith tracks the parent-child relationship via `parent_id`.

```bash
gr list --tree                    # show hierarchy
gr list --children my-session     # show descendants
gr stop --children                # stop all children (from inside a session)
gr delete --children              # delete all children
gr msg send --children "rebase"   # message all descendants
gr msg send --parent "done"       # message the parent
```

When `--children` is used without a positional argument inside a graith session, the current session is auto-detected from `GRAITH_SESSION_ID` and excluded from the operation.

## Environment variables

The daemon sets these in every agent process:

| Variable | Value |
|----------|-------|
| `GRAITH_SESSION_ID` | Unique session ID |
| `GRAITH_SESSION_NAME` | Human-readable session name |
| `GRAITH_AGENT_TYPE` | Agent type (e.g. `claude`, `codex`) |
| `GRAITH_WORKTREE_PATH` | Absolute path to the worktree |
| `GRAITH_REPO_PATH` | Absolute path to the source repository (canonical) |
| `GRAITH_TMPDIR` | Per-repo temporary directory (persists across sessions) |
| `TMPDIR` | Set to `GRAITH_TMPDIR` |

When includes are configured:

| Variable | Value |
|----------|-------|
| `GRAITH_INCLUDE_<BASENAME>_PATH` | Absolute path to each included repo's worktree |

## State persistence

Session state is stored in `state.json` in the data directory. The file is loaded on daemon start and saved on every mutation. Sessions survive daemon restarts.

Runtime-only state (hook reports, attached clients, pending approvals) is not persisted and is rebuilt on restart.

## Scrollback

Each session's PTY output is appended to a scrollback file at `<data_dir>/logs/<session-id>.log`. The scrollback supports tail reads for the `gr logs` command and preview rendering in the overlay.

`gr doctor` warns when scrollback files are oversized. `gr doctor --autofix` truncates them.

## Starring

```bash
gr star important-session
gr unstar important-session
```

Starred sessions:
- Cannot be deleted. Unstar first, then delete
- Are skipped by batch `stop`/`delete` operations (e.g. `--stale`, `--stopped`)
- Can still be stopped directly with `gr stop <session>`
- Appear in the Starred view in the session picker
- Show a star indicator in the session list

## Status summaries

Agents (or users) can set a status summary visible in the session picker:

```bash
gr status "Exploring code"
gr status --ttl 30m "Waiting for CI"
gr status --clear
```

The status auto-expires when the agent produces output without updating it (default TTL: 5 minutes). When the agent is idle, the status fades but persists visually.

The session picker also auto-derives activity summaries from hook reports (e.g. "Using Bash", "Using Edit") when no explicit status is set.
