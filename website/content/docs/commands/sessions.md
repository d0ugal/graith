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
| `--base <branch>` | Base branch to fork the worktree from, or branch to reference for `--read-only` (default: repo default branch) |
| `-C, --repo <path>` | Path to git repo (default: current directory) |
| `--label <label>` | Add a session label; repeat the flag for multiple labels |
| `--follow-events <events>` | Ask the new session's direct parent to follow selected event classes from this child; comma-separate or repeat, currently `ci` |
| `--no-repo` | Create session without a git repo or worktree |
| `--in-place` | Run agent directly in the repo without creating a worktree |
| `--allow-concurrent` | Allow multiple in-place sessions on the same repo (requires `--in-place`) |
| `--mirror <session>` | Mount another session's worktree read-only (requires sandbox) |
| `--read-only` | Mount the selected repository branch read-only from a writable scratch cwd (requires sandbox) |
| `--background` | Create without attaching |
| `-p, --prompt <text>` | Initial prompt for the agent |
| `--prompt-file <path>` | Read initial prompt from file |
| `-m, --model <name>` | Model for the agent (Codex: passed as `--model`; other agents: expands `{model}` in agent args) |
| `--codex-profile <name>` | Codex only: config profile to layer on top (`codex --profile`) |
| `--codex-reasoning-effort <level>` | Codex only: reasoning effort — `minimal`, `low`, `medium`, `high`, `xhigh` |
| `--codex-service-tier <tier>` | Codex only: service tier — `auto`, `default`, `flex`, `priority` |
| `--codex-web-search` | Codex only: enable live web search (`codex --search`) |
| `--headless` | Run the agent headless (stream-json) instead of an interactive PTY, for fire-and-forget sessions (experimental; Claude only) |
| `--no-fetch` | Skip `git fetch origin` and create the worktree from local repo state |

`--no-fetch` overrides `fetch_on_create` for that session. Use it when SSH auth
is unavailable (e.g. a biometric agent that can't sign non-interactively) or when
you're offline.

Interactive sessions use terminal-owned attach by default. The daemon seeds the
client with a coherent current-screen snapshot and bounded terminal-aware
primary-screen history before live output resumes, and the client owns the outer
alternate screen instead of injecting chrome into the child PTY stream. Sessions
created by older versions with the former `--experimental-attach` opt-in need no
manual migration: the persisted opt-in bit is ignored because upgraded clients
request terminal-owned attach for every interactive session, and the daemon drops
the obsolete state key the next time it saves state. The control protocol is
bumped to 3.0 so older clients and daemons do not silently reconnect through the
removed experimental attach request.

CLI attach requires the daemon to return a terminal-owned seed. If the daemon
cannot provide one, attach fails with an error.

Graith interprets child terminal mode changes from its daemon-side terminal
model and mirrors mouse, focus, bracketed-paste, application cursor-key, and
keypad behavior to the outer terminal for the attach. The daemon-side model is
the only authority for child terminal query replies; attached clients filter
those query sequences plus unsafe
OSC/DCS/APC side-effect protocols before they can reach the host terminal.
Window-title changes stay model-local, clipboard writes are denied, hyperlink
labels remain visible but are not forwarded as host hyperlinks, and
notification/image protocols are ignored or stripped. Wheel events go to child
mouse tracking when requested, use alternate-scroll cursor keys for
alternate-screen children without mouse tracking, or trigger a configured
Graith input gesture such as `mouse_wheel_up = "scroll_mode"` when the
effective [`[input]`]({{< relref "/docs/configuration/interface.md#input" >}})
policy allows it. The Graith status bar is composed inside the owned frame,
host scrollback is not the primary history surface, and `ctrl+b [` uses the
retained formatted attach seed until live output advances the screen before
falling back to raw logs. Refreshes use dirty-row updates when
the client and daemon share a compatible snapshot base, falling back to full
snapshots when needed.
When the status bar or read-only indicator is visible and the terminal has at
least two rows, the terminal-owned client reserves one top or bottom row according
to `[status_bar].position` and resizes the child PTY to the remaining viewport.
The reserved row is Graith chrome, not child terminal content; cursor and mouse
cell coordinates that reach the child are translated accordingly. One-row
terminals suppress chrome so the child keeps the line.
Current limitations still include
pixel-coordinate mouse reporting, OSC clipboard/title and hyperlink features,
and terminal query responses.

When an existing session creates a child session, omitted labels inherit from
the parent by default. Supplying `--label` sets the child's complete label set
instead of merging with the inherited labels.

`--follow-events=ci` creates the child and a durable direct-parent follow rule
in the same daemon state update. It is valid only when the new session has a
direct parent, and it is rejected if that parent is the config-managed system
orchestrator. The rule survives daemon restarts and delivers daemon-authored
forwarded CI notices to the parent without changing the parent's own PR or CI
state. Use `gr events follow` for existing child sessions.

In a repository with no commits, the default base is its unborn `HEAD` branch.
Graith creates an empty orphan worktree on the generated session branch, leaving
the source checkout and its unborn branch unchanged. An explicit `--base` must
name that unborn branch. Creating this kind of session requires Git 2.42 or
newer.

### Branch-backed read-only sessions

`gr new observer --repo ~/Code/app --read-only --base main` creates a
mirror-classified session without another source session. Graith resolves the
repository, fetches according to `fetch_on_create` unless `--no-fetch` is given,
checks out the selected branch revision into a detached worktree, and launches
the agent from a writable scratch directory. `GRAITH_WORKTREE_PATH` points to
the read-only repository view; `gr path` returns the writable scratch cwd.

If `--base` is omitted, Graith uses the repository default branch. On
resume/restart, the detached worktree is refreshed to the latest resolved branch
revision and status JSON reports `read_only_branch: true` plus
`read_only_revision`. The mode requires sandbox enforcement and is mutually
exclusive with `--mirror`, `--in-place`, `--no-repo`, and included repos. File
watch triggers skip these sessions just like `--mirror` sessions.

### Codex options

For `--agent codex`, graith passes typed per-session options to the Codex CLI.
`--model` becomes `codex --model <name>`; reasoning effort and service tier ride
`-c model_reasoning_effort=…` / `-c service_tier=…`; profile and web search map to
`--profile` and `--search`. Each is passed only when set (unset leaves Codex's
default), and all persist so resume or fork replays them. The `--codex-*` flags
are Codex-specific — using one with another agent is an error. Their *values* are
validated by Codex, not graith, so an unrecognised value surfaces as a Codex
startup error. Don't also template `{model}` into the codex `args` — use
`--model` (or `-m`), or `--model` is passed twice. Example:

```bash
gr new review --agent codex \
  --model gpt-5.1-codex \
  --codex-reasoning-effort high \
  --codex-web-search
```

graith parses the typed event stream, so `gr logs -f` shows rendered output and
the result envelope captures cost/token usage. `--headless` is inert unless
`[headless] experimental = true`, requires a prompt (`-p`), and runs one-shot
(one prompt to completion, then exit). Headless sessions use the same optional
sandbox setting as PTY sessions. Requesting `--headless` on an agent that can't do
it errors rather than silently downgrading to PTY. Starting without a PTY,
`--headless` implies
`--background`; attaching later converts it to interactive. See [Configuration → Headless
sessions]({{< relref "/docs/configuration/sessions.md#headless-sessions" >}}) and
[Session Lifecycle → Headless sessions]({{< relref "/docs/sessions.md#headless-sessions" >}}).

When a session is created:

1. Fetches origin (if `fetch_on_create` is true and `--no-fetch` was not given)
2. Creates branch `<branch_prefix>/<session-name>-<session-id>` from the base branch
3. Creates a worktree at `<data_dir>/worktrees/<repo-name>/<repo-hash>/<session-id>/`
4. Starts the agent process in the worktree
5. Attaches (unless `--background`)

## `gr attach [name-or-id]` (alias: `a`)

Attach to a session. If no name is given, opens the Session Navigator.

For PTY sessions, the daemon-side terminal model owns terminal query replies.
Attach output is filtered so the host terminal does not also answer child
queries or apply unsafe OSC/DCS/APC side effects such as clipboard,
notification, or image protocols.

| Flag | Description |
|------|-------------|
| `-y, --yes` | Skip the convert-to-interactive confirmation when attaching to a headless session |
| `--read-only` | Observe without sending input: stream output but block the keyboard |

Attached output is bounded per client. Raw attach and `gr logs -f` keep exact
bytes, coalesce adjacent queued chunks, and buffer until a slow client's queue
reaches 1 MiB or 16,384 chunks. The daemon disconnects a client whose queue
overflows or whose queued write cannot complete within 2 seconds, while the
session keeps draining output. Terminal-owned attach coalesces live
output into repaint hints and refreshes from screen snapshots.

### Read-only attach

`gr attach --read-only <name>` observes with a persistent `🔒 READ-ONLY`
indicator. The prefix key (default `ctrl+b`) still works — detach, open the
Navigator, switch sessions — only agent input is blocked. Input is gated in the
client and, as a backstop, the daemon. The mode covers the whole attach session,
including sessions selected from the Navigator.

A **headless** session has no PTY, so `gr attach` **converts it to interactive**:
graith stops the headless process and relaunches via `claude --resume
<session-id>`, preserving the conversation, worktree, branch, and env. Since this
restarts the agent (any in-flight tool call is cancelled, not resumed), attach
prompts for confirmation; pass `-y`/`--yes` to skip. To watch it read-only
*without* converting, use `gr logs -f <name>`.

## `gr path [name-or-id]`

Print the authoritative working directory assigned to a session. The output has
no trailing newline, so it can be used directly with a shell:

```bash
cd "$(gr path braw)"
cd "$(gr path --self)"
```

| Flag | Description |
|------|-------------|
| `--self` | Resolve the calling session from `GRAITH_SESSION_ID`, falling back to `GRAITH_SESSION_NAME` |

`--self` takes no positional argument and reports an error outside a Graith
session. Named and ID lookup always use the cwd persisted by the daemon; they do
not depend on the shell's current directory. Worktree and in-place sessions
return their repository directory, repo-less sessions return their managed
scratch directory, and mirror and orchestrator sessions return the writable
scratch directory where their agent launches. A missing, relative, deleted, or
non-directory saved cwd is rejected rather than emitting a path that `cd` would
interpret relative to the caller.

With `--json`, the result contains `session_id`, `name`, and `cwd`.

## `gr stop <name-or-id>`

Stop a running session. The agent process is killed, but the worktree and branch are kept.

| Flag | Description |
|------|-------------|
| `--self` | Stop the current session (resolved from `GRAITH_SESSION_ID`/`GRAITH_SESSION_NAME`) |
| `--children` | Also stop all descendant sessions |
| `--repo <name>` | Filter by repo name (batch mode) |
| `--stopped` | Match stopped and errored sessions (batch mode) |
| `--stale <duration>` | Match sessions not attached for this duration, e.g. `7d`, `24h` (batch mode) |
| `-f, --force` | Skip confirmation prompt (batch mode) |

Without a positional argument inside a graith session, `--children` auto-resolves
the current session from `GRAITH_SESSION_ID` and excludes it from the stop.

`--self` targets the session it's run from (`gr stop --self`). It takes no
positional argument, can't be combined with `--children` or the batch filters,
and errors outside a graith session (no
`GRAITH_SESSION_ID`/`GRAITH_SESSION_NAME`).

## `gr restart <name-or-id>`

Restart a stopped session in the existing worktree using the agent's `resume_args`.
For a branch-backed read-only session, restart also refreshes the detached
worktree to the current selected branch revision before launching.

| Flag | Description |
|------|-------------|
| `--background` | Restart without attaching |

## `gr delete <name-or-id>` (alias: `rm`)

Soft-delete a session. This stops and hides it while retaining its worktree,
branch, and state for the configured retention window. Recover it with
`gr restore`, or remove it immediately with `gr purge`. Branch-backed read-only
sessions have no generated branch; on purge or retention cleanup, Graith removes
their detached worktree and writable scratch directory.

| Flag | Description |
|------|-------------|
| `--self` | Soft-delete the current session (resolved from `GRAITH_SESSION_ID`/`GRAITH_SESSION_NAME`) |
| `--children` | Also delete all descendant sessions |
| `-f, --force` | Deprecated no-op retained for compatibility |
| `--repo <name>` | Filter by repo name (batch mode) |
| `--stopped` | Match stopped and errored sessions (batch mode) |
| `--stale <duration>` | Match sessions not attached for this duration (batch mode) |

`--self` targets the session it's run from (`gr delete --self`), so an agent can
clean itself up without interpolating `$GRAITH_SESSION_NAME`. As with `gr stop`,
it takes no positional argument, can't combine with `--children` or the batch
filters, and errors outside a graith session. `gr purge --self` does the same for
an immediate, irrecoverable purge.

A delete or purge is rejected when the target has children, including children
already in the deleted view. This protects the ownership tree: use
`gr delete <parent> --children` (or `gr purge <parent> --children`) to handle
the complete subtree, delete or purge the children first, or explicitly move
each child with `gr update <child> --parent <parent>` before deleting the old
parent. Batch operations report the same daemon error for unsafe parents and
never reparent children implicitly.

The config-managed orchestrator is an exception to recoverable deletion:
`gr delete orchestrator` discards its current context, then the daemon creates a
fresh replacement when `[orchestrator] enabled = true`. Use `gr stop orchestrator`
to keep it stopped; to purge it permanently, disable it in config first.
When resetting an enabled orchestrator, the reset includes its complete owned
subtree; protected descendants must be handled explicitly first.

## `gr fork <source-session> <new-name>`

Fork a session. Creates a new worktree, branch, and agent process while the original keeps running. If the agent has `fork_args` configured, the new agent inherits the source's conversation history. A fork also inherits a snapshot of every source label; later label changes on either session are independent.

With `--agent <target>` this becomes a **cross-agent fork**: the source's
conversation is rendered to a neutral context file to seed a *different* agent
type. Unlike `gr migrate` (which swaps the agent in place, keeping the worktree),
a fork branches a new worktree from the base branch, so the source's changes —
**uncommitted edits and any commits on its branch — aren't carried over**. Claude
and Codex work as fork *sources*; any configured agent can be a *target*.

| Flag | Description |
|------|-------------|
| `--agent <name>` | Fork into a different agent, seeding it with the source's conversation history (cross-agent fork) |
| `--model <model>` | Model for the target agent (cross-agent fork only; default: the target's default) |
| `--background` | Fork without attaching |

## `gr migrate <name-or-id>`

Migrate a session to a different agent **in place** — e.g. switch from Claude to
Codex during a provider outage. The conversation is rendered to a neutral context
file, the agent is stopped, and the target starts **in the same worktree** seeded
with that history. The session keeps its id, name, worktree, and branch, so all
code state (commits and uncommitted edits) carries over with no branching.

This is a lossy reseed, not a native resume: reasoning/thinking and exact
tool-call replay aren't carried over, and the process is restarted (attached
clients re-attach to the new agent). If the target fails to start, the original
is restored. Claude and Codex work as migration *sources*; any configured agent
can be a *target*.

| Flag | Description |
|------|-------------|
| `--agent <name>` | Target agent to migrate to (required) |
| `--model <model>` | Model for the target agent (default: the target's default) |
| `--background` | Migrate without attaching |

## `gr update <name-or-id>`

Update one or more mutable session properties atomically. At least one flag is
required; omitted properties are left unchanged, and re-setting the current value
succeeds. A rename leaves everything else — session ID, worktree, branch,
ownership, scenario membership, parent relationship — untouched. Soft-deleted
sessions must be restored first.

| Flag | Description |
|------|-------------|
| `--name <new-name>` | Set the session name |
| `--parent <name-or-id>` | Set the parent session; pass an empty string to explicitly orphan |
| `--orphan` | Clear the parent only when the command is run from the session's direct parent |
| `--starred[=true\|false]` | Set deletion protection and Starred-view membership; a bare flag means true |
| `--add-label <label>` | Add one label; repeat for multiple additions |
| `--remove-label <label>` | Remove one label; repeat for multiple removals |

The target and a non-empty parent may be a unique session name or an ID; an
ambiguous name is rejected. `--orphan` is mutually exclusive with `--parent` and
is intended to be run inside a parent session when handing a direct child back
to the user without accidentally clearing another session's parent. Flags can
be combined in one persisted update:

```bash
gr update important-session --name release-watch --parent orchestrator --starred
gr update child-session --orphan  # from child-session's direct parent
gr update release-watch --starred=false
gr update release-watch --add-label urgent --add-label release
gr update release-watch --remove-label urgent
```

Plain output reports each requested property's resulting value; `--json` and
agent mode return one object with `session_id`, `name`, `parent_id`, and the
explicit `starred` boolean plus the complete resulting `labels` array. Label
additions and removals are applied in the same persisted metadata update as
name, parent, and starred changes, so concurrent updates do not replace the
whole label set or lose unrelated fields. Adding an existing label or removing
an absent label succeeds without changing the set; one request cannot add and
remove the same case-insensitive label.
