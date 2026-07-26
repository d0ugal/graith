---
title: "Design Doc: P11 JS Policy Surface Retirement"
authors: graith maintainers
created: 2026-07-26
status: Draft (foundation tranche)
reviewers: (none yet)
informed: ci-north-star rollout owners
---

# P11 JS Policy Surface Retirement

P11 retires the repository-owned JavaScript helpers under
`.github/workflows/scripts/` one compatibility tranche at a time. This first
foundation tranche does not edit those helpers: it names the surface, records
owner contracts and deletion criteria, adds a Go comparison harness beside the
P2/P3 policy/result fixture, and shadows the `regen.yml` trust assertions
semantically without moving the signed P0/P1 checksums.

## Background

The CI north-star plan treats the scripts directory as policy, not convenience
code. Those helpers currently enforce or test credential boundaries, workflow
routing, native artifact policy, screenshot publication, and workflow-lint
supply-chain rules. Some files are runtime helpers invoked by workflows; others
are tests that guard workflow text because the current behavior still executes
inside GitHub Actions YAML and shell blocks.

P0 already inventories the files as policy surfaces, while P2/P3 provide the
Go manifest, plan, result, fan-in, credential-operation, and hermetic fixture
APIs. P11 consumes those APIs. It must not bulk-port scripts, delete paths, or
reshape workflow YAML before compatibility evidence exists.

## Problem

Replacing JavaScript tests with Go tests looks mechanically simple, but each
edit changes signed P0 inventory checksums and the P1 manifest digest. Doing
that while P4/P5 are active would invalidate the current accepted baseline
binding. The raw `regen-auth.test.js` regex assertions also cannot be deleted
until a serialized epoch updates that baseline and proves the semantic Go
assertions cover the same security claims.

## Goals

- Inventory every current file under `.github/workflows/scripts/` with owner,
  callers, policy inputs/outputs, disposition, deletion criterion, and sample
  requirements.
- Add deterministic Go foundation code that validates the P11 inventory against
  the current P0 inventory and script directory.
- Add a comparison harness that uses `BuildHermeticPlan`,
  `GenerateWorkflowData`, `FanInFixture`, and credential-operation validation.
- Shadow `regen.yml` trust assertions semantically in Go without editing
  `regen-auth.test.js`.
- Record the ordered remaining P11 tranches and the P0/P1 regeneration chain
  required before the next baseline-bound helper edit.

### Non-Goals

- Replacing `regen-auth.test.js` in this PR.
- Retiring, deleting, or modifying any existing JavaScript helper in this PR.
- Editing workflow YAML, generated metadata, `internal/cibaseline/inventory.json`,
  or `internal/cipolicy/manifest.json` in this PR.
- Replacing `pngjs`; it remains the explicit retained exception for PNG
  decoding and encoding.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI/server | Targeted | P11 contracts and comparisons live in Go under `internal/cipolicy`. |
| macOS | Excluded | This tranche changes policy metadata and tests only. |
| iOS | Excluded | This tranche has no GUI runtime behavior. |
| GitHub Actions | Observed | Existing workflow behavior is parsed and asserted, but not changed. |

## Proposals

### Proposal 0: Do Nothing

Leave the JS helpers as informal workflow implementation details. This keeps
the current signed inventory stable but fails the P11 exit criteria: there is
no helper-by-helper owner contract, no deletion criterion, and no semantic
replacement path for regex-based security assertions.

### Proposal 1: Foundation Before Edits (Recommended)

Add a Go-owned P11 surface inventory and comparison harness first, without
touching existing helpers. The harness records compatibility samples as data,
loads current P0/P1 inputs, builds hermetic Go plans, fans in synthetic
successful observations, and validates credential operations against the P3
fixture policy.

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

This creates executable evidence without invalidating baseline-bound files.

### Proposal 2: Replace `regen-auth.test.js` Immediately

Delete or rewrite the JS test now and regenerate P0/P1 in the same PR. Rejected
for this tranche because it would change signed baseline checksums while P4/P5
are active. That update needs a serialized epoch with owner-approved inventory
rebind and manifest regeneration.

## Helper Inventory

Every entry below is owned by `graith-maintainers`.

### `docs-diff-run.js`

- Callers: `docs-preview.yml` screenshot diff step.
- Inputs: `pages.json`, base/head screenshot directories, PNG files decoded via
  `pngjs`.
- Outputs: flat screenshot directory, `manifest.json`, count summary.
- Disposition: explicitly retained with `docs-diff.js`.
- Deletion criterion: byte-equivalent manifests and images for added, deleted,
  same, row-change, and divergent screenshot samples.

### `docs-diff.js`

- Callers: `docs-diff-run.js`, manual CLI, `docs-diff.test.js`.
- Inputs: base/head PNGs, row hashes, hunk settings.
- Outputs: diff PNG with exit 0, no output with exit 3 for identical renders,
  exit 2 for invalid CLI arguments.
- Disposition: explicitly retained because `pngjs` is the P11 exception.
- Deletion criterion: pure row-diff and PNG render outputs match existing
  helper over synthetic and captured screenshots; `pngjs` remains unless a
  later owner-approved image-decoding decision replaces it.

### `docs-diff.test.js`

- Callers: `workflow-lint.yml` scripts job.
- Inputs: `docs-diff.js` pure API and synthetic RGBA rows.
- Outputs: Node test pass/fail for row hashing, Myers alignment, denoise,
  hunking, and render geometry.
- Disposition: port after `docs-diff.js` pure logic is wrapped or ported.
- Deletion criterion: Go tests cover the same row-diff sample matrix and
  docs-preview retains PNG parity evidence.

### `docs-preview.js`

- Callers: `docs-preview.yml` publish, cleanup, and prune jobs;
  `docs-preview.test.js`.
- Inputs: GitHub PR context, screenshots branch ref/tree, comments, wall clock.
- Outputs: screenshots branch commits, sticky comments, fork write no-op,
  fail-closed truncated-tree errors.
- Disposition: wrap.
- Deletion criterion: GitHub API fixture and Go policy fixture agree on fork
  skip, same-repo publish, truncated tree rejection, empty-tree commit, and ref
  race retries.

### `docs-preview.test.js`

- Callers: `workflow-lint.yml` scripts job.
- Inputs: `docs-preview.js`, fake GitHub clients, synthetic PR contexts.
- Outputs: Node test pass/fail for branch rewrite and write-boundary behavior.
- Disposition: port with `docs-preview.js`.
- Deletion criterion: Go semantic tests cover the same API state transitions
  and P2/P3 credential-operation boundaries.

### `libghostty-policy.test.js`

- Callers: `workflow-lint.yml` scripts job.
- Inputs: native, release, coverage workflows; `scripts/libghostty-native.sh`;
  `libghostty-native.lock.json`.
- Outputs: Node test pass/fail for native/release/coverage policy.
- Disposition: port after P5 artifact/cache contracts merge.
- Deletion criterion: Go semantic tests compare workflow YAML, lock data, and
  shell policy with P2/P3 capability and artifact contracts.

### `package.json` and `package-lock.json`

- Callers: `docs-preview.yml` `npm ci --prefix .github/workflows/scripts
  --ignore-scripts`.
- Inputs: pinned `pngjs` dependency declaration and integrity lock.
- Outputs: deterministic `pngjs` install for docs-preview image decoding.
- Disposition: explicitly retained.
- Deletion criterion: delete only if docs-preview no longer uses `pngjs` or an
  owner-approved replacement lock exists.

### `regen-auth.test.js`

- Callers: `workflow-lint.yml` scripts job.
- Inputs: `regen.yml`, PR trust context, `RELEASE_TOKEN` exposure sites,
  checkout persistence, generated commit bundle, push script.
- Outputs: Node test pass/fail for regeneration credential and publication
  boundaries.
- Disposition: port in the next serialized tranche.
- Deletion criterion: Go semantic assertions and P2/P3 trust fixture pass,
  old-vs-new comparison has zero unexplained disagreement, P0 inventory is
  rebound, and P1 manifest is regenerated.

### `renovate-retry.test.js`

- Callers: `workflow-lint.yml` scripts job.
- Inputs: `scripts/verify-renovate-libghostty.sh`, fake Renovate binaries,
  Renovate JSON logs.
- Outputs: Node test pass/fail for bounded transient retry behavior.
- Disposition: port.
- Deletion criterion: Go tests drive the shell verifier through fake binaries
  and match retry count/stdout/stderr/status for transient, deterministic,
  mixed, and repeated failures.

### `shellcheck-policy.test.js`

- Callers: `workflow-lint.yml` scripts job.
- Inputs: `Makefile`, `workflow-lint.yml`.
- Outputs: Node test pass/fail for ShellCheck coverage and strictness.
- Disposition: port.
- Deletion criterion: Go semantic tests prove tracked shell scripts and
  nested/root shell path filters remain covered.

### `workflow-lint-supply-chain.test.js`

- Callers: `workflow-lint.yml` scripts job.
- Inputs: `workflow-lint.yml`, actionlint and zizmor install steps.
- Outputs: Node test pass/fail for provenance-verified workflow-lint installs.
- Disposition: port.
- Deletion criterion: Go semantic tests prove attestation verification happens
  before extraction/install and cannot be swallowed.

## Next Serialized Tranche

The proposed next helper is `regen-auth.test.js`. It is low runtime risk
because it is test-only, but high trust importance because it guards credential
publication boundaries and is a prerequisite to any later workflow YAML
reshaping.

Compatibility sample matrix:

| Sample | Expected evidence |
|--------|-------------------|
| same-repository agent PR | Existing workflow keeps jobs same-repo guarded; generated input produces the documented superset reason; soft regen manifest rows remain outside required fan-in; same-repository maintainer-token access is rejected. |
| fork PR | Event classifies as `fork-untrusted`; soft regen rows are limited to same-repository/trusted-base tiers; workflow same-repo guards skip credentialed jobs. |
| trusted-base PR replay | Event classifies as `trusted-base`; generated-metadata policy remains selected for replay while soft regen rows stay outside required fan-in. |
| push boundary | Credentialed checkout is isolated to the fresh runner; generated commit parent, identity, allowlisted paths, and non-force push are verified. |

Required chain before editing that helper:

1. Wait for P4/P5 active branches to merge or for rollout owners to approve a
   new serialized baseline epoch.
2. Replace the JS regex/slicing assertions with the semantic Go contract, or
   retain both temporarily until parity is proven.
3. Run the old and new assertions over the documented sample matrix; zero
   unexplained disagreement is required.
4. Regenerate P0 inventory:
   `go run ./cmd/cibaseline -output internal/cibaseline/inventory.json generate`.
5. Add the owner-approved P0 inventory rebind/acceptance metadata for the new
   policy-surface checksum delta. The current acceptance validator only admits
   the earlier Makefile rebind, so this is an explicit epoch update, not a
   mechanical checksum refresh.
6. Regenerate P1 manifest from the rebound inventory:
   `go run ./cmd/cipolicy -output internal/cipolicy/manifest.json generate`.
7. Run `go test ./internal/cibaseline ./internal/cipolicy`, JS workflow-script
   tests, actionlint/shellcheck where relevant, and the wider validation
   required by the changed baseline epoch.

Pre-deletion hardening required by the `regen-auth.test.js` tranche:

- Add a whole-document scalar sweep, or equivalent parsed coverage, so
  `secrets.RELEASE_TOKEN`, `github.token`, and `GITHUB_TOKEN` cannot hide in
  fields not projected into the structural workflow summary.
- Replace declarative credential trust-tier fields with an explicit
  plan-trust-to-credential-trust allowlist, and bind denied credential
  expectations to the plan when the sample claims a plan-specific boundary.
- Add a non-superset compatibility sample so generated-metadata capability
  selection and credential-to-plan capability binding can fail for the right
  reason.
- Align the current-helper enumerator with P0's git-index source of truth, not
  transient filesystem entries such as ignored `node_modules/`.
- Make repository-controlled command detection at least as strict as the
  retained JS assertion for embedded `go test`, `make package-graph`, and
  `scripts/libghostty-native.sh` invocations.

## Remaining Tranche Order

1. `regen-auth.test.js`: semantic replacement and P0/P1 serialized rebind.
2. `renovate-retry.test.js`: port shell-verifier retry contract using fake
   binaries and log fixtures.
3. `shellcheck-policy.test.js`: port text-only ShellCheck coverage contract.
4. `workflow-lint-supply-chain.test.js`: port actionlint/zizmor provenance
   install contract.
5. `docs-diff.test.js` and pure `docs-diff.js` row logic: port or wrap pure
   row operations while retaining `pngjs`.
6. `docs-preview.test.js` and `docs-preview.js`: wrap GitHub API branch/comment
   mutation paths after credential-boundary tests are semantic.
7. `libghostty-policy.test.js`: port native/release/coverage policy after P5
   artifact/cache contracts are merged and available to compare against.
8. `package.json` and `package-lock.json`: retain as the explicit `pngjs`
   exception until docs-preview no longer needs that dependency.

## Other Notes

### Testing

The foundation tests live in `internal/cipolicy/p11_js_surface_test.go`.
They validate the hardcoded P11 inventory against the current script directory
and P0 inventory, run the P11 compatibility matrix through the P2/P3 plan and
fan-in APIs, and parse `regen.yml` structurally so YAML formatting cannot erase
the trust assertions.

The safe prerequisite tranche for `regen-auth.test.js` adds semantic parity
coverage and pre-deletion hardening while retaining the helper. Helper deletion,
P0 inventory acceptance rebind, and P1 manifest regeneration remain the next
owner-approved serialized action.

### Open questions

- The next epoch needs owner approval for the exact P0 acceptance rebind shape.
- The docs-preview tranche needs a retained live or high-fidelity fake GitHub
  API sample before deleting the JS branch-mutation helper.
