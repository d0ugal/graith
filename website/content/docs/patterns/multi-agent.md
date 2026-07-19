---
weight: 1430
title: "Multi-agent patterns"
description: "Structured multi-agent workflows: tribunals, red/blue, swarms, and more."
icon: "groups"
toc: true
draft: false
---

These compose graith's primitives -- sessions, messaging, store, forking, mirrored worktrees -- into structured workflows; adapt to your codebase and agents.

## Devil's advocate

One agent proposes; a second attacks it, forcing a defense or revision.

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

## Judge panel (tribunal)

Independent agents review the same target at once, blind to each other; results are triaged by convergence.

Use when: high-stakes code review, security audit, architectural decisions. Findings converging across judges are high-confidence; lone ones warrant investigation.

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

A different model per judge (via `--agent cursor --model <model>`) maximizes diversity -- models have different blind spots.

## Red team / blue team

Adversarial pairing: red attacks, blue patches, iterating until red runs dry.

Use when: security hardening, robustness testing, error-handling edge cases.

```bash
gr new red-team --mirror app --background \
  --prompt "attack the auth system, try SQL injection, token theft, session fixation, publish each finding to /topic red-findings"
gr new blue-team --repo ~/Code/api \
  --prompt "subscribe to /topic red-findings, fix each vulnerability as reported, confirm fix to /topic blue-fixes"

# Red team publishes findings
# Blue team patches and confirms
# Iterate until red team finds nothing new
```

## RALPH loop (Read, Analyze, Learn, Plan, Help)

An iterative refinement cycle; each cycle stores structured output for the next.

Use when: complex tasks benefiting from iteration, or when the first attempt is likely incomplete.

```bash
gr new ralph --repo ~/Code/api \
  --prompt "implement the migration, use RALPH: after each attempt, read test results, analyze failures, plan fixes, iterate"

# External feedback loop via messaging
gr msg send ralph "tests fail on postgres 14 due to enum handling, try the cast approach"

# Agent stores progress per cycle
gr store append progress/ralph.jsonl '{"cycle":1,"tests_passing":42,"tests_failing":3}'
gr store append progress/ralph.jsonl '{"cycle":2,"tests_passing":44,"tests_failing":1}'
```

## Assembly line (pipeline)

Sequential handoffs between specialized agents -- one's output is the next's input.

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

## Consensus building

Agents propose independently; a synthesizer finds common ground and produces a unified approach.

Use when: design decisions with multiple valid approaches, or to converge diverse perspectives.

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

## Swarm audit

Many agents scan a codebase in parallel, reporting findings independently for aggregation.

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

## Supervisor / worker hierarchy

A supervisor spawns workers, assigns tasks, monitors progress, and handles failures; workers report upward.

Use when: dynamic task allocation, coordinated long-running operations, fault-tolerant workflows.

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

## Continuous reviewer

A monitor watches a primary agent live via a mirrored worktree, sending feedback through messages.

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

## Composing patterns

These compose freely. Common combinations:

**Explore-then-tribunal:** an explorer maps the space, you fork promising directions, a judge panel evaluates each.

**Red/blue with supervisor:** a supervisor spawns both teams, monitors the cycle, and stops on diminishing returns.

**Pipeline with consensus:** each stage uses consensus internally -- three architects propose designs, the best passes to the implementer.

**Swarm with RALPH:** each swarm agent runs its own RALPH loop; the supervisor collects results once all have converged.
