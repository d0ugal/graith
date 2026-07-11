---
weight: 1400
title: "Patterns and Recipes"
description: "Common patterns and recipes for daily use."
icon: "auto_awesome"
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

Scenarios are reproducible — the same TOML file always creates the same fleet. See [Scenarios](scenarios.md) for the full reference.

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

## Agent-to-agent communication patterns

### Publish/subscribe broadcast

One agent publishes findings; multiple agents react:

```bash
# Scanner agent
gr msg pub --topic vulnerabilities "SQL injection in user.go:89"

# Fixer agents (each subscribing)
gr msg sub --topic vulnerabilities --follow --ack
```

### Request/reply

Structured request with a designated reply channel:

```bash
# Requester
gr msg send worker-1 "analyze auth.go for race conditions" --reply-to analysis-results
gr msg sub --topic analysis-results --wait

# Worker
gr msg inbox --all --ack
# ... does analysis ...
gr msg pub --topic analysis-results "No race conditions found. Thread-safe."
```

### Hierarchical coordination

Parent orchestrates children:

```bash
# Parent creates workers and sends tasks
gr new worker-1 --repo ~/Code/api --background
gr new worker-2 --repo ~/Code/api --background
gr msg send worker-1 "fix the auth tests"
gr msg send worker-2 "fix the API tests"

# Workers report back
gr msg send --parent "auth tests fixed, all passing"

# Parent reads results
gr msg inbox --all --ack
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

## Multi-agent patterns

These patterns compose graith's primitives (sessions, messaging, store, forking, mirrored worktrees) into structured multi-agent workflows. They are generic -- adapt them to your codebase and agents.

### Devil's advocate

One agent proposes a solution. A second agent systematically attacks it with counterarguments, edge cases, and failure modes. Forces the proposer to defend or revise.

Use when: validating designs, stress-testing plans, surfacing hidden assumptions.

```bash
gr new proposer --repo ~/Code/api --prompt "design the new caching layer"
gr new devil --mirror proposer --background \
  --prompt "read the proposed design, find 10 counterarguments and edge cases, publish to /topic challenge"

# Proposer reads challenges
gr msg sub --topic challenge --wait --ack

# Proposer responds
gr msg send devil "addressed issues 1-7, disagree on 8 because ..."

# Devil reviews response
gr msg inbox --wait --ack
```

### Judge panel (tribunal)

Multiple independent agents review the same code or proposal simultaneously. No visibility into each other's findings. Results are collected and triaged by convergence.

Use when: high-stakes code review, security audit, architectural decisions. Convergent findings across judges are high-confidence. Findings from only one judge warrant investigation.

```bash
# Launch 3 independent reviewers sharing the same worktree
gr new judge-bugs --mirror feature-branch --background \
  --prompt "review for correctness and logic bugs, publish findings to /topic review-bugs"
gr new judge-security --mirror feature-branch --background \
  --prompt "review for security issues and input validation, publish to /topic review-security"
gr new judge-perf --mirror feature-branch --background \
  --prompt "review for performance and scalability, publish to /topic review-perf"

# Collect verdicts
gr msg sub --topic review-bugs --wait --ack
gr msg sub --topic review-security --wait --ack
gr msg sub --topic review-perf --wait --ack

# Store results
gr store put reviews/2026-06-17.md --file /tmp/synthesized-review.md
```

Using different models per judge (via `--agent cursor --model <model>`) maximizes perspective diversity. Each model has different blind spots.

### Red team / blue team

Adversarial pairing. Red team tries to break the system. Blue team defends and patches. Iterate until the red team runs dry.

Use when: security hardening, robustness testing, finding edge cases in error handling.

```bash
gr new red-team --mirror app --background \
  --prompt "attack the auth system, try SQL injection, token theft, session fixation, publish each finding to /topic red-findings"
gr new blue-team --repo ~/Code/api \
  --prompt "subscribe to /topic red-findings, fix each vulnerability as reported, confirm fix to /topic blue-fixes"

# Red team publishes findings
# Blue team patches and confirms
# Iterate until red team finds nothing new
```

### RALPH loop (Read, Analyze, Learn, Plan, Help)

An iterative refinement cycle. The agent reads its prior output and external feedback, analyzes what worked and what did not, learns from errors, plans the next iteration, and requests help when stuck. Each cycle stores structured output for the next.

Use when: complex tasks that benefit from iteration, when the first attempt is expected to be incomplete.

```bash
gr new ralph --repo ~/Code/api \
  --prompt "implement the migration, use RALPH: after each attempt, read test results, analyze failures, plan fixes, iterate"

# External feedback loop via messaging
gr msg send ralph "tests fail on postgres 14 due to enum handling, try the cast approach"

# Agent stores progress per cycle
gr store append progress/ralph.jsonl '{"cycle":1,"tests_passing":42,"tests_failing":3}'
gr store append progress/ralph.jsonl '{"cycle":2,"tests_passing":44,"tests_failing":1}'
```

### Assembly line (pipeline)

Sequential handoffs between specialized agents. Each completes a phase and signals the next. Output of one is input to another.

Use when: multi-phase work with clear handoff points (design, implement, test, document).

```bash
# Phase 1: architect designs
gr new architect --repo ~/Code/api --background \
  --prompt "design the new API endpoints, write design to gr store put design/api.md"

# Phase 2: implementer builds from design
gr new implementer --repo ~/Code/api --background \
  --prompt "wait for message, then implement the API from design/api.md"
gr msg send implementer "design is ready at design/api.md, begin implementation"

# Phase 3: tester validates
gr new tester --mirror implementer --background \
  --prompt "wait for message, then write tests for the new API"
gr msg send tester "implementation complete, begin testing"
```

### Consensus building

Multiple agents propose solutions independently, then a synthesizer agent finds common ground and produces a unified approach.

Use when: design decisions with multiple valid approaches, when you want diverse perspectives to converge.

```bash
# Three independent proposals
gr new proposal-1 --no-repo --background \
  --prompt "propose a caching strategy for the API, publish to /topic proposals"
gr new proposal-2 --no-repo --background \
  --prompt "propose a caching strategy for the API, publish to /topic proposals"
gr new proposal-3 --no-repo --background \
  --prompt "propose a caching strategy for the API, publish to /topic proposals"

# Wait for all three
gr msg sub --topic proposals --wait --ack
gr msg sub --topic proposals --wait --ack
gr msg sub --topic proposals --wait --ack

# Synthesize
gr new synthesizer --no-repo \
  --prompt "read all proposals from /topic proposals, find common ground, produce one unified solution"
```

### Swarm audit

Many agents scan different parts of a codebase in parallel. Each reports findings independently. Results are aggregated.

Use when: large codebases, security audits, dependency reviews, batch linting across modules.

```bash
for module in auth database cache api middleware; do
  gr new audit-$module --repo ~/Code/api --in-place --background \
    --prompt "security audit of the $module package, publish findings to /topic audit-results"
done

# Monitor progress
gr list

# Collect all findings
gr msg sub --topic audit-results --all --ack

# Clean up
gr delete --repo api --stopped -f
```

### Supervisor / worker hierarchy

A supervisor agent creates workers, assigns tasks, monitors progress, and handles failures. Workers report status and results upward.

Use when: dynamic task allocation, long-running operations that need coordination, fault-tolerant workflows.

```bash
# Supervisor manages the fleet
gr new supervisor --no-repo \
  --prompt "create workers for each failing test file, monitor progress, reassign on failure"

# From the supervisor session:
gr new worker-1 --repo ~/Code/api --background --prompt "fix tests in auth_test.go"
gr new worker-2 --repo ~/Code/api --background --prompt "fix tests in cache_test.go"
gr status "managing 2 workers"

# Workers report back
gr msg send --parent "auth_test.go: all 12 tests passing"

# Supervisor reads results
gr msg inbox --all --ack
gr msg send --children "rebase on main before pushing"
```

### Continuous reviewer

A monitoring agent watches a primary agent's work in real time via a mirrored worktree and provides ongoing feedback through messages.

Use when: long implementation tasks, mentoring, real-time quality gates.

```bash
gr new implementer --repo ~/Code/api --prompt "implement the user profile system"
gr new reviewer --mirror implementer --background \
  --prompt "continuously review changes as they appear, send feedback via messages"

# Reviewer spots an issue and sends feedback
gr msg send implementer "handler.go:45 -- missing error check on db.Query return"

# Implementer reads feedback inline
gr msg inbox --all --ack
```

### Composing patterns

These patterns compose freely. Common combinations:

**Explore-then-tribunal:** An explorer maps the problem space. Fork promising directions. Run a judge panel on each fork to evaluate quality.

**Red/blue with supervisor:** A supervisor spawns red and blue teams, monitors the adversarial cycle, and decides when to stop based on diminishing returns.

**Pipeline with consensus:** Each pipeline stage uses consensus building internally. Three architects propose designs; the best is passed to the implementer stage.

**Swarm with RALPH:** Each swarm agent runs its own RALPH loop internally. The supervisor collects final results after each agent has iterated to convergence.
