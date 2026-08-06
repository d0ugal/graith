---
weight: 700
title: "Orchestrator"
description: "Coordinate multiple agents with the orchestrator."
icon: "hub"
toc: true
draft: false
---

The orchestrator is a special system session that coordinates other agent sessions. It has no repository or worktree of its own — its power comes from the graith control plane.

## Prerequisites

The orchestrator uses the same independent sandbox, native-prompt, and command-
policy settings as ordinary sessions. Enabling the sandbox is strongly
recommended because the orchestrator can create and control descendants; it is
off until you configure a backend. If an enabled backend or configured command
policy is unavailable, creation fails; an explicitly disabled sandbox is
allowed, with the usual startup warning and `gr doctor` diagnostic.

## Enabling

```toml
[sandbox]
enabled = true
backend = "nono"       # or "safehouse" on macOS

[orchestrator]
enabled      = true
agent        = ""          # agent to run as; empty inherits the top-level default_agent
model        = ""
idle_timeout = "30m"
prompt       = "..."       # custom prompt (optional)
prompt_file  = ""          # or read from file
```

When `[orchestrator] agent` is empty, the orchestrator inherits the top-level `default_agent` (falling back to `claude` only if that's also unset). Set it explicitly to run the orchestrator as a different agent than your session default.

When enabled, the orchestrator session is created automatically and reachable via `ctrl+b o`.

## Update notices

When the daemon detects that the running Graith build changed while the
orchestrator is enabled, it sends the orchestrator a system inbox notice. The
notice includes the previous and new Graith versions, commit metadata when
available, the detection time, and whether the transition looks like an
upgrade, downgrade, same-version daemon replacement, or unavailable version
metadata.

Delivery is durable and deduplicated through the daemon message store. The daemon
retries pending notices on startup, orchestrator reconciliation, and successful
orchestrator supervisor restart. A stopped orchestrator is resumed for the inbox
notice, and an enabled orchestrator that is recreated after the update receives
the pending notice once it exists. If message publication keeps failing, the
notice stays pending until one of those retry paths runs again. No notice is
queued for updates detected while `[orchestrator] enabled = false`.

Same-version daemon replacements include commit-only rebuilds. Version
comparison uses Graith's existing numeric parsing, so prerelease suffixes can be
reported as same-version replacements when the numeric version is unchanged.

On receipt, the orchestrator should inspect release notes, configuration
changes, and new capabilities when useful, then proactively suggest applicable
configuration, workflow, trigger, or skill updates to the user.

## Starting fresh

Delete the orchestrator to discard its current conversation and recreate it
from the current configuration:

```bash
gr delete orchestrator
```

Unlike ordinary sessions, this is an immediate reset, not a recoverable soft
delete. With `[orchestrator] enabled = true`, the daemon recreates a fresh
orchestrator within a few seconds, using the currently configured agent, model,
and prompt. Use `gr stop orchestrator` to keep it stopped. To remove it
permanently with `gr purge`, disable it in config first.

## Capabilities

The orchestrator runs in a scratch directory with no repo. It manages other sessions through `gr` commands:

```bash
gr new <name> --repo <path>       # create sessions (ALWAYS pass --repo)
gr scenario start <file>          # start a declarative multi-session scenario
gr scenario status <name>         # check scenario status
gr scenario stop <name>           # stop all sessions in a scenario
gr scenario delete <name>         # delete a scenario and its sessions
gr stop <session>                 # stop sessions
gr delete <session>               # delete sessions
gr restart <session>              # restart sessions
gr list                           # list all sessions with status
gr msg send <session> "text"      # message a specific session
gr msg send --children "text"     # message all child sessions
gr msg pub --topic <topic> "text" # broadcast to a topic
gr msg inbox --all --ack              # read inbox messages
gr store put --shared <key> <body> # persist documents (use --shared)
gr status "message"               # set status visible in the Session Navigator
gr attention "Need user input"     # show an outstanding request in the status bar
gr type <session> "text"          # type into another session
```

For reproducible, multi-repo session fleets, use [scenarios](scenarios.md) — they define sessions declaratively in a TOML file and create them atomically, rolling back on failure.

## Status-bar attention

The orchestrator can request the user's attention without sending a desktop
notification:

```bash
gr attention "Need release decision" --context "Use gr msg jail list"
```

Every attached session's status bar shows a red orchestrator attention marker.
The marker clears automatically when the user attaches or switches to the
orchestrator. If the request is at least a few minutes old when the user arrives,
the daemon sends the orchestrator one system inbox notice saying the user has
arrived, including the stored context. Fresh requests clear silently to avoid
repeating the active conversation.

Clear an outstanding request explicitly:

```bash
gr attention --clear
```

## Important constraints

- **No repo:** The orchestrator has no repo or worktree. Always use `--repo <path>` when creating sessions. Use `--shared` for store operations.
- **Parent of its children:** Sessions created by the orchestrator have it as their parent. Use `--children` flags to manage them.
- **Idle timeout:** Defaults to 30 minutes. Override with `idle_timeout`.

## Default prompt

The built-in orchestrator prompt teaches the agent about its capabilities, constraints, and the graith control plane. Override it with a custom `prompt` or `prompt_file` in config.

Prompt injection fails closed on both create and resume: if the configured prompt can't be assembled or injected (say, a Cursor rules file that can't be written), the orchestrator doesn't start and the operation returns an error — graith won't launch a privileged orchestrator without its role prompt.

## Workflow example

```
User opens orchestrator (ctrl+b o):

  "Set up three agents to work on the auth rewrite.
   One for the middleware, one for the tests, one for the migration."

Orchestrator runs:
  gr new auth-middleware --repo ~/Code/my-project --prompt "Rewrite the auth middleware..."
  gr new auth-tests --repo ~/Code/my-project --prompt "Write comprehensive tests for..."
  gr new auth-migration --repo ~/Code/my-project --prompt "Create the database migration..."

  gr status "Managing 3 auth rewrite sessions"

Orchestrator monitors:
  gr list
  gr msg inbox --all --ack

Orchestrator coordinates:
  gr msg send auth-tests "middleware is done, you can start integration tests now"
  gr msg send auth-migration "hold off until tests pass"
```

## Access

- `ctrl+b o` switches to the orchestrator session from any attached session
- The orchestrator appears in the Session Navigator with a system kind indicator
- `gr list` shows it alongside regular sessions
