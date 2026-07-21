---
weight: 1650
title: "Capability Matrix"
description: "Which capabilities each graith frontend (CLI, iOS, macOS) supports."
icon: "checklist"
toc: true
draft: false
---

graith has three frontends — the `gr` **CLI**, **iOS**, and **macOS** apps — all
clients of one daemon over the same wire protocol. This page tracks each
surface's capabilities, making any gap explicit and reviewable.

## Source of truth

The matrix is **generated** from a machine-readable manifest,
[`internal/capabilities/capabilities.json`](https://github.com/d0ugal/graith/blob/main/internal/capabilities/capabilities.json)
— the source of truth. A Go test (`TestDocMatchesManifest`) fails if the two
disagree. To change it, edit the manifest and regenerate:

```bash
go test ./internal/capabilities -run TestDocMatchesManifest -update
```

A `planned` cell is a gap we mean to close; `n/a` marks a surface excluded by
the linked platform decision — e.g. CLI scripting primitives the GUIs express
as live state.

## Matrix

<!-- BEGIN GENERATED CAPABILITY MATRIX -->

### Legend

| State | Meaning |
|-------|---------|
| ✅ `supported` | Wired end-to-end and usable on this surface. |
| 🚧 `planned` | Not yet wired on this surface; a gap we intend to close. |
| — `n/a` | Deliberately excluded from this surface by a linked platform decision. |

### Session lifecycle

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| List sessions | ✅ | ✅ | ✅ |
| Filter and search sessions | ✅ | ✅ | ✅ |
| Create session | ✅ | ✅ | ✅ |
| Stop session | ✅ | ✅ | ✅ |
| Resume session | ✅ | ✅ | ✅ |
| Restart session | ✅ | ✅ | ✅ |
| Delete session (soft) | ✅ | ✅ | ✅ |
| Restore a soft-deleted session | ✅ | ✅ | ✅ |
| Purge (hard delete) | ✅ | ✅ | ✅ |
| Rename session | ✅ | ✅ | ✅ |
| Update starred state | ✅ | ✅ | ✅ |
| Fork session | ✅ | ✅ | ✅ |
| Migrate session to another agent | ✅ | ✅ | ✅ |
| Set session status summary | ✅ | ✅ | ✅ |
| Block until a session matches a condition <sup>1</sup> | ✅ | — | — |
| List available repositories for new sessions | ✅ | ✅ | ✅ |

<sup>1</sup> Block until a session matches a condition: A scripting/automation gate; the GUIs surface live state instead. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-platform-scope-policy.md#platform-support)

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

<sup>1</sup> Type into another session remotely: An attached GUI types directly; the standalone remote-type command is a CLI convenience. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-platform-scope-policy.md#platform-support)

### Pairing

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Request device pairing | ✅ | ✅ | ✅ |
| List / approve / revoke paired devices <sup>1</sup> | ✅ | — | — |

<sup>1</sup> List / approve / revoke paired devices: Device-list administration is local-human-only and remote-denied. Native apps pair with a daemon but deliberately do not manage its trust list. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-native-gui-scope.md#platform-support)

### Messaging

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Send direct messages <sup>1</sup> | ✅ | ✅ | ✅ |
| Read direct-message conversations <sup>2</sup> | ✅ | ✅ | ✅ |
| Publish / subscribe to messaging topics <sup>3</sup> | ✅ | — | — |
| Inspect / release jailed PR comments <sup>4</sup> | ✅ | — | — |

<sup>1</sup> Send direct messages: Native apps compose a direct message to a session's inbox from the session context menu.
<sup>2</sup> Read direct-message conversations: Native apps show a session's direct-message conversation with mark-as-read.
<sup>3</sup> Publish / subscribe to messaging topics: Topics are an agent and orchestrator coordination primitive rather than a native human-chat surface. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-native-gui-scope.md#platform-support)
<sup>4</sup> Inspect / release jailed PR comments: Quarantined PR comments are an untrusted-input moderation workflow kept in the CLI. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-native-gui-scope.md#platform-support)

### Scenarios

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Start / stop / resume / inspect scenarios <sup>1</sup> | ✅ | — | — |
| Completion actions and lifecycle cleanup <sup>2</sup> | ✅ | — | — |
| Declare and publish scenario results <sup>3</sup> | ✅ | — | — |
| Scenario timeout, retry & quorum policy <sup>4</sup> | ✅ | — | — |

<sup>1</sup> Start / stop / resume / inspect scenarios: Scenarios are operated through the CLI and orchestrator. Native apps show scenario-created sessions as ordinary sessions without scenario grouping or lifecycle controls. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-scenarios-cli-only.md#platform-support)
<sup>2</sup> Completion actions and lifecycle cleanup: Scenario TOML and the CLI expose completion actions and cleanup state; native GUI presentation is deliberately excluded. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-scenarios-cli-only.md#platform-support)
<sup>3</sup> Declare and publish scenario results: The CLI declares result contracts and reports durable publication state; native GUI publication and result-detail presentation are deliberately excluded. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-scenarios-cli-only.md#platform-support)
<sup>4</sup> Scenario timeout, retry & quorum policy: Scenario runtime policies are CLI/daemon-only; iOS and macOS do not model or surface policy state. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-scenarios-cli-only.md#platform-support)

### Todo list

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Manage todo items <sup>1</sup> | ✅ | — | — |
| View todo lists and progress <sup>2</sup> | ✅ | 🚧 | 🚧 |

<sup>1</sup> Manage todo items: Add, claim, assign, transition, and remove are agent/orchestrator operations; native apps deliberately do not edit todo state. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-native-gui-scope.md#platform-support)
<sup>2</sup> View todo lists and progress: A read-only native overview is in scope; todo mutation remains CLI/orchestrator-only.

### Triggers

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| List / status / run / pause / resume triggers <sup>1</sup> | ✅ | — | — |

<sup>1</sup> List / status / run / pause / resume triggers: Trigger lifecycle is an automation control plane kept in the CLI and orchestrator. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-native-gui-scope.md#platform-support)

### MCP servers

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| List / restart / inspect MCP servers <sup>1</sup> | ✅ | — | — |

<sup>1</sup> List / restart / inspect MCP servers: MCP server inspection and restart are developer operations kept in the CLI. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-native-gui-scope.md#platform-support)

### Document store

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Put / append / remove documents <sup>1</sup> | ✅ | — | — |
| Browse and read documents (list keys, view a document body) | ✅ | ✅ | ✅ |

<sup>1</sup> Put / append / remove documents: Document mutation is an agent and scripting primitive; native apps retain the separate read-only browser. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-native-gui-scope.md#platform-support)

### Notifications

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Send a desktop / push notification to the human <sup>1</sup> | ✅ | — | — |

<sup>1</sup> Send a desktop / push notification to the human: Agents and scripts send notifications; native apps are notification recipients and presenters. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-native-gui-scope.md#platform-support)

### Sandbox introspection

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| Explain / watch sandbox policy and denials <sup>1</sup> | ✅ | — | — |

<sup>1</sup> Explain / watch sandbox policy and denials: Sandbox explain/watch output is diagnostic and streaming, making the terminal its natural interface. [Platform decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-17-native-gui-scope.md#platform-support)

### Diagnostics

| Capability | CLI | iOS | macOS |
|------------|:---:|:---:|:---:|
| View effective config + diff-vs-defaults | ✅ | ✅ | ✅ |
| Health check / doctor diagnostics (orphan GC stays CLI-only) | ✅ | ✅ | ✅ |

<!-- END GENERATED CAPABILITY MATRIX -->
