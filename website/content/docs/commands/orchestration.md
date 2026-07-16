---
weight: 440
title: "Scenarios, triggers & todos"
description: "Multi-session scenario, daemon trigger, and todo-list commands."
icon: "playlist_add_check"
toc: true
draft: false
---

## Scenarios

See [Scenarios]({{< relref "/docs/scenarios.md" >}}) for full details.

### `gr scenario start <file>`

Start a scenario from a TOML file. Pass `-` to read from stdin. Only the orchestrator session can start scenarios.

```bash
gr scenario start tracing.toml
cat tracing.toml | gr scenario start -
```

### `gr scenario status <name>`

Show the status of each session in a scenario.

### `gr scenario list`

List all scenarios with their aggregate status.

### `gr scenario stop <name>`

Stop all running sessions in a scenario.

### `gr scenario delete <name>`

Delete a scenario and all its sessions, including worktrees and branches.

## Triggers

Daemon-fired automation on a schedule or on file changes. Triggers are defined in
`config.toml`; these commands inspect and control them. See
[Triggers]({{< relref "/docs/triggers.md" >}}) for the full model.

### `gr trigger list`

List all configured triggers with their source, action, next fire / watch scope,
and state.

### `gr trigger status <name>`

Show detail for one trigger: next fire, last run/result/error, and (for watch
triggers) live bindings.

### `gr trigger run <name>`

Fire a schedule trigger once, now (respects the overlap policy).

### `gr trigger pause <name>` / `gr trigger resume <name>`

Pause a trigger (persists across restart) or resume a paused one. Requires the
orchestrator or a descendant.

## Todo list

A durable, claimable list of work shared across a session subtree or a scenario.
See [Todo list]({{< relref "/docs/todo.md" >}}) for the full model.

### `gr todo add <title>`

Add an item to your subtree's list (or a scenario's).

| Flag | Description |
|------|-------------|
| `--tag <tag>` | Add a tag (repeatable) |
| `--parent <id>` | Make it a sub-item of another item (one level) |
| `--note <text>` | An optional one-line note |
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
unclaimed item in your scope; `start` is an alias for `claim`.

### `gr todo done <id>`

Mark a claimed item done.

### `gr todo block <id> <note>`

Mark a claimed item blocked, with a note.

### `gr todo reopen <id>`

Return an item to `todo` and clear its owner.

### `gr todo rm <id>`

Remove an item (and any sub-items).

### `gr todo export <scope>`

Dump a scope to a markdown/JSON document in the store for archival.
