---
title: "Design Doc: Separate Scenario Startup Prompts"
authors: OpenAI Codex
created: 2026-07-17
status: Implemented
reviewers: Independent implementation review
informed: Graith maintainers and scenario users
issue: https://github.com/d0ugal/graith/issues/1413
---

# Separate Scenario Startup Prompts

Scenario members gain an optional `prompt` that supplies launch instructions
without creating tracked todo work. `task` remains the title of a seeded todo
and a durable completion contract, while task-only files keep launching with
their task for compatibility.

## Background

The canonical parser in `internal/scenariofile` maps each `[[sessions]]` entry
to `protocol.ScenarioSessionInput`. The daemon validates the complete roster,
starts every owned member, seeds one assigned todo from each non-empty `task`,
and persists member metadata plus per-member manifests. Required declared
results and assigned todos are the two durable member-completion contracts.

Today the daemon also passes `task` to `CreateOpts.Prompt`, so one string is
simultaneously launch instructions, a todo title, and a completion contract.
Todo titles are limited to 500 bytes, while useful launch instructions are
often multi-paragraph bodies.

## Problem

Result-producing members need instructions even when their only intended
completion contract is a required declared result. Supplying those instructions
as `task` creates a redundant todo, and realistic instructions can fail during
todo seeding after members have already started. Omitting `task` avoids the todo
but leaves no initial instructions. Runtime policies must also avoid treating
instructions alone as proof that durable tracked work exists.

## Goals

- Let a member receive startup instructions without seeding a todo.
- Preserve task-only launch, todo, and completion behaviour.
- Let `prompt` and `task` coexist with independent meanings.
- Keep runtime-policy contracts limited to tasks and required results.
- Validate prompt bodies and todo titles before any member starts.
- Persist enough information for status and regenerated manifests to retain the
  distinction after daemon restart.

### Non-Goals

- Changing ordinary `gr new` prompt handling.
- Adding prompt templates, files, or document-store references to scenario TOML.
- Changing todo completion, result publication, or dependency semantics.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | Scenario TOML and `gr scenario add` are the authoring surfaces. |
| iOS | Excluded | Native clients do not author or start scenarios. |
| macOS | Excluded | The native app does not author or start scenarios. |

## Proposals

### Proposal 0: Do Nothing

Keep using `task` for all three purposes. Result-only workers retain redundant
todos, rich instructions remain constrained by todo presentation limits, and a
late seed failure can still require full scenario rollback.

### Proposal 1: Add an Independent Prompt (Recommended)

Add optional `prompt` fields to the canonical TOML, start/add wire input,
durable member state, status, and self manifest. The effective launch prompt is
`prompt` when non-empty and otherwise `task`, preserving existing definitions.
Only a non-empty `task` seeds a todo or participates in todo dependencies.

The canonical parser and authoritative daemon both validate every explicit
prompt against a fixed 64 KiB body limit and reject NUL in either prompt or
task. The daemon separately validates each raw task against the effective
todo-title limit before reservation or process creation. To keep scenario
start/status frames bounded, the JSON-encoded effective prompts and tasks for a
roster may occupy at most 3 MiB, reserving 1 MiB of the 4 MiB control frame for
the envelope and other member metadata. The same aggregate check includes the
existing roster before `scenario add` starts a new member.

Runtime-policy validation continues to require a task or at least one required
result; prompt is deliberately absent from that check.

Storing the explicit prompt lets daemon restart and manifest republishing keep
the distinction. Detailed status and an owned member's self manifest expose
the effective startup prompt alongside the tracked task; list summaries omit
prompt bodies. Shared members expose no effective startup prompt because the
scenario never launches them. Old persisted owned members have no `prompt`;
their effective prompt therefore remains their task.

The main cost is a larger state/status payload for unusually long prompts. The
64 KiB per-member cap is far above normal instructions while remaining below
common process-argument limits; the separate 3 MiB aggregate encoded budget
keeps multi-member control messages below the 4 MiB frame ceiling.

### Proposal 2: Put Rich Instructions in Todo Notes

Keep `task` as a short title and add launch instructions as a todo note. This
still creates tracked todo work for result-only members and makes startup depend
on todo presentation data, so it does not separate the contracts.

## Other Notes

### References

- [Issue #1413](https://github.com/d0ugal/graith/issues/1413)
- `internal/scenariofile/scenariofile.go`
- `internal/daemon/scenario.go`
- `docs/design/2026-07-16-todo-list.md`
- `docs/design/2026-07-17-scenario-result-contracts.md`
- `docs/design/2026-07-17-scenario-runtime-policies.md`

### Implementation Notes

Both TOML parsing and daemon entry points validate the new shape. Blank raw
fields are size-checked and then canonicalized to empty, while NUL is rejected
before argv construction. `depends_on` continues to require tasks on both
sides. `gr scenario add` receives a matching `--prompt` flag; result
declarations remain file-only as before.

### Testing

Parser tests cover task-only compatibility, prompt-only required results, both
fields, unknown fields, policy rejection for prompt alone, and prompt/task size
limits, NUL rejection, and aggregate frame headroom. Daemon tests prove
preflight rejection occurs before state/process mutation, long prompts launch
intact without a todo, task-only members retain their todo, both fields stay
independent, shared members expose no launch prompt, and prompt-only result
publication completes the member. Protocol manifest and focused race tests
cover the wire and lifecycle changes.
