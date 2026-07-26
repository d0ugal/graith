---
weight: 37
title: CI policy replacement tests
description: Run the retained Go replacements for retired workflow-script policy tests
icon: shield-alert
toc: true
---

# CI policy replacement tests

The CI policy replacement tests are the retained Go coverage for retired
workflow-script policy tests. They run inside `internal/cipolicy` with no
repository secrets and do not perform external network access. The tests bind
the same checked-in [CI policy manifest]({{< relref "/docs/contributing/ci-policy.md" >}})
to repository workflow files, declared P11 compatibility samples, credential
boundaries, and local plan construction.

Run it with the normal package tests:

```bash
go test ./internal/cipolicy
```

Policy changes already touch `internal/cipolicy`, so this coverage runs in the
existing Go test gate whenever the evaluator, manifest contracts, summary
renderer, or replacement-test bindings change.

## What It Proves

The replacement tests fail closed when a deterministic fault tries to make the
policy look narrower or more trusted than the checked-in manifest allows:

- missing files, unknown paths, or unknown changed-file lists;
- compatibility samples that are not bound to content-addressed workflow
  files;
- manifest workflow traces that drift away from checked-in workflow content;
- changed-file samples that are not known to the local detector;
- manifest-level unsupported platform decisions that silently pass; and
- P11 semantic replacement files whose closed-world inventory identity no
  longer matches the accepted baseline.

The same-repository agent branch tier is treated as untrusted for credentials.
Synthetic tokens and filesystem roots prove that docs-preview writes,
regeneration pushes, and coverage/comment publication do not gain maintainer
credentials merely because the branch lives in `d0ugal/graith`. Credential
operations must also name a capability that is both allowed for that operation
and present in the evaluated plan.

## Local Scope

These tests are local compatibility checks. They do not claim to prove
check-creator source restriction, fork token behavior, merge queue or
`merge_group` triggering, check freshness, live artifact provenance, live cache
provenance, or repository ruleset binding. Current CI evidence comes from
repository-owned workflows plus the retained
[CI baseline]({{< relref "/docs/contributing/ci-baseline.md" >}}) and
[CI policy]({{< relref "/docs/contributing/ci-policy.md" >}}) checks.
