---
weight: 1430
title: "Multi-agent patterns"
description: "Structured multi-agent workflows: tribunals, red/blue, swarms, and more."
icon: "groups"
toc: true
draft: false
---

These patterns compose graith's primitives (sessions, messaging, store, forking, mirrored worktrees) into structured multi-agent workflows. They are generic -- adapt them to your codebase and agents.

## Devil's advocate

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

## Judge panel (tribunal)

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

## Red team / blue team

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

## RALPH loop (Read, Analyze, Learn, Plan, Help)

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

## Assembly line (pipeline)

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

## Consensus building

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

## Swarm audit

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

## Supervisor / worker hierarchy

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

## Continuous reviewer

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

## Composing patterns

These patterns compose freely. Common combinations:

**Explore-then-tribunal:** An explorer maps the problem space. Fork promising directions. Run a judge panel on each fork to evaluate quality.

**Red/blue with supervisor:** A supervisor spawns red and blue teams, monitors the adversarial cycle, and decides when to stop based on diminishing returns.

**Pipeline with consensus:** Each pipeline stage uses consensus building internally. Three architects propose designs; the best is passed to the implementer stage.

**Swarm with RALPH:** Each swarm agent runs its own RALPH loop internally. The supervisor collects final results after each agent has iterated to convergence.
