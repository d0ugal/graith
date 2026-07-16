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

Triggers are defined as `[[trigger]]` blocks — in `config.toml` for global
automation, or inside a scenario TOML for
[scenario-embedded triggers]({{< relref "scenarios.md#trigger-blocks-scenario-embedded-triggers" >}})
that activate with the scenario. Each block has exactly one **source**
(`[trigger.schedule]` or `[trigger.watch]`) and one **action**
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
- A `role` selector matches any running session with that scenario role. It is
  also how a scenario ships its own automation: a
  [scenario-embedded trigger]({{< relref "scenarios.md#trigger-blocks-scenario-embedded-triggers" >}})
  uses a `role` its scenario defines and binds only inside that scenario.
- `.gitignore` is always honoured (ignored directories are pruned from the watch
  set, so `node_modules/` etc. don't exhaust the watcher).
- A burst of edits is coalesced by the `debounce` quiet-window into one fire.
- If a binding can't register its watch (e.g. the OS watch limit
  `fs.inotify.max_user_watches` is exhausted) it is marked **degraded** and
  retried on an exponential backoff (5s, 10s, 20s, … capped at 5m). It recovers
  on its own once the limit clears — no session restart needed.
  `gr trigger status <name>` and `gr doctor` surface the degraded state and the
  next retry time.

## Actions

```toml
[trigger.action]
type = "command"   # command | session | scenario | message | tracker
```

| Type | What it does |
|------|--------------|
| `command` | Run a command (schedule: in `repo`; watch: in the bound worktree), capture output, deliver it. Sandboxed by default. |
| `session` | Spawn a session, parented to the orchestrator. |
| `scenario` | Start a named scenario from `~/.config/graith/scenarios/`. |
| `message` | Route a fixed `body` to an inbox or topic. |
| `tracker` | Poll an issue tracker and reconcile sessions against it — spawn one per active issue, reap it when the issue goes inactive (schedule source only). |

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

### Auto-cleanup (session)

A `session` action can soft-delete the session it spawns once that session
stops, so finished briefing/report sessions don't accumulate in `gr list`:

```toml
[trigger.action]
type         = "session"
agent        = "claude"
prompt       = "Summarise open PRs and post to the orchestrator inbox."
auto_cleanup = true            # delete the session once it stops
```

`auto_cleanup` accepts:

| Value | Behaviour |
|-------|-----------|
| `false` / absent | No cleanup (default — the session is left stopped). |
| `true` / `"always"` | Soft-delete on any stop, clean exit or crash. |
| `"on_success"` | Soft-delete only on a clean stop (agent exit code 0). |

`"on_success"` means the agent completed and exited **on its own** with code 0.
A session ended by `gr stop`, an idle timeout, a daemon shutdown, or a crash is
never a success — not even if it happens to exit 0 (the stop reason, not just
the exit code, decides) — so it is left in place.

Cleanup is a **soft delete**, so the session stays recoverable with `gr restore`
within the `[delete] retention` window before it is purged. It only applies to
trigger-spawned sessions — never a manually created one — and is incompatible
with `ensure = true` (a reused reactor is deliberately long-lived). If soft
delete is disabled (`[delete] retention = "0"`) auto-cleanup is skipped rather
than turned into an immediate hard delete, and a session interrupted by a daemon
shutdown is preserved so `gr daemon restart` can resume it.

#### Reaping the session promptly (`idle_timeout`)

Cleanup only fires once the session actually stops. An interactive agent (e.g.
Claude's TUI) doesn't exit when it finishes — it sits idle at its prompt — so
the daemon has to idle-stop it first. To make that prompt, `auto_cleanup =
true` / `"always"` gives the spawned session a **1-minute idle timeout** by
default, so the chain runs quickly: finish → idle-stop → soft-delete. Override
it with `idle_timeout` (a Go duration):

```toml
[trigger.action]
type         = "session"
agent        = "claude"
prompt       = "Summarise open PRs and post to the orchestrator inbox."
auto_cleanup = true
idle_timeout = "2m"            # override the 1m auto_cleanup default
```

`idle_timeout` works on any `session` action (not just with `auto_cleanup`),
must be at least `1s`, and overrides the agent's default idle window. The
1-minute default is only applied for `"always"`: an `"on_success"` session is
never auto-idled, because an idle-stop is not a success and so wouldn't be
cleaned up — idling it would just leave stopped clutter. Setting `idle_timeout`
explicitly on an `"on_success"` session still only *stops* it; it does not clean
it up (only a clean self-exit does).

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

### Tracker (issue-tracker sync)

A `tracker` action keeps live sessions in sync with an issue tracker. On each
scheduled poll it fetches the tracker's **active** issues and reconciles sessions
against them: it spawns one session per active issue (seeded from the issue body)
and reaps the session when its issue leaves the active state. The tracker is the
source of truth — the daemon drives the live session set toward it every tick.
It requires a `[schedule]` source and an enabled `[orchestrator]` (which owns the
spawned sessions). GitHub Issues is the only provider in v1.

```toml
[[trigger]]
name = "issue-sessions"

[trigger.schedule]
every = "5m"

[trigger.action]
type   = "tracker"
agent  = "claude"
prompt = "Work on GitHub issue #{issue_number}: {issue_title}\n\n{issue_body}\n\n{issue_url}"

[trigger.action.tracker]
provider       = "github"          # v1: github (default)
repo           = "~/Code/graith"   # resolves the GitHub slug + is the spawn repo
active_state   = "open"            # open | closed | all (default open)
active_labels  = ["in-progress"]   # active iff the issue has one of these (empty = any)
assignee       = "@me"             # optional gh assignee filter
grace          = "10m"             # inactive this long before reaping (default 5m)
max_concurrent = 3                 # cap on live tracker sessions (0 = unlimited)
reap           = "stop"            # stop | delete | none (default stop)
limit          = 50                # max issues fetched per poll (default 50)

[trigger.action.deliver]
inbox = "orchestrator"             # optional: the reconcile summary
```

The spawned session's `prompt` is templated per issue with `{issue_number}`,
`{issue_title}`, `{issue_body}`, `{issue_url}`, and `{issue_labels}` (plus the
usual `{name}`/`{date}`/`{datetime}`/`{fire_time}`).

**Reconciliation semantics:**

- **Idempotent.** Sessions are deduplicated by a durable per-issue tag, so a
  session is never respawned while a live (running or stopped) one for the same
  issue exists — even across a daemon restart.
- **Grace window.** An issue must stay inactive for `grace` before its session is
  reaped, so a brief mislabel or column bounce doesn't kill in-flight work. An
  issue that becomes active again clears the grace clock; a stopped session for a
  re-activated issue is resumed rather than duplicated.
- **Reap policy.** `stop` (default) stops the agent (recover with `gr resume`);
  `delete` soft-deletes the session (recover with `gr restore` within the
  retention window); `none` leaves it and only reports. Starred and system
  sessions are never reaped.
- **Concurrency.** `max_concurrent` caps how many tracker sessions run at once, so
  a large backlog can't spawn dozens of agents in one poll; the rest are deferred
  to later ticks.

The tracker is **read-only** — graith never writes back to it (no closing issues,
no comments). Reaping never hard-deletes.

**Security — issue text is fed to an autonomous agent.** The issue title and body
are expanded verbatim into the spawned agent's prompt. On a public repo, anyone
who can open or edit an issue can therefore inject instructions into an agent
that has write access to a worktree. Unlike PR comments (which graith jails
behind an author-trust gate), issue bodies have **no trust gate** in v1. Only run
the tracker against repos/issues you trust, scope it tightly with `active_labels`
and/or `assignee` to a trusted set, and keep the agent sandbox enabled. An
author-trust gate for issues is a possible follow-up.

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
