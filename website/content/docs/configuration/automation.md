---
weight: 340
title: "Automation & PR awareness"
description: "PR/CI watching, the comment author-trust gate, and triggers."
icon: "bolt"
toc: true
draft: false
---

## PR & CI awareness

When enabled, the daemon resolves each session's GitHub PR via `gh`, polls its CI checks and comments, and delivers a structured notification to the session's inbox on a meaningful change — auto-resuming a stopped agent so it reacts to review feedback and CI results without you relaying them.

```toml
[pr_watch]
enabled                  = false  # opt in to PR/CI awareness
notify_ci_failures       = true   # notify on a CI transition to failing
notify_pr_comments       = true   # notify on new PR conversation comments
notify_review_comments   = true   # notify on new inline review comments
poll_pending             = "1m"   # poll interval while checks are still running
debounce                 = "2m"   # minimum cooldown between notifications to a session
```

### Advanced watcher tuning

The `[pr_watch.advanced]` table exposes the low-level policy knobs that pace the watch loop and its git-ref accelerator. Every key is optional and falls back to the default shown, so omitting the table keeps the built-in behaviour. Duration overrides in this block must be positive; zero does not disable its anti-flood or coalescing windows. Tune these only to trade off `gh` load, detection latency, notification retention, and the untrusted-author prompt surface.

```toml
[pr_watch.advanced]
base_tick                      = "15s"    # base poll-loop cadence (per-session gating paces gh below it)
batch_size                     = 3        # max sessions polled per tick (bounds gh load on a large fleet)
no_pr_negative_cache           = "5m"     # re-resolve a branch with no PR at most this often
comment_body_max_bytes         = 1024     # truncate each delivered comment body to this many bytes
notification_rate_limit        = 5        # per-session rolling notification cap (anti-thrash backstop)
notification_rate_window       = "30m"    # rolling window for notification_rate_limit
untrusted_author_prompt_rate   = 5        # max untrusted-author trust prompts to the orchestrator per window
untrusted_author_prompt_window = "30m"    # rolling window for untrusted_author_prompt_rate
max_prompted_authors           = 5000     # cap on the persisted set of already-surfaced untrusted authors
kick_cooldown                  = "3s"     # min interval between git-ref-triggered polls of one session
kick_channel_size              = 64       # buffered kick-channel capacity (a full channel drops the kick)
kicked_no_pr_backoff           = "20s"    # short re-poll delay after a kicked poll finds no PR yet
ref_reconcile_interval         = "2s"     # how often the git-ref watcher set is reconciled against sessions
ref_debounce                   = "750ms"  # coalesce a burst of ref writes from one push/commit into one kick
gh_timeout                     = "5s"     # per-command timeout for the daemon's gh invocations
```

The loop-lifetime knobs — `base_tick`, `kick_channel_size`, and `ref_reconcile_interval` — are read when the daemon starts, so changing them takes effect on the next daemon restart; the rest apply on the next poll.

### Near-instant detection

The GitHub poll runs on a timer, so on its own a freshly pushed PR would only be picked up on the next tick. To make detection near-instant, the daemon also puts a lightweight local file watch on each running session's git refs (`HEAD`, `refs/`, and the reflogs — never the object store). When a `git push`, commit, or checkout touches them, it triggers an immediate PR re-check for that session instead of waiting for the poll. The poll stays on as the always-on fallback, so a push from outside the worktree — or a platform where the file watch degrades — is still caught on the next cycle. There is nothing to configure: the watch turns on and off with `[pr_watch] enabled` and needs no extra permissions.

When graith first associates a PR, it surfaces any **currently-broken mechanical state** — a failing CI build or a merge conflict — so a session that was stopped (or a daemon that restarted) doesn't strand an agent on a red PR. It deliberately does **not** replay pre-existing review comments, PR comments, or review decisions from before it noticed the PR: those are baselined silently to avoid dumping a whole backlog when re-discovering an old PR. Near-instant detection keeps that pre-association window down to about a second, so little is missed in practice.

### Comment author-trust gate

PR comments are free text from arbitrary GitHub users. Because they reach the agent verbatim and can auto-resume a stopped session, an untrusted comment is a **prompt-injection vector**. graith gates comment notifications on the author's trust: a comment only notifies if its author is trusted.

```toml
[pr_watch]
# Trust individual authors by login (case-insensitive). This is the ONLY way to
# trust a bot or GitHub App — a bot's author_association is unreliable and never
# confers trust on a "<slug>[bot]" login.
comment_author_allowlist = ["github-actions[bot]", "coderabbitai[bot]"]

# Trust anyone whose GitHub author_association is in this set. Omit the key to
# keep the default tier below (CONTRIBUTOR is deliberately excluded — on a public
# repo it only means "merged a commit once", and bots can carry it).
trusted_author_associations = ["OWNER", "MEMBER", "COLLABORATOR"]

# Surface a not-yet-trusted author to the orchestrator once, as metadata only
# (login/type/association/PR + a `gh pr view` pointer) — NEVER the comment body,
# so the human can decide whether to allowlist them.
notify_untrusted_authors = true
```

An untrusted comment is **held (jailed)**, not discarded: its full content is quarantined in a store the agent can't release, and the comment cursor advances so a later trusted comment isn't reported alongside the whole untrusted backlog. For the common case — an owner working on their own repos — every comment is from the owner, so behaviour is unchanged.

### Comment jail

Jailed comments can be inspected and released with `gr msg jail`:

```bash
gr msg jail list                              # quarantined comments
gr msg jail show <id>                          # one comment, including its body
gr msg jail release <id>                        # deliver it to its target session
gr msg jail release --all --author <login>      # release everything from an author
```

**Releasing is restricted to the human or the orchestrator** — a plain agent session is rejected. Releasing delivers previously-untrusted content to a working agent, so allowing an agent to release its own quarantined comment would defeat the point.

The **raw comment body is only ever shown to the human or the orchestrator**. `list` is a metadata-only summary (never carries a body); `show` returns the full body only to a release-authorized caller — an agent or a read-only remote guest sees the metadata with the body withheld. This keeps the prompt-injection boundary intact: an agent can see *what* is jailed (PR, author, association) but can't be fed the untrusted content through a graith channel.

When you add an author to `comment_author_allowlist` (or widen `trusted_author_associations`) and reload the config, their jailed comments are **released automatically** — the reload is a local-human action, so it's implicitly authorized. Jailed comments respect the `[messages] max_age` retention window and don't accumulate forever.

Two trust knobs, fail-closed:

- **`trusted_author_associations`** defaults to `OWNER`/`MEMBER`/`COLLABORATOR` when the key is **absent**. Setting it to an explicit empty list (`trusted_author_associations = []`) is honoured as **allowlist-only** mode — no association is trusted — and is *not* silently widened back to the default.
- **`comment_author_allowlist`** is the only way to trust a bot/App. Allowlisting a bot login trusts that workflow to not echo untrusted PR text back into its comments; fine for a repo's own first-party CI, a documented caveat on public repos.

## Triggers

Daemon-fired automation: a trigger is `(source) → (action)`. Define them as
`[[trigger]]` blocks (each with exactly one `[trigger.schedule]` or
`[trigger.watch]` source and one `[trigger.action]`), plus an optional
`[triggers]` table for daemon-wide settings.

```toml
[triggers]
max_concurrent = 4          # cap on concurrently-running trigger actions

# Schedule → command: run tests nightly and report to the orchestrator.
[[trigger]]
name = "nightly-tests"
[trigger.schedule]
cron = "0 3 * * *"          # 5-field cron / @daily; or: every = "15m"
[trigger.action]
type    = "command"
command = "go test ./..."
repo    = "~/Code/graith"
[trigger.action.deliver]
inbox = "orchestrator"

# Watch → session: keep a reviewer reacting to an implementer's changes.
[[trigger]]
name = "review-go"
[trigger.watch]
role  = "implementer"       # policy selector: repo or role, never a live session
paths = ["**/*.go"]
debounce = "30s"
[trigger.action]
type   = "session"
ensure = true               # message the owned reactor if it exists, else spawn
agent  = "claude"
prompt = "Review the changes since your last look; send feedback via gr msg."
```

Actions: `command` (sandboxed by default; `sandbox`/`sandbox_config` mirror
MCP-server config), `session`, `scenario`, `message`. Delivery routes to
`inbox`/`topic`/`store`. Policy: `catch_up` (default false), `overlap` (default
`skip`), `rate_limit` (default `5/30m`). See [Triggers]({{< relref "/docs/triggers.md" >}}) for the full
reference.

### Advanced scheduler and file-watch tuning

The `[triggers.advanced]` table exposes the low-level policy knobs that pace the
trigger scheduler and the file-watch runtime. Every key is optional and falls
back to the default shown, so omitting the table keeps the built-in behaviour.
Tune these only to trade off dispatch latency, file-watch reconcile load,
degraded-binding recovery, and notification size.

```toml
[triggers.advanced]
scheduler_tick           = "1s"     # trigger scheduler loop cadence (cron granularity is 1 minute)
run_history_max          = 20       # per-trigger runs retained in persisted history
watch_reconcile_interval = "2s"     # how often file-watch bindings are reconciled against live sessions
watch_retry_base_backoff = "5s"     # positive first-retry delay for a degraded file-watch binding (then exponential)
watch_retry_max_backoff  = "5m"     # positive cap on retry backoff; must be >= the base
command_output_cap       = 4096     # bytes of a command action's captured output kept (rest truncated)
watch_builtin_ignores    = [".git/", ".git", ".hg/", ".svn/", "*.swp", "*.swx", "4913", ".DS_Store"]
```

`watch_builtin_ignores` is the daemon-wide set of directories/patterns never
watched by any file-watch trigger (on top of git ignore rules and per-trigger
`watch.ignore`). `.git` is always ignored regardless of this list, because a
watched `.git` churns constantly and creates a feedback loop.

The degraded-watch retry bounds must be positive and
`watch_retry_base_backoff` must not exceed `watch_retry_max_backoff`; invalid
bounds are rejected on load or reload rather than creating a retry loop.

The loop-lifetime knobs — `scheduler_tick` and `watch_reconcile_interval` — are
read when the daemon starts, so changing them takes effect on the next daemon
restart; the rest apply on the next fire.

### Headless session actions (planned)

Letting a `session`-type action spawn its session in [headless mode]({{< relref "sessions.md#headless-sessions" >}})
via `[trigger.action] headless = true` — a natural fit for fire-and-forget
briefings and cleanup reactors — is planned (issue #1075). For now, create
headless sessions with `gr new --headless`.
