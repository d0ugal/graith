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

To replay only the deterministic change-class fixtures, run:

```bash
go test ./internal/cipolicy -run TestDeterministicChangeClassFixtures
```

Policy changes already touch `internal/cipolicy`, so this coverage runs in the
existing Go test gate whenever the evaluator, manifest contracts, summary
renderer, or replacement-test bindings change.

## Deterministic Change Classes

`TestDeterministicChangeClassFixtures` builds local plans from checked-in path
sets and renders the CI shadow summary with the committed baseline inventory.
It covers these sample classes without elapsed-time gates, live GitHub history,
or a second repository:

| Class | Local evidence |
| --- | --- |
| Go-only | Go core paths select exactly the pull-request capability floor, keep the seven authoritative required modes, and do not select release or publication capabilities. |
| Docs-only | Docs-preview and docs-publication classes are detected, same-repository screenshot writes stay explicitly scoped, and supplied macOS skip results render as job-level skip text. |
| GUI-only | GUI/iOS routing is detected, GUI policy modes stay applicable for the plan tier, and supplied macOS detector results render the run decision. |
| Sandbox | Sandbox paths retain the nono and safehouse modes, while detector failures remain explicit and select the full fail-safe capability set. |
| Libghostty runtime | Native runtime paths detect Go and native capabilities and keep the native source-build, adapter, and gate modes tied to detected runtime evidence. |
| Generated metadata | Generated inputs escalate to the safe superset and keep regen prepare, mutation, and validation modes tied to the generated-input reason. |
| Release path | Release metadata escalates to release-shaped validation while pull-request plans still cannot bind publication credentials. |
| Workflow/script | Workflow-policy helper changes select the full safe superset and keep workflow-lint modes tied to detected policy-helper evidence. |
| Fork PR | Publication credentials, docs-preview writes, and regeneration mutation are denied under fork trust. |
| Same-repository mutation | Same-repository docs-preview writes are scoped, while regeneration still cannot borrow maintainer credentials from repository location. |

Each class also checks that the rendered shadow summary still includes the
shared committed inventory blocks for required contexts, workflow job rows,
helper surfaces, and core runtime coverage. Those shared rows prove summary
inventory rendering stays visible; the class rows above describe only
fixture-specific detection, routing, escalation, and credential evidence.

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
