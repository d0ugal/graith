---
weight: 37
title: CI workflow replacement tests
description: Run the retained Go replacements for retired workflow-script checks
icon: shield-alert
toc: true
---

# CI workflow replacement tests

The CI workflow replacement tests are the retained Go coverage for retired
workflow-script checks. They run inside `internal/ciworkflow` with no
repository secrets and do not perform external network access.

Run them with the normal package tests:

```bash
go test ./internal/ciworkflow
```

To replay only the shared classifier parity fixtures, run:

```bash
go test ./internal/ciworkflow -run TestWorkflowClassifierParityFixtures
```

Workflow-check changes already touch `internal/ciworkflow`, so this coverage
runs in the existing Go test gate whenever the classifier or workflow
assertions change.

## What It Proves

The replacement tests fail closed when local workflow behavior drifts away from
the retained checks:

- migrated workflow gates keep their expected changed-path outputs;
- detector scripts run the expensive jobs when file listing or classification
  fails;
- workflow triggers, concurrency groups, timeouts, and pinned tool installs keep
  the expected boundaries;
- docs-preview, renovate retry, libghostty native, dev-release, and stable
  release workflows keep their local invariants; and
- synthetic credential operations cannot gain broader token classes, scopes, or
  filesystem roots than their operation allows.

## Local Scope

These tests are local compatibility checks. They do not claim to prove
check-creator source restriction, fork token behavior, merge queue or
`merge_group` triggering, check freshness, live artifact provenance, live cache
provenance, or repository ruleset binding. Current CI evidence comes from
repository-owned workflows plus the retained
[CI workflow checks]({{< relref "/docs/contributing/ci-policy.md" >}}).
