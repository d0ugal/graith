---
weight: 340
title: "Automation & PR awareness"
description: "PR/CI watching, the comment author-trust gate, and triggers."
icon: "bolt"
toc: true
draft: false
---

## PR & CI awareness

When enabled, the daemon resolves each session's PR via `gh`, polls its CI checks and comments, and delivers an inbox notification on any meaningful change — auto-resuming a stopped agent so it reacts to review feedback and CI.

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

The `[pr_watch.advanced]` table paces the watch loop and its git-ref accelerator. Every key is optional (defaults shown).

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
kick_channel_size              = 64       # buffered kick-channel capacity (maximum 4096; a full channel drops the kick)
kicked_no_pr_backoff           = "20s"    # short re-poll delay after a kicked poll finds no PR yet
ref_reconcile_interval         = "2s"     # how often the git-ref watcher set is reconciled against sessions
ref_debounce                   = "750ms"  # coalesce a burst of ref writes from one push/commit into one kick
gh_timeout                     = "5s"     # per-command timeout for the daemon's gh invocations (PR watch + tracker poll)
```

`base_tick`, `kick_channel_size`, and `ref_reconcile_interval` are read at daemon start (restart to apply); others are read live. `ref_debounce` is read when a git-ref watcher arms or re-arms its timer; a timer pending during a reload keeps its armed duration, so the reload doesn't discard the pending ref event.

`base_tick` and `ref_reconcile_interval` fall back to the default shown for a `"0"`, `"0s"`, negative, or unparseable value. The anti-flood windows `notification_rate_window` and `untrusted_author_prompt_window` fall back the same way; they bound their paired caps (`notification_rate_limit` and the security-sensitive `untrusted_author_prompt_rate`), and a non-positive window is never honoured as "no limit" — it would silently disable the cap.

`kick_channel_size` must not exceed `4096`; larger values are rejected before
the daemon starts. Kicks are best-effort at any capacity; timer polling is the
fallback.

### Experimental GitHub push hints

Push hints are disabled by default. The opt-in transport is a loopback receiver
for an explicitly allowlisted repository, normally fed by the
[`cli/gh-webhook` extension](https://github.com/cli/gh-webhook) via `gh webhook
forward`. Install and authenticate that extension yourself when you opt in
(for example, `gh extension install cli/gh-webhook`). Enabling push causes
graith to launch `gh webhook forward` for each allowlisted repository; it does
not install extensions or request permissions. GitHub creates an ephemeral
repository webhook while that forwarder is running, subject to the extension's
webhook-administration permission and one-forwarder-per-repository limit.
GitHub documents that command as testing/development-only (with webhook-admin
permission and one active forwarder per repository or organization); it is not a
production relay. No public listener or hook is created by default.

```toml
[pr_watch.push]
enabled        = false
repositories   = ["d0ugal/graith"] # explicit allowlist required
bind_address   = "127.0.0.1:0"
body_max_bytes = 1048576
queue_size     = 256
dedupe_size    = 2048
dedupe_ttl     = "24h"
debounce       = "750ms" # coalesce same repo/PR/head bursts
# External-tool argv is configuration-driven; these placeholders are expanded.
forward_args   = ["webhook", "forward", "--repo", "{repository}", "--events", "{events}", "--url", "{url}", "--secret", "{secret}"]
# secret must be at least 32 bytes; omit it to generate one per daemon run
```

Accepted deliveries only wake matching sessions; the existing `gh` refresh then
authoritatively fetches CI and comments, so payload check conclusions are not
trusted and payload comment bodies never reach agents. Invalid signatures,
unknown events, repository mismatches, replayed IDs, and oversized bodies are
rejected. If the receiver or forwarder is unavailable, slower polling continues
and reconciles later. `gr doctor` reports push degradation separately from
polling health when the experimental transport is enabled.
The push configuration is read when the daemon starts; restart the daemon after
changing its repository allowlist, bind address, route, or secret so the old
listener and forwarders are replaced.

### Near-instant detection

The poll runs on a timer, catching a pushed PR only on the next tick. For near-instant detection the daemon also file-watches Git refs (`HEAD`, `refs/`, and the reflogs — never the object store): same-repo sessions share one recursive watch of the common refs and reflogs, each linked worktree adding only its local `HEAD` and reflog. A `git push`, commit, or checkout re-checks affected sessions immediately. The poll stays the always-on fallback, catching next cycle a push from outside the worktree — or one where the watch degrades or hits a watch limit. Nothing to configure: it follows `[pr_watch] enabled` and needs no extra permissions.

On first associating a PR, graith surfaces any **currently-broken mechanical state** — a failing CI build or merge conflict — so a stopped session (or restarted daemon) isn't stranded on a red PR. It does **not** replay review comments, PR comments, or review decisions from before it noticed the PR — those are baselined silently, so re-discovering a PR doesn't dump a backlog. Near-instant detection keeps that pre-association window to about a second.

### Comment author-trust gate

PR comments are free text from anyone, reaching the agent verbatim and able to auto-resume a stopped session — an untrusted comment is a **prompt-injection vector**. graith gates comment notifications on author trust: it notifies only if the author is trusted.

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

An untrusted comment is **held (jailed)**, not discarded: quarantined in a store the agent can't release, with the comment cursor advanced so a later trusted comment isn't reported alongside the backlog. For an owner on their own repos, every comment is theirs, so behaviour is unchanged.

### Comment jail

Inspect and release jailed comments with `gr msg jail`:

```bash
gr msg jail list                              # quarantined comments
gr msg jail show <id>                          # one comment, including its body
gr msg jail release <id>                        # deliver it to its target session
gr msg jail release --all --author <login>      # release everything from an author
```

**Releasing is restricted to the human or the orchestrator** — a plain agent session is rejected, so it can't release its own untrusted content.

The **raw comment body is only ever shown to the human or the orchestrator**: `list` is metadata-only, and `show` returns the full body only to that release-authorized caller. An agent or read-only remote guest gets metadata (PR, author, association) with the body withheld, so it can't be fed the untrusted content through a graith channel.

Adding an author to `comment_author_allowlist` (or widening `trusted_author_associations`) and reloading **releases their jailed comments automatically** — the reload is an authorized local-human action. Jailed comments respect the `[messages] max_age` retention window and don't accumulate forever.

Two trust knobs, fail-closed:

- **`trusted_author_associations`** defaults to `OWNER`/`MEMBER`/`COLLABORATOR` when **absent**. An explicit `trusted_author_associations = []` means **allowlist-only** (no association trusted), *not* silently widened back to the default.
- **`comment_author_allowlist`** is the only way to trust a bot/App: it trusts that workflow not to echo untrusted PR text into its comments — fine for first-party CI, a caveat on public repos.

## Triggers

Daemon-fired automation: a trigger is `(source) → (action)`, defined as a
`[[trigger]]` block with exactly one `[trigger.schedule]`, `[trigger.watch]`, or
`[trigger.gcx]` source and one `[trigger.action]`. An optional `[triggers]` table
holds daemon-wide settings.

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

Actions: `command` (sandboxed by default; `sandbox`/`sandbox_config` control its
boundary), `session`, `scenario`, `message`, `tracker`. Delivery routes to
`inbox`/`topic`/`store`. Policy: `catch_up` (default false), `overlap` (default
`skip`), `rate_limit` (default `5/30m`). See [Triggers]({{< relref "/docs/triggers.md" >}}) for the full reference.

Scenario files can also use `[trigger.completion]` and `[scenario.lifecycle]`
for todo-derived final actions and opt-in delayed soft cleanup. Completion is
rejected in global `config.toml` — it must bind to one persisted scenario. See
[Scenarios — completion examples]({{< relref "/docs/scenarios.md#completion-examples" >}}).

### Advanced scheduler and file-watch tuning

The `[triggers.advanced]` table paces the trigger scheduler and file-watch
runtime. Every key is optional (defaults shown).

```toml
[triggers.advanced]
scheduler_tick           = "1s"     # trigger scheduler loop cadence (cron granularity is 1 minute)
run_history_max          = 20       # per-trigger runs retained in persisted history
watch_reconcile_interval = "2s"     # how often file-watch bindings are reconciled against live sessions
watch_retry_base_backoff = "5s"     # first-retry delay for a degraded file-watch binding (then exponential)
watch_retry_max_backoff  = "5m"     # cap on the degraded-binding retry backoff
command_output_cap       = 4096     # bytes of a command action's captured output kept (rest truncated)
watch_builtin_ignores    = [".git/", ".git", ".hg/", ".svn/", "*.swp", "*.swx", "4913", ".DS_Store"]
```

An empty, invalid, or non-positive `watch_retry_base_backoff` or
`watch_retry_max_backoff` uses its default. If the resolved base exceeds the
maximum, the maximum also caps the first retry.

`watch_builtin_ignores` is the daemon-wide set of directories/patterns never
watched by any file-watch trigger (on top of git ignore rules and per-trigger
`watch.ignore`). `.git` is always ignored regardless (a watched `.git` would
create a feedback loop). Omitting the key uses the defaults above;
`watch_builtin_ignores = []` keeps only that mandatory `.git` protection and
drops every optional built-in ignore.

Reloading `watch_builtin_ignores` (add, remove, or clear to `[]`) applies to
already-running bindings on the next reconcile: each is rebuilt with the new
matcher and its watched directory set reconciled — no source-session restart
needed.

`scheduler_tick` and `watch_reconcile_interval` are read at daemon start (restart
to apply); other settings apply on the next reconcile or fire. Both fall back to
the default shown for a `"0"`, `"0s"`, negative, or unparseable value.

### Headless session actions (planned)

Spawning a `session`-type action in [headless mode]({{< relref "sessions.md#headless-sessions" >}})
via `[trigger.action] headless = true` — a natural fit for fire-and-forget
briefings and cleanup reactors — is planned (issue #1075). For now, create
headless sessions with `gr new --headless`.
