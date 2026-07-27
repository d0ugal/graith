---
title: "Design Doc: CI North Star Implementation Plan"
authors: graith maintainers
created: 2026-07-25
status: Draft (corrected for issue #1715)
reviewers: d0ugal
informed: maintainers, release owners, GUI owners, security owners
---

# CI North Star Implementation Plan

This plan implements the corrected [CI North Star](2026-07-24-ci-north-star.md)
with existing repository infrastructure only. It is deliberately smaller than
the previous P0-P11 rollout: no external gate, no new identity, no settings
change, no merge queue, no live fixture repository, and no required-check
cutover.

The near-term objective is faster, more reliable CI with fewer duplicated
policy surfaces. The first implementation signal is a non-required shadow
summary inside existing GitHub Actions. Current checks remain authoritative
until a later in-repository change has direct evidence and a reversible reason
to simplify them.

## Background

The current workflows already protect important boundaries:

- untrusted PR code does not receive release or branch-mutation credentials;
- privileged publication paths do not execute untrusted PR code while
  credentials are present;
- checkout credential persistence and job token permissions are explicit;
- fail-safe detectors, where already present, escalate macOS, sandbox,
  native/libghostty, release, docs-preview, generated-file, and
  branch-mutation-sensitive paths rather than silently skipping;
- native/libghostty source, artifact, manifest, archive, and consumer checks
  protect the terminal runtime;
- docs-preview and regen branch mutation paths have same-repository and
  fresh-runner separation;
- release-shaped PR builds do not publish.

The libghostty migration is complete enough that libghostty should be treated
as the core runtime validation surface, not an optional add-on. Cleanup should
remove obsolete add-on framing and duplicated helper logic, while preserving the
native protections that now guard the main runtime.

### Existing-Infrastructure Ceiling

Every package in this plan must stay within the current repository setup:

| Area | Allowed |
| --- | --- |
| Repository | The existing `d0ugal/graith` repository only. |
| Automation | Current GitHub Actions workflows and repository-owned scripts. |
| Runners | GitHub-hosted Ubuntu and macOS runners already used by current workflows. |
| Tokens | Existing `GITHUB_TOKEN` behavior only. |
| Secrets | Existing publication secrets only where current workflows already use them: `GPG_PASSPHRASE`, `GPG_PRIVATE_KEY`, and `RELEASE_TOKEN`. |
| Environments | The existing `github-pages` environment only. |
| Settings | Current repository settings; no rulesets, repository variables, external CI service, or extra environment are assumed. Current classic branch protection on `main` has `enforce_admins` enabled, non-strict required status checks, and seven required GitHub Actions contexts. |

The seven current required contexts are `Test (macos-latest)`, `Lint`,
`Conventional commits`, `Test (ubuntu-latest)`,
`macOS (safehouse / Seatbelt)`, `Linux (nono / Landlock)`, and
`Native backend gate`. This plan does not add, remove, rename, or reconfigure
required checks.

If a work package needs anything outside this table, it is not part of this
rollout.

## Problem

The previous implementation plan required infrastructure that does not exist
and is not approved for this rollout. It also made later phases depend on a
GitHub App, external deployment, live fixture repository, replay store,
protected settings, and calendar-style burn-in before delivering everyday CI
improvements.

The corrected work should instead answer practical questions:

- Which current checks are expected for a PR and why did they run or skip?
- Which jobs dominate feedback time for common change classes?
- Which helper scripts duplicate policy across Go, shell, JavaScript, Python,
  and YAML?
- Which libghostty/native checks are core runtime coverage and which are stale
  migration scaffolding?
- Which landed north-star packages should be kept, simplified, or deleted when
  they lack a real caller?

## Goals

- Ship a visible, non-required CI shadow summary quickly inside existing
  GitHub Actions.
- Keep current required checks authoritative throughout this plan.
- Use deterministic fixtures and explicit sample change classes for evidence.
- Improve speed by deleting stale migration scaffolding, avoiding unnecessary
  duplicate work, and making slow paths visible before changing routing.
- Improve reliability by keeping fail-safe routing and native/release artifact
  protections intact.
- Reduce language sprawl only where it removes duplicated CI policy or brittle
  parsing.
- Delete or retire landed external-gate code that cannot be used under the
  existing-infrastructure ceiling.
- Keep implementation packages PR-sized, independently reviewable, and
  reversible.

### Non-Goals

- Do not provision a GitHub App, bot, machine identity, cloud service, webhook
  receiver, external evaluator, replay service, KMS, signing service, database,
  bucket, new repository, live fixture repository, new secret, self-hosted
  runner, merge queue, `merge_group`, ruleset, branch protection, environment,
  required-check setting, paid account, or new scheduled job.
- Do not edit workflow YAML, Go code, generated metadata, GitHub settings, or
  issue hierarchy in the corrective design PR.
- Do not weaken existing publication, credential, sandbox, artifact, native,
  release, docs-preview, or regen boundaries.
- Do not claim GitHub Actions source isolation that it does not provide.
- Do not port working JavaScript helpers merely to change language.
- Do not use elapsed calendar time as an acceptance gate.

## Platform support

| Surface | Plan |
| --- | --- |
| CLI and daemon | Covered by current Go jobs and any local validation packages retained after simplification. |
| Linux | Existing GitHub-hosted Ubuntu runners only. |
| macOS | Existing GitHub-hosted macOS runners only. |
| GUI/iOS package surface | Existing GUI workflow coverage only. |
| Libghostty runtime | Treated as core runtime CI, not an optional add-on. Existing native checks remain until a PR proves an equivalent simpler shape. |
| Documentation | Existing docs preview and Pages workflows only. |
| Releases | Existing PR release-shaped builds and trusted publication workflows only. |
| External CI control plane | Not supported. |

## Proposals

### Proposal 0: Do Nothing

Keeping the previous implementation plan would leave future work blocked on a
GitHub App, external deployment, live fixture repository, replay store, and
required-check cutover that this rollout cannot use. It would also delay the
first visible CI improvement behind infrastructure and cleanup that do not
directly address speed, reliability, or helper sprawl.

Doing nothing avoids churn, but it leaves the landed gate-era code looking like
an expected destination and keeps libghostty cleanup mixed with migration-era
framing. The current required checks would still protect the repository, but
maintainers would not get the clearer diagnostic signal needed to simplify CI
safely.

### Proposal 1: Existing-Actions Rollout (Recommended)

Ship a short sequence inside the existing repository and existing GitHub
Actions setup. The sequence starts with this design correction, deletes the
external gate, adds the smallest useful shadow summary against current static
inventory, prunes baseline/policy helpers from the actual caller graph, then
uses deterministic fixtures to justify libghostty and workflow simplifications.

This is recommended because it produces a visible signal before broad cleanup,
keeps current required checks authoritative, and avoids introducing a CI control
plane.

#### Work Package C0: Corrective Design PR

Purpose: replace the external-gate north-star and implementation plan with this
existing-infrastructure design.

Expected files:

- `docs/design/2026-07-24-ci-north-star.md`
- `docs/design/2026-07-25-ci-north-star-implementation-plan.md`

Dependencies: none beyond starting from current `origin/main`.

Acceptance:

- Frontmatter and section order match the design-doc template.
- The existing-infrastructure ceiling is prominent in both documents.
- Prohibited external requirements appear only as rejected alternatives or
  non-goals.
- The component audit classifies landed P0-P5, P11, and P4 work.
- Issue disposition for #1700, #1705, and #1707-#1712 is explicit.
- `git diff --check` is clean.
- One applicable review is clean.
- Exact-head CI is green before the PR is undrafted.

Rollback: revert the corrective docs PR. No production code or workflow state is
changed.

#### Work Package C1: Delete External-Gate Dead End

Purpose: remove code and docs that exist only for prohibited external
infrastructure.

Expected files:

- delete `cmd/cigate/**`;
- delete `internal/cigate/**`;
- remove `cigate` package metadata from `internal/architecture/manifest.json`;
- delete or rewrite `website/content/docs/contributing/ci-gate.md`;
- prune references to the external gate from contributing docs.

Dependencies: C0.

Acceptance commands:

```bash
go test ./...
go vet ./...
make architecture-check
git diff --check
```

Acceptance evidence:

- No current workflow imports or invokes `cmd/cigate` or `internal/cigate`.
- User docs no longer describe an external App, webhook, replay service, live
  fixture repository, deployment digest, or App-owned check as part of graith
  CI.
- Deletion is a package/docs removal with no behavior change to current
  workflows.

Rollback: revert C1. Because no workflow should call the deleted package, the
rollback boundary is isolated.

Parallelism: C1 can run in parallel with C2 because C2 must not reuse `cigate`.

#### Work Package C2: Add Minimal Existing-Actions Shadow Summary

Purpose: show maintainers the expected CI shape, local detector decisions, and
helper surfaces quickly, without changing merge authority or depending on a
large helper cleanup first.

Expected files:

- update one existing workflow, preferably `.github/workflows/ci.yml`, to add a
  non-required `CI shadow summary` job;
- reuse existing static inventory code from `internal/cibaseline` or
  `internal/cipolicy` only where it is simpler than shell/YAML;
- add a small helper command only if the summary would otherwise duplicate
  parsing logic;
- add focused tests for any helper behavior used by the summary;
- update contributing docs only if a new user-facing command is introduced.

Dependencies: C0. C2 does not depend on C3 helper pruning.

Required workflow properties:

- `permissions: contents: read`;
- no secrets;
- `persist-credentials: false` for checkout;
- no branch mutation;
- no publication credentials;
- no claim that the job is source-isolated on PRs;
- diagnostic output to `GITHUB_STEP_SUMMARY`;
- normal job success unless summary generation itself fails.

V1 scope:

- summarize PR change classes from checked-in path rules and existing detector
  outputs;
- list expected current workflows/jobs and the seven current required contexts;
- show skip/escalation reasons the current workflow can compute locally;
- identify repository-owned workflow helper scripts and language surfaces;
- link maintainers to the normal Actions UI for durations rather than
  synthesizing repository-wide job timing.

V1 must not query or aggregate independent workflow jobs, cross-workflow
durations, retained GitHub history, or live check-run completion state. If a
later PR adds live check-run data, it must state the exact existing
`GITHUB_TOKEN` permission, scope, and completion semantics, and the result must
remain non-required.

Acceptance commands:

```bash
go test ./internal/cibaseline ./cmd/cibaseline ./internal/cipolicy
git diff --check
```

Acceptance evidence:

- The summary appears on the exact PR head.
- Current required checks still decide mergeability.
- A workflow change PR cannot use the summary job to obtain credentials or
  publish a check that becomes authoritative.
- The summary identifies libghostty/native coverage as core runtime validation.
- The summary does not claim repository-wide observed job results or durations.

Rollback: remove the new job and helper in one PR. Current required checks keep
working.

Parallelism: C2 can run while C1 is in review if it does not touch `cigate`.

#### Work Package C3: Prune Baseline and Policy Helpers to Actual Callers

Purpose: keep useful static inventory and validation revealed by C2, while
deleting retained live evidence, historical-window acceptance, and gate-oriented
policy shape that has no concrete caller.

Expected files:

- keep or simplify `internal/cibaseline/inventory.go`,
  `internal/cibaseline/inventory_test.go`, and `internal/cibaseline/inventory.json`
  only as needed by C2/C4;
- simplify or delete `cmd/cibaseline/**` based on the C2 caller shape;
- delete `internal/cibaseline/github.go`,
  `internal/cibaseline/evidence.go`, `internal/cibaseline/acceptance.go`, and
  retained evidence fixtures;
- simplify or delete `internal/cipolicy/manifest.go`, `build.go`,
  `validate.go`, `io.go`, and `manifest.json` based on the C2/C4 caller shape;
- keep `cmd/cipolicy/**` while `dev-release.yml` still invokes its plan command
  from the trusted base checkout;
- delete `internal/cipolicy/plan.go`, `result.go`, and `fixture.go` unless C2
  or C4 already imports them directly;
- treat `internal/cipolicy/cache.go` as already deleted by #1737;
- delete `internal/cipolicy/artifact.go` unless current native/release
  validation has a direct caller.

Dependencies: C0 and C2. Coordinate with C1 to avoid conflicts in architecture
or docs.

Acceptance commands:

```bash
go test ./internal/cibaseline ./cmd/cibaseline ./internal/cipolicy
go test ./...
git diff --check
```

Acceptance evidence:

- Every remaining command or package has a concrete C2/C4/native/release caller.
- No retained-evidence acceptance depends on fixed dates, mature run windows,
  or GitHub history collection.
- No speculative plan/result/fixture/cache code remains without an accepted
  caller.
- Static inventory drift tests still catch workflow/job rename mistakes when
  the summary relies on them.

Rollback: revert C3 or restore the deleted helper package in one PR.

Parallelism: C3 can be split into `cibaseline` and `cipolicy` pruning PRs after
C2 identifies the real imports.

#### Work Package C4: Deterministic Change-Class Fixtures

Purpose: replace calendar burn-in with explicit examples that prove routing and
summary behavior for common changes.

Expected files:

- fixture or table tests in the retained inventory/policy package;
- optional testdata files for path sets and expected check classes;
- documentation of the sample classes in contributing docs if maintainers need
  to run the checks manually.

Dependencies: C2. C3 is useful before C4 when it removes unused fixture code,
but C4 may also justify keeping a small fixture surface.

Required sample classes:

| Class | Expected evidence |
| --- | --- |
| Go-only | Go build/test/coverage paths remain covered; unrelated libghostty/release paths are not newly required beyond current authoritative checks. |
| Docs-only | Docs preview behavior is visible; same-repository write guard remains explicit. |
| GUI-only | Existing macOS/GUI routing and required-check satisfaction semantics remain visible. |
| Sandbox | Sandbox fail-safe escalation remains visible. |
| Libghostty runtime | Native source-build, artifact, manifest, archive, and consumer checks are treated as core runtime coverage. |
| Generated metadata | Existing drift tests remain visible. |
| Release path | Release-shaped PR builds run without publication credentials. |
| Workflow/script | Workflow-lint and helper tests cover policy changes. |
| Fork PR | Publication credentials, docs-preview writes, and regen mutation remain unavailable. |
| Same-repository mutation | Docs-preview and regen same-repository guards remain explicit. |

Acceptance commands:

```bash
go test ./internal/cibaseline ./internal/cipolicy
git diff --check
```

Acceptance evidence:

- Each fixture is deterministic and runs locally.
- Fail-safe detector failure remains an explicit expected output.
- No sample class depends on elapsed time, live GitHub history, or a separate
  repository.

Rollback: revert fixture/test additions. The shadow summary remains diagnostic.

Parallelism: C4 can be split by sample class once C2's summary output is
visible.

#### Work Package C5: Libghostty CI Cleanup

Purpose: clean up stale migration-era shape now that libghostty is the core
runtime, while preserving the native protections that matter.

Expected files may include:

- `.github/workflows/ci.yml`;
- `.github/workflows/libghostty-native.yml`;
- `.github/workflows/dev-release.yml`;
- `.github/workflows/goreleaser.yml`;
- `internal/cipolicy/libghostty_policy_test.go`;
- `cmd/libghosttyarchive/**`;
- `internal/libghosttyarchive/**`;
- `scripts/libghostty-native.sh`;
- release and macOS service helper docs/tests when touched.

Dependencies: C2 and the libghostty sample class from C4.

Allowed simplifications:

- rename or regroup summary labels so libghostty is described as core runtime
  coverage;
- remove stale fallback/add-on wording from docs and helper output;
- factor repeated artifact/manifest/archive checks into one tested helper if it
  reduces duplication;
- make slow native jobs easier to inspect by separating setup, source-build,
  artifact, and consumer timings;
- remove redundant checks only when a fixture and exact-head PR evidence show
  equivalent coverage.

Prohibited simplifications:

- treating libghostty as optional coverage;
- silently skipping native/source/artifact/consumer checks after detector
  failure;
- moving release credentials into PR-controlled code;
- weakening archive shape, digest, manifest, source-build, candidate privacy,
  or consumer checks.

Acceptance commands:

```bash
go test ./...
scripts/libghostty-native.sh test-archive-cleanup
scripts/libghostty-native.sh test-linux-archive
git diff --check
```

Acceptance evidence:

- The PR's shadow summary and current checks agree for the libghostty runtime
  sample class.
- Exact-head CI shows either lower observed duration for the affected path or a
  clear reliability simplification without duration regression.
- Rollback restores the previous workflow/helper shape.

Rollback: workflow/helper revert. Current required checks remain authoritative.

Parallelism: Split Ubuntu artifact cleanup, macOS source-build cleanup, and
release consumer cleanup into separate PRs if they touch different files.

#### Work Package C6: Targeted Helper Surface Retirement

Purpose: reduce language sprawl where it improves reliability or deletes
duplicated policy.

Expected files:

- one helper or helper family per PR under `.github/workflows/scripts/`;
- matching tests in Go, JS, shell, or Python depending on the retained caller;
- workflow YAML only when the caller changes;
- contributing docs only when user-facing commands change.

Dependencies: C2. For YAML-regex trust tests, depend on C4 fixtures first.

Acceptance:

- Each PR names the concrete deleted or simplified caller.
- Semantic tests exist before the old helper is removed.
- JavaScript helpers remain when they are the smallest reliable surface, such
  as `docs-diff*` with `pngjs`.
- No helper retirement changes credential boundaries unless the PR is explicitly
  about preserving that boundary and has focused tests.

Acceptance commands:

```bash
go test ./...
git diff --check
```

Rollback: restore the previous helper and workflow caller in one revert.

Parallelism: independent helpers can be handled in parallel after their shared
semantic fixtures exist.

#### Work Package C7: Demonstrated Workflow Simplifications

Purpose: make only the workflow simplifications that C2-C6 evidence supports.

Expected files vary by simplification. Each PR must list its affected workflows
and helper packages explicitly.

Dependencies: relevant C4 fixture plus exact-head evidence from C2.

Allowed examples:

- remove duplicated path predicates after a shared tested detector covers them;
- collapse redundant job setup after the same commands are covered elsewhere;
- make slow jobs conditional only when fail-safe escalation and required-check
  satisfaction are preserved;
- improve job names and summaries so core runtime, release, docs, GUI, sandbox,
  and generated-file coverage are readable.

Acceptance:

- Current required checks still pass on the exact head.
- Sample-class fixtures prove intended routing.
- Shadow summary explains the change.
- Rollback is a workflow/helper revert.

Rollback: revert the single workflow/helper PR.

Parallelism: run workflow-specific simplifications in parallel only when their
sample classes and files do not overlap.

## Issue Disposition After C0 Merges

Do not update issue hierarchy in the corrective design PR. After C0 merges:

| Issue | Disposition |
| --- | --- |
| #1700 `Epic: implement the CI north-star rollout` | Close as superseded or rewrite into a smaller epic for existing-Actions CI speed, reliability, libghostty cleanup, and helper consolidation. The old P0-P11 checklist should no longer be the source of truth. |
| #1705 `P4: repository-independent trusted CI gate App` | Close as superseded/rejected. The merged `cmd/cigate/**` and `internal/cigate/**` code should be removed by C1. |
| #1707 `P6: shadow planner and gate dual-run` | Close as superseded. Replace with C2: existing-Actions shadow summary, not planner/gate dual-run. |
| #1708 `P7: promote bounded dual-run lanes` | Close as superseded. Replace with C4 deterministic sample classes and C7 demonstrated simplifications. |
| #1709 `P8: trusted publication and credential boundary` | Close as superseded by current-workflow preservation unless a specific existing release workflow cleanup is needed. Any replacement issue must avoid new settings or credentials. |
| #1710 `P9: native and release producer-consumer adoption` | Rewrite or replace as C5 libghostty core-runtime CI cleanup. It should start from the fact that libghostty is core, not optional. |
| #1711 `P10: required-check cutover and rollback proof` | Close as rejected for this rollout. Required-check changes are not part of the plan. |
| #1712 `P11: retire repository-owned JS policy surfaces` | Rewrite as C6 targeted helper surface retirement. Keep inventory and semantic-parity work; remove broad language-porting acceptance. |

New issues should be C1-C7 sized. They should include exact expected files,
commands, rollback boundary, and whether they can run in parallel.

## Merge Order and Parallelism

1. C0 merges first.
2. C1 and C2 can proceed in parallel because the shadow summary must not reuse
   `cigate`.
3. C3 follows C2 so pruning is based on the actual summary/helper imports.
4. C4 depends on C2's summary output. It may run before or beside C3 when a
   fixture proves a helper should be retained.
5. C5 depends on C4's libghostty runtime sample class.
6. C6 can start after C2, but helpers that protect trust boundaries should wait
   for their C4 semantic fixtures.
7. C7 follows the relevant sample-class evidence and should stay split by
   workflow or helper family.

No package depends on an external deployment. No package depends on a repository
settings change. No package depends on a fixed elapsed time window.

## Old-To-New Phase Mapping

| Old phase | New package |
| --- | --- |
| P0 baseline evidence | C2 local inventory signal first; C3 deletes retained live evidence. |
| P1 policy manifest | C2 uses only the static inventory/manifest it needs; C3 prunes the rest. |
| P2 plan/result policy | C3 deletes unless C2/C4 already proved a direct local caller. |
| P3 hermetic policy fixture | C4 deterministic sample classes. |
| P4 trusted CI gate App | C1 delete/reject. |
| P5 artifact/cache contracts | #1758 removes the artifact contract after finding no direct native/release caller; #1737 already deleted cache authority. Active native/release artifact validation stays in workflow/script tests. |
| P6 shadow planner and gate dual-run | C2 existing-Actions shadow summary. |
| P7 bounded dual-run lanes | C4 fixtures plus C7 PR-sized simplifications. |
| P8 trusted publication boundary | Preserve current release workflows; make only specific in-repository cleanup PRs. |
| P9 native/release adoption | C5 libghostty core-runtime cleanup. |
| P10 required-check cutover | Rejected for this rollout. |
| P11 JS policy retirement | C6 targeted helper retirement. |

## Other Notes

### References

- Corrected north-star design:
  `docs/design/2026-07-24-ci-north-star.md`.
- P11 helper-retirement design:
  `docs/design/2026-07-26-p11-js-policy-surface-retirement.md`.
- Current workflows under `.github/workflows/`.
- Retired workflow helper directory `.github/workflows/scripts/`.
- Current native/release helpers under `scripts/` and `macos/service/`.
- Corrective issue #1715 and superseded rollout issues #1700, #1705, and
  #1707-#1712.

### Testing

For C0:

```bash
git diff --check
```

Also inspect the corrected docs for prohibited legacy requirements and confirm
any remaining mentions are in non-goals, rejected alternatives, or issue
disposition.

For implementation PRs, use the acceptance commands listed in each work package
and widen to `go test ./...`, `go vet ./...`, race tests, integration tests, or
workflow-script tests in proportion to the files touched.

### Open questions

- Which libghostty cleanup in C5 produces the largest speed or reliability
  improvement without weakening core runtime coverage.
- Whether any JavaScript helper beyond the regen/native trust-boundary tests is
  worth replacing once C2 makes helper ownership visible.
