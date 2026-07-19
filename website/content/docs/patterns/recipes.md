---
weight: 1410
title: "Everyday recipes"
description: "Day-to-day workflows built from graith's primitives."
icon: "list_alt"
toc: true
draft: false
---

## Parallel feature development

Multiple agents on different features of one repo:

```bash
gr new auth-rewrite --repo ~/Code/api --prompt "rewrite the auth middleware to use JWT"
gr new add-pagination --repo ~/Code/api --prompt "add cursor-based pagination to all list endpoints"
gr new fix-n-plus-one --repo ~/Code/api --prompt "find and fix N+1 queries in the user endpoints"
```

Each agent gets its own worktree and branch -- no conflicts. Switch with `ctrl+b n/p` or the session picker (`ctrl+b w`).

## Explore-then-fork

Fork once a direction looks promising:

```bash
gr new explore-auth --prompt "investigate the auth middleware, find all the issues"
# ... agent explores, you read the findings ...
gr fork explore-auth fix-token-refresh
gr fork explore-auth fix-session-handling
```

Each fork inherits the git state; with `fork_args`, it also inherits the source's conversation history. The original is unaffected.

## Code review pipeline

One agent writes code, another reviews it:

```bash
gr new implement-feature --prompt "implement the user profile endpoint"
gr new review-feature --mirror implement-feature --prompt "review the code changes in this worktree"
```

The reviewer shares the worktree read-only, seeing changes live. Coordinate via messaging:

```bash
# From implement-feature:
gr msg send review-feature "ready for review"

# From review-feature:
gr msg send implement-feature "found an issue in handler.go:45, missing error check"
```

## Automated triggers

The [code review pipeline](#code-review-pipeline) above is manual. A
**[trigger]({{< relref "triggers" >}})** automates it in the daemon — no attached
orchestrator, survives terminal close.

**Continuous reviewer.** A watch trigger's `session` action with `ensure = true`
messages the owned reviewer (resuming it if stopped), else spawns one mirroring
the implementer's worktree read-only:

```toml
# config.toml
[[trigger]]
name = "review-go"
[trigger.watch]
role  = "implementer"          # binds to any session with this scenario role
paths = ["**/*.go"]
[trigger.action]
type   = "session"
ensure = true
agent  = "claude"
prompt = "Review the changes since your last look; send feedback via gr msg."
```

**Tests on change** — run the suite when source changes:

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

**Scheduled report** — a daily PR summary to the orchestrator:

```toml
[[trigger]]
name = "daily-pr-report"
[trigger.schedule]
cron = "0 9 * * *"
[trigger.action]
type   = "session"
prompt = "Summarise open PRs and post to the orchestrator inbox."
repo   = "~/Code/graith"
agent  = "claude"
[trigger.action.deliver]
inbox = "orchestrator"
```

Inspect and control with `gr trigger list/status/run/pause/resume`; see the
[triggers docs]({{< relref "triggers" >}}) for the full model.


## Orchestrated multi-agent workflow

Enable the orchestrator:

```toml
[orchestrator]
enabled = true
```

Then from the orchestrator (`ctrl+b o`):

```bash
# Create specialized workers
gr new lint-fixes --repo ~/Code/api --prompt "fix all linting issues"
gr new test-coverage --repo ~/Code/api --prompt "add tests to reach 80% coverage"
gr new docs --repo ~/Code/api --prompt "add godoc comments to all exported functions"

# Monitor progress
gr list --tree

# Coordinate
gr msg send --children "rebase on main before pushing"
```

## Declarative multi-repo scenario

For a known topology across repos, use a scenario file instead of imperative `gr new`:

```toml
# integration-test.toml
version = 1

[scenario]
name = "integration-tests"
goal = "Build and test the integration between API and worker services"

[scenario.policy]
completion = "quorum"
quorum = 2
on_exhausted = "fail"

[[sessions]]
name = "api"
repo = "~/Code/api"
role = "API developer"
task = "Add the batch processing endpoint with OpenTelemetry tracing"

[sessions.policy]
timeout = "45m"
retries = 1

[[sessions]]
name = "worker"
repo = "~/Code/worker"
role = "Worker developer"
task = "Add the batch consumer with retry logic and dead-letter queue"

[sessions.policy]
timeout = "45m"
retries = 1

[[sessions]]
name = "integration"
repo = "~/Code/integration-tests"
agent = "codex"
role = "Test engineer"
task = "Write integration tests for the batch processing pipeline"

[sessions.policy]
required = false
timeout = "1h"
```

From the orchestrator:

```bash
gr scenario start integration-test.toml
gr scenario status integration-tests
gr scenario stop integration-tests
```

Each session gets a manifest with the full topology — siblings, roles, and how to message them — and coordinates via `gr msg send <sibling-name> "message"`.

Scenarios are reproducible: the same TOML always creates the same fleet. See [Scenarios]({{< relref "scenarios" >}}) for the full reference.

## Background batch processing

Create sessions in the background, check later:

```bash
for repo in ~/Code/api ~/Code/web ~/Code/cli; do
  name=$(basename $repo)-audit
  gr new $name --repo $repo --background --prompt "audit for security issues, report findings"
done

# Check progress
gr list

# Read findings
gr logs api-audit
gr logs web-audit
gr logs cli-audit
```

## Persistent research notes

Persist findings across sessions with the store:

```bash
# Agent stores research
gr store put research/auth-analysis.md --file ./analysis.md

# Later, a new session reads it
gr store get research/auth-analysis.md

# Accumulate structured data
gr store append metrics/complexity.jsonl '{"file":"auth.go","cyclomatic":15}'
gr store append metrics/complexity.jsonl '{"file":"handler.go","cyclomatic":8}'
```

## Cross-repo work

Use `includes` for projects spanning multiple repos:

```toml
[[repos]]
path     = "~/Code/api"
includes = ["~/Code/shared-lib", "~/Code/proto"]
```

A session for `~/Code/api` also creates worktrees for the included repos:

```bash
gr new cross-repo-fix --repo ~/Code/api
# Agent sees:
#   GRAITH_WORKTREE_PATH       = /path/to/api-worktree
#   GRAITH_INCLUDE_SHARED_LIB_PATH = /path/to/shared-lib-worktree
#   GRAITH_INCLUDE_PROTO_PATH  = /path/to/proto-worktree
```

## Cleanup stale sessions

Remove sessions idle for a week:

```bash
gr delete --repo my-project --stale 7d -f
```

Or just stopped ones:

```bash
gr delete --repo my-project --stopped -f
```

## CI integration

Drive a session from a CI script:

```bash
# Create, run, and collect output
gr new ci-run --repo . --background --prompt "run the full test suite and report results"

# Wait for completion (poll status)
while gr list --json | jq -e '.[] | select(.name=="ci-run" and .status=="running")' > /dev/null 2>&1; do
  sleep 30
done

# Collect output
gr logs ci-run --lines 1000 > test-results.txt
gr delete ci-run -f
```

## In-place sessions

For quick one-off tasks without worktree isolation:

```bash
gr new quick-check --in-place --prompt "run the tests and tell me if anything fails"
```

No worktree -- the agent runs directly in the repo. Good for read-only tasks or seeing uncommitted changes.

Allow multiple in-place sessions on one repo:

```bash
gr new check-1 --in-place --allow-concurrent
gr new check-2 --in-place --allow-concurrent
```

## Remote session driving

Type commands into a running session:

```bash
gr type my-session "/help"
gr type my-session "please also check the error handling"
gr type my-session --no-newline "y"    # answer a prompt
```

Watch output without attaching:

```bash
gr logs my-session -f
```

## Status-driven workflows

Agents update status for visibility:

```bash
gr status "Phase 1: analyzing codebase"
# ... agent works ...
gr status "Phase 2: implementing fixes"
# ... agent works ...
gr status "Phase 3: running tests"
# ... agent works ...
gr status "Done - all tests passing"
```

The orchestrator or user follows progress in the session picker (`ctrl+b w`), which shows all sessions' status summaries.
