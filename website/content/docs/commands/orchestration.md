---
weight: 440
title: "Scenarios & triggers"
description: "Multi-session scenario and daemon trigger commands."
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
