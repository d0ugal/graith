# Orchestrator

The orchestrator is a special system session that coordinates other agent sessions. It has no repository or worktree of its own; its power comes from the graith control plane.

## Prerequisites

The orchestrator requires sandbox to be enabled. If sandbox is not available (safehouse not installed or `sandbox.enabled = false`), orchestrator creation fails with an error.

## Enabling

```toml
[sandbox]
enabled = true

[orchestrator]
enabled      = true
agent        = "claude"
model        = ""
idle_timeout = "30m"
prompt       = "..."       # custom prompt (optional)
prompt_file  = ""          # or read from file
```

When enabled, the orchestrator session is created automatically and accessible via `ctrl+b o`.

## Capabilities

The orchestrator runs in a scratch directory with no repo. It manages other sessions through `gr` commands:

```bash
gr new <name> --repo <path>       # create sessions (ALWAYS pass --repo)
gr stop <session>                 # stop sessions
gr delete <session>               # delete sessions
gr restart <session>              # restart sessions
gr list                           # list all sessions with status
gr msg send <session> "text"      # message a specific session
gr msg send --children "text"     # message all child sessions
gr msg pub --topic <topic> "text" # broadcast to a topic
gr msg sub --topic "inbox:$GRAITH_SESSION_ID" --all --ack  # read inbox messages
gr store put --shared <key> <body> # persist documents (use --shared)
gr status "message"               # set status visible in picker
gr type <session> "text"          # type into another session
```

## Important constraints

- **No repo:** The orchestrator has no repo or worktree. Always use `--repo <path>` when creating sessions. Use `--shared` for store operations.
- **Parent of its children:** Sessions created by the orchestrator have it as their parent. Use `--children` flags to manage them.
- **Idle timeout:** Defaults to 30 minutes. Override with `idle_timeout`.

## Default prompt

The built-in orchestrator prompt teaches the agent about its capabilities, constraints, and the graith control plane. Override with a custom `prompt` or `prompt_file` in config.

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
  gr msg sub --topic "inbox:$GRAITH_SESSION_ID" --all --ack

Orchestrator coordinates:
  gr msg send auth-tests "middleware is done, you can start integration tests now"
  gr msg send auth-migration "hold off until tests pass"
```

## Access

- `ctrl+b o` switches to the orchestrator session from any attached session
- The orchestrator appears in the session picker with a system kind indicator
- `gr list` shows it alongside regular sessions
