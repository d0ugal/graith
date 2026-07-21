---
weight: 600
title: "Session Lifecycle"
description: "How sessions are created, attached, detached, and destroyed."
icon: "account_tree"
toc: true
draft: false
---

## States

A session is always in one lifecycle state:

| State | Meaning |
|-------|---------|
| `running` | Agent process is alive |
| `stopped` | Agent process was stopped; worktree and branch are preserved |
| `errored` | Agent process exited with a non-zero exit code |
| `creating` | Session is being set up (transient) |
| `deleting` | Session is being torn down (transient) |

Running sessions also carry an **agent status** for current activity (`active`,
`ready`, or `error`, plus tool names from hook reports) — separate from lifecycle
state and not persisted. An agent waiting in its own permission TUI stays
`running`; Graith doesn't create or answer an approval status.

## Creation

```bash
gr new fix-auth-bug
```

Steps:

1. **Fetch** -- `git fetch origin` when `fetch_on_create = true`; `--no-fetch` skips (offline or no SSH auth)
2. **Branch** -- `<branch_prefix>/<session-name>-<session-id>` from the base branch (default: repo default, override `--base`)
3. **Worktree** -- git worktree at `<data_dir>/worktrees/<repo-name>/<repo-hash>/<session-id>/`
4. **Environment** -- sets `GRAITH_SESSION_ID`, `GRAITH_SESSION_NAME`, `GRAITH_AGENT_TYPE`, `GRAITH_WORKTREE_PATH`, `GRAITH_REPO_PATH`, `GRAITH_TMPDIR`, `TMPDIR`
5. **Sandbox** -- when enabled, requires a configured backend and wraps the command (`safehouse wrap` or `nono run --profile`); explicitly disabled starts with a warning
6. **Agent** -- starts with optional `non_interactive_args` then `args`; clear the prefix to keep the native approval TUI
7. **Prompt** (`--prompt`/`--prompt-file`) -- typed into stdin after startup
8. **Attach** (unless `--background`) -- enters passthrough mode

For a freshly initialized repository with no commits, Graith uses the unborn
`HEAD` branch as the base and creates the generated session branch in an empty,
isolated orphan worktree. The first commit therefore belongs to the session
branch; Graith does not create a bootstrap commit or change the source checkout.
If `--base` is supplied, it must name the unborn `HEAD` branch. This workflow
requires Git 2.42 or newer.

### Variants

**No repo:** `gr new scratch --no-repo` — a scratch directory, no git repo or worktree.

**In-place:** `gr new quick --in-place` runs the agent directly in the repo, no worktree or branch. `--allow-concurrent` permits multiple in-place sessions on one repo.

**Mirror:** `gr new observer --mirror my-session` mounts another session's worktree read-only, for observation or review. Requires Graith's enforceable sandbox for the read-only guarantee.

### Headless sessions

**Experimental.** `gr new watcher --headless -p "…"` runs the agent in Claude Code's stream-json mode instead of a PTY — **non-interactive**, for fire-and-forget work like review judges and one-shot helpers. graith parses the typed event stream, so `gr logs -f` renders it and cost/token usage comes from the result envelope. v1 is Claude-only, one-shot (one prompt, run to completion, exit), requires a prompt, uses the same optional Graith sandbox setting as PTY sessions, and implies `--background`. It's inert unless `[headless] experimental = true`, and can't be resumed as headless once it exits. See [Configuration → Headless sessions]({{< relref "configuration/sessions.md#headless-sessions" >}}).

With no PTY to stream, `gr attach` **converts a headless session to
interactive**: it stops the headless process and relaunches via
`claude --resume <session-id>`, preserving conversation, worktree, branch, and
env. This restarts the agent, so attach confirms first (in-flight tool calls are
cancelled, not resumed); `-y`/`--yes` skips. To watch read-only *without*
converting, use `gr logs -f`.

**Interrupts and permission errors.** Over Claude Code's stdin control protocol,
graith cleanly `interrupt`s an in-flight turn instead of firing terminal signals.
Native tool-permission requests are denied immediately and mark the driver
degraded — no native TUI or human-response channel. The optional synchronous
`[command_policy]` applies the same shell restrictions as PTY sessions.

## Attachment

```bash
gr attach fix-auth-bug
```

Attaching connects your terminal's stdin/stdout to the session's PTY through the daemon. Only one client attaches at a time; a new client kicks the previous.

The attach loop transitions between passthrough mode (raw terminal I/O) and the overlay (session picker), cycling without dropping the daemon connection: detach with `ctrl+b d`, switch sessions with `ctrl+b n/p/l`.

## Detachment

Press `ctrl+b d` to detach. The agent keeps running; the daemon maintains the PTY and buffers output to a scrollback file.

## Stop and resume

```bash
gr stop fix-auth-bug       # kill agent process, keep worktree
gr restart fix-auth-bug    # restart with resume_args in existing worktree
```

Stopping sends SIGTERM (worktree and branch preserved); restarting resumes the previous conversation via `resume_args`.

Agents with `resume_args` default to a 1-hour idle timeout, after which the daemon stops the session automatically — resume it later.

## Fork

```bash
gr fork fix-auth-bug auth-approach-2
```

Forking explores alternative approaches from the same git state:

1. Creates a new worktree from the source session's current branch
2. Creates a new branch
3. Starts a new agent using `fork_args` (if configured) — typically passing the source's session ID so it inherits conversation history. Without `fork_args`, only git/worktree state is forked and the agent starts with its regular `args`
4. The source session is unaffected

### Cross-agent fork

```bash
gr fork fix-auth-bug auth-codex --agent codex
```

Pass `--agent <target>` to fork into a **different agent**. Since the target
can't natively resume the source's conversation, graith renders the source's
history to a neutral context file (the renderer `gr migrate` uses) and seeds the
new agent with it. The original keeps running, so both work in parallel.

Unlike `gr migrate` (worktree in place), a fork branches a new worktree from the
base branch — so the source's changes (**uncommitted edits and any commits on its
branch**) don't carry over; re-apply any you need. Claude and Codex are supported
as fork *sources*; any configured agent can be a *target*. Use `--model` to
override the target's model.

## Migrate to a different agent

```bash
gr migrate fix-auth-bug --agent codex
```

Migration swaps the agent on a session **in place** — most useful during a provider outage (e.g. Claude API down, keep working in Codex). Unlike fork, it creates **no** new worktree or branch; the session keeps its id, name, worktree, and branch.

Migrating:

1. Renders the current conversation to a neutral Markdown context file (fail-fast: missing or empty transcript changes nothing)
2. Stops the current agent
3. Switches the session's agent type (and model, via `--model`)
4. Starts the target agent **in the same worktree**, seeded with the rendered context
5. Runs a health check; if the target fails to start, the **original agent is restored**

The retained worktree carries over all code state (commits and uncommitted edits), no branching. The handoff is a lossy reseed, not a native resume: hidden reasoning/thinking and exact tool-call replay aren't transferred, and the process restarts (attached clients re-attach).

Claude and Codex are supported as migration *sources* (transcript formats graith can read); any configured agent can be a *target*, including system sessions like the orchestrator. Migration is soft-reversible — `gr list` records the agent migrated from, so `gr migrate ... --agent <original>` hands the work back.

## Deletion

```bash
gr delete fix-auth-bug
```

Deletion:

1. Kills the agent process (if running)
2. Removes the git worktree
3. Deletes the branch
4. Removes the session from state

With uncommitted changes or unpushed commits, graith prompts for confirmation; `-f` skips.

## Parent-child relationships

When a session spawns children (e.g. an orchestrator and its workers), graith tracks the link via `parent_id`.

```bash
gr list --tree                    # show hierarchy
gr list --children my-session     # show descendants
gr stop --children                # stop all children (from inside a session)
gr delete --children              # delete all children
gr msg send --children "rebase"   # message all descendants
gr msg send --parent "done"       # message the parent
```

Without a positional argument inside a graith session, `--children` auto-detects the current session from `GRAITH_SESSION_ID` and excludes it.

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

Session state lives in `state.json` in the data directory, loaded on daemon start and saved on every mutation, so sessions survive restarts. Runtime-only state (hook reports, attached clients) isn't persisted — it's rebuilt on restart.

## Scrollback

Each session's PTY output is appended to `<data_dir>/logs/<session-id>.log`, for tail reads by `gr logs` and preview rendering in the overlay.

`gr doctor` warns on oversized scrollback files; `gr doctor --autofix` truncates them.

## Starring

```bash
gr update important-session --starred
gr update important-session --starred=false
```

Starred sessions:
- Can't be deleted — set `--starred=false` first
- Are skipped by batch `stop`/`delete` operations (e.g. `--stale`, `--stopped`)
- Can still be stopped directly (`gr stop <session>`)
- Appear in the Starred view in the session picker
- Show a star indicator in the session list

`--starred` combines with `--name` and `--parent`; the daemon validates and persists them as one update. Repeating the same true/false value is safe.

## Status summaries

Agents or users can set a status summary shown in the session picker:

```bash
gr status "Exploring code"
gr status --ttl 30m "Waiting for CI"
gr status --clear
```

The status auto-expires when the agent produces output without updating it (default TTL: 5 minutes); while idle it fades but persists visually.

With no explicit status, the picker auto-derives activity summaries from hook reports (e.g. "Using Bash", "Using Edit").
