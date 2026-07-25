---
title: "Design Doc: CI North Star Implementation Plan"
authors: graith maintainers
created: 2026-07-25
status: Draft
reviewers: Claude/Opus (completed; requested additional judges unavailable)
informed: maintainers, release owners, GUI owners, security owners
---

# CI North Star Implementation Plan

This plan turns the [CI North Star](2026-07-24-ci-north-star.md) into
independently reviewable implementation issues. It sequences evidence,
contracts, policy, execution, and cutover so no migration step weakens the
fail-closed target or interferes with native/libghostty repair work.

## Background

The north star defines a capability-driven DAG, typed run-plan and result
contracts, immutable artifacts, trusted publication, and explicit operational
evidence. The repository already contains useful proofs—fail-safe changed-file
detectors, pinned native artifacts, generated-file checks, sandbox probes,
release-shaped consumers, and macOS/GUI validation—but these are distributed
across workflow concerns. The plan consolidates their semantics before moving
required checks.

## Problem

A migration can make CI look simpler while introducing a skipped mode, a
trusted job consuming an untrusted cache, a PR-controlled detector deciding its
own scope, or a release artifact that was never independently consumed. Work
must therefore produce machine-checkable evidence at each boundary and retain
the old required proof until equivalence is demonstrated.

## Goals

- Create one typed, locally replayable policy/result implementation with thin
  runner adapters.
- Enumerate every supported capability, mode, coordinate, platform, owner, and
  event in a versioned policy manifest.
- Prove current behavior before changing gates; preserve fail-safe detectors,
  native producer/consumer validation, generated-file trust separation, and
  platform-specific execution.
- Provide deterministic dual-run comparison, safe cutover, and rollback.
- Make each work package small enough to become one focused implementation
  issue with a falsifiable acceptance test.

### Non-Goals

- Rewriting workflows, production code, release assets, or repository settings
  in this design task.
- Porting every ecosystem-mandated JavaScript helper immediately.
- Changing the supported platform matrix without a separate design decision.
- Making coverage, GUI, native, or security checks required merely because the
  plan names them; requiredness changes only through evidence-backed policy.

## Platform support

| Surface | Plan obligation |
|---------|-----------------|
| CLI/server | Go policy tool, Linux build/test/race/integration, package consumer, protocol and generated-file contracts. |
| macOS GUI | SwiftPM/macOS build and tests, Xcode/toolchain recording, native adapter consumer, and protected signing-shaped checks. |
| iOS GUI | iOS cross-compilation and simulator proof; device signing/submission remains release-only. |
| Linux | Go, sandbox enforcement, native artifact producer/consumer, security, reproducible package and release verification. |
| macOS runners | Swift, simulator, macOS runtime behavior, Darwin native adapter, archive and signing verification. |
| Release platforms | Closed-world artifact coordinates, checksums, SBOM/provenance, install/smoke, independent verification, and protected publication. |

## Proposals

### Proposal 0: Do Nothing

Continue adding isolated workflow fixes. This avoids migration work but leaves
the current proof surface difficult to enumerate and makes equivalence,
required-check changes, and artifact trust hard to demonstrate. Rejected.

### Proposal 1: Contract-first capability migration (Recommended)

Implement the work packages below in dependency order. Every package has a
local replay path, a fixture or contract test, an owner, and a rollback point.
No package changes required branch protection until the cutover package is
accepted.

### Work packages

| ID | Deliverable | Depends on | Exit evidence |
|----|-------------|------------|---------------|
| P0 | Baseline and inventory | None | Four weeks of queue, duration, retry, flake, cost, cache, artifact, and actual-mode coverage data; capability-to-current-proof matrix reviewed by owners; all current workflows, events, permissions, JS policy helpers, and YAML-regex trust tests enumerated. |
| P1 | Policy schema and capability manifest | P0 | Schema validation rejects unknown modes, duplicate coordinates, missing owners, unsupported silent passes, and ambiguous requiredness; deterministic manifest digest. |
| P2 | Go policy/result library | P1 | Local plan replay from event + file list; stable JSON results; policy tests cover fork/base trust, expiry, cancellation, stale output, and zero-job plans. |
| P3 | Hermetic fixture and fault injector | P2 | Fixture fails closed for missing files, polluted environment, stale/corrupt cache/artifact, archive differences, cancellation, misleading names, and unsupported platforms. |
| P4 | Trusted evaluator and synthetic gate adapter | P2/P3 | Fork plan/gate uses trusted base or pinned evaluator; the gate is enforced from a trusted default-branch ruleset, safe wrapper, or merge queue; a PR cannot rewrite it to unconditional success; exactly one always-reporting gate blocks absent/zero-job fan-in; policy-change canary passes. |
| P5 | Artifact and cache contracts | P1/P2 | Manifest, digest, provenance, trust tier, and consumer verifier; untrusted cache cannot be consumed by trusted jobs; native and release-shaped artifacts have independent verification. |
| P6 | Fast PR lane dual-run | P3/P4/P5 | Representative Go, protocol, docs, generated, GUI, native, sandbox, workflow, and dependency changes have matching mode sets and classified outcomes for two weeks. |
| P7 | Main/deep/scheduled lanes | P6 | Main complete bundle, race/integration, full platform, coverage, security, fuzz/soak, and dependency freshness modes have stable fan-in and dashboards. |
| P8 | Dependency and generated-file promotion | P5/P6 | Dependency updates use the same policy; unprivileged regeneration remains separate from credentialed push; stale generated outputs fail for forks and trusted branches. |
| P9 | Release candidate verification | P5/P7 | Reproducible Linux/Darwin candidate, package consumer, checksum/SBOM/provenance, signature, independent verification, rollback dry run, and protected publication rehearsal. |
| P10 | Required-check cutover and deletion | P6/P7/P8/P9 | Branch protection requires only the tamper-resistant always-reporting synthetic gate; its fork rewrite resistance is demonstrated before old contexts are removed; old checks remain observable through the evidence window, then are removed in a reversible settings change. |
| P11 | JS policy surface retirement | P0/P2/P3 | Repo-owned JS helpers have owners and grandfathered contracts; each is wrapped, ported to Go, or explicitly retained with deletion criteria; YAML-regex trust tests are replaced by semantic contract tests before workflow reshaping. |

#### P0 — Baseline and inventory

Record the current workflow events, permissions, runner labels, toolchain
versions, action pins, cache scopes, artifact names/digests, static required
contexts, skip behavior, and all current capability proofs. For each capability,
record whether proof is source-level, package-consumer, compile-only, runtime,
scheduled, soft, or required. Measure queue and execution separately. Do not
change workflow behavior while collecting this baseline.

The inventory is closed-world: enumerate the approximately 18 workflows
(`ci`, `coverage`, `gui-ci`, `libghostty-native`,
`libghostty-native-publish`, `regen`, `docs-preview`, `dev-release`,
`release-please`, `goreleaser`, `sandbox`, `dependency-review`, `codeql`,
`scorecard`, `secret-scan`, `gitleaks`, `workflow-lint`, and `commits`) plus
their event and permission shapes. Include push/PR dual-triggered development
release, scheduled docs-preview cleanup, and main-only native publication with
`contents:write`. Inventory `docs-preview.js`, `regen-auth.test.js`,
`libghostty-policy.test.js`, `renovate-retry.test.js`,
`shellcheck-policy.test.js`, `workflow-lint-supply-chain.test.js`, and the
other repo-owned scripts under `.github/workflows/scripts/`; record the
YAML-regex trust assertions as contracts to replace semantically. The GUI
coverage detector failure path and the current exit-zero “delta not measurable”
path are explicit defects: represent them as `unknown`/deferred with expiry and
fail the detector closed before migration.

Acceptance requires owner sign-off on the matrix and a sample of merged
changes replayed against the observed mode set. Any unexplained mode is an
inventory failure, not an assumption to resolve during cutover.

#### P1–P3 — Contracts, implementation, and fixture

The policy manifest is data; evaluation, plan expansion, result validation, and
fan-in are Go code. The schema includes policy digest, source/event identity,
trust tier, capability, mode, platform, cost class, requiredness, owner,
unsupported rationale, expiry, and evidence references. The result schema
includes attempts, status, failure class, timestamps, artifact/cache digests,
and supersession identity.

The fixture runs without secrets or external network. Fault injection must be
deterministic and run on every policy change. Generated workflow data is
compared with the same manifest consumed by the evaluator so fixture drift
cannot hide live-graph drift.

The fixture includes a third trust tier for same-repository agent-authored
branches. It proves that docs-preview writes, regeneration pushes, and
coverage/comment publication cannot obtain maintainer credentials merely from
their repository location. It also replaces raw-YAML regex tests with semantic
assertions over permissions, event trust, safe checkout, and publication
boundaries.

#### P4–P5 — Trust and proof boundaries

The trusted evaluator is built from the base ref or a pinned released tool on
fork PRs. PR policy changes are data to the old evaluator and are exercised in
the fixture before promotion. A checksum-bound untrusted plan is not treated
as authentic; trusted main/release plans are signed by protected credentials.
P4 must additionally demonstrate, on a fork fixture, that changing the
PR-head workflow gate to unconditional success still cannot satisfy the
default-branch required check. The proof must use a trusted ruleset/workflow
wrapper or merge-queue re-verification; a trusted binary inside PR-controlled
YAML is insufficient. P10 is blocked until this evidence is retained in the
dual-run bundle.

Artifacts are addressed by digest, verified before extraction, and rejected for
extra/missing members. Cache keys include source/dependency/toolchain/platform
identity, but trust tier is an independent boundary: untrusted writes cannot
enter trusted reads. Native/libghostty producers, release builders, and package
consumers adopt the same manifest and provenance contract without changing
their supported coordinates.

#### P6–P8 — Dual-run lanes

Run the new planner and gate in shadow mode beside current required checks. The
shadow result must be visible but cannot publish, mutate branches, or override
existing gates. Compare expected coordinates, status, evidence, duration,
queue, retries, and classifications. Include changes that affect only docs,
generated files, Go runtime branches, protocol, GUI/iOS, native dependencies,
sandbox backends, release metadata, workflow policy, and fork trust.

Promote only after two weeks with zero unexplained mode disagreement, zero
artifact identity mismatch, no fixture escape, and capability-specific latency
within provisional budgets. Main promotion expands deferred PR modes according
to policy; it does not infer that a skipped PR mode passed.

#### P11 — JS policy surface retirement

Treat the roughly 2,100 lines under `.github/workflows/scripts/` as a named
repo-owned policy surface, not an incidental dependency. Assign an owner and
grandfather each helper with an executable contract. Port or wrap one helper at
a time, compare its output with the Go policy/result library, and delete it
only after a documented compatibility window and zero unexplained disagreement.
The `pngjs` dependency remains an explicit exception where image decoding is
ecosystem-specific. Before YAML reshaping, convert regex-based trust tests such
as `regen-auth.test.js` into semantic fixture tests so a formatting change
cannot silently erase a security assertion.

#### P9–P10 — Release and cutover

Rehearse release publication with non-publishing credentials and an independent
consumer. Verify the candidate from a clean checkout, then test rollback to the
last known-good artifact set. Cut over protected publication only after two
successful candidates and a security/release-owner audit.

Change required checks in this order: deploy the tamper-resistant
always-reporting gate, verify it blocks a missing/zero-job result and a
PR-rewritten unconditional-success gate, require it alongside old contexts,
observe one full evidence window, then remove obsolete contexts in a separate
reversible settings change. Retain historical result bundles and a documented
rollback; do not delete old contexts while the trust-root proof is open.

## Acceptance and rollback

The migration is accepted only when all work-package exit evidence is linked
from an immutable result bundle. A failure is classified as product, policy,
runner, queue, toolchain, dependency, security, or flake; retries preserve the
first result. A disagreement, missing evidence, stale policy, or unexpected
unsupported row blocks cutover.

Rollback disables only the new shadow/gate path and restores the previous
required proof. It does not delete artifacts, alter source, loosen permissions,
or bypass the native/libghostty repair. If the old path is unavailable, the
release remains blocked rather than falling through to an unverified publish.

## Risks and mitigations

| Risk | Mitigation |
|------|------------|
| Policy tool becomes a false-red single point of failure | Small pinned bootstrap, local replay, fixture tests, owner rota, explicit diagnostics, and no direct publication authority. |
| Existing JS adapters drift during Go migration | Grandfather with contract tests; port one adapter at a time and compare outputs before deletion. |
| Dynamic checks interact badly with branch protection | One tamper-resistant synthetic gate; zero-job, absent-report, and PR-rewrite tests; staged settings cutover. |
| macOS queue/cost misses PR SLO | Capability-specific budgets, bounded matrix, shadow measurement, and main/scheduled deferral recorded as policy. |
| Native artifact substitution or stale headers | Digest/provenance manifests and independent producer/consumer verification. |
| Fork or same-repository agent code reaches secrets or push credentials | Explicit fork/agent/trusted tiers, trusted-base evaluator, no secrets in untrusted jobs, unprivileged generation, protected publication environment. |
| Current and new graphs disagree during migration | Immutable coordinate comparison and old gate remains authoritative until evidence window closes. |

## Consensus

The requested second all-model tribunal was attempted after drafting this plan.
The mandated Cursor catalog helper could not provide a live model list, so
unverified Cursor judges were not substituted. Direct Claude and Codex judges
were launched in read-only mirrors; the completed Claude review identified and
the plan now addresses the PR-head gate trust-root hole, pre/post-retry SLO
denominators, zero-incident false-green target, provisional cost funding,
coverage fail-closed defects, the same-repository agent trust tier, workflow/JS
inventory, and semantic replacement of YAML-regex tests. The incomplete judge
attempts and cleanup are recorded in shared tribunal history. The plan retains
trusted-base evaluation, one tamper-resistant gate, trust-tiered caches,
immutable artifact contracts, explicit compile-only/runtime distinctions, and
reversible required-check cutover.

## Testing

Each package adds unit/contract tests before workflow wiring. The fixture is the
minimum end-to-end test. Targeted tests cover policy edits, archive portability,
trust tiers, cancellation, stale outputs, retries, generated files, and release
rollback. Weekly scheduled fault injection and release dry runs provide drift
evidence; broad product suites remain owned by the eventual CI lanes.

## Open questions

- Which protected attestation/signing service and artifact retention satisfy the
  release-owner and security requirements?
- What current baseline determines final latency and cost budgets for each
  capability class?
- Which existing static checks can be represented by the synthetic gate without
  losing diagnostics or making branch protection opaque?
- What policy versioning and compatibility window is required for in-flight PRs
  when the base branch changes?
