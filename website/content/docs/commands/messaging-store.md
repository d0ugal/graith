---
weight: 430
title: "Messaging & store"
description: "Inter-agent messaging and the document store commands."
icon: "forum"
toc: true
draft: false
---

## Messaging

See [Inter-Agent Messaging]({{< relref "/docs/messaging.md" >}}) for full details.

### `gr msg pub <body>`

Publish a message to a stream.

| Flag | Description |
|------|-------------|
| `-t, --topic <name>` | Stream/topic name (required) |
| `-f, --file <path>` | Read body from file |
| `--thread <id>` | Thread ID to continue |
| `--reply-to <stream>` | Stream for replies |

### `gr msg send <session> [body]`

Send a message to a session's inbox. By default, also types a notification into the session's PTY.

| Flag | Description |
|------|-------------|
| `-f, --file <path>` | Read body from file |
| `--thread <id>` | Thread ID to continue |
| `--reply-to <stream>` | Stream for replies |
| `-q, --quiet` | Don't type a notification into the session |
| `--children` | Send to all descendant sessions |
| `--parent` | Send to the parent session |

### `gr msg sub`

Read messages from a stream.

| Flag | Description |
|------|-------------|
| `-t, --topic <name>` | Stream/topic name (required) |
| `-w, --wait` | Block until a message arrives |
| `-F, --follow` | Stream continuously |
| `--ack` | Acknowledge after reading |
| `-a, --all` | Show all messages, not just unread |
| `--thread <id>` | Filter to a specific thread |

### `gr msg ack`

Acknowledge all messages in a stream.

| Flag | Description |
|------|-------------|
| `-t, --topic <name>` | Stream/topic name (required) |

### `gr msg topics`

List streams with total and unread message counts.

| Flag | Description |
|------|-------------|
| `--system` | Include `_system.*` streams |

### `gr msg jail list`

List PR comments quarantined by the author-trust gate (see [Comment author-trust gate]({{< relref "/docs/configuration/automation.md#comment-author-trust-gate" >}})).

| Flag | Description |
|------|-------------|
| `--released` | Include already-released comments |

### `gr msg jail show <id>`

Show a single quarantined comment, including its body.

### `gr msg jail release [id]`

Release a quarantined comment — deliver its content to the target session's inbox. **Restricted to the human or the orchestrator**; a plain agent session is rejected.

| Flag | Description |
|------|-------------|
| `--all` | Release all jailed comments from an author (requires `--author`) |
| `--author <login>` | Author login to release (with `--all`) |

## Document store

See [Document Store]({{< relref "/docs/store.md" >}}) for full details.

### `gr store put <key> [body]`

Store a document. Reads from stdin if no body or `--file` is given.

| Flag | Description |
|------|-------------|
| `-f, --file <path>` | Read body from file |

### `gr store get <key>`

Retrieve a document. Outputs the raw body.

### `gr store list [prefix]` (alias: `ls`)

List documents, optionally filtered by key prefix.

| Flag | Description |
|------|-------------|
| `-a, --all` | List across all repos |

### `gr store append <key> [line]`

Append a line to a document. Creates the document if it does not exist. Reads from stdin if no body or `--file` is given.

| Flag | Description |
|------|-------------|
| `-f, --file <path>` | Read line from file |

### `gr store rm <key>`

Remove a document from the store.

### Store persistent flags

All store subcommands accept:

| Flag | Description |
|------|-------------|
| `--repo <path>` | Repo path (default: auto-detect) |
| `--shared` | Use the global store (not scoped to any repo) |
