---
weight: 440
title: "Scenarios, triggers & todos"
description: "Multi-session scenario, daemon trigger, and todo-list commands."
icon: "playlist_add_check"
toc: true
draft: false
---

## Scenarios

See [Scenarios]({{< relref "/docs/scenarios.md" >}}) for details.

### `gr scenario start <file>`

Start a scenario from a TOML file (`-` reads stdin). Orchestrator-only.

Scenario and member names accept the bounded instance tokens `{caller}`,
`{parent}`, `{initiator}`, `{date}`, `{time}`, `{datetime}`, `{scenario_id}`,
`{short_id}`, and `{scenario}` (only after the name is rendered). The daemon
renders member references from the same immutable context, returning the
rendered names. See [Scenarios → Instance name templates]({{< relref
"/docs/scenarios.md#instance-name-templates" >}}).

```bash
gr scenario start tracing.toml
cat tracing.toml | gr scenario start -
```

### `gr scenario status <name>`

Show each session's lifecycle, todo progress, and declared result status in
width-aware, labeled member blocks. Long identifiers use a middle ellipsis so
templated suffixes remain visible, and each declared result occupies its own
line. When output is redirected, the command uses `lifecycle.default_cols`
(80 by default) instead of assuming a terminal is present. JSON output is
unchanged and adds resolved shared-store destinations and validation errors.

### `gr scenario result put <name> [body]`

Publish the authenticated member's declared text, Markdown, or JSON result.
`--file <path>` or stdin supplies file content; `--scenario <name>`
disambiguates a member shared across scenarios.

### `gr scenario list`

List all scenarios with aggregate status, plus quorum/required progress under a
runtime policy.

### `gr scenario stop <name>`

Stop all running sessions. For policy scenarios this suspends automatic actions
without moving immutable deadlines. `gr scenario resume <name>` resumes members,
unsuspends actions, and reconciles elapsed deadlines immediately.

### `gr scenario add <name>`

Add a member from the orchestrator. Beyond `--name`, `--repo`, `--agent`,
`--model`, `--role`, `--prompt`, `--task`, and `--base`, policy members accept
the flags below. `--prompt` supplies startup instructions without tracked work;
`--task` seeds an assigned todo and doubles as the startup prompt when `--prompt`
is omitted. `<name>`, `--name`, and `--depends-on` take already-rendered instance
and member names — `scenario add` doesn't evaluate instance-name templates.

| Flag | Description |
|------|-------------|
| `--optional` | Do not require this member for completion |
| `--timeout <duration>` | Immutable attempt timeout (minimum `1s`) |
| `--retries <n>` | Additional timeout retries (`0`–`10`; requires timeout) |

### `gr scenario delete <name>`

Delete a scenario and all its sessions, including worktrees and branches.

## Triggers

Daemon-fired automation on a schedule or on file changes, defined in
`config.toml`. See [Triggers]({{< relref "/docs/triggers.md" >}}) for the full
model.

### `gr trigger list`

List triggers with their source, action, next fire / watch scope, and state.

### `gr trigger status <name>`

Detail for one trigger: next fire, last run/result/error, and (for watch
triggers) live bindings.

### `gr trigger run <name>`

Fire a schedule trigger once, now (respects the overlap policy).

### `gr trigger pause <name>` / `gr trigger resume <name>`

Pause a trigger (persists across restart) or resume a paused one. Orchestrator
or descendant only.

## Todo list

A durable, claimable list shared across a session subtree or scenario. See
[Todo list]({{< relref "/docs/todo.md" >}}) for the full model.

### `gr todo add <title>`

Add an item to your subtree's list (or a scenario's).

| Flag | Description |
|------|-------------|
| `--tag <tag>` | Add a tag (repeatable) |
| `--parent <id>` | Make it a sub-item of another item (one level) |
| `--note <text>` | An optional one-line note |
| `--depends-on <id>` | Require a todo to be done first (repeatable) |
| `--scenario <name>` | Add to a scenario's shared list |
| `--session <id>` | Anchor to a specific session subtree instead of the auto-anchor |

### `gr todo list`

List items, grouped by status.

| Flag | Description |
|------|-------------|
| `--status <s>` | Filter by status (`todo`/`in-progress`/`done`/`blocked`) |
| `--tag <tag>` | Filter by tag |
| `--scenario <name>` | List a scenario's shared list |
| `-a, --all` | Fleet-wide, across every scope (human/orchestrator) |

### `gr todo claim <id>` / `gr todo next` / `gr todo start <id>`

Atomically claim an item (→ `in-progress`, owned by you). `next` claims the next
eligible unclaimed item in your scope; `start` aliases `claim`. An assigned item
can be claimed only by its assignee or the scope's override authority.

### `gr todo done <id>`

Mark a claimed item done. For an assigned ownerless item, run
`gr todo claim <id>` first — skip it and the command returns the exact claim command.

### `gr todo block <id> <note>`

Mark a claimed item blocked, with a note.

### `gr todo reopen <id>`

Return an item to `todo` and clear its owner.

### `gr todo deps <id> [dependency-id...]`

Replace an item's dependency set (omit IDs to clear it). Dependencies must exist
in the same scope, and the resulting graph must be acyclic.

### `gr todo rm <id>`

Remove an item (and any sub-items). Rejected while another retained item
depends on it.

### `gr todo export <scope>`

Dump a scope to a markdown/JSON store document for archiving.
