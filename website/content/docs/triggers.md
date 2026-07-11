---
weight: 1350
title: "Triggers"
description: "Daemon-fired automation on a schedule or on file changes."
icon: "bolt"
toc: true
draft: false
---

Triggers let the daemon run actions **on its own** — on a time schedule or when
files change in a session worktree — so automation survives terminal close and
needs no attached orchestrator. A trigger is `(source) → (action)`.

Triggers are defined in `config.toml` as `[[trigger]]` blocks. Exactly one
**source** (`[trigger.schedule]` or `[trigger.watch]`) and one **action**
(`[trigger.action]`).

## Sources

### Schedule (time-driven)

```toml
[trigger.schedule]
cron     = "0 9 * * *"   # 5-field cron, or @hourly/@daily/@weekly/@monthly
timezone = "Europe/London"
# or, instead of cron:
# every = "15m"          # Go duration; supports 7d-style day suffixes
```

- Exactly one of `cron` or `every`. `timezone` applies to `cron` only.
- Cron is timezone/DST-aware. Missed fires while the daemon was down are **not**
  backfilled unless `policy.catch_up = true` (which fires at most once on
  startup).

### Watch (file-event-driven)

```toml
[trigger.watch]
repo     = "~/Code/graith"   # OR: role = "implementer"
paths    = ["**/*.go"]       # optional include globs
ignore   = ["**/*_test.go"]  # optional extra ignores
debounce = "30s"             # quiet-window; lower for fast commands
```

- A watch trigger is a **policy selector** by `repo` or `role` — never a live
  session name. It binds to matching running sessions and watches their
  worktrees.
- `.gitignore` is always honoured (ignored directories are pruned from the watch
  set, so `node_modules/` etc. don't exhaust the watcher).
- A burst of edits is coalesced by the `debounce` quiet-window into one fire.

## Actions

```toml
[trigger.action]
type = "command"   # command | session | scenario | message
```

| Type | What it does |
|------|--------------|
| `command` | Run a command (schedule: in `repo`; watch: in the bound worktree), capture output, deliver it. Sandboxed by default. |
| `session` | Spawn a session, parented to the orchestrator. |
| `scenario` | Start a named scenario from `~/.config/graith/scenarios/`. |
| `message` | Route a fixed `body` to an inbox or topic. |

### Command sandboxing

`command` actions are sandboxed by default, mirroring MCP-server config:

```toml
[trigger.action]
type    = "command"
command = "go test ./..."
# sandbox = false            # run unconfined (opt-out; fail-closed otherwise)
[trigger.action.sandbox_config]  # grant extra access while staying sandboxed
write_files = ["/var/run/docker.sock"]
[trigger.action.sandbox_config.network]
block = false                # allow network egress (blocked by default)
```

Watch commands are read-only on the worktree in v1 (`mutating` is rejected).

### Ensure-reviewer (watch + session)

The flagship pattern — keep a reviewer reacting to an implementer's changes,
idempotently:

```toml
[trigger.action]
type   = "session"
ensure = true   # message the owned reactor if it exists (auto-resumes a stopped
                # one), else spawn one sharing the bound worktree read-only
agent  = "claude"
prompt = "Review the changes since your last look; send feedback via gr msg."
```

## Delivery

`[trigger.action.deliver]` routes action output. Fields are templated at fire
time (`{name}`, `{date}`, `{datetime}`, `{fire_time}`, and for watch triggers
`{session_name}`, `{worktree_path}`, `{changed_files}`, `{change_count}`).

```toml
[trigger.action.deliver]
inbox = "orchestrator"          # a session name, "orchestrator", or {session_name}
topic = "ci-reports"            # a pub/sub topic
store = "reports/{date}.md"     # a store doc (prefix "shared:" for the shared store)
wake  = false                   # resume a stopped non-orchestrator inbox target
```

`inbox` auto-resumes the orchestrator (or any target with `wake = true`), and
never wakes a soft-deleted session.

## Policy

```toml
[trigger.policy]
catch_up   = false     # never backfill a burst of missed schedule fires (default)
overlap    = "skip"    # skip if the previous run is in flight (default); or "allow"
rate_limit = "5/30m"   # rolling per-trigger fire cap (default)
```

A daemon-wide cap bounds aggregate fan-out:

```toml
[triggers]
max_concurrent = 4
```

## CLI

Triggers are defined in config; `gr trigger` observes and controls them:

```bash
gr trigger list                 # all triggers: source, action, next fire / watch scope, state
gr trigger status <name>        # detail: next fire, last run/result/error, live bindings
gr trigger run <name>           # fire a schedule trigger once now (respects overlap)
gr trigger pause <name>         # pause (persists across restart)
gr trigger resume <name>
```

`list`/`status` are read-only; `run`/`pause`/`resume` require the orchestrator or
a descendant.

## Examples

Daily PR report:

```toml
[[trigger]]
name = "daily-pr-report"
[trigger.schedule]
cron     = "0 9 * * *"
timezone = "Europe/London"
[trigger.action]
type   = "session"
prompt = "Summarise open PRs and post to the orchestrator inbox."
repo   = "~/Code/graith"
agent  = "claude"
[trigger.action.deliver]
inbox = "orchestrator"
store = "reports/pr/{date}.md"
```

Run tests on change:

```toml
[[trigger]]
name = "test-on-change"
[trigger.watch]
repo  = "~/Code/graith"
paths = ["**/*.go"]
[trigger.action]
type    = "command"
command = "go test ./..."
[trigger.action.deliver]
inbox = "{session_name}"
```
