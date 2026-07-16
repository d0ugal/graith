---
weight: 1650
title: "Capability Matrix"
description: "Which capabilities each graith frontend (CLI, iOS, macOS) supports."
icon: "checklist"
toc: true
draft: false
---

graith has three frontends — the `gr` **CLI**, the **iOS** app, and the
**macOS** app — and all three are clients of one daemon over the same wire
protocol. This page tracks which capabilities each surface supports, so a gap
between them is an explicit, reviewable fact rather than something discovered by
hand.

## Source of truth

The matrix below is **generated** from a machine-readable manifest,
[`internal/capabilities/capabilities.json`](https://github.com/d0ugal/graith/blob/main/internal/capabilities/capabilities.json).
The manifest is the source of truth; this page is rendered from it and a Go
test (`TestDocMatchesManifest`) fails if the two ever disagree. To change the
matrix, edit the manifest and regenerate:

```bash
go test ./internal/capabilities -run TestDocMatchesManifest -update
```

A `planned` cell is an intentional statement of intent — a gap we mean to
close — while `n/a` marks a capability that deliberately does not apply to a
surface (for example, CLI scripting primitives that the GUIs express as live
state instead).

## Matrix

<!-- BEGIN GENERATED CAPABILITY MATRIX -->

### Legend

| State | Meaning |
|-------|---------|
| ✅ `supported` | Wired end-to-end and usable on this surface. |
| 🚧 `planned` | Not yet wired on this surface; a gap we intend to close. |
| — `n/a` | Deliberately not applicable to this surface. |

### Session lifecycle

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| List sessions | ✅ | ✅ | ✅ |
| Filter, search & view-mode sessions | ✅ | ✅ | ✅ |
| Create session | ✅ | ✅ | ✅ |
| Stop session | ✅ | ✅ | ✅ |
| Resume session | ✅ | ✅ | ✅ |
| Restart session | ✅ | ✅ | ✅ |
| Delete session (soft) | ✅ | ✅ | ✅ |
| Restore a soft-deleted session | ✅ | ✅ | ✅ |
| Purge (hard delete) | ✅ | ✅ | ✅ |
| Rename session | ✅ | ✅ | ✅ |
| Star / unstar session | ✅ | ✅ | ✅ |
| Fork session | ✅ | ✅ | ✅ |
| Migrate session to another agent | ✅ | ✅ | ✅ |
| Set session status summary | ✅ | ✅ | ✅ |
| Block until a session matches a condition <sup>1</sup> | ✅ | — | — |
| List available repositories for new sessions | ✅ | ✅ | ✅ |

<sup>1</sup> Block until a session matches a condition: A scripting/automation gate; the GUIs surface live state instead.

### Terminal I/O

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Attach to a session PTY | ✅ | ✅ | ✅ |
| Send input / keystrokes | ✅ | ✅ | ✅ |
| Resize the terminal | ✅ | ✅ | ✅ |
| Detach from a session | ✅ | ✅ | ✅ |
| View logs / scrollback | ✅ | ✅ | ✅ |
| Render a screen snapshot / preview | ✅ | ✅ | ✅ |
| Type into another session remotely <sup>1</sup> | ✅ | — | — |

<sup>1</sup> Type into another session remotely: An attached GUI types directly; the standalone remote-type command is a CLI convenience.

### Approvals & pairing

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| View pending tool approvals | ✅ | ✅ | ✅ |
| Respond to a tool approval | ✅ | ✅ | ✅ |
| Request device pairing | ✅ | ✅ | ✅ |
| List / approve / revoke paired devices <sup>1</sup> | ✅ | 🚧 | 🚧 |

<sup>1</sup> List / approve / revoke paired devices: Listing/approving/revoking devices is gated to the local human on the daemon (remote-denied), so it stays CLI-only for now; the GUIs pair *with* a daemon (pairing.request) but don't manage its device list.

### Messaging

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Send / publish inter-agent messages <sup>1</sup> | ✅ | ✅ | ✅ |
| Read inbox / subscribe to topics <sup>2</sup> | ✅ | ✅ | ✅ |
| Inspect / release jailed PR comments | ✅ | 🚧 | 🚧 |

<sup>1</sup> Send / publish inter-agent messages: GUIs compose a direct message to a session's inbox (msg send) from the session context menu; topic publish (msg pub) stays CLI-only for now.
<sup>2</sup> Read inbox / subscribe to topics: GUIs show a session's direct-message conversation (inbox view) with mark-as-read (ack); topic subscribe/follow stays CLI-only for now.

### Scenarios

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Start / stop / resume / inspect scenarios <sup>1</sup> | ✅ | ✅ | ✅ |

<sup>1</sup> Start / stop / resume / inspect scenarios: The GUIs list scenarios, show per-session role/task and todo-derived progress (per-member completion is derived from assigned todo items), group scenario members in the sidebar, and run the human-authorized stop/resume/delete actions; `start`/`add` stay CLI-only (they are orchestrator-session-scoped, not human-client operations).

### Todo list

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Todo list <sup>1</sup> | ✅ | 🚧 | 🚧 |

<sup>1</sup> Todo list: A durable, shared todo list with atomic claim of items across sessions; the `gr todo` CLI ships now, GUI surfacing is a planned follow-up (issue #591).

### Triggers

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| List / status / run / pause / resume triggers | ✅ | 🚧 | 🚧 |

### MCP servers

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| List / restart / inspect MCP servers | ✅ | 🚧 | 🚧 |

### Document store

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Put / get / list / append / remove documents | ✅ | 🚧 | 🚧 |
| Browse and read documents (list keys, view a document body) | ✅ | ✅ | ✅ |

### Notifications

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Send a desktop / push notification to the human | ✅ | 🚧 | 🚧 |

### Sandbox introspection

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Explain / watch sandbox policy and denials | ✅ | 🚧 | 🚧 |

### Diagnostics

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| View effective config + diff-vs-defaults | ✅ | ✅ | ✅ |
| Health check / doctor diagnostics (orphan GC stays CLI-only) | ✅ | ✅ | ✅ |

<!-- END GENERATED CAPABILITY MATRIX -->
