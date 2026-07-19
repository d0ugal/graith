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

Running sessions also have an **agent status** reflecting the agent's current
activity (`active`, `ready`, or `error`, plus tool names from hook reports). An
agent waiting in its own permission TUI stays `running` — Graith doesn't create
an approval status or answer it. Agent status is separate from lifecycle state
and isn't persisted.

## Creation

```bash
gr new fix-auth-bug
```

Steps:

1. **Fetch** -- runs `git fetch origin` (when `fetch_on_create = true`; skipped by `--no-fetch`, e.g. when SSH auth is unavailable or offline)
2. **Branch** -- creates `<branch_prefix>/<session-name>-<session-id>` from the base branch (default: repo's default branch, override with `--base`)
3. **Worktree** -- creates a git worktree at `<data_dir>/worktrees/<repo-name>/<repo-hash>/<session-id>/`
4. **Environment** -- sets `GRAITH_SESSION_ID`, `GRAITH_SESSION_NAME`, `GRAITH_AGENT_TYPE`, `GRAITH_WORKTREE_PATH`, `GRAITH_REPO_PATH`, `GRAITH_TMPDIR`, `TMPDIR`
5. **Sandbox** -- when enabled, requires an available configured backend and wraps the command (`safehouse wrap` or `nono run --profile`); when explicitly disabled, starts with a warning
6. **Agent** -- starts the agent process with optional `non_interactive_args` followed by `args`; clear the prefix to keep the agent's native approval TUI
7. **Prompt** (if `--prompt` or `--prompt-file`) -- types the prompt into the agent's stdin after startup
8. **Attach** (unless `--background`) -- enters passthrough mode

### Variants

**No repo:** `gr new scratch --no-repo` creates a session in a scratch directory without a git repo or worktree.

**In-place:** `gr new quick --in-place` runs the agent directly in the repo without creating a worktree. No branch is created. Use `--allow-concurrent` to permit multiple in-place sessions on the same repo.

**Mirror:** `gr new observer --mirror my-session` creates a session that mounts another session's worktree read-only — useful for observation or review. Mirror sessions require Graith's enforceable sandbox, which provides the read-only guarantee.

### Headless sessions

**Experimental.** `gr new watcher --headless -p "…"` runs the agent in Claude Code's stream-json mode instead of an interactive PTY. Headless sessions are **non-interactive** — meant for fire-and-forget work such as review judges and one-shot helpers. graith parses the typed event stream, so `gr logs -f` renders it and the run's cost/token usage is captured from the result envelope. v1 is Claude-only, one-shot (one prompt, run to completion, exit), requires a prompt, uses the same optional Graith sandbox setting as PTY sessions, and implies `--background`; the whole path is inert unless `[headless] experimental = true` is set. Since it's one-shot, a headless session can't be resumed as headless once it exits. See [Configuration → Headless sessions]({{< relref "configuration/sessions.md#headless-sessions" >}}).

With no PTY to stream, `gr attach` on a headless session **converts it to
interactive**: it stops the headless process and relaunches the session in a
real PTY via `claude --resume <session-id>`, preserving the conversation,
worktree, branch, and env. Since this restarts the agent, attach prompts you to
confirm first (any in-flight tool call is cancelled, not resumed); pass
`-y`/`--yes` to skip the prompt. To watch a headless session read-only *without*
converting it, use `gr logs -f` instead.

**Interrupts and permission errors.** A headless session runs over Claude Code's
stdin control protocol, so graith can cleanly `interrupt` an in-flight turn
rather than firing terminal signals. Native tool-permission requests are denied
immediately and mark the driver degraded — there's no native TUI or human-response
channel. The optional synchronous `[command_policy]` applies the same additional
shell restrictions it does to PTY sessions.

## Attachment

```bash
gr attach fix-auth-bug
```

Attaching connects your terminal's stdin/stdout to the session's PTY through the daemon. Only one client can attach to a session at a time; the previous client is kicked when a new one attaches.

The attach loop handles transitions between passthrough mode (raw terminal I/O) and the overlay (session picker). Detaching (`ctrl+b d`) or switching sessions (`ctrl+b n/p/l`) cycles through these states without dropping the daemon connection.

## Detachment

Press `ctrl+b d` to detach. The agent keeps running; the daemon maintains the PTY and buffers output to a scrollback file.

## Stop and resume

```bash
gr stop fix-auth-bug       # kill agent process, keep worktree
gr restart fix-auth-bug    # restart with resume_args in existing worktree
```

Stopping sends SIGTERM to the agent process; the worktree and branch are preserved. Restarting uses the agent's `resume_args` to continue the previous conversation.

Agents with `resume_args` configured default to a 1-hour idle timeout. After it, the daemon stops the session automatically — you can resume it later.

## Fork

```bash
gr fork fix-auth-bug auth-approach-2
```

Forking:

1. Creates a new worktree from the source session's current branch
2. Creates a new branch
3. Starts a new agent process using `fork_args` (if configured for the agent), which typically passes the source agent's session ID so the new agent inherits the conversation history. Without `fork_args`, graith forks only the git/worktree state and starts the agent with its regular `args`
4. The source session is unaffected

Fork to explore alternative approaches from the same git state.

### Cross-agent fork

```bash
gr fork fix-auth-bug auth-codex --agent codex
```

Pass `--agent <target>` to fork into a **different agent**. Since the target
can't natively resume the source's conversation, graith renders the source's
history to a neutral context file (the same reader/renderer `gr migrate` uses)
and seeds the new agent with it. The original session keeps running, so both
agents work in parallel.

Unlike `gr migrate` (which keeps the worktree in place), a fork branches a new
worktree from the base branch — so the source's changes (**uncommitted edits and
any commits on its branch**) don't carry over. Re-apply any code changes you
still need in the new session.
Claude and Codex are supported as fork *sources*; any configured agent can be a
*target*. Use `--model` to override the target agent's model.

## Migrate to a different agent

```bash
gr migrate fix-auth-bug --agent codex
```

Migration swaps the agent on an existing session **in place** — most useful during a provider outage (e.g. the Claude API is down and you want to keep working in Codex). Unlike fork, it does **not** create a new worktree or branch; the session keeps its id, name, worktree, and branch.

Migrating:

1. Renders the current agent's conversation to a neutral Markdown context file (fail-fast: if the transcript is missing or empty, nothing is changed)
2. Stops the current agent
3. Switches the session's agent type (and model, via `--model`)
4. Starts the target agent **in the same worktree**, seeded with the rendered context so it can continue the work
5. Runs a short health check; if the target agent fails to start, the **original agent is restored**

Since the worktree is retained, all code state — commits and uncommitted edits — carries over with no branching. The handoff is a lossy reseed, not a native resume: hidden reasoning/thinking and exact tool-call replay aren't transferred, and the agent process restarts (attached clients re-attach to the new agent).

Claude and Codex are supported as migration *sources* (the transcript formats graith can read); any configured agent can be a *target*. System sessions such as the orchestrator can be migrated too. Migration is soft-reversible — `gr list` records the agent it was migrated from, so you can `gr migrate ... --agent <original>` to hand the work back.

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

When a session creates child sessions (e.g. an orchestrator spawning workers), graith tracks the parent-child link via `parent_id`.

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

Runtime-only state (hook reports and attached clients) isn't persisted — it's rebuilt on restart.

## Scrollback

Each session's PTY output is appended to a scrollback file at `<data_dir>/logs/<session-id>.log`. It supports tail reads for `gr logs` and preview rendering in the overlay.

`gr doctor` warns when scrollback files are oversized; `gr doctor --autofix` truncates them.

## Starring

```bash
gr update important-session --starred
gr update important-session --starred=false
```

Starred sessions:
- Can't be deleted. Set `--starred=false` first, then delete
- Are skipped by batch `stop`/`delete` operations (e.g. `--stale`, `--stopped`)
- Can still be stopped directly with `gr stop <session>`
- Appear in the Starred view in the session picker
- Show a star indicator in the session list

`--starred` can be combined with `--name` and `--parent`; the daemon validates
and persists the requested properties as one update. Repeating the same true or
false value is safe.

## Status summaries

Agents (or users) can set a status summary visible in the session picker:

```bash
gr status "Exploring code"
gr status --ttl 30m "Waiting for CI"
gr status --clear
```

The status auto-expires when the agent produces output without updating it (default TTL: 5 minutes). While the agent is idle, the status fades but persists visually.

When no explicit status is set, the session picker auto-derives activity summaries from hook reports (e.g. "Using Bash", "Using Edit").
