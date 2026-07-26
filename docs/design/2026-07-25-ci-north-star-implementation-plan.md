---
title: "Design Doc: CI North Star Implementation Plan"
authors: graith maintainers
created: 2026-07-25
status: Draft (revised after post-merge tribunal)
reviewers: independent post-merge tribunal
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
| P0 | Baseline and inventory | None | Bounded collector and replay path are operational; retained complete fixed-window evidence covers representative current activity and the enumerated workflow/mode inventory; representative merged changes replay successfully; capability-to-current-proof matrix is closed-world and owner-reviewed; unexplained modes or incomplete evidence fail the gate. |
| P1 | Policy schema and capability manifest | P0 | Schema validation rejects unknown modes, duplicate coordinates, missing owners, unsupported silent passes, and ambiguous requiredness; deterministic manifest digest. |
| P2 | Go policy/result library | P1 | Local plan replay from event + file list; stable JSON results; policy tests cover fork/base trust, expiry, cancellation, stale output, and zero-job plans. |
| P3 | Hermetic fixture and fault injector | P2 | Fixture fails closed for missing files, polluted environment, stale/corrupt cache/artifact, archive differences, cancellation, misleading names, and unsupported platforms. |
| P4 | `graith-ci-gate` GitHub App evaluator | P2/P3 | App is installed from a digest-pinned release with metadata/contents/actions/pull-request read and checks write; the default-branch ruleset binds the required check to its App ID with no bypass actors; live fork/agent/merge-group fixture proves rewrite, replay, stale, missing, and zero-job failures. |
| P5 | Artifact and cache contracts | P1/P2 | Manifest, digest, provenance, trust tier, and consumer verifier; untrusted cache cannot be consumed by trusted jobs; native and release-shaped artifacts have independent verification. |
| P6 | Fast PR lane dual-run | P3/P4/P5 | Owner-approved bounded sample matrix covers representative Go, protocol, docs, generated, GUI, native, sandbox, workflow, and dependency changes across trusted/untrusted event shapes, required modes, retry/cancellation/failure classes, and the matching mode-set, latency, and classification criteria, with zero unexplained disagreement. |
| P7 | Main/deep/scheduled lanes | P6 | Main complete bundle, race/integration, full platform, coverage, security, fuzz/soak, and dependency freshness modes have stable fan-in and dashboards. |
| P8 | Dependency and generated-file promotion | P5/P6 | Dependency updates use the same policy; credentialed regeneration/docs publication moves behind a trusted-base workflow or protected environment that PR YAML cannot edit; stale outputs fail for forks and trusted branches; live same-repository-agent fixture passes before cutover. |
| P9 | Release candidate verification | P5/P7 | Reproducible Linux/Darwin candidate, package consumer, checksum/SBOM/provenance, signature, independent verification, rollback dry run, and protected publication rehearsal. |
| P10 | Required-check cutover and deletion | P6/P7/P8/P9 | Branch protection requires only the App-owned `graith-ci-gate`; old contexts remain observational until an owner-approved bounded sample of real merged changes/events covers the required change/event/mode matrix with no unexplained disagreement, false-green escape, stale/missing proof, or artifact identity mismatch, then obsolete required contexts and workflows are removed in separate reversible changes. |
| P11 | JS policy surface retirement | P0/P2/P3 | Repo-owned JS helpers have owners and grandfathered contracts; each is wrapped, ported to Go, or explicitly retained with deletion criteria; YAML-regex trust tests are replaced by semantic contract tests before workflow reshaping. |

#### P0 — Baseline and inventory

Record the current workflow events, permissions, runner labels, toolchain
versions, action pins, cache scopes, artifact names/digests, static required
contexts, skip behavior, and all current capability proofs. For each capability,
record whether proof is source-level, package-consumer, compile-only, runtime,
scheduled, soft, or required. Measure queue and execution separately. Do not
change workflow behavior while collecting this baseline.

The inventory is closed-world: enumerate all 18 workflows
(`ci`, `coverage`, `gui-ci`, `libghostty-native`,
`libghostty-native-publish`, `regen`, `docs-preview`, `dev-release`,
`release-please`, `goreleaser`, `sandbox`, `dependency-review`, `codeql`,
`scorecard`, `secret-scan`, `workflow-lint`, `docs`, and `commits`) plus
their event and permission shapes. Include push/PR dual-triggered development
release, scheduled docs-preview cleanup, and main-only native publication with
`contents:write`. Inventory `docs-preview.js`, `regen-auth.test.js`,
`libghostty-policy.test.js`, `renovate-retry.test.js`,
`shellcheck-policy.test.js`, `workflow-lint-supply-chain.test.js`, and the
other repo-owned scripts under `.github/workflows/scripts/`; record the
YAML-regex trust assertions as contracts to replace semantically. Gitleaks is a
secret-scan job (not a separate workflow), while `docs.yml` is a main-only
Pages build/deploy workflow and must be inventoried separately from PR
`docs-preview.yml`. The GUI coverage detector failure path and current
exit-zero “delta not measurable” path are explicit defects: PR coverage is
informational and missing/stale evidence is `unknown`; main coverage is
required and must have signed evidence for the exact merge SHA, source/tree,
toolchain, profile, totals, policy revision, and producer run, no older than
24 hours. Main fan-in fails closed if it is absent, malformed, superseded, or
mismatched; it never falls back to the last report.

P0 also inventories root `scripts/**` shell policy with owners and contracts,
and produces an old-to-new relation: every legacy proof obligation (job leg or
check coordinate, including skip conditions) maps to one or more new mode
coordinates or an owned retirement row; every new required mode traces to a
legacy obligation or an explicitly approved new obligation. The validator
rejects missing, duplicate, orphaned, or unjustified mappings before dual-run
and again before cutover. Actual skipped jobs are recorded separately from
successful jobs because GitHub treats a conditionally skipped job as success.

Acceptance requires the bounded collector and replay path to be operational,
retained complete fixed-window evidence covering representative current
activity and the enumerated workflow/mode inventory, owner sign-off on the
closed-world matrix, and a representative sample of merged changes replayed
against the observed mode set. P1 may begin as soon as those evidence-quality
conditions are satisfied; it does not wait for a calendar minimum. Ongoing
baseline collection continues in parallel with P1 and later packages and
calibrates final latency and cost targets before dual-run enforcement or
cutover. Until calibration is retained, the provisional 20/35/45/90-minute
targets and the dual-run 2x-plus-20% budget are binding ceilings, not goals
that may be exceeded. Any unexplained mode or incomplete evidence is an
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
branches. Local tests use synthetic tokens and filesystem boundaries to prove
that docs-preview writes, regeneration pushes, and coverage/comment publication
cannot obtain maintainer credentials merely from repository location. A
separate disposable live GitHub fixture proves App source restriction, fork and
agent permissions, `merge_group` triggering (which requires enabling the merge
queue and adding the event trigger), check freshness, and artifact/run
provenance; local emulation cannot satisfy those claims. It also replaces raw
YAML regex tests with semantic assertions over permissions, event trust, safe
checkout, and publication boundaries.

#### P4–P5 — Trust and proof boundaries

The trusted evaluator is a repository-independent `graith-ci-gate` GitHub App,
deployed from a reviewed digest-pinned release with metadata/contents/actions/
pull-request read and checks write (statuses write only if used). It reads trusted-base policy and GitHub run
metadata, then publishes the sole required check. The default-branch ruleset
requires this check from the App specifically; rulesets enforce the result but
are not the trust root. The App records event delivery ID, intended SHA, base
SHA, trusted workflow blob SHA, policy/evaluator digests, producer run/attempt,
workflow identity, and artifact digest, and rejects any mismatch or replay.
P4 must install it and pass the live fixture proving that changing PR YAML to
emit success, omitting evidence, changing the head SHA, or replaying an old
bundle cannot satisfy the check. P10 is blocked until this evidence is retained
in the dual-run bundle; until then current required checks remain authoritative.

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

Promote only after an owner-approved bounded representative sample matrix is
complete with zero unexplained mode disagreement, zero artifact identity
mismatch, no fixture escape, and capability-specific latency within provisional
budgets. The matrix covers every named change class, trusted and untrusted
event shape, required mode, retry/cancellation/failure class, matching mode-set
criterion, latency criterion, and classification criterion; every required
cell has explicit retained samples or an owner-approved retirement row. Full
dual-run comparison is capped at 2x the matched baseline runner minutes plus
20% of that baseline as queue overhead for the bounded matrix. Exhausting that
budget before the sample matrix is complete aborts or resizes the shadow
migration; it never promotes by elapsed time. After the App-owned gate becomes
the required decision, old workflows may run observationally only until the P10
real-change/event sample passes; they are not required and cannot affect the
App gate. Sampling is allowed only when it fills explicit matrix cells, and a
missing required cell fails the observation gate. Abort shadow migration and
restore the old gate if the bounded sample exceeds the migration budget, p95
required latency for the completed matrix exceeds its target by 25%, any
trust/provenance mismatch occurs, or any false-green fixture escapes. Main
promotion expands deferred PR modes according to policy; it does not infer
that a skipped PR mode passed.

Operational reliability reports use a fixed rolling 28-day UTC window and separate
attempt and change denominators. Attempt failure is failed attempts divided by
all attempts; change failure is changes with an eventual product failure
divided by changes with a terminal run. Retries count in the attempt measure
and preserve the first failure. Cancelled and superseded runs are separate
outcomes, not omitted observations. Policy revisions are pinned per run; a
base-branch policy change starts a new evaluation epoch and cannot reuse an
older plan or evidence bundle.

#### P11 — JS policy surface retirement

Treat the roughly 2,100 lines under `.github/workflows/scripts/` as a named
repo-owned policy surface, not an incidental dependency. Assign an owner and
grandfather each helper with an executable contract. Port or wrap one helper at
a time, compare its output with the Go policy/result library across a
documented compatibility sample matrix, and delete it only after that matrix
has zero unexplained disagreement.
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
PR-rewritten unconditional-success gate, make the App-owned gate the required
decision while old contexts run observationally, complete the owner-approved
bounded observation sample of real merged changes/events, then remove obsolete
contexts in a separate reversible settings change. Retain historical result
bundles and a documented rollback; do not delete old contexts while the
trust-root proof or observation matrix is open.

## Consensus

The exact-head reviews identified and this revision fixes the inventory,
coverage, evaluator permission, mapping, and bounded migration contracts. The
same-repository credentialed publication boundary remains an explicit P8
prerequisite and blocks cutover until its live fixture passes.

## Other Notes

### Acceptance and rollback

The migration is accepted only when all work-package exit evidence is linked
from an immutable result bundle. A failure uses the canonical enum `product`,
`test-flake`, `runner`, `queue`, `toolchain`, `external-dependency`, `policy`,
or `security`; retries preserve the
first result. A disagreement, missing evidence, stale policy, or unexpected
unsupported row blocks cutover.

Rollback disables only the new shadow/gate path and restores the previous
required proof. It does not delete artifacts, alter source, loosen permissions,
or bypass the native/libghostty repair. If the old path is unavailable, the
release remains blocked rather than falling through to an unverified publish.

### Risks and mitigations

| Risk | Mitigation |
|------|------------|
| Policy tool becomes a false-red single point of failure | Small pinned bootstrap, local replay, fixture tests, owner rota, explicit diagnostics, and no direct publication authority. |
| Existing JS adapters drift during Go migration | Grandfather with contract tests; port one adapter at a time and compare outputs before deletion. |
| Dynamic checks interact badly with branch protection | One tamper-resistant synthetic gate; zero-job, absent-report, and PR-rewrite tests; staged settings cutover. |
| macOS queue/cost misses PR SLO | Capability-specific budgets, bounded matrix, shadow measurement, and main/scheduled deferral recorded as policy. |
| Native artifact substitution or stale headers | Digest/provenance manifests and independent producer/consumer verification. |
| Fork or same-repository agent code reaches secrets or push credentials | Explicit fork/agent/trusted tiers, trusted-base evaluator, no secrets in untrusted jobs, unprivileged generation, protected publication environment. |
| Current and new graphs disagree during migration | Immutable coordinate comparison; before App cutover the old gate remains authoritative, and after App cutover old contexts remain observational until the owner-approved bounded sample passes. |

### Review history

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

### Testing

Each package adds unit/contract tests before workflow wiring. The fixture is the
minimum end-to-end test. Targeted tests cover policy edits, archive portability,
trust tiers, cancellation, stale outputs, retries, generated files, and release
rollback. Weekly scheduled fault injection and release dry runs provide drift
evidence; broad product suites remain owned by the eventual CI lanes.

### Open questions

- The `graith-ci-gate` App is the selected enforcement mechanism. P4 must
  choose its hosted deployment and attestation key service, record rotation,
  retention, and incident-revocation procedures, and stop with no cutover if
  those controls cannot be demonstrated in the live fixture.
- P0 supplies the baseline for final latency and cost budgets. Until the
  retained fixed-window evidence is complete and calibration is owner-reviewed,
  the provisional 20/35/45/90-minute targets and the dual-run 2x-plus-20%
  budget are binding ceilings, not goals that may be exceeded.
- The App check remains the single required decision while individual mode
  diagnostics stay visible as separate checks/evidence. No diagnostic check may
  be substituted into branch protection without the same App source binding.
- In-flight PRs are evaluated against the policy digest recorded at intake;
  when the base changes, the App starts a new epoch and rejects evidence from
  the prior digest. There is no compatibility grace period for required proof.
