---
title: "Design Doc: CI Path Classifier"
authors: Codex
created: 2026-07-27
status: Implemented (v1)
reviewers: (none yet)
informed: graith-maintainers, release-owners, native-owners
issue: https://github.com/d0ugal/graith/issues/1761
---

# CI Path Classifier

This design introduces one Go classifier for changed-path decisions that gate expensive CI jobs. The first slice migrates non-release job-level gates, reuses the existing Go plan classifier for dev-release parity, and keeps stable-release workflow migration out of scope until parity remains proven through review.

## Background

Several workflows currently fetch pull-request files through the GitHub API and classify those paths in shell with inline `grep` rules. The important consumers are the macOS legs in `ci.yml`, Swift coverage in `coverage.yml`, macOS sandbox enforcement in `sandbox.yml`, native runtime validation in `libghostty-native.yml`, and release-shaped validation in `dev-release.yml` and `goreleaser.yml`.

The workflows avoid workflow-level path filters where required checks are involved. A skipped workflow can strand a required context, while a job skipped by `if:` still reports a conclusion that branch protection treats as satisfied. The existing job-level detectors also fail safe: if the PR file list cannot be fetched, they emit the expensive path as selected.

## Problem

The path rules have drifted. Release classifiers already differ from nearby native classifiers, and every new workflow policy change requires editing several independent shell regexes. That increases the chance that a required validation path is omitted, a release path is skipped, or a detector failure silently becomes a skipped required check.

## Goals

- Keep one authoritative path classifier in Go, matching the repository's current workflow-check helper direction.
- Preserve job-level skip behavior for required contexts.
- Run pull-request classification from trusted base code rather than trusting PR-modified classifier code.
- Prove parity with representative path fixtures before migrating each consumer.
- Keep stable-release workflow migration out of v1 until parity is reviewed.

### Non-Goals

- Replace the removed CI policy manifest or run-plan evaluator.
- Move stable-release publication decisions into the shared classifier in v1.
- Replace docs-preview page selection, which also depends on base/head file existence.
- Add iOS or macOS app UI surface.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | `cmd/ciclassify` is the workflow-facing Go command and can be run locally for parity checks. |
| iOS | Excluded | CI routing is repository automation, not app behavior. |
| macOS | Excluded | CI routing is repository automation, not app behavior. |

## Proposals

### Proposal 0: Do Nothing

Leaving the shell regexes in place keeps the workflows cheap and familiar, but every future policy update repeats the same risk. Release and native rules would keep drifting, and parity would remain implicit in reviewer memory rather than tested fixtures.

### Proposal 1: Shared Go classifier with staged workflow migration (Recommended)

Add `internal/ciworkflow` classifier rules plus a small `cmd/ciclassify` CLI. The CLI reads newline-delimited changed paths and emits either GitHub output key/value pairs or JSON diagnostics. Workflows still fetch the PR file list with `gh api`, but migrated PR detectors check out the base SHA, set up Go, and run the base classifier. If listing, checkout, setup, or classification fails, the workflow runs the gated validation rather than skipping it.

The first migration covers CI macOS, coverage GUI, sandbox macOS, libghostty native/dependency-unit outputs, and dev-release path decisions. Stable-release remains inline; its current rules are captured by parity fixtures so a later PR can migrate it with a small rollback boundary: revert only the stable-release workflow call-site change if release parity or event trust does not hold.

Trade-offs: the cheap detector jobs now need a base checkout and Go setup on pull requests. That is slower than `grep`, but far cheaper than accidental macOS or release drift, and failures are conservative.

### Proposal 2: Generate shell regexes from Go rules

This would keep detector jobs lighter, but it preserves shell as the runtime contract and creates generated workflow churn. It also makes invalid path handling and local diagnostics harder to reason about than a single Go command.

## Other Notes

### References

- Issue: https://github.com/d0ugal/graith/issues/1761
- CI workflow checks docs: `website/content/docs/contributing/ci-policy.md`
- Classifier command: `cmd/ciclassify`
- Rules and parity fixtures: `internal/ciworkflow/workflow_classifier.go`, `internal/ciworkflow/testdata/workflow_classifiers.json`

### Implementation Notes

The migrated workflows use the PR files API as before. They do not classify with PR-head code. The checkout step uses `github.event.pull_request.base.sha`, which keeps policy changes in the PR as inputs to be validated rather than authority for narrowing the run.

CI workflow source changes select every migrated non-release gate. Changes to `cmd/ciclassify` or `internal/ciworkflow` also select dev-release because that workflow now relies on the shared classifier. Stable-release classifier output remains a parity diagnostic until a later release migration proves and adopts the broader behavior deliberately.

### Alternatives considered

Embedding workflow gate outputs in the removed `cmd/cipolicy plan` command was rejected because run-plan replay and workflow job gating have different contracts. The run-plan layer also depended on a generated manifest that current CI does not need. The workflow classifier emits the exact output names expected by existing jobs without preserving that extra policy source.

### Testing

The v1 proof is fixture-based parity over representative paths, CLI contract coverage, invalid changed-file fail-safe tests, and workflow policy tests that assert migrated workflows call `cmd/ciclassify` while keeping detector-failure conditions conservative.
