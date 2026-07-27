---
title: "Design Doc: CI North Star"
authors: graith maintainers
created: 2026-07-24
status: Draft (corrected for issue #1715)
reviewers: d0ugal
informed: maintainers, release owners, GUI owners, security owners
---

# CI North Star

This design resets the CI north star around the infrastructure graith already
has. The goal is faster, more reliable feedback with fewer brittle helper
surfaces, not a new CI control plane.

The current GitHub Actions checks remain the authority. The first new signal is
a visible, non-required shadow summary inside the existing Actions setup. It
must help maintainers see which checks are expected, why local detectors
escalated or skipped, and where helper logic is duplicated, while preserving
every existing credential, artifact, native, release, sandbox, and fail-safe
boundary.

## Background

graith is a Go daemon and CLI with Go/Swift protocol models, PTY and process
lifecycle code, sandbox backends, a document store, the libghostty-backed
terminal runtime, macOS/iOS GUI packages, documentation, generated fixtures,
and release publication. libghostty is now the core runtime path, not an
optional add-on. Its CI currently spans Go, Swift, shell, JavaScript,
Python, macOS, Ubuntu, native artifacts, generated-file checks, documentation
previews, release-shaped builds, secret scanning, CodeQL, Scorecard, and
dependency review.

That breadth has real value, but the feedback loop is too slow and hard to
reason about. Some policy checks are duplicated across workflow YAML, shell,
JavaScript, Go helpers, and documentation. Some landed north-star code now
describes an external trust root that this repository will not provision. The
corrected design keeps the useful local contracts and deletes or retires the
parts that only make sense with infrastructure outside the repository.

### Existing-Infrastructure Ceiling

This ceiling is a hard requirement for this rollout:

| Area | Allowed |
| --- | --- |
| Repository | The existing `d0ugal/graith` repository only. |
| Automation | Current GitHub Actions workflows, jobs, runners, actions, and repository-owned scripts. |
| Runners | GitHub-hosted Ubuntu and macOS runners already used by current workflows. |
| Tokens | Existing `GITHUB_TOKEN` behavior only. |
| Secrets | Existing publication secrets only where the current workflows already use them: `GPG_PASSPHRASE`, `GPG_PRIVATE_KEY`, and `RELEASE_TOKEN`. |
| Environments | The existing `github-pages` environment only. |
| Settings | Current repository settings. The launch audit found no repository rulesets, no repository variables, no external CI service, and no environment besides `github-pages`. Current classic branch protection on `main` has `enforce_admins` enabled, non-strict required status checks, and seven required GitHub Actions contexts. |

The current required contexts are `Test (macos-latest)`, `Lint`,
`Conventional commits`, `Test (ubuntu-latest)`,
`macOS (safehouse / Seatbelt)`, `Linux (nono / Landlock)`, and
`Native backend gate`. They remain authoritative in this rollout. The design
does not require adding, removing, renaming, or reconfiguring required checks.

The design must not require a GitHub App, bot, new machine identity, cloud
runtime, webhook service, external evaluator, KMS, signing service, database,
bucket, persistent replay service, another repository, disposable live fixture
repository, new secret, self-hosted runner, merge queue, `merge_group`, new
environment, ruleset, branch-protection setting, required-check setting,
scheduled waiting period, routine new weekly job, paid service, or new account.

These items may appear only as rejected or deferred alternatives outside this
rollout.

### Current Trust Boundary

The repository already has security-sensitive CI boundaries that the reset must
preserve:

| Boundary | Current protection to keep |
| --- | --- |
| Pull requests | Untrusted PR code must not receive publication credentials. Fork PRs run with downgraded token behavior. Same-repository write-capable paths must keep their explicit guards. |
| Privileged workflows | Publication and branch mutation paths must run from trusted refs and must not execute or source untrusted PR code while credentials are available. |
| Checkout credentials | `persist-credentials: false` stays explicit on untrusted or read-only checkouts. Jobs that need credentials must show why. |
| Token permissions | Job-level `permissions` stay explicit. Read-only jobs stay read-only. Write permissions stay confined to existing same-repository or trusted publication paths. |
| Path detection | Current fail-safe PR-file detection where already present for macOS, sandbox, native/libghostty, release, generated-file, docs-preview, and branch-mutation-sensitive paths remains intact. Detector failure must keep escalating rather than silently skipping. Existing path filters that are not fail-safe must not become credential boundaries. |
| Artifacts and native code | Existing libghostty/native artifact digest, manifest, archive-shape, source-build, and consumer checks remain intact. |
| Releases | PR release-shaped builds remain separate from trusted publication; current release credentials stay unavailable to untrusted code. |
| Workflow source | This design does not claim GitHub Actions provides source isolation for PR-changed workflow logic. A PR can affect diagnostic jobs that run from its workflow definition, so those jobs are not an authority. |

The corrected design can add visibility inside Actions, but it cannot convert a
PR-controlled workflow or helper into a trusted gate by naming it differently.

## Problem

The previous north-star design overshot the repository's real constraints. It
defined an external gate, App-owned check, live fixture repository, external
replay store, deployment digest, ruleset cutover, and calendar-based acceptance
window. Those requirements are outside the allowed infrastructure and would add
new operational dependencies before improving day-to-day CI speed or
reliability.

The practical problems to solve are narrower:

- PR feedback is spread across many workflows, making it hard to see which jobs
  matter for a change and why they ran.
- Some checks are slow or flaky because routing, artifact, and release logic is
  duplicated across languages and scripts.
- The libghostty migration left some CI shape that still reads like an
  additional native lane instead of the core runtime validation path.
- Helper language sprawl makes CI behavior harder to review. JavaScript, shell,
  Python, and Go all have legitimate current uses, but policy-like logic should
  not be duplicated across all of them.
- Existing required checks must remain authoritative while any replacement
  signal proves value.
- Any simplification must keep the current fail-closed credential, artifact,
  native, sandbox, generated-file, and release boundaries.

## Goals

- Improve CI speed and reliability using only current GitHub Actions
  infrastructure.
- Produce a visible, non-required shadow summary quickly from an existing
  workflow, while current checks remain authoritative.
- Make current CI routing, job intent, skip reasons, required contexts, and
  helper ownership easier to inspect per PR. Speed evidence can use the normal
  GitHub Actions UI or specific job-local measurements, not a new
  repository-wide aggregator.
- Treat libghostty as the core runtime CI surface and clean up leftover
  add-on-style routing only when equivalent core coverage is explicit.
- Prefer static workflow and job composition over a new planner, control plane,
  webhook, or external gate.
- Keep useful landed inventory, policy, fixture, artifact, and helper-retirement
  code only where it reduces risk or duplication.
- Remove or retire landed code that exists only to support prohibited external
  infrastructure.
- Consolidate CI helper languages only when doing so removes duplicated policy,
  removes fragile parsing, or improves tests. Do not port working JavaScript
  merely to change language.
- Use bounded evidence from deterministic fixtures and explicit sample change
  classes, not elapsed calendar windows.

### Non-Goals

- Do not change workflow YAML, production Go code, generated metadata, GitHub
  settings, issue hierarchy, or release configuration in the corrective design
  PR.
- Do not require a GitHub App, external evaluator, live fixture repository,
  replay service, new environment, new secret, self-hosted runner, merge queue,
  `merge_group`, ruleset, branch protection, or new required check.
- Do not move publication credentials closer to PR code.
- Do not weaken current path detection, artifact verification, native checks,
  release-shaped PR builds, docs-preview same-repository guards, or regen
  branch-mutation isolation.
- Do not preserve landed north-star code just because it already merged.
- Do not require routine weekly jobs, fixed waiting periods, or calendar-based
  burn-in to accept a PR-sized improvement.

## Platform support

| Surface | Support in this design |
| --- | --- |
| CLI and daemon | Covered by current Go checks and any local helper packages retained for CI summaries or contracts. |
| Linux CI | Covered on existing GitHub-hosted Ubuntu runners. |
| macOS CI | Covered on existing GitHub-hosted macOS runners and current fail-safe routing. |
| GUI and Swift packages | Covered by existing GUI workflows and current macOS validation. |
| iOS | Covered only through existing GUI package build/test surfaces. No new simulator or device infrastructure is introduced. |
| Documentation | Covered by existing docs preview and Pages workflows, using the existing `github-pages` environment. |
| Libghostty runtime | Covered by existing native and release artifact checks because this is the core terminal runtime path. Any new helper must consume those contracts rather than replace them with a weaker one. |
| Release publication | Current trusted release workflows remain the boundary. PR code does not get release credentials. |
| External CI infrastructure | Not supported in this rollout. |

## Proposals

### Proposal 0: Do Nothing

Keeping the previous north-star as-is would leave the repository pointed at an
external App-owned gate, live fixture repository, replay service, and settings
cutover that are outside the approved infrastructure. It would also keep
helper cleanup blocked behind that unavailable control plane, while the current
CI remains slow to understand and libghostty cleanup stays mixed with stale
add-on framing.

Doing nothing preserves the current required checks, but it does not solve the
speed, reliability, or helper-sprawl problems. It also leaves landed
`cigate`/gate-era code looking like a future requirement even though the
repository will not provision the infrastructure it needs.

### Proposal 1: Existing-Actions CI Reset (Recommended)

Reset the north-star to a small in-repository sequence: add one diagnostic
summary inside existing Actions, prove routing with deterministic examples,
delete external-gate code, and simplify helpers only when a concrete caller
shows value. This proposal is recommended because it starts producing visible
information quickly while current required checks and current branch protection
remain the authority.

#### Existing-Actions Shadow Summary

Add one visible, non-required `CI shadow summary` job in an existing workflow,
most likely `ci.yml`, after this corrective design is merged. It runs with
read-only permissions, no secrets, and checkout credential persistence disabled.
It writes to `GITHUB_STEP_SUMMARY` and may upload an ordinary diagnostic
artifact using actions already present in the repository.

The first version must be static and simple. It must not try to aggregate
repository-wide Actions results, independent workflow jobs, or cross-workflow
durations. With `permissions: contents: read`, it should rely on checked-in
workflow inventory, local detector outputs, and files already available in the
job. If a later PR wants live check-run data, that PR must name the exact
existing `GITHUB_TOKEN` permission and completion semantics it will use, and
must still keep the job non-required.

| Summary section | Source |
| --- | --- |
| PR change class | Existing changed-file detector outputs and deterministic path rules. |
| Expected current checks | A checked-in inventory of current workflows, job names, and the seven current required contexts. |
| Skip/escalation reasons | Existing detector outputs, including fail-safe escalation paths. |
| Helper surface inventory | Repository-owned workflow scripts and language surfaces, grouped by owner and caller. |
| Timing pointers | Links or labels that send maintainers to the normal Actions UI for durations, not a synthesized duration gate. |

The job initially succeeds unless its own parser or summary generation fails.
Local inventory or detector mismatches are reported visibly but do not block. A
later PR may make specific diagnostic mismatches fail the non-required job after
deterministic fixtures prove the behavior, but the current required checks
remain the authority.

This is intentionally not a trusted gate. On pull requests, the workflow source
and helper code can be affected by the PR under review. That is acceptable only
because the summary is diagnostic, receives no privileged credentials, and does
not decide mergeability.

#### Deterministic Equivalence Before Simplification

Every workflow simplification must be justified by explicit evidence from
sample change classes or deterministic fixtures. No package waits for a fixed
number of days or weeks.

Required sample classes:

| Class | Evidence target |
| --- | --- |
| Go-only change | Current Go tests, build matrix, coverage behavior, and no unrelated native/release escalation. |
| Docs-only change | Docs preview behavior, Pages isolation, and no unnecessary Go/native work beyond current required checks. |
| GUI-only change | Existing GUI/macOS path routing and required-check satisfaction semantics. |
| Sandbox change | Sandbox jobs escalate and fail closed on detector errors. |
| Libghostty runtime change | Native source-build, artifact, manifest, archive, and consumer contracts still run because libghostty is core, not optional. |
| Generated-metadata change | Protocol/capability/generated fixture drift is caught by existing tests. |
| Release-path change | Release-shaped PR builds run without publication credentials. |
| Workflow/script change | Workflow lint, shellcheck, actionlint/zizmor provenance, and script tests cover the changed logic. |
| Fork PR behavior | No publication credentials, docs-preview write path, or regen branch mutation is available to fork code. |
| Same-repository mutation path | Existing same-repository guards and fresh-runner separation still hold for docs-preview and regen. |

For speed claims, the evidence is the affected sample class on the exact PR
head: job count, skipped-job count, command count, and per-job duration
observations before and after the change. Those observations can justify a PR,
but they are not a calendar acceptance gate.

#### Helper Surface Consolidation Policy

The repository can reduce the "weird collection of multiple languages" without
creating churn:

| Language surface | Corrected policy |
| --- | --- |
| Go | Prefer for reusable CI contracts, static workflow inventory, deterministic fixtures, and checks that benefit from typed tests. |
| Shell | Keep for thin runner glue, platform setup, release scripts, and native build orchestration where it already maps directly to command-line tools. |
| JavaScript | Keep existing tested helpers when they are small or GitHub/Node-oriented. Replace when the owner-approved migration deletes duplicated policy, removes an extra dependency surface such as the retired C6 `pngjs` install, or materially improves reliability. |
| Python | Do not add repository-owned Python CI policy or archive-helper surfaces; C6 migrates the libghostty archive helper to Go and deletes the Python script. |
| YAML | Keep declarative workflow routing explicit. Avoid putting complicated policy in YAML expressions when a tested helper is clearer. |

The P11 helper-retirement program remains useful only as an inventory and
targeted consolidation effort. It should not port working JavaScript to Go just
to reduce language count. Each retirement PR must delete or simplify a concrete
caller, preserve semantic tests, and show that rollback is a file-level revert.

#### Landed Component Audit

The corrective implementation plan must classify landed north-star components
before more code is added.

| Component | Disposition | Rationale | Dependencies | Rollback boundary |
| --- | --- | --- | --- | --- |
| `.github/workflows/*.yml` | Keep | Current workflows are the authoritative checks and publication boundary. | None. | Workflow-specific revert. |
| `.github/workflows/scripts/docs-diff*` | Retired by C6 | The owner-approved Go migration preserves PNG diff semantics, deletes the docs-preview `pngjs` install and package lock, and keeps rollback as one helper/caller restoration. | `cmd/docsdiff` parity tests. | Restore the helper family, package lock, and workflow caller in one revert. |
| `.github/workflows/scripts/docs-preview*` | Keep, then consider targeted simplification | Same-repository and cleanup logic is security-sensitive and already tested. | Existing docs preview workflow. | Revert docs-preview PR. |
| `.github/workflows/scripts/regen-auth.test.js` | Retired by C2 | Semantic Go tests now protect the branch-mutation trust boundary. | Regen workflow. | Revert the replacement and restore the JS test. |
| `.github/workflows/scripts/libghostty-policy.test.js` | Retired by C2 | Native/release contracts moved to semantic Go policy tests. | Native/release workflows and scripts. | Revert the replacement and restore the JS test. |
| Other workflow script tests | Partially retired by C2 and C6 | Shellcheck, Renovate, supply-chain verifier, and docs-diff behavior moved to semantic Go tests; docs-preview JavaScript tests remain. | Existing workflow-lint job. | Revert individual simplification. |
| `scripts/libghostty-native.sh` | Keep, then simplify cautiously | Current native artifact/source/consumer checks protect the core runtime. Simplification should remove leftover add-on framing or duplication, not coverage. | Native and release workflows. | Script-specific revert. |
| `cmd/libghosttyarchive/**`, `internal/libghosttyarchive/**` | Migrated by C6 | Deterministic archive shape verification remains useful and tested, but repository-owned archive tooling moves from Python to Go and deletes `scripts/libghostty-linux-archive.py`. | Native/release artifact paths. | Revert the Go helper, restore the Python script, and restore its callers together. |
| Release rendering and publish scripts | Keep | Current trusted publication workflows depend on them; no new publication boundary is introduced. | Existing release workflows and secrets. | Script-specific revert. |
| macOS release helpers | Keep | Existing optional signing/notarization and archive checks stay as current release logic. | Current release workflows. | Helper-specific revert. |
| `cmd/cibaseline/**` | Simplify | A local inventory command is useful; GitHub-history collection and retained proof are no longer part of acceptance. | Corrected static inventory package. | Remove command or revert to previous local-only behavior. |
| `internal/cibaseline/inventory.go`, `inventory_test.go`, `inventory.json` | Keep | Static workflow inventory can feed the shadow summary and drift tests. | Current workflow files. | Regenerate or delete with the summary PR. |
| `internal/cibaseline/github.go`, `evidence.go`, `acceptance.go`, retained evidence fixtures | Revert/delete | Historical windows, retained live evidence, and mature-run acceptance are outside the corrected evidence model. | None after the design reset. | Delete in one PR; rollback restores files only. |
| `cmd/cipolicy/**` | Simplify | A local validation/report command may be useful, but not a gate or deployment artifact. | Static summary and deterministic fixtures. | Remove command if no caller lands. |
| `internal/cipolicy/manifest.go`, `build.go`, `validate.go`, `io.go`, `manifest.json` | Simplify | Keep only the parts needed for current workflow inventory and local validation. | Current workflows. | Regenerate or delete with static inventory PR. |
| `internal/cipolicy/plan.go`, `result.go`, `fixture.go` | Revert/delete unless a concrete C2/C4 caller lands first | Full plan/result fan-in was designed for the external gate. Do not preserve it as speculative infrastructure. | A direct summary or fixture caller, otherwise none. | Delete in the helper-pruning PR; rollback restores files only. |
| `internal/cipolicy/artifact.go` and tests | Keep | Artifact identity checks match current native/release risks. Wire them only where they replace duplication. | Native/release scripts. | Revert helper wiring. |
| `internal/cipolicy/cache.go` and tests | Revert/delete unless a current native/release caller is proven | Cross-run cache authority is not needed for the corrected rollout. | A concrete artifact/native caller, otherwise none. | Delete in the helper-pruning PR; rollback restores files only. |
| `internal/cipolicy/p11_js_surface.go` and tests | Simplify | Retain inventory and semantic comparison value; drop broad porting assumptions. | Workflow script tests. | Revert individual helper-retirement PR. |
| `cmd/cigate/**` | Revert/delete | The command exists for an external evaluator and live proof path that is prohibited. | None in current workflows. | Delete command and architecture metadata in one PR. |
| `internal/cigate/**` | Revert/delete | It requires App contracts, webhook signatures, replay storage, live proof bundles, deployment digests, and `merge_group`. | None in current workflows. | Delete package and tests in one PR. |
| `website/content/docs/contributing/ci-gate.md` | Revert/delete | User documentation should not describe an external gate outside the corrected rollout. | `cmd/cigate` removal. | Revert docs deletion if the command is restored later. |
| Other CI north-star user docs for baseline/policy/fixture/artifact helpers | Rewrite or prune with their code changes | User docs must match what remains callable after simplification. | The corresponding follow-up PR. | Revert docs and code together. |

### Proposal 2: External Gate and Control-Plane Alternatives

| Alternative | Decision |
| --- | --- |
| External GitHub App or App-owned check | Rejected for this rollout. It requires new identity, deployment, secrets, and settings. |
| External webhook/evaluator/replay service | Rejected for this rollout. It is outside existing infrastructure and does not address CI speed first. |
| Disposable live fixture repository | Rejected for this rollout. Deterministic in-repository fixtures and sample classes are enough for the corrected evidence model. |
| Merge queue or `merge_group` reliance | Rejected for this rollout. Current repository settings do not provide this and the plan cannot require it. |
| Ruleset, branch-protection, or required-check cutover | Rejected as an implementation-plan requirement. Current checks stay authoritative until a later in-repository change has direct evidence and a reversible reason. |
| Calendar burn-in or new routine weekly jobs | Rejected. Acceptance uses explicit sample classes and deterministic fixtures. |
| Wholesale JavaScript-to-Go port | Rejected. Helper consolidation must remove duplicated policy or improve reliability. |

## Other Notes

### References

- Issue #1715: corrective design task.
- Epic #1700 and sub-issues #1705, #1707, #1708, #1709, #1710, #1711, and #1712:
  current issue hierarchy to close, rewrite, or replace after this design lands.
- Current workflows under `.github/workflows/`.
- Current workflow helpers under `.github/workflows/scripts/`.
- Current native and release scripts under `scripts/` and `macos/service/`.
- P11 helper-retirement design:
  `docs/design/2026-07-26-p11-js-policy-surface-retirement.md`.

### Implementation Notes

The implementation plan replaces the old P4-P10 external cutover with a short
sequence:

| Old phase | Corrected mapping |
| --- | --- |
| P0 baseline evidence | Keep static inventory value; drop retained live evidence windows. |
| P1 policy manifest | Reduce to local inventory/summary contracts. |
| P2 plan/result policy | Delete unless the minimal summary or deterministic fixtures already prove a direct local caller. |
| P3 hermetic fixture | Keep deterministic fixture idea, not external acceptance. |
| P4 trusted App | Rejected and scheduled for deletion. |
| P5 artifact/cache contracts | Keep artifact validation where it strengthens current native/release checks; delete cache authority unless a direct caller exists. |
| P6 shadow planner/gate | Replace with existing-Actions shadow summary. |
| P7 dual-run promotion | Replace with deterministic sample-class comparisons. |
| P8 trusted publication boundary | Keep current trusted release workflows; no new credential boundary. |
| P9 native/release producer-consumer adoption | Keep current native/release protections and make only demonstrated simplifications. |
| P10 required-check cutover | Rejected for this rollout. Current checks remain authoritative. |
| P11 JS policy retirement | Narrow to targeted helper simplification with semantic parity. |

After the corrective PR merges, close old external-gate issues rather than
continuing to treat them as blockers. New implementation issues should be small
and PR-sized.

### Testing

For this design-only PR:

- Validate frontmatter and section consistency by inspection against
  `docs/design/TEMPLATE.md`.
- Search the two corrected documents for prohibited legacy requirements and
  keep any remaining mentions explicitly rejected.
- Cross-check every proposed requirement against current workflow resources and
  the launch audit ceiling.
- Run `git diff --check`.

Follow-up implementation PRs must run focused package tests for the files they
touch, relevant workflow-script tests, and wider Go tests in proportion to the
change.

### Open questions

No external-infrastructure decisions are open for this rollout. The main open
implementation choice is how much of the existing `cibaseline` and `cipolicy`
code is simpler to reduce versus delete after the minimal shadow-summary PR
shows its actual caller shape.
