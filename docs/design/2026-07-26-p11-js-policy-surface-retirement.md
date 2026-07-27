---
title: "Design Doc: P11 JS Policy Surface Retirement"
authors: graith maintainers
created: 2026-07-26
status: Draft (serialized deletion tranche)
reviewers: (none yet)
informed: ci-north-star rollout owners
---

# P11 JS Policy Surface Retirement

P11 retires the repository-owned JavaScript helpers under
`.github/workflows/scripts/` one compatibility tranche at a time. This
serialized deletion tranche removes the small test-only policy scripts whose
claims now have semantic Go coverage and regenerates the P0/P1 metadata in the
same owned epoch. Its original docs-diff/`pngjs` and docs-preview runtime-helper
retention carve-outs were superseded by owner-approved C6 Go migrations after
equivalent parity coverage was established.

## Background

The CI north-star plan treats the scripts directory as policy, not convenience
code. Those helpers currently enforce or test credential boundaries, workflow
routing, native artifact policy, screenshot publication, and workflow-lint
supply-chain rules. Some files are runtime helpers invoked by workflows; others
are tests that guard workflow text because the current behavior still executes
inside GitHub Actions YAML and shell blocks.

P0 already inventories the files as policy surfaces, while current Go workflow
policy tests preserve the live routing and credential semantics. The original
P11 closed-world compatibility and checksum rebind harness proved useful during
the migration, but it is no longer an active policy surface once the retained
JavaScript helper set is empty.

## Problem

Replacing JavaScript tests with Go tests looks mechanically simple, but each
edit changes signed P0 inventory checksums and the P1 manifest digest. The
serialized epoch therefore has to update generated metadata together with the
semantic Go tests that own the replacement behavior. Helper churn must not be
ignored generically: a tracked helper under `.github/workflows/scripts/` is now
rejected by the P0 inventory generator, while authoritative job/context changes
remain manifest drift failures.

## Goals

- Inventory every retained file under `.github/workflows/scripts/` with owner,
  callers, policy inputs/outputs, disposition, deletion criterion, and sample
  requirements.
- Retire the approved JS files with explicit Go replacements:
  `libghostty_policy_test.go`, `p11_js_surface_test.go`,
  `renovate_retry_test.go`, `workflow_lint_policy_test.go`, and the
  docs-preview mutation helper/tests via `cmd/docspreview` and
  `internal/docspreview`.
- Keep the regen trust assertions semantic in Go, including whole-document
  credential scalar sweep, explicit plan-to-credential trust allowlist,
  non-superset negative sample, P0 rejection of tracked workflow script helpers,
  and strict repository-command detection.
- Regenerate P0 inventory and P1 manifest after deletion.
- Remove the empty retained-JavaScript inventory contract and compatibility
  sample harness after their migration objective is complete.

### Non-Goals

- Replacing unrelated JavaScript dependencies; the former `pngjs` exception was
  retired only as part of the C6 docs-diff Go migration, and browser-required
  JavaScript remains outside this tranche.
- Aggregating live Actions history, cross-workflow durations, or check-run
  completion state through C2.
- Loosening acceptance to ignore helper-surface changes outside the P0
  inventory guard and semantic workflow policy tests.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI/server | Targeted | Workflow parsing and semantic policy tests live in Go under `internal/cipolicy`. |
| macOS | Excluded | This tranche changes policy metadata and tests only. |
| iOS | Excluded | This tranche has no GUI runtime behavior. |
| GitHub Actions | Targeted | Workflow policy behavior is parsed and asserted by Go tests. |

## Proposals

### Proposal 0: Do Nothing

Leave the JS helpers as informal workflow implementation details. This keeps
the current signed inventory stable but fails the P11 exit criteria: there is
no helper-by-helper owner contract, no deletion criterion, and no semantic
replacement path for regex-based security assertions.

### Proposal 1: Serialized Go Replacement (Recommended)

Add or reuse Go-owned workflow parsing and semantic policy tests, then delete
only the named test-only JS files in the same baseline epoch. After every
helper under `.github/workflows/scripts/` was retired, the empty retained-JS
inventory contract and compatibility sample harness stopped being active
policy surfaces.

The `regen.yml` shadow test parses YAML into structured jobs, steps, `env`, and
`with` maps. Comments and formatting cannot satisfy it. It asserts:

- the workflow triggers only on `pull_request`;
- top-level token permissions remain `contents: read`;
- all regen jobs are same-repository guarded;
- the validation job receives `RELEASE_TOKEN` only for the empty-token check and
  runs no checkout or generator code;
- the preparation checkout is uncredentialed;
- the push checkout is the only persisted `RELEASE_TOKEN` checkout;
- the generated commit push verifies parent, identity, allowlisted paths, and
  uses a non-force branch update;
- P2 event selection distinguishes fork, same-repository-agent, and trusted-base
  PRs;
- P3 credential validation rejects same-repository maintainer-token access while
  allowing trusted-publication regeneration within its boundary.

This creates executable evidence and updates the baseline-bound files together.
Once the retained JavaScript set is empty, the P0 inventory guard rejects any
tracked workflow script helper instead of carrying an empty compatibility
mapping forward.

### Proposal 2: Generic Helper Churn Exemption

Allow acceptance to ignore any workflow-helper test deletion once Go tests
exist. Rejected because it is not closed-world: an unrelated helper removal,
missing replacement, mismatched checksum, or authoritative workflow change
could be masked as test migration. The retained guard is explicit instead: any
tracked helper under `.github/workflows/scripts/` fails P0 inventory, while
workflow behavior stays covered by semantic Go tests.

## Helper Inventory

Every entry below is owned by `graith-maintainers`.

### `docs-diff-run.js`

- Callers: `docs-preview.yml` screenshot diff step.
- Inputs: `pages.json`, base/head screenshot directories, PNG files decoded via
  `pngjs`.
- Outputs: flat screenshot directory, `manifest.json`, count summary.
- Disposition: retired by C6 and replaced by `cmd/docsdiff`.
- Deletion criterion: byte-equivalent manifests and images for added, deleted,
  same, row-change, and divergent screenshot samples.

### `docs-diff.js`

- Callers: `docs-diff-run.js`, manual CLI, `docs-diff.test.js`.
- Inputs: base/head PNGs, row hashes, hunk settings.
- Outputs: diff PNG with exit 0, no output with exit 3 for identical renders,
  exit 2 for invalid CLI arguments.
- Disposition: retired by C6 and replaced by `cmd/docsdiff`.
- Deletion criterion: pure row-diff and PNG render outputs match the retired
  helper over synthetic and captured screenshots.

### `docs-diff.test.js`

- Callers: `workflow-lint.yml` scripts job.
- Inputs: `docs-diff.js` pure API and synthetic RGBA rows.
- Outputs: Node test pass/fail for row hashing, Myers alignment, denoise,
  hunking, and render geometry.
- Disposition: retired by C6 and replaced by Go tests for `cmd/docsdiff`.
- Deletion criterion: Go tests cover the same row-diff sample matrix and
  docs-preview retains PNG parity evidence.

### `docs-preview.js`

- Callers: `docs-preview.yml` publish, cleanup, and prune jobs;
  formerly `docs-preview.test.js`.
- Inputs: GitHub PR context, screenshots branch ref/tree, comments, wall clock.
- Outputs: screenshots branch commits, sticky comments, fork write no-op,
  fail-closed truncated-tree errors.
- Disposition: retired by C6 and replaced by `cmd/docspreview` and
  `internal/docspreview`.
- Deletion criterion: satisfied by high-fidelity fake GitHub state-transition
  tests and REST request-shape tests covering fork skip, same-repo publish,
  truncated tree rejection, empty-tree commit handling, cleanup/prune rewrites,
  sticky comments, and ref race retries while preserving the same-repository
  mutation boundary.

### `docs-preview.test.js`

- Callers: formerly `workflow-lint.yml` scripts job.
- Inputs: formerly `docs-preview.js`, fake GitHub clients, synthetic PR
  contexts.
- Outputs: formerly Node test pass/fail for branch rewrite and write-boundary
  behavior.
- Disposition: retired by C6 and replaced by Go semantic tests under
  `internal/docspreview` and `cmd/docspreview`.
- Deletion criterion: satisfied by Go state-transition tests that cover the same
  API mutation paths and P2/P3 credential-operation boundaries.

### `libghostty-policy.test.js`

- Callers: `workflow-lint.yml` scripts job.
- Inputs: native, release, coverage workflows; `scripts/libghostty-native.sh`;
  `libghostty-native.lock.json`.
- Outputs: Node test pass/fail for native/release/coverage policy.
- Disposition: retired in this tranche.
- Replacement: `internal/cipolicy/libghostty_policy_test.go`.
- Deletion criterion: the Go test preserves native routing, fail-safe
  detector, release gating, artifact lock/publish trust, local build isolation,
  and tagged coverage graph assertions.

### `package.json` and `package-lock.json`

- Callers: `docs-preview.yml` `npm ci --prefix .github/workflows/scripts
  --ignore-scripts`.
- Inputs: pinned `pngjs` dependency declaration and integrity lock.
- Outputs: deterministic `pngjs` install for docs-preview image decoding.
- Disposition: retired by C6 with the docs-diff Go migration.
- Deletion criterion: docs-preview no longer uses `pngjs` and C6 parity tests
  cover the screenshot diff helper behavior.

### `regen-auth.test.js`

- Callers: `workflow-lint.yml` scripts job.
- Inputs: `regen.yml`, PR trust context, `RELEASE_TOKEN` exposure sites,
  checkout persistence, generated commit bundle, push script.
- Outputs: Node test pass/fail for regeneration credential and publication
  boundaries.
- Disposition: retired in this tranche.
- Replacement: `internal/cipolicy/p11_js_surface.go` and
  `internal/cipolicy/p11_js_surface_test.go`.
- Deletion criterion: Go semantic assertions and P2/P3 trust fixture pass,
  including the pre-deletion hardening matrix below; P0 inventory is rebound
  and P1 manifest is regenerated.

### `renovate-retry.test.js`

- Callers: `workflow-lint.yml` scripts job.
- Inputs: `scripts/verify-renovate-libghostty.sh`, fake Renovate binaries,
  Renovate JSON logs.
- Outputs: Node test pass/fail for bounded transient retry behavior.
- Disposition: retired in this tranche.
- Replacement: `internal/cipolicy/renovate_retry_test.go`.
- Deletion criterion: Go tests drive the shell verifier through fake binaries
  and match retry count/stdout/stderr/status for transient, deterministic,
  mixed, and repeated failures.

### `shellcheck-policy.test.js`

- Callers: `workflow-lint.yml` scripts job.
- Inputs: `Makefile`, `workflow-lint.yml`.
- Outputs: Node test pass/fail for ShellCheck coverage and strictness.
- Disposition: retired in this tranche.
- Replacement: `internal/cipolicy/workflow_lint_policy_test.go`.
- Deletion criterion: Go semantic tests prove tracked shell scripts and
  nested/root shell path filters remain covered.

### `workflow-lint-supply-chain.test.js`

- Callers: `workflow-lint.yml` scripts job.
- Inputs: `workflow-lint.yml`, actionlint and zizmor install steps.
- Outputs: Node test pass/fail for provenance-verified workflow-lint installs.
- Disposition: retired in this tranche.
- Replacement: `internal/cipolicy/workflow_lint_policy_test.go`.
- Deletion criterion: Go semantic tests prove attestation verification happens
  before extraction/install and cannot be swallowed.

## Serialized Rebind

Compatibility sample matrix:

| Sample | Expected evidence |
|--------|-------------------|
| same-repository agent PR | Existing workflow keeps jobs same-repo guarded; generated input produces the documented superset reason; soft regen manifest rows remain outside required fan-in; same-repository maintainer-token access is rejected. |
| fork PR | Event classifies as `fork-untrusted`; soft regen rows are limited to same-repository/trusted-base tiers; workflow same-repo guards skip credentialed jobs. |
| trusted-base PR replay | Event classifies as `trusted-base`; generated-metadata policy remains selected for replay while soft regen rows stay outside required fan-in. |
| push boundary | Credentialed checkout is isolated to the fresh runner; generated commit parent, identity, allowlisted paths, and non-force push are verified. |

Required chain for this epoch:

1. Delete only the named retired JS paths.
2. Add or update the named Go semantic replacements and their focused tests.
3. Regenerate P0 inventory:
   `go run ./cmd/cibaseline -output internal/cibaseline/inventory.json generate`.
4. Keep the P0 inventory guard that rejects any tracked helper under
   `.github/workflows/scripts/`; do not reintroduce an empty retained-JS
   compatibility mapping.
5. Regenerate P1 manifest from the rebound inventory:
   `go test ./internal/cipolicy -run TestCommittedManifestMatchesInventory -update`.
6. Run `go test ./internal/cibaseline ./cmd/cibaseline ./internal/cipolicy`,
   actionlint/shellcheck where relevant, and the wider validation required by
   the changed baseline epoch.

Pre-deletion hardening satisfied by the `regen-auth.test.js` replacement:

- Add a whole-document scalar sweep, or equivalent parsed coverage, so
  `secrets.RELEASE_TOKEN`, `github.token`, and `GITHUB_TOKEN` cannot hide in
  fields not projected into the structural workflow summary.
- Replace declarative credential trust-tier fields with an explicit
  plan-trust-to-credential-trust allowlist.
- Keep deterministic non-superset fixtures so generated-metadata capability
  selection and credential-to-plan capability binding can fail for the right
  reason.
- Keep the baseline inventory fail-closed if a tracked helper reappears under
  the retired `.github/workflows/scripts/` directory.
- Make repository-controlled command detection at least as strict as the
  retired JS assertion for embedded `go test`, `make package-graph`, and
  `scripts/libghostty-native.sh` invocations.

## Remaining Tranche Order

None. The C6 docs-preview Go migration completed the final repository-owned JS
helper tranche under `.github/workflows/scripts/`.

## Other Notes

### Testing

The replacement tests live in `internal/cipolicy/p11_js_surface_test.go`,
`internal/cipolicy/renovate_retry_test.go`,
`internal/cipolicy/workflow_lint_policy_test.go`, and
`internal/cipolicy/libghostty_policy_test.go`. They parse `regen.yml`
structurally, execute the Renovate verifier through fake binaries, and keep the
workflow-lint/native assertions executable in Go.

### Open questions

- Any later epoch needs owner approval for changes to the P0 workflow-script
  inventory guard shape.
- The docs-preview tranche's high-fidelity fake GitHub API sample now lives in
  `internal/docspreview`.
