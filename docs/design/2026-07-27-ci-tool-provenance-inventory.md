---
title: "Design Doc: CI Tool Provenance Inventory"
authors: graith maintainers
created: 2026-07-27
status: Draft
reviewers: d0ugal
informed: maintainers, release owners, security owners
issue: https://github.com/d0ugal/graith/issues/1765
---

# CI Tool Provenance Inventory

This audit records the remaining provenance posture for executable tools,
containers, and downloaded artifacts used by graith's GitHub Actions workflows.
It is intentionally bounded: the PR that adds this inventory does not replace
tooling, relax permissions, or change CI behavior. Follow-up fixes should be
small, prioritized by required and release-critical jobs, and independently
reversible.

## Background

The workflow files under `.github/workflows/` are the current source of truth
for CI jobs, action pins, artifact actions, cache actions, permissions, and
required contexts. They show that normal action references are commit-pinned,
but they do not classify every executable downloaded from `run:` blocks or
every tool fetched by a pinned action.

Existing workflow hardening already covers several high-risk cases:

- workflow action references are full-commit SHA pins in the checked-in
  workflow files, with action-internal tool fetches classified separately
  below;
- `golangci-lint` and Renovate run from digest-pinned containers;
- `nono`, `actionlint`, and `zizmor` release tarballs are verified with GitHub
  artifact attestations bound to their signer workflows before execution;
- native libghostty artifacts, Zig, and SPDX validator downloads are bound to
  `libghostty-native.lock.json` checksums and fail closed on mismatch;
- release artifacts are reverified by same-revision manifests, checksums, and
  GitHub attestations before publication.

## Problem

Some CI tools still enter the runner through version strings, tag-only
containers, Homebrew taps, or platform package managers. Without a checked
inventory, maintainers cannot tell whether a finding is already covered by an
accepted follow-up, a release-critical gap, or an acceptable platform-managed
dependency.

## Goals

- Inventory executable tools, containers, and downloaded artifacts used by all
  workflows.
- Classify each item as immutable/provenance-verified, checksum-only, mutable,
  or platform-managed.
- Record explicit dispositions and small follow-up PR shapes for material gaps.
- Avoid duplicating the sibling work named by #1765: CI-DN-04, CI-DN-05,
  CI-FU-08, and CI-FU-09.
- Avoid duplicating the existing release-integrity follow-up in #706.
- Preserve least privilege and fail-closed behavior in any later implementation.

### Non-Goals

- Do not replace platform-managed GitHub Actions, runner tools, Homebrew, snap,
  or apt packages in this audit PR.
- Do not change workflow permissions, credentials, artifact publication, branch
  mutation, path routing, required checks, or release behavior.
- Do not add external CI infrastructure, a new trusted identity, or a new
  provenance service.
- Do not solve the already-scoped Safehouse pinning work in #1763 or the Hugo,
  k6, and govulncheck dependency-management work in #1764.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| GitHub Actions CI | Targeted | This inventory covers workflow-managed tools, containers, and artifacts. |
| CLI and daemon | Excluded | No runtime behavior changes are made. |
| macOS app | Excluded | App behavior is unchanged; macOS runners are covered only as CI consumers. |
| iOS app | Excluded | App behavior is unchanged; SwiftPM downloads are covered only as CI inputs. |
| Documentation site | Targeted as CI input | Docs build tools are inventoried, but public documentation behavior is unchanged. |

## Proposals

### Proposal 0: Do Nothing

Leaving #1765 as an umbrella without a checked inventory would keep every
future supply-chain finding ambiguous. Reviewers would need to rediscover which
workflow consumes each tool, whether a gap is already owned by another issue,
and whether the job is required or release-critical before deciding if a change
is urgent.

### Proposal 1: Checked Inventory With Follow-Up PRs (Recommended)

Commit this document, mirror the checked inventory into #1765, and keep
implementation changes split by tool family. That keeps the audit reversible
while giving maintainers an evidence-backed queue.

Classification meanings:

- **Immutable/provenance-verified:** selected by commit SHA, OCI digest, GitHub
  build provenance attestation, or an exact source commit with repository-owned
  validation before execution.
- **Checksum-only:** the repository stores or generates the expected digest and
  verifies bytes before use, or an ecosystem checksum transparency log verifies
  source content, but independent build provenance is not verified.
- **Mutable:** selection can move independently of the graith commit, usually
  through a tag, version range, tap formula, package manager default, or
  unchecked release asset.
- **Platform-managed:** provided by GitHub-hosted runner images or GitHub-owned
  actions/services. The audit records these rather than replacing them unless a
  concrete reliability or security benefit is shown.

Action rows classify the executed tool path. A SHA-pinned wrapper action can
still be mutable if its `action.yml`, Dockerfile, or bundled JavaScript fetches
unverified binaries or tag-only containers at run time.

Exact versions and digests below are a 2026-07-27 evidence snapshot. The
workflow files, lock files, and Renovate managers remain authoritative for
current values; this document is authoritative for the classification and
disposition of each tool family.

#### Inventory

| Item | Workflows / jobs | Classification | Evidence | Disposition |
|------|------------------|----------------|----------|-------------|
| GitHub action references | All workflows with `uses:` | Immutable/provenance-verified | Workflow scan found no tag-only `uses:` references; normal action refs are full action SHAs in `.github/workflows/`. | No change. Keep action refs commit-pinned; action-internal tools are separate inventory rows. |
| Go toolchain from `actions/setup-go` | Go, docs, native, sandbox, release, and workflow-lint jobs | Platform-managed | Pinned `actions/setup-go` action reads `go-version-file: go.mod`; `go.mod` pins Go `1.26.5`. | No replacement without a demonstrated platform reliability issue. |
| GitHub Pages actions | `docs.yml` build/deploy | Platform-managed | `actions/configure-pages`, `actions/upload-pages-artifact`, and `actions/deploy-pages` are GitHub-owned and SHA-pinned. | No change; platform-managed. |
| GitHub artifact actions | Native, regen, release, scorecard workflows | Platform-managed | `actions/upload-artifact` and `actions/download-artifact` are SHA-pinned; artifact consumers reverify expected names, manifests, and checksums where artifacts become release inputs. | No change; keep consumer checks. |
| CodeQL actions | `codeql.yml`, `scorecard.yml` SARIF upload | Platform-managed | GitHub-owned CodeQL action refs are SHA-pinned. | No replacement; CodeQL tool bundle is GitHub-managed. |
| Dependency Review action | `dependency-review.yml` | Platform-managed | GitHub-owned action is SHA-pinned. | No replacement. |
| GitHub artifact attestation action | `goreleaser.yml`, `dev-release.yml` attest jobs | Platform-managed | `actions/attest` is GitHub-owned, SHA-pinned, and isolated to jobs with `attestations: write`. | No replacement; keep attestation write permission scoped to attest jobs. |
| Release Please action | `release-please.yml` | Immutable/provenance-verified | Third-party action source is pinned to commit `45996ed...`. | No current material gap found. |
| Commitsar action container build | `commits.yml` | Mutable | Third-party action source is pinned to commit `909c3ab...`, but its Docker action builds from `golang:1.25.5-alpine` and `alpine:3.18` tag-only base images with `apk` and `go mod download` during the action build. | New required-check follow-up: either replace with a digest/provenance-verified commitsar install or pin the Docker build inputs tightly enough that executed bytes cannot float independently. |
| Scorecard action container | `scorecard.yml` | Mutable | Third-party action source is pinned to commit `2d11466...`, but `action.yaml` executes `docker://ghcr.io/ossf/scorecard-action:v2.4.4` without an OCI digest; SARIF upload remains SHA-pinned GitHub CodeQL. | New security follow-up: evaluate a digest-pinned Scorecard image or equivalent action path while preserving `security-events` and `id-token` least privilege. |
| TruffleHog action image | `secret-scan.yml` | Mutable | Third-party action source is pinned to commit `6f3c981...`, but the action's default input runs `ghcr.io/trufflesecurity/trufflehog:latest`; the workflow does not override `version` or `image`. | New security follow-up: pin an exact TruffleHog version and, if supported, an image digest. This is the top non-release action-internal gap because it currently uses `latest`. |
| Gitleaks action binary | `secret-scan.yml` | Mutable | Third-party action source is pinned to commit `e0c47f4...`, but bundled JavaScript downloads `zricethezav/gitleaks` release assets with `@actions/tool-cache`; the default is version `8.24.3` and no checksum or attestation is verified. | New security follow-up: use a checksum/provenance-verified Gitleaks install or an action path that pins executable bytes. |
| `golangci-lint` container | `ci.yml` lint via `make lint-only` and `make lint-darwin`; local `make lint*` | Immutable/provenance-verified | `Makefile` selects `golangci/golangci-lint:v2.12.2@sha256:5ccee...`; Renovate manages version and digest as one unit. | No change. Required `Lint` already uses immutable container bytes. |
| Renovate container | `workflow-lint.yml` Renovate native dependency fixtures | Immutable/provenance-verified | Workflow pins `renovate/renovate:43.280.4@sha256:3f01d...`. | No change. |
| `nono` release tarball | `sandbox.yml` Linux required job | Immutable/provenance-verified | Version `0.69.0`; tarball is verified with `gh attestation verify --repo nolabs-ai/nono --signer-workflow nolabs-ai/nono/.github/workflows/release.yml` before extraction. | No change; required job already fails closed. |
| `actionlint` release tarball | `workflow-lint.yml` actionlint job | Immutable/provenance-verified | Version `1.7.12`; tarball is verified with GitHub attestation bound to `rhysd/actionlint/.github/workflows/release.yaml`. | No change. |
| `zizmor` release tarball | `workflow-lint.yml` zizmor job | Immutable/provenance-verified | Version `1.28.0`; tarball is verified with GitHub attestation bound to `zizmorcore/zizmor/.github/workflows/release-binaries.yml`. | No change. |
| `govulncheck` | `ci.yml` Vulnerability Check | Checksum-only | `go install golang.org/x/vuln/cmd/govulncheck@v1.3.0` builds from a Go module version checked by the Go module checksum database. | Version-management follow-up belongs to #1764; do not duplicate here. |
| GoReleaser binary | `goreleaser.yml`, `dev-release.yml` build jobs | Checksum-only | Workflows require exactly one exact `GORELEASER_VERSION` in `.github/ci-tool-versions.env`, then `scripts/install-goreleaser.sh` downloads the matching archive and `checksums.txt`, fails closed if either download fails or the selected asset checksum is missing/mismatched, and only then adds `goreleaser` to `PATH`. Renovate manages the exact version pin. | Release-critical mutable range closed. Future Sigstore/provenance hardening can be a separate small PR if it fails closed without broad permission or publication-path changes. |
| Hugo extended `.deb` | `docs.yml`, `docs-preview.yml` docs jobs | Mutable | `HUGO_VERSION=0.154.5` selects a GitHub release asset downloaded with `wget`, with no checksum or attestation verification before `dpkg -i`. | Owned by #1764 for authoritative version/update management; any integrity pin should be grouped there. |
| Hugo modules | `docs.yml`, `docs-preview.yml` docs jobs | Checksum-only | `website/hugo.toml` imports Hugo modules and `website/go.mod` / `website/go.sum` pin module versions and checksums; there is no vendored module directory, so Hugo fetches them during the docs build. | Accept for now because Go module checksums bind content; if #1764 centralizes docs tooling, keep this row in that authority. |
| Dart Sass snap | `docs.yml`, `docs-preview.yml` docs jobs | Mutable | `sudo snap install dart-sass` uses the default snap channel with no repository-owned version or digest. | New docs-tooling follow-up: pin or otherwise verify Dart Sass, preferably in the same small PR that evaluates Hugo integrity so the docs build has one toolchain authority. |
| k6 browser image | `docs-preview.yml` screenshot job | Mutable | `K6_IMAGE=grafana/k6:1.8.0-with-browser` is tag-only and passed to `docker run`. | Owned by #1764 for version management; add an OCI digest in that PR if supported by Renovate grouping. |
| Safehouse Homebrew formula | `sandbox.yml` macOS required job when relevant | Mutable | `brew install eugene1g/safehouse/agent-safehouse` resolves the tap formula at run time. | Owned by #1763; keep this audit PR from changing sandbox tests or permissions. |
| Zig Linux archive | Native, stable release, dev release, libghostty publish workflows | Checksum-only | URL, version, and SHA-256 live in `libghostty-native.lock.json`; workflows verify `sha256sum --check` before adding Zig to `PATH`. | Accept for now because release-critical native builds fail closed on lock mismatch; revisit only as a focused native dependency provenance PR. |
| SPDX tools-java validator | Native, stable release, dev release workflows | Checksum-only | URL and SHA-256 live in `libghostty-native.lock.json`; `scripts/libghostty-native.sh install-spdx-validator` verifies the zip before extracting the expected jar. | Accept for now; it validates SPDX documents and is not compiled into shipped artifacts. |
| Zig package dependencies for libghostty | Native and release source-build jobs | Checksum-only | `libghostty-native.lock.json` records `highway`, `uucode`, and `simdutf` dependency versions and hashes; the native helper validates Zig metadata and verifies source file checksums before packaging. | Accept for now; revisit with Zig/SPDX provenance only as a focused native dependency PR. |
| libghostty Apple xcframework | SwiftPM GUI build, native macOS workflow | Checksum-only | SwiftPM binary target and `scripts/libghostty-native.sh` use the reviewed URL and checksum projected from `libghostty-native.lock.json`. | Accept for now; dependency-unit generation and native checks own rotation. |
| libghostty Linux artifacts | Coverage, native local build, native consumer tests | Checksum-only | `libghostty-native.lock.json` stores URL and SHA-256 per architecture; helper verifies checksum and archive shape before extraction. | Accept for now; release-critical Linux workflows also source-build from the exact Ghostty commit. |
| Ghostty source checkout | Native, libghostty publish, and release Linux source-build jobs | Immutable/provenance-verified | `scripts/libghostty-native.sh` fetches and checks out the exact `ghostty.commit` from `libghostty-native.lock.json`, then verifies dependency metadata and the resulting archive shape. | No change. |
| SwiftPM remote binary target for `GhosttyVt` | `gui-ci.yml`, Swift coverage | Checksum-only | `gui/shared/Package.swift` pins URL and SwiftPM checksum matching the native lock. | Accept for now; same artifact as libghostty Apple xcframework. |
| apt packages (`cpio`, `rpm2cpio`, `gnupg`, `rpm`, `aptly`, `createrepo-c`) | Stable release Linux/aggregation/publish jobs | Platform-managed | Installed from the GitHub-hosted Ubuntu runner's configured apt repositories only in trusted release-shaped jobs. | No replacement without a specific release reliability issue. |
| Runner-provided tools, including `bash`, `git`, `make`, `curl`, `wget`, `jq`, `tar`, `gzip`, `unzip`, `sha256sum`, `dpkg`, `snap`, `python3`, `ruby`, `node`, `shellcheck`, `swift`, `xcodebuild`, `codesign`, `notarytool`, `xcrun`, `openssl`, `security`, `lipo`, `java`, `docker`, and `gh` | Multiple workflows | Platform-managed | Provided by GitHub-hosted Ubuntu/macOS images or the GitHub Actions runner environment; this category covers ubiquitous shell, coreutils, Git, language, Docker, and macOS signing tools rather than vendoring each binary. | Do not vendor or replace wholesale; open targeted issues only for observed drift. |
| Release assets re-downloaded with `gh release download` | `goreleaser.yml`, `dev-release.yml` publish/verify jobs | Checksum-only or provenance-verified, depending on path | Stable verifies exact remote bytes against local `dist/` and checks GitHub attestations before publication; dev verifies versioned remote assets against local checksums and verifies Linux attestations. | Existing #706 covers missing dev Darwin attestation. Do not duplicate here. |
| External downstream repositories | Stable/dev release publication jobs | Platform-managed mutation targets | Homebrew tap, apt/yum repo, and optional AUR checkout use scoped publication steps after artifact verification; they are not tool downloads used to build graith. | Out of scope for tool-provenance replacement; keep existing credential guards. |

#### Follow-Up PR Queue

1. Action-internal security and required-check tools: TruffleHog first, then
   Gitleaks, Scorecard, and Commitsar. Priority: required and security-adjacent
   CI. Keep each PR small; pin executable bytes or prove the replacement path has
   stronger integrity without broad workflow permission changes.
2. Dart Sass pin or integrity verification for `docs.yml` and
   `docs-preview.yml`. Priority: non-required docs tooling. Prefer grouping
   with the Hugo integrity work if #1764 already introduces a shared docs
   toolchain declaration.
3. Hugo/k6/govulncheck version and digest management in #1764. Do not create
   competing managers in this issue.
4. Safehouse immutable install and verification in #1763. Do not change the
   sandbox jobs in this issue.
5. Optional native dependency provenance hardening for Zig and SPDX tools-java.
   Treat this as lower priority because the current lock stores checksums
   in-repository and release jobs fail closed before execution.

### Proposal 2: Replace Every Platform-Managed Tool

This would vendor or independently pin runner tools, GitHub-owned actions, apt
packages, snap, Homebrew, and CodeQL. It is rejected because it expands the
attack surface, increases CI maintenance, and conflicts with #1765's constraint
that platform-managed actions should not be replaced without a demonstrated
security or reliability benefit.

## Other Notes

### References

- Issue #1765: `https://github.com/d0ugal/graith/issues/1765`.
- Existing Safehouse follow-up: #1763.
- Existing Hugo/k6/govulncheck follow-up: #1764.
- Existing release-integrity follow-up: #706.
- Current workflows: `.github/workflows/`.
- Native dependency lock: `libghostty-native.lock.json`.
- Native helper: `scripts/libghostty-native.sh`.

### Testing

This audit-only PR changes documentation. Verification is:

```bash
git diff --check
```

Implementation follow-ups must add focused workflow-policy checks or local
script tests in proportion to the change, and must keep workflow permissions,
credential exposure, and fail-closed behavior at least as strict as today.
