---
weight: 36
title: CI workflow checks
description: Validate the retained CI workflow classifiers and local workflow checks
icon: shield-check
toc: true
---

# CI workflow checks

The repository no longer keeps a generated CI baseline inventory or CI policy
manifest. CI routing is intentionally smaller: workflows either use their
native GitHub filters or the direct changed-path classifier in `cmd/ciclassify`.

`cmd/ciclassify` reads newline-delimited repository paths, rejects blank,
absolute, traversal, or whitespace-padded rows, and emits the exact GitHub
output names expected by each migrated workflow gate.

## Run the checks

Run the retained workflow checks with the normal package tests:

```bash
go test ./internal/ciworkflow ./cmd/ciclassify
```

Try the classifier locally with a changed-file list:

```bash
git diff --name-only origin/main...HEAD |
  go run ./cmd/ciclassify -mode libghostty
```

Use `-json` for local diagnostics, or `-github-output "$GITHUB_OUTPUT"` when a
workflow wants the command to append outputs directly.

Pull-request workflows that use the classifier check out the PR base SHA before
running it. PR-modified policy code is treated as input to validate, not
authority for narrowing CI.

## Classifier consumers

Current classifier consumers and rollback boundaries:

| Workflow | Consumer | Outputs | Status | Fail-safe behavior |
|----------|----------|---------|--------|--------------------|
| `ci.yml` | macOS test and integration jobs | `macos` | migrated | file-list or classifier failure runs macOS jobs |
| `coverage.yml` | Swift coverage job and coverage comment | `gui` | migrated | file-list or classifier failure runs Swift coverage |
| `sandbox.yml` | macOS safehouse job | `macos` | migrated | file-list or classifier failure runs the macOS enforcement job |
| `libghostty-native.yml` | native runtime matrix and dependency-unit race/fuzz gates | `native`, `dependency-unit` | migrated | file-list, classifier, or detector job failure requires native and dependency-unit validation |
| `dev-release.yml` | dev release-shaped package validation | `release` | migrated | file-list or classifier failure runs dev release |
| `docs-preview.yml` | workflow trigger and Hugo build gate; page selection stays local to the workflow | `trigger`, `global`, `build` | migrated | file-list or classifier failure runs the Hugo build; local detector failure expands page selection globally |
| `goreleaser.yml` | stable release-shaped package validation | `release` | parity only | existing inline classifier remains; file-list failure runs stable release |

Stable release must not be migrated until the parity fixtures in
`internal/ciworkflow/testdata/workflow_classifiers.json` prove the shared rules
match the current inline stable-release classifier for representative release
paths. Keep that migration in a separate rollback boundary from non-release
gates.

CI workflow source changes conservatively select every migrated non-release
gate. Changes to `cmd/ciclassify` or `internal/ciworkflow` also select the
dev-release and docs-preview gates, because both workflows now rely on the
shared classifier.

## Retained Workflow Tests

`internal/ciworkflow` still owns local tests that parse workflow YAML and assert
the security properties that matter for current workflows:

- changed-path parity for migrated gates and stable-release fixtures;
- fail-safe behavior when file listing or classification fails;
- workflow trigger, timeout, concurrency, shell, and pinned-tool policies;
- docs-preview, renovate retry, libghostty native, and release workflow checks;
- credential boundary checks for synthetic token class, trust tier, scope, and
  filesystem target roots.

The tests are local compatibility checks. They do not claim to prove live
GitHub branch protection, check freshness, repository ruleset binding, or live
artifact provenance. Current mergeability still comes from repository-owned
workflows and GitHub required checks.

## Artifact boundary

Native and release artifact boundaries remain enforced by the current workflow
and libghostty-specific checks. `internal/ciworkflow` does not expose a supported
artifact manifest, run-plan, or producer-result contract.
