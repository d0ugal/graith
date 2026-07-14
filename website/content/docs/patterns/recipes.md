---
weight: 1410
title: "Everyday recipes"
description: "Day-to-day workflows built from graith's primitives."
icon: "list_alt"
toc: true
draft: false
---

Practical patterns for using graith's primitives together.

## Parallel feature development

Run multiple agents on different features of the same repo simultaneously:

```bash
gr new auth-rewrite --repo ~/Code/api --prompt "rewrite the auth middleware to use JWT"
gr new add-pagination --repo ~/Code/api --prompt "add cursor-based pagination to all list endpoints"
gr new fix-n-plus-one --repo ~/Code/api --prompt "find and fix N+1 queries in the user endpoints"
```

Each agent works in its own worktree and branch. No working-tree conflicts. Switch between them with `ctrl+b n/p` or the session picker (`ctrl+b w`).

## Explore-then-fork

Start an exploratory session, then fork when you find a promising direction:

```bash
gr new explore-auth --prompt "investigate the auth middleware, find all the issues"
# ... agent explores, you read the findings ...
gr fork explore-auth fix-token-refresh
gr fork explore-auth fix-session-handling
```

Each fork inherits the git state. If the agent has `fork_args` configured, the new agent also gets the source session's conversation history. The original session is unaffected.

## Code review pipeline

One agent writes code, another reviews it:

```bash
gr new implement-feature --prompt "implement the user profile endpoint"
gr new review-feature --mirror implement-feature --prompt "review the code changes in this worktree"
```

The reviewer shares the implementer's worktree (read-only) and can see changes as they happen. Use messaging to coordinate:

```bash
# From implement-feature:
gr msg send review-feature "ready for review"

# From review-feature:
gr msg send implement-feature "found an issue in handler.go:45, missing error check"
```

## Automated triggers

The [code review pipeline](#code-review-pipeline) above is set up by hand. A
**[trigger]({{< relref "triggers" >}})** makes the daemon do it automatically — no attached
orchestrator, surviving terminal close.

**Continuous reviewer** — keep a reviewer reacting to an implementer's changes.
A watch trigger with a `session` action (`ensure = true`) messages the owned
reviewer if it exists (auto-resuming a stopped one), else spawns one mirroring
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

**Tests on change** — run the suite when source changes, results to the session's
inbox:

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

**Scheduled report** — a daily PR summary posted to the orchestrator:

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

Inspect and control them with `gr trigger list/status/run/pause/resume`. See the
[triggers docs]({{< relref "triggers" >}}) for the full model.

## Orchestrated multi-agent workflow

Use the orchestrator to manage a fleet of agents:

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

When you have a known topology of sessions across multiple repos, use a scenario file instead of imperative `gr new` commands:

```toml
# integration-test.toml
version = 1

[scenario]
name = "integration-tests"
goal = "Build and test the integration between API and worker services"

[[sessions]]
name = "api"
repo = "~/Code/api"
role = "API developer"
task = "Add the batch processing endpoint with OpenTelemetry tracing"

[[sessions]]
name = "worker"
repo = "~/Code/worker"
role = "Worker developer"
task = "Add the batch consumer with retry logic and dead-letter queue"

[[sessions]]
name = "integration"
repo = "~/Code/integration-tests"
agent = "codex"
role = "Test engineer"
task = "Write integration tests for the batch processing pipeline"
```

From the orchestrator:

```bash
gr scenario start integration-test.toml
gr scenario status integration-tests
gr scenario stop integration-tests
```

Each session receives a manifest with the full scenario topology — who the siblings are, their roles, and how to message them. Sessions coordinate via `gr msg send <sibling-name> "message"`.

Scenarios are reproducible — the same TOML file always creates the same fleet. See [Scenarios]({{< relref "scenarios" >}}) for the full reference.

## Background batch processing

Create sessions in the background and check on them later:

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

Use the store to persist findings across sessions:

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

Use `includes` for projects that span multiple repositories:

```toml
[[repos]]
path     = "~/Code/api"
includes = ["~/Code/shared-lib", "~/Code/proto"]
```

Creating a session for `~/Code/api` also creates worktrees for the included repos:

```bash
gr new cross-repo-fix --repo ~/Code/api
# Agent sees:
#   GRAITH_WORKTREE_PATH       = /path/to/api-worktree
#   GRAITH_INCLUDE_SHARED_LIB_PATH = /path/to/shared-lib-worktree
#   GRAITH_INCLUDE_PROTO_PATH  = /path/to/proto-worktree
```

## Cleanup stale sessions

Remove sessions that have been idle for a week:

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

For quick one-off tasks that don't need worktree isolation:

```bash
gr new quick-check --in-place --prompt "run the tests and tell me if anything fails"
```

No worktree is created. The agent runs directly in the repo. Useful for read-only tasks or when you want the agent to see uncommitted changes.

Allow multiple in-place sessions on the same repo:

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

Agents update their status for visibility:

```bash
gr status "Phase 1: analyzing codebase"
# ... agent works ...
gr status "Phase 2: implementing fixes"
# ... agent works ...
gr status "Phase 3: running tests"
# ... agent works ...
gr status "Done - all tests passing"
```

The orchestrator or user monitors progress in the session picker (`ctrl+b w`), which shows status summaries for all sessions.
