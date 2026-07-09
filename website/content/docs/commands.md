---
weight: 400
title: "CLI Reference"
description: "Complete gr command-line reference."
icon: "terminal"
toc: true
draft: false
---

## Global flags

All commands accept:

| Flag | Description |
|------|-------------|
| `--config <path>` | Use a specific config file |
| `--json` | Output in JSON format |
| `--agent-mode` | Force agent mode (auto-enables JSON output) |

Agent mode is auto-detected when running inside a graith session or other AI agent environment (Claude Code, Cursor, Copilot, Amazon Q, OpenCode). Override with `GR_AGENT_MODE=0` to disable or `GR_AGENT_MODE=1` to force.

## Session management

### `gr new <name>` (alias: `n`)

Create a new agent session.

| Flag | Description |
|------|-------------|
| `--agent <name>` | Agent to run (default: `default_agent` from config) |
| `--base <branch>` | Base branch to fork the worktree from (default: repo default branch) |
| `-C, --repo <path>` | Path to git repo (default: current directory) |
| `--no-repo` | Create session without a git repo or worktree |
| `--in-place` | Run agent directly in the repo without creating a worktree |
| `--allow-concurrent` | Allow multiple in-place sessions on the same repo (requires `--in-place`) |
| `--share-worktree <session>` | Share another session's worktree (read-only; requires sandbox) |
| `--background` | Create without attaching |
| `-p, --prompt <text>` | Initial prompt for the agent |
| `--prompt-file <path>` | Read initial prompt from file |
| `-m, --model <name>` | Model for the agent (expands `{model}` in agent args) |

When a session is created:

1. Fetches origin (if `fetch_on_create` is true)
2. Creates branch `<branch_prefix>/<session-name>-<session-id>` from the base branch
3. Creates a worktree at `<data_dir>/worktrees/<repo-name>/<repo-hash>/<session-id>/`
4. Starts the agent process in the worktree
5. Attaches (unless `--background`)

### `gr attach [name-or-id]` (alias: `a`)

Attach to a session. If no name is given, opens the session picker overlay.

### `gr stop <name-or-id>`

Stop a running session. The agent process is killed but the worktree and branch are preserved for later resumption.

| Flag | Description |
|------|-------------|
| `--children` | Also stop all descendant sessions |
| `--repo <name>` | Filter by repo name (batch mode) |
| `--stopped` | Match stopped and errored sessions (batch mode) |
| `--stale <duration>` | Match sessions not attached for this duration, e.g. `7d`, `24h` (batch mode) |
| `-f, --force` | Skip confirmation prompt (batch mode) |

When `--children` is used without a positional argument inside a graith session, it auto-resolves the current session from `GRAITH_SESSION_ID` and excludes it from the stop.

### `gr restart <name-or-id>`

Restart a stopped session. The agent process is restarted in the existing worktree using the agent's `resume_args`.

| Flag | Description |
|------|-------------|
| `--background` | Restart without attaching |

### `gr delete <name-or-id>` (alias: `rm`)

Delete a session. Kills the agent process, removes the worktree, and deletes the branch. Prompts for confirmation if there are uncommitted changes or unpushed commits.

| Flag | Description |
|------|-------------|
| `--children` | Also delete all descendant sessions |
| `-f, --force` | Skip confirmation prompt |
| `--repo <name>` | Filter by repo name (batch mode) |
| `--stopped` | Match stopped and errored sessions (batch mode) |
| `--stale <duration>` | Match sessions not attached for this duration (batch mode) |

### `gr fork <source-session> <new-name>`

Fork a session. Creates a new worktree (from the source session's branch), a new branch, and a new agent process. If the agent has `fork_args` configured, the new agent inherits the source agent's conversation history.

| Flag | Description |
|------|-------------|
| `--background` | Fork without attaching |

### `gr migrate <name-or-id>`

Migrate a session to a different agent **in place** — for example, switch from Claude to Codex during a provider outage without losing your work. The current agent's conversation is rendered to a neutral context file, the agent is stopped, and the target agent is started **in the same worktree** seeded with that history. The session keeps its id, name, worktree, and branch, so all code state (commits and uncommitted edits) carries over with no branching.

This is a lossy reseed, not a native resume: reasoning/thinking and exact tool-call replay are not carried over, and the agent process is restarted (attached clients re-attach to the new agent). If the target agent fails to start, the original agent is restored. Claude and Codex are supported as migration *sources*; any configured agent can be a *target*.

| Flag | Description |
|------|-------------|
| `--agent <name>` | Target agent to migrate to (required) |
| `--model <model>` | Model for the target agent (default: the target's default) |
| `--background` | Migrate without attaching |

### `gr rename <name-or-id> <new-name>`

Rename a session.

### `gr star <name-or-id>`

Star a session. Starred sessions are protected from accidental deletion and appear in the Starred view.

### `gr unstar <name-or-id>`

Remove the star from a session.

## Information and monitoring

### `gr list` (alias: `ls`)

List all sessions with status.

| Flag | Description |
|------|-------------|
| `--repo <path>` | Filter by repo path |
| `--tree` | Show parent-child hierarchy |
| `--children <name-or-id>` | Filter to descendants of a session |
| `--starred` | Show only starred sessions |

### `gr logs <name-or-id>` (alias: `l`)

Show session output without attaching.

| Flag | Description |
|------|-------------|
| `-f, --follow` | Follow output (like `tail -f`) |
| `-n, --lines <num>` | Number of lines to show (default: 300) |

### `gr info`

Show info for the current session. Auto-detects the session by matching the current working directory against session worktree paths.

### `gr dashboard`

Live-updating TUI dashboard of all sessions. Supports inline attach, stop, delete, and resume.

### `gr approvals`

List sessions waiting for approval.

### `gr doctor` (alias: `doc`)

Run health checks and diagnostics. Checks daemon status, safehouse availability, orphaned worktrees, oversized scrollback files, and stale PID files.

By default `gr doctor` avoids walking the data dir to measure on-disk sizes — that walk can take tens of seconds on a large install (worktrees full of `node_modules` and `.git` objects), so it's opt-in. Pass `--disk` to report the size of the data dir, tmp repos, and orphaned worktrees. When it finds leftover artifacts whose size is worth knowing (orphaned worktrees, a legacy directory), the default run recommends re-running with `--disk`. In `--json` output, the `disk_measured` field indicates whether sizes were computed.

| Flag | Description |
|------|-------------|
| `--autofix` | Automatically fix issues |
| `--disk` | Measure on-disk sizes (walks the data dir; can be slow on large installs) |

### `gr sandbox why`

Explain whether the configured sandbox would allow or deny a filesystem or network access, without launching an agent. Builds the nono profile graith would generate from config and queries nono's policy oracle. Requires the `nono` backend.

| Flag | Description |
|------|-------------|
| `--path <p>` | Filesystem path to check (use with `--op`) |
| `--op <read\|write\|readwrite>` | Operation for `--path` |
| `--host <h>` | Network host to check (e.g. `github.com`) |
| `--port <n>` | Network port for `--host` (default 443) |
| `--agent <name>` | Resolve the merged (global + per-agent) policy for this agent |

```bash
gr sandbox why --path ~/.ssh/id_rsa --op read
gr sandbox why --host github.com --port 443
```

## Remote interaction

### `gr type <name-or-id> <text>` (alias: `t`)

Type text into a session's PTY stdin. Appends a newline by default.

| Flag | Description |
|------|-------------|
| `--no-newline` | Do not append a newline after the text |

### `gr status [session] <message>`

Set a status summary for a session, visible in the session picker overlay and `gr list`. When run inside a graith session, the session is auto-detected.

| Flag | Description |
|------|-------------|
| `--clear` | Clear the status summary |
| `--ttl <duration>` | Override TTL for this status update (e.g. `10m`, `1h`) |

## Messaging

See [Inter-Agent Messaging](messaging.md) for full details.

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

## Document store

See [Document Store](store.md) for full details.

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

## Scenarios

See [Scenarios](scenarios.md) for full details.

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

## Daemon management

### `gr daemon start`

Start the daemon. This is normally automatic and rarely needed.

### `gr daemon stop`

Stop the daemon gracefully.

### `gr daemon restart`

Restart the daemon, preserving live sessions via exec.

| Flag | Description |
|------|-------------|
| `--force` | Clean stop/start that kills running sessions |

After rebuilding `gr`, run `gr daemon restart` to pick up the new daemon binary.

### `gr daemon reload`

Reload configuration without restarting the daemon.

## Other commands

### `gr config show`

Print the effective (merged) configuration.

### `gr config diff`

Show changes from built-in defaults.

### `gr config reset`

Write built-in defaults to the config file.

| Flag | Description |
|------|-------------|
| `--force` | Overwrite without confirmation |

### `gr mcp`

Run graith as an MCP (Model Context Protocol) server over stdio. See [MCP Server](mcp.md).

### `gr completion <shell>`

Generate a shell completion script. Supported shells: `bash`, `zsh`, `fish`, `powershell`.

### `gr version`

Print version information.

## Hidden/internal commands

These are used by graith internally and are not intended for direct use:

| Command | Purpose |
|---------|---------|
| `gr report-status` | Report agent status (used by hooks) |
| `gr check-inbox` | Check unread inbox messages (used by hooks) |
| `gr approve-request` | Handle approval requests |
| `gr mcp-proxy` | MCP proxy for session-scoped MCP connections |
