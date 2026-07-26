---
weight: 37
title: CI policy fixture
description: Run the hermetic CI policy fault-injection fixture
icon: shield-alert
toc: true
---

# CI policy fixture

The CI policy fixture is the local P3 contract test for the
[CI policy manifest]({{< relref "/docs/contributing/ci-policy.md" >}}). It
runs inside `internal/cipolicy` with no repository secrets; the fixture tests
do not perform external network access. It wraps the same manifest, plan,
result, and fan-in APIs used by the policy evaluator. Workflow trace files are
supplied as content-addressed
fixture inputs and checked against the manifest consumed by the evaluator, so
generated workflow data and live evaluation cannot drift behind different
policy inputs.

Run it with the normal package tests:

```bash
go test ./internal/cipolicy
```

Policy changes already touch `internal/cipolicy`, so this fixture runs in the
existing Go test gate whenever the evaluator, manifest contracts, or fixture
logic changes.

## What It Proves

The fixture fails closed when a deterministic fault tries to make the policy
look green without valid evidence:

- missing files, unknown paths, or unknown changed-file lists;
- polluted `PATH`, locale, timezone, compiler variables, or credential
  environment variables;
- stale, corrupt, mismatched, self-mislabelled, writer-proof-mismatched, or
  cross-trust cache entries;
- corrupt, substituted, stale, cross-commit, or partially uploaded artifacts;
- verified cache and artifact digests not bound to the accepted result row;
- Linux/macOS archive member order, mode-bit, line-ending, symlink, or digest
  differences;
- cancelled, timed-out, superseded, missing, duplicate, or partially uploaded
  result coordinates;
- misleading generated or displayed job names;
- generated workflow data not bound to the same plan and policy digest;
- manifest workflow traces not bound to content-addressed fixture files;
- gates that omit a requested mode; and
- unsupported platforms reported as passed.

The same-repository agent branch tier is treated as untrusted for credentials.
Synthetic tokens and filesystem roots prove that docs-preview writes,
regeneration pushes, and coverage/comment publication do not gain maintainer
credentials merely because the branch lives in `d0ugal/graith`. Credential
operations must also name a capability that is both allowed for that operation
and present in the evaluated plan.

## Local Scope

This fixture is local emulation. Cache writer proof is a deterministic binding
to the evaluated plan and job coordinate, not a live GitHub identity proof. The
fixture does not claim to prove check-creator source restriction, fork token
behavior, merge queue or `merge_group` triggering, check freshness, live
artifact provenance, live cache provenance, or repository ruleset binding.
Current CI evidence comes from repository-owned workflows plus the retained
[CI baseline]({{< relref "/docs/contributing/ci-baseline.md" >}}) and
[CI policy]({{< relref "/docs/contributing/ci-policy.md" >}}) fixtures.
