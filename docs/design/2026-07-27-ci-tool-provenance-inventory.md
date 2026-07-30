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
| Commitsar Go module install | `commits.yml` | Immutable/provenance-verified | Workflow installs `github.com/aevea/commitsar` at `COMMITSAR_VERSION` from `.github/ci-tool-versions.env` using the SHA-pinned `actions/setup-go` action and `GOSUMDB=sum.golang.org`, then verifies the installed binary reports the selected version. | Required-check mutable Docker-build gap closed by #1888. Keep the version managed by Renovate's Go datasource. |
| Scorecard image | `scorecard.yml` | Immutable/provenance-verified | Workflow runs the official `ghcr.io/ossf/scorecard-action` image directly by OCI digest selected from `SCORECARD_IMAGE` in `.github/ci-tool-versions.env`; SARIF upload and code-scanning upload remain SHA-pinned platform-managed actions. | Action-internal mutable image gap closed by #1888 while preserving `security-events` and `id-token` least privilege. Keep tag and digest managed together. |
| TruffleHog image | `secret-scan.yml` | Immutable/provenance-verified | Workflow runs `ghcr.io/trufflesecurity/trufflehog` directly by OCI digest selected from `TRUFFLEHOG_IMAGE` in `.github/ci-tool-versions.env`; the same value retains the version tag for Renovate's Docker manager, and the workflow rejects values without a `sha256` digest before execution. | Action-internal mutable image gap closed by #1888. Keep tag and digest managed together. |
| Gitleaks image | `secret-scan.yml` | Immutable/provenance-verified | Workflow runs `ghcr.io/gitleaks/gitleaks` directly by OCI digest selected from `GITLEAKS_IMAGE` in `.github/ci-tool-versions.env`; the workflow rejects values without a version tag and `sha256` digest before execution. | Action-internal release-download gap closed by #1888. Keep tag and digest managed together. |
| `golangci-lint` container | `ci.yml` lint via `make lint-only` and `make lint-darwin`; local `make lint*` | Immutable/provenance-verified | `Makefile` selects `golangci/golangci-lint:v2.12.2@sha256:5ccee...`; Renovate manages version and digest as one unit. | No change. Required `Lint` already uses immutable container bytes. |
| Renovate container | `workflow-lint.yml` Renovate native dependency fixtures | Immutable/provenance-verified | Workflow pins `renovate/renovate:43.280.4@sha256:3f01d...`. | No change. |
| `nono` release tarball | `sandbox.yml` Linux required job | Immutable/provenance-verified | Version `0.69.0`; tarball is verified with `gh attestation verify --repo nolabs-ai/nono --signer-workflow nolabs-ai/nono/.github/workflows/release.yml` before extraction. | No change; required job already fails closed. |
| `actionlint` release tarball | `workflow-lint.yml` actionlint job | Immutable/provenance-verified | Version `1.7.12`; tarball is verified with GitHub attestation bound to `rhysd/actionlint/.github/workflows/release.yaml`. | No change. |
| `zizmor` release tarball | `workflow-lint.yml` zizmor job | Immutable/provenance-verified | Version `1.28.0`; tarball is verified with GitHub attestation bound to `zizmorcore/zizmor/.github/workflows/release-binaries.yml`. | No change. |
| `govulncheck` | `ci.yml` Vulnerability Check | Checksum-only | `GOVULNCHECK_VERSION=v1.6.0` lives in `.github/ci-tool-versions.env`; `go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}` builds from a Go module version checked by the Go module checksum database, and Renovate manages the module pin. | Version-management gap closed by #1764. Accept Go checksum-database verification for now. |
| GoReleaser binary | `goreleaser.yml`, `dev-release.yml` build jobs | Checksum-only | Workflows require exactly one exact `GORELEASER_VERSION` in `.github/ci-tool-versions.env`, then `scripts/install-goreleaser.sh` downloads the matching archive and `checksums.txt`, fails closed if either download fails or the selected asset checksum is missing/mismatched, and only then adds `goreleaser` to `PATH`. Renovate manages the exact version pin. | Release-critical mutable range closed. Future Sigstore/provenance hardening can be a separate small PR if it fails closed without broad permission or publication-path changes. |
| Hugo extended `.deb` | `docs.yml`, `docs-preview.yml` docs jobs | Checksum-only | `HUGO_VERSION=0.164.0` and `HUGO_LINUX_AMD64_DEB_SHA256` live in `.github/ci-tool-versions.env`; docs workflows install with `scripts/install-hugo.sh`, which verifies the repository-owned checksum before `dpkg -i` and checks `hugo version`. | Version-management gap closed by #1764; missing integrity gap closed by #1888. Rotate the checksum with the version. |
| Hugo modules | `docs.yml`, `docs-preview.yml` docs jobs | Checksum-only | `website/hugo.toml` imports Hugo modules and `website/go.mod` / `website/go.sum` pin module versions and checksums; there is no vendored module directory, so Hugo fetches them during the docs build. | Accept for now because Go module checksums bind content; if #1764 centralizes docs tooling, keep this row in that authority. |
| Dart Sass Linux archive | `docs.yml`, `docs-preview.yml` docs jobs | Checksum-only | Workflows install `DART_SASS_VERSION` from the upstream GitHub release tarball using `scripts/install-dart-sass.sh`, which verifies repository-owned `DART_SASS_LINUX_X64_SHA256` before extraction and version-checks the installed binary. | Mutable snap-channel gap closed by #1888. Rotate the checksum with the version because upstream does not publish checksum metadata for Renovate to manage. |
| k6 browser image | `docs-preview.yml` screenshot job | Immutable/provenance-verified | `K6_IMAGE=grafana/k6:2.1.0-with-browser@sha256:406122...` lives in `.github/ci-tool-versions.env`; Renovate manages tag and OCI digest together, and docs preview runs that pinned image. | Tag-only image gap closed by #1764. Keep tag and digest managed together. |
| Safehouse shell artifact | `sandbox.yml` macOS required job when relevant | Checksum-only | `SAFEHOUSE_VERSION=0.11.1` and a reviewed SHA-256 live in the install step; the workflow downloads `safehouse.sh`, verifies the downloaded and installed bytes, then checks `safehouse --version` before running enforcement tests. Renovate opens review-gated version updates. | Homebrew mutable install gap closed by #1763. Keep checksum review-gated. |
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

1. Optional native dependency provenance hardening for Zig and SPDX tools-java.
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
