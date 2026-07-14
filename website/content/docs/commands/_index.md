---
weight: 400
title: "CLI Reference"
description: "Complete gr command-line reference."
icon: "terminal"
toc: true
draft: false
---

The complete `gr` command-line reference, grouped by area:

- **[Session management]({{< relref "sessions.md" >}})** — create, attach, stop, fork, migrate, and delete sessions.
- **[Monitoring & interaction]({{< relref "monitoring.md" >}})** — list, logs, tokens, doctor, and driving a running session.
- **[Messaging & store]({{< relref "messaging-store.md" >}})** — inter-agent messaging and the document store.
- **[Scenarios & triggers]({{< relref "orchestration.md" >}})** — multi-session scenarios and daemon-fired triggers.
- **[Daemon & other commands]({{< relref "daemon.md" >}})** — daemon lifecycle, config, MCP, completion, and internal commands.

## Global flags

All commands accept:

| Flag | Description |
|------|-------------|
| `--config <path>` | Use a specific config file |
| `--json` | Output in JSON format |
| `--agent-mode` | Force agent mode (auto-enables JSON output) |

Agent mode is auto-detected when running inside a graith session or other AI agent environment (Claude Code, Cursor, Copilot, Amazon Q, OpenCode). Override with `GR_AGENT_MODE=0` to disable or `GR_AGENT_MODE=1` to force.
