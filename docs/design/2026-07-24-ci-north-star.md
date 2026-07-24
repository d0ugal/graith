---
title: "Design Doc: CI North Star"
authors: graith maintainers
created: 2026-07-24
status: Draft (revised after independent review — see Consensus)
reviewers: Claude/Opus (completed; requested additional judges unavailable)
informed: maintainers, release owners, GUI owners
---

# CI North Star

This design defines a CI system that proves the modes graith ships, produces
that proof quickly and reproducibly, and makes a false green difficult to
create accidentally. It is a target architecture for pull requests, main,
scheduled maintenance, dependency updates, and releases; it deliberately does
not preserve the shape of today's workflows.

## Background

graith is a Go daemon and CLI with a framed Go/Swift protocol, PTY and process
lifecycle code, sandbox backends, a document store, native/libghostty consumers,
and macOS/iOS GUI packages. It ships source-oriented binaries and packaged
release artifacts. Its CI therefore spans Linux and macOS, Go and Swift, native
toolchains, generated manifests, documentation assets, security-sensitive
sandbox boundaries, and release publication.

The important boundary is between a change under review and a trusted system
that can publish. Pull requests may be fork-controlled and contain arbitrary
source, scripts, generated code, workflow edits, and test data. Main and
release publication are trusted operations with separate credentials and
protected environments. CI must make that distinction explicit rather than
letting a job's name or event type imply it.

### Current-state verification baseline

The target is greenfield, but it must not regress protections already present
in the repository. The current baseline includes PR-file API detection rather
than a potentially stale local merge-ref diff, fail-safe escalation when a
detector fails, pinned actions and toolchains, native/libghostty artifact
provenance and consumer checks, release-shaped Linux/Darwin execution, and a
credential-separated generated-file regeneration path. It also has explicit
sandbox enforcement probes, platform-specific Go lint/build coverage, and GUI
cross-compilation checks. These are capabilities to preserve and re-prove, not
workflow boundaries to copy.

The baseline has intentional trade-offs that the north star must make
first-class policy rows: some macOS runtime-only Go branches are deferred from
PRs and covered on main; the ordinary integration job performs a compile-only
tagged check while the native workflow performs runtime lifecycle tests;
coverage currently has a hard in-job threshold but is soft relative to the
primary merge gate; and same-repository regeneration may publish only after
an unprivileged preparation step. A migration may change any decision only
with an owner, evidence, and an explicit mode transition. It must never turn
these distinctions into an unnamed skip or treat a current skipped required
context as proof that the underlying mode ran.

This inventory also identifies defects to fix rather than preserve: the GUI
coverage detector must fail safe when its detector job fails, and an
unmeasurable coverage delta must be `unknown`/deferred with expiry rather than
an exit-zero pass. Same-repository agent-authored branches form a distinct
trust tier from maintainer-controlled trusted branches; write-capable docs
preview, regeneration, and comment paths require that tier's restrictions.
The existing JavaScript policy tools and YAML-regex trust tests are baseline
contracts to inventory and either wrap or replace with Go contract tests before
their wiring changes.

## Problem

CI can be fast yet misleading if a detector skips a changed mode, a cache
restores the wrong toolchain, a producer artifact is consumed without identity
verification, or a required check succeeds while its matrix leg never ran.
Conversely, an over-connected graph makes every change wait for expensive GUI,
native, and deep security work, while retries conceal infrastructure health.

The design needs measurable service levels, a small set of proof-carrying
contracts, deterministic routing, and operational evidence. It must distinguish
product regressions from runner, queue, and shared-dependency failures without
turning the latter into an unreviewable red wall.

## Goals

- Make a false green exceptional: every required mode has an explicit proof
  record, and any unknown, missing, stale, or unsupported result fails closed.
- Give a Go-only PR a first actionable signal within 5 minutes and required
  green within 20 minutes (p95, queue plus execution; 30 minutes p99). A
  GUI/native-touching PR has a separate provisional target of 35 minutes p95
  and 50 minutes p99, pending the Wave-1 baseline.
- Give main a complete confidence result within 45 minutes p95 and a release
  candidate a signed, reproducible verification result within 90 minutes p95;
  these are provisional until baseline measurements confirm runner capacity.
- Achieve at least 99.5% successful required-run completion after the permitted
  retry policy, excluding product failures; measure infrastructure failures
  pre-retry per job and post-retry per run, with both denominators reported.
  Keep pre-retry infrastructure failures below 1% of jobs and post-retry
  incomplete runs below 0.5%. A retry may recover a signal but never erases
  the original outcome.
- Maintain zero confirmed false-green incidents and zero fixture escapes in a
  rolling 90-day window. Report audit findings and contract-test escapes as
  leading indicators; do not claim a percentage rate until merge volume gives
  that rate useful resolution.
- Keep test flake below 0.5% of test executions (not test cases), with the
  execution denominator and retry history emitted by the result bundle.
- Keep p95 queue wait below 2 minutes for PR fast lanes and below 10 minutes
  for deep lanes; cap routine PR retries at one automatic retry per failed
  shard and require classification before another retry.
- Measure cost per changed PR and per main run. Provisionally target a 20%
  reduction in the Go-only fast lane from its instrumented baseline, net of
  orchestration overhead and without reducing proof coverage; P0 must name the
  funding mechanism (sharding, deduplication, or safe caching) before this is
  accepted. Dual-run migration cost is budgeted separately. Keep
  cache restore success above 85% as a trend target (not a correctness SLO),
  with zero cross-commit cache poisoning.
- Make every result diagnosable from immutable metadata: commit, mode,
  platform, toolchain, input digest, artifact digests, policy revision, and
  owner.
- Minimize CI's cognitive surface: prefer one typed, repository-native policy
  implementation (Go) with thin declarative scheduling and permission wrappers.
  An ordinary behavior change should touch one logic language plus minimal
  wiring; cross-language changes require an explicit justification and owner.

### Non-Goals

- Replacing GitHub, the runner fleet, package registries, or native toolchains.
- Making every check run on every platform for every documentation-only change.
- Treating a green dashboard as a substitute for code review or release
  ownership.
- Changing production code, release formats, repository settings, labels, or
  the urgent libghostty repair as part of this design.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI/server (Go) | Targeted | Linux is the primary server/runtime proof platform; supported Go build, unit, race, integration, protocol, sandbox, and packaging modes are required. |
| macOS GUI | Targeted | macOS is required for Swift packages, macOS app compilation, GUI tests, signing-shaped validation, and native/libghostty integration. Publication credentials remain protected. |
| iOS GUI | Targeted for build and test proof | iOS package/app compilation and simulator tests prove the supported client surface; device signing and store submission are release-only protected steps. |
| Linux runners | Targeted | Required for the daemon, CLI, Linux sandbox backends, reproducible archives, security scans, and server release artifacts. |
| macOS runners | Targeted | Required for Swift/Xcode, iOS simulator, macOS GUI, and libghostty producer/consumer checks. |
| Release platforms | Targeted by declared artifact matrix | Each published platform has a build, checksum, provenance, install/smoke, and consumer-verification contract. Unsupported combinations are explicit policy rows, never silently omitted legs. |

## Proposals

### Proposal 0: Do Nothing

Retain the existing collection of workflow concerns and add checks when a
failure appears. This has the lowest immediate migration cost, but keeps
routing, gate semantics, artifacts, and trust boundaries implicit. It cannot
provide a defensible false-green bound or tell maintainers whether an omitted
matrix leg was intentional. It is rejected.

### Proposal 1: Capability-driven proof graph (Recommended)

CI is a typed, event-specific DAG. A small, versioned policy manifest describes
change capabilities (Go, protocol, GUI, native, docs, packaging, security),
required proof modes, supported platforms, owners, and cost class. A detector
emits a signed or checksum-bound *run plan*; it may only narrow work when every
input is known and the detector itself passes validation. Unknown paths, changed
CI policy, generated inputs, lockfiles, release metadata, and detector errors
expand to the safe superset.

The graph has four layers:

```text
source + event
      |
  intake / policy / fail-closed plan
      |
  fast proof (format, compile, focused tests, policy, generated files)
      |------------------------------+
  required matrix fan-out           deep matrix fan-out
      |                              |
  deterministic required fan-in  scheduled/main/release fan-in
      |------------------------------+
       signed result bundle + diagnostic classification
                         |
                 protected publish (release only)
```

The event-specific edges and barriers are:

```text
PR:      intake -> plan -> {fast proofs, required matrix} -> required-gate
                         \-> deep annotations
main:    merge-intake -> trusted-plan -> {all platform proofs, race/integration,
                         package-consumer, security} -> main-confidence
release: source-lock -> reproducible-build -> artifact-verify -> consumer-smoke
                         -> sign/attest -> independent-verify -> publish
```

Braces denote deterministic fan-out; the named node after each brace is a
barrier that waits for the complete expected coordinate set. Scheduled and
dependency-update runs use the main graph with explicit scope expansion; they
cannot bypass the plan or result schema. A PR policy change additionally runs
the trusted base evaluator and the new evaluator in the fixture before it can
alter the plan.

#### Event DAGs

| Event | Fast and required | Deferred or deep | Terminal evidence |
|-------|-------------------|------------------|-------------------|
| PR from fork or branch | Intake, plan, format/lint, Go compile/unit shard, protocol/generated checks, changed-capability required platform proofs, dependency/security policy | Full race/integration, exhaustive GUI/device, long fuzz/benchmark, broad supply-chain audit | Merge gate requires plan completeness plus every selected proof; deep results annotate the PR and can block according to policy. |
| Merge to main | The PR proof is re-verified against the merge commit; smoke and startup checks | Full race/integration, all supported GUI/native/sandbox legs, coverage, package install/consumer checks | Main confidence bundle is complete, retained, and dashboarded; failures page the owning area. |
| Scheduled | A rotating full matrix, long race/fuzz/soak, toolchain and runner compatibility, dependency freshness, security scans | N/A; expensive suites are intentionally scheduled and budgeted | Trend and drift report; no scheduled green is allowed to mask a main regression. |
| Dependency update | Plan plus focused affected modes, lockfile/provenance review, license/security policy | Full matrix if toolchain, native, GUI, protocol, or runtime dependency changes | Update may merge only with the same proof contract as an equivalent source change. |
| Release candidate/tag | Rebuild from immutable source; all release platforms; consumer install/smoke; checksum, SBOM, provenance, signature and attestation verification | Extended upgrade/rollback and external mirror verification | Protected publication gate requires a complete candidate bundle, independent verification, and human approval. |

The current-state inventory for P0 must enumerate the approximately 18 existing
workflow files and their event/permission shapes, including `ci`, `coverage`,
`gui-ci`, `libghostty-native`, `libghostty-native-publish`, `regen`,
`docs-preview`, `dev-release`, `release-please`, `goreleaser`, `sandbox`,
`dependency-review`, `codeql`, `scorecard`, `secret-scan`, `gitleaks`,
`workflow-lint`, and `commits`. It must record unusual paths such as
`dev-release` on push and pull request, scheduled docs-preview cleanup, and
main-only native publication with `contents:write`; these are observations to
replace with capability policy, not topology to preserve.

Fast checks are small, deterministic, and required when they prove a changed
capability. Deep checks are not silently converted into optional checks: their
policy status is recorded as `deferred`, `passed`, `failed`, or `not-applicable`
with a reason and expiry. Main and release paths promote deferred modes to
required where their risk warrants it.

#### Correctness model and contracts

1. **Hermetic inputs.** A job receives a pinned repository revision, declared
   toolchain image/runner label, dependency lockfiles, explicit environment,
   and network policy. It records locale, timezone, architecture, compiler/Xcode
   versions, and relevant kernel/runtime features. Home-directory state,
   ambient credentials, undeclared binaries, mutable latest tags, and host
   caches are excluded. Network access is denied by default and allowlisted by
   dependency acquisition policy.
2. **Run-plan contract.** The plan contains event, commit/tree digest, policy
   version, detector version, capability set, required modes, unsupported-mode
   decisions, and an expiry. A fan-in accepts only a plan with matching commit,
   policy, and complete decision rows. Detection errors or unknown files select
   the superset and fail if the plan cannot be produced. On a fork PR, plan and
   gate evaluation runs from the trusted base ref or a pinned released policy
   tool; the PR's policy code is untrusted input. A PR that changes policy is
   evaluated by the old trusted evaluator while its replacement is proven in
   the fixture, then promoted only after merge. This prevents self-certifying
   plan narrowing.
3. **Producer/consumer artifact contract.** Producers publish a content-addressed
   artifact plus a machine-readable manifest containing schema version, source
   digest, toolchain digest, platform/architecture, build flags, file list,
   SHA-256 digests, and provenance. Consumers fetch by immutable digest,
   verify the manifest and provenance before extraction, and fail on extra,
   missing, or changed files. A filename, job name, or successful upload is
   never an identity claim.
4. **Source versus package proof.** Source tests prove repository behavior from
   checked-out inputs. Package-consumer tests install exactly the produced
   archive/bundle/container and exercise its public CLI, daemon startup,
   protocol compatibility, permissions, and upgrade/rollback behavior. Passing
   source tests cannot substitute for a consumer proof.
5. **Mode proof.** Every required mode emits a result record with mode ID,
   matrix coordinates, attempt history, status, and evidence digest. The gate
   validates the expected set from the plan, not a count of successful jobs.
   A skipped, cancelled, stale, superseded, or unrecognized mode is not green.
6. **Platform proof.** Supported platforms have positive rows. Deliberate
   exclusions are versioned policy decisions with rationale and owner; an
   unavailable runner or unsupported toolchain is `blocked`/`unknown`, not
   `passed`. The release artifact matrix is closed-world: every declared target
   is present exactly once.

#### Reusable units and deterministic fan-in

Reusable units own one concern: intake/plan, toolchain setup, Go proof, Swift
proof, native producer, native consumer, sandbox proof, docs/assets, security,
package consumer, and result publication. Each accepts typed inputs and emits
the same result schema. The policy manifest and mode IDs are the single source
of truth; job labels and display names are generated from them so a renamed job
cannot create a phantom gate.

The orchestration implementation has a complexity budget. Plan evaluation,
matrix expansion, result validation, and gate policy should live in a tested,
repo-owned Go command/library that is runnable locally and in a hermetic
fixture. YAML should schedule jobs, declare permissions, and pass typed inputs;
it should not contain business logic. Bash is limited to narrow process
invocation, and JavaScript/Python are acceptable only for ecosystem-mandated
tools or isolated adapters with contract tests. Generated JSON/YAML is data
emitted by the Go implementation, not a second policy language. A change that
crosses languages must state why the native implementation cannot provide the
needed capability and how local reproduction remains possible. This budget
reduces polyglot drift, improves diagnostics, and makes vendor migration less
expensive while preserving thin runner-specific adapters.

This budget has real costs: existing ecosystem-mandated JavaScript adapters
may be grandfathered while their contracts remain tested, and porting one is a
separate migration with rewrite risk. A cold policy-tool build consumes the
first-signal budget, so it must be small, reproducible, and optionally supplied
as a trusted pinned binary. The Go evaluator is also a deliberate single point
of failure: a defect may create a false-red wall, which is preferable to a
false green but still requires a tested fallback diagnostic and owner. These
costs are part of the design rather than hidden cleanup work.

For GitHub required-check integration, one synthetic gate is always scheduled
and always reports. It has no path filter or job-level skip condition. Branch
protection requires this gate, not a dynamic collection of matrix names; an
absent report, zero-job plan, missing fan-in, or stale run is a hard block. A
contract test must prove that a plan selecting no jobs produces a red gate,
not an absent status that GitHub could treat as satisfied.

Fan-out is deterministic: coordinates are sorted, each coordinate has a stable
ID, and retries append attempts under that ID. Fan-in waits for all expected
coordinates, then evaluates a pure policy function. A superseded PR run is
cancelled for resource control, but its partial result is retained as
`superseded`; only the newest run for a commit may satisfy a required check.
Concurrency groups cancel stale PRs, never an in-progress protected release
publication, and cancellation is tested as a first-class state.

#### Security and trust

Fork PR jobs run with read-only, least-privilege tokens, no repository secrets,
no write-capable environments, and untrusted input treated as data. A
same-repository agent-authored branch is a distinct untrusted tier from a
maintainer-controlled trusted branch; docs-preview writes, regeneration pushes,
and coverage/comment publication require explicit restrictions for that tier.
Generated code and third-party action inputs are not trusted merely because
they are checked in. Actions and reusable components are pinned by immutable
revision and reviewed; shell, JavaScript, and workflow policy are scanned.
Credentials are available only in a protected trusted-branch environment after
all non-publication proofs pass.

Run-plan integrity and authenticity are separate: an untrusted fork plan is
checksum-bound and authenticated by the trusted-base evaluator, while trusted
main/release plans are signed by the protected policy publisher. Untrusted
builds never write caches that trusted main or release jobs may consume; cache
trust tiers are separate and a trusted job rejects an untrusted-origin entry.

The workflow file on a GitHub `pull_request` event is PR-head content, so a
trusted evaluator binary alone is not a trust root: a PR can replace the gate
job body with `exit 0` while retaining its required name. The merge gate must
therefore be enforced by a default-branch ruleset/required workflow, a
`pull_request_target` trusted wrapper with an explicitly safe checkout, or a
trusted merge-queue (`merge_group`) re-verification. Job-name-matched checks
from an untrusted PR workflow are never sufficient. A contract test must prove
that a PR rewriting the gate job to unconditional success cannot become
mergeable, and P4/P10 cannot pass until that test is demonstrated.

Build and test jobs cannot publish release assets, mutate checks, or approve
their own environments. Artifact signing/attestation occurs in a separate
protected step over digest-pinned inputs; release consumers independently verify
signature, provenance, SBOM, dependency integrity, and checksums. Dependency
updates use a restricted bot identity and receive the same untrusted-input
handling as a fork until promoted by the trusted pipeline.

#### Testing CI itself

The repository has executable contracts for the policy schema, plan closure,
mode-to-gate mapping, action pinning, permissions, artifact manifest schema,
retention, concurrency, and release-environment boundaries. A hermetic fixture
repository exercises the graph end to end without external secrets. Contract
tests must reject:

- a missing or unknown file in change detection;
- polluted `PATH`, locale, timezone, compiler variables, or credentials;
- a cache with a mismatched key, toolchain, or checksum;
- a cache written by an untrusted build being accepted by a trusted build;
- a corrupt, substituted, stale, or cross-commit artifact;
- Linux/macOS archive ordering, mode-bit, line-ending, and symlink differences;
- cancelled, timed-out, superseded, or partially uploaded jobs;
- duplicate or missing matrix coordinates and misleading display names;
- a gate that reports green without proving the requested mode ran;
- a PR that rewrites the gate job to unconditional success;
- an unsupported platform that is silently treated as passed.

Fault injection runs on every policy change and periodically against the
fixture. A canary policy change must demonstrate that the expected gate fails
closed before it is allowed to affect required checks. CI tests are versioned
alongside the policy and are themselves required for policy changes.

#### Observability and operations

Each result is structured with `failure_class` (`product`, `test-flake`,
`runner`, `queue`, `toolchain`, `external-dependency`, `policy`, or `security`),
owner, retry count, timestamps for queue/start/finish, and links to logs,
artifacts, and the exact input manifest. Classification is evidence-based: a
retry can prove transient infrastructure only when the same immutable inputs
pass and the original failure is retained. A product failure cannot be hidden
by a successful rerun.

An owner rota maintains the policy and each capability has a named owner. SLO
dashboards show first-signal and required-green latency, queue time, flake and
retry rates, false-green incidents, cache hit/miss and poisoned-cache rejects,
artifact verification failures, cost, and deferred-mode age. Alerts cover
missing result bundles, unexplained classification spikes, stale policy, and
release evidence gaps. Monthly audits sample merged changes against their run
plans; quarterly audits review permissions, pins, supported-platform rows,
retention, and cost. Automatic retries are limited and visible; manual reruns
must use the same commit and explain the selected scope. Repeated flake is
quarantined only with an owner, expiry, replacement proof, and an explicit
reduced-confidence status; quarantine never becomes an invisible pass.

#### Performance and cost

Caches are content-addressed and scoped by repository, immutable dependency
lockfile, toolchain digest, platform, architecture, and relevant build mode.
Restores verify a manifest and checksum before use. Caches are acceleration,
never correctness inputs: a miss must produce the same result, and a mismatch
is discarded. Do not cache secrets, mutable tool outputs whose provenance is
unclear, failing test state, or broad host directories.

Large external dependencies such as native/libghostty inputs may be prebuilt,
but only as signed, digest-pinned artifacts with source revision, build recipe,
toolchain, and license metadata. A source build remains available on a
scheduled cadence and whenever producer/consumer contracts or toolchains
change. Native compilation is deduplicated by producing once per immutable
coordinate and consuming that artifact in compatible test shards; incompatible
ABI/toolchain coordinates build separately.

The matrix is sized by proof value: shard Go tests by historical duration with
stable hashes, keep expensive integration tests isolated, and use bounded
parallelism per runner pool. Queue budgets trigger capacity or sharding work,
not more retries. A cost controller reports expected and actual minutes before
adding a matrix row. A cache is omitted when upload/restore costs exceed the
saved work, inputs are too volatile, provenance cannot be established, or a
small job is faster and safer without it.

## Consensus

The first draft received one complete accepted review from Claude/Opus. Codex
started but exited without publishing a verdict. The mandated Cursor catalog
helper was attempted twice; one run failed with provider `permission_denied`
because Router Optimize For was disabled, and the retry did not publish a live
model list. No unverified Cursor model was substituted. The complete review
identified two load-bearing omissions: trusted-base evaluation for fork plans
and an always-reporting synthetic gate for skipped-check semantics. It also
required per-event DAG edges, trust-tiered caches, measurable false-green and
segmented provisional SLOs, migration cost accounting, and an honest
complexity-budget trade-off. Those revisions are incorporated above.

The review supported the fail-closed mode set, artifact identity, source versus
package proof, and self-testing approach. Open design choices remain runner
capacity, exact attestation service, and baseline-calibrated thresholds; these
may change through policy review, but the trust-root, closed-world gate, and
protected release invariants do not.

The follow-up current-state audit attempted direct Claude and Codex judges and
the mandated Cursor catalog helper. The Cursor helper failed with the provider
`permission_denied` Router Optimize For restriction, so no unverified Cursor
model was run. A later completed Claude review also required an explicit
default-branch trust root for the synthetic gate, a third same-repository agent
trust tier, pre/post-retry SLO denominators, zero-incident false-green targets,
coverage fail-closed defects, a closed workflow/JavaScript inventory, and
semantic replacement of YAML-regex trust tests. These are incorporated above
and in the implementation plan; incomplete direct judge attempts and cleanup
are recorded in shared tribunal history. The repository comparison retains the
current fail-safe detectors, native artifact contracts, generated file trust
split, and explicit compile-only/runtime coverage distinctions while replacing
their scattered routing with the typed plan and gate model.

## Other Notes

### References

- `docs/design/TEMPLATE.md` — design lifecycle and required section order.
- `internal/protocol/` — Go/Swift wire-shape and conformance constraints.
- `internal/capabilities/` — generated capability and manifest patterns.
- `gui/` — macOS/iOS package and app constraints.
- `internal/sandbox/` and `internal/pty/` — platform and process-lifecycle
  proof categories.
- `website/` and `website/content/docs/` — documentation and asset validation.
- `docs/design/2026-07-25-ci-north-star-implementation-plan.md` — issue-ready
  sequencing, acceptance evidence, cutover, and rollback plan.

### Implementation Notes

The north star is capability- and contract-driven, not workflow-driven. A
future implementation must first define the policy schema, result schema, and
fixture contracts before moving required checks. No migration step may weaken
the target's fail-closed semantics.

#### Safe migration waves

1. **Evidence baseline.** Instrument current runs without changing gates;
   measure queue, duration, retries, flakes, cost, cache behavior, and the
   actual mode coverage for four weeks. Publish owners and failure classes.
2. **Contracts and fixture.** Implement policy/run-plan/result schemas,
   deterministic fan-in, artifact manifests, and hermetic fault-injection
   tests. Prove the fixture catches every listed false-green case.
3. **Fast proof pilot.** Dual-run the new PR fast lane beside existing checks
   for representative Go, protocol, GUI, native, docs, and workflow changes.
   Require matching mode sets, latency, and classification for two weeks.
4. **Deep and platform promotion.** Add main, scheduled, dependency, and
   package-consumer proof; dual-run full Linux/macOS matrices and compare
   artifacts and coverage. Keep old checks informative, not a second source of
   truth, until evidence meets the SLOs.
5. **Protected release cutover.** Dry-run signing, attestation, independent
   verification, and rollback on non-publication candidates. Cut publication
   over only after two successful candidates and an owner-approved audit.

Dual-run acceptance requires no unexplained mode disagreement, zero artifact
identity mismatch, no false-green fixture escape, p95 latency within budget,
and classified failures for every non-pass. If the new graph is incomplete,
the old required proof remains authoritative; if it is unsafe, disable only
the new path and return to the last known-good gate. After 30 days of stable
coverage, delete duplicate workflows and obsolete required checks in a
separate, reversible settings change; retain result history and rollback
instructions. Migration convenience must never justify `continue-on-error`,
silent skips, unpinned dependencies, or unprotected publication.

The first implementation issue in this sequence should establish the
repo-owned Go policy/result library and its complexity budget before any gate
cutover. Its acceptance criteria are local plan replay, schema validation,
fixture fault injection, generated-data determinism, and a reviewable report of
all language and configuration surfaces. This makes maintainability a required
property of the architecture rather than a cleanup task after migration.

### Alternatives considered

Incremental cleanup—add caching, split slow jobs, and pin a few dependencies—
would improve speed but leaves the central proof and trust model implicit. A
single monolithic workflow would simplify discovery but maximizes queue coupling,
secret exposure, and blast radius. An external CI orchestrator could provide
stronger scheduling and attestations, but adds vendor lock-in, migration risk,
and a second policy language; the proposed contracts keep that option open
without making it a prerequisite. A repo-owned Go policy engine with thin
Actions wrappers is preferred to a YAML-expression/Bash/JavaScript/Python
polyglot: it costs an initial binary and adapter design, but gives one typed
implementation, local replay, portable fixture tests, and a clear boundary if
the runner vendor changes. A fully external orchestrator may offer richer
scheduling, but would increase lock-in and make local reproduction weaker.

### Testing

Every implementation wave must add contract tests before changing a required
gate. The fixture must cover valid and invalid plans, all artifact transitions,
platform archive normalization, cancellation, retries, stale outputs, polluted
environments, and permission boundaries. Targeted tests should run on each
policy change; scheduled fault injection and an end-to-end release dry run
must run at least weekly. Acceptance evidence is the signed result bundle and
the dashboard measurements, not merely a green check mark.

### Open questions

- Which runner and artifact-attestation service can meet the stated SLOs and
  retention requirements without introducing unacceptable lock-in?
- What baseline measurements will calibrate the initial cost target and the
  20-minute PR p95 for the real contributor population?
- Which iOS simulator/device versions are part of the supported release matrix,
  and which are scheduled compatibility probes?
