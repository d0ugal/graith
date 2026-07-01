---
weight: 10
title: "graith"
description: "A terminal multiplexer for AI coding agent sessions, each in an isolated git worktree."
icon: "menu_book"
toc: true
draft: false
---

A terminal multiplexer for AI coding agent sessions.

graith manages multiple agents (Claude, Codex, OpenCode, Cursor, Agy) running in isolated git worktrees, each in its own PTY session that survives terminal closures. The binary is called `gr`.

**graith** (Scots) -- *noun:* equipment, tools, gear for a specific trade. *verb:* to make ready, prepare, equip.

## How it works

A long-lived daemon (`graithd`) owns PTY sessions and persists state. A stateless CLI client (`gr`) connects over a Unix socket using a framed binary protocol. Sessions survive terminal closures, daemon restarts, and SSH disconnections.

```
┌──────────┐     Unix Socket      ┌──────────┐     PTY      ┌─────────┐
│ gr (CLI) │ <──── frames ──────> │ graithd  │ <──────────> │ claude  │
│  client  │   control + data     │  daemon  │              │ codex   │
└──────────┘                      └──────────┘              │ opencode│
                                       │                    └─────────┘
                                  state.json
                                  (persisted)
```

The wire protocol uses 5-byte framed multiplexing: `[channel:1][length:4][payload:N]`. See [Architecture]({{< relref "architecture" >}}) for protocol details.

## Core concepts

**Sessions** are the primary unit of work. Each session has a name, an agent process, and (usually) a git worktree on its own branch. Sessions can be created, attached, detached, stopped, resumed, forked, and deleted.

**Worktrees** provide git-level isolation. Each session gets its own worktree and branch, so agents can work on different tasks in the same repo without conflicts.

**The prefix key** (default `ctrl+b`) intercepts keystrokes while attached. Press the prefix followed by a command key (e.g. `w` for the session picker, `d` to detach).

**Messaging** enables inter-agent communication via a SQLite-backed pub/sub system. Sessions can publish to topics, send direct messages, and subscribe to streams.

**The store** persists documents across sessions. It is a flat-file, git-backed key-value store scoped per-repo (or global with `--shared`).
