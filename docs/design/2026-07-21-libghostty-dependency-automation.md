---
title: "Design Doc: Native libghostty Dependency Automation"
authors: Dougal Matthews
created: 2026-07-21
status: Implemented
reviewers: (pending implementation review)
informed: Graith maintainers
issue: https://github.com/d0ugal/graith/issues/1496
---

# Native libghostty Dependency Automation

Graith will make the complete native libghostty dependency closure visible to
Renovate while retaining human review and exact-source, checksum, license, and
platform verification. A machine-readable lock becomes the update source; the
SPDX inventory and notice inventory become verified projections of that lock.

## Background

The native candidate combines the Go wrapper, an exact Ghostty revision, its
Zig and C/C++ dependency closure, a separately published Apple xcframework,
and checksum-pinned SPDX validation tooling. Today those values are repeated in
`go.mod`, shell constants, Swift package metadata, the SPDX document, and the
third-party notices. `scripts/libghostty-native.sh verify-metadata` catches some
divergence, but the ordinary Go Renovate manager can offer and automerge a
wrapper update without rotating the rest of the closure.

Several inputs are not normal released packages. Ghostty and go-libghostty are
selected by commit, Highway is compiled at the commit embedded by Ghostty, and
simdutf is a vendored amalgamation whose package manifest can be stale. Updating
only a displayed version would therefore lose the exact-source guarantee.

## Problem

Native pins can silently age, while an isolated Go-module bump can make the Go
wrapper disagree with the headers, archive, license evidence, and SPDX
inventory it is meant to describe. Free-form notice prose is also an unsafe
place for a dependency bot to perform independent search-and-replace updates.

## Goals

- Let Renovate discover every externally versioned native component and the
  SPDX validator.
- Keep the native closure in one non-automerge dependency group.
- Generate or verify every derived pin from one machine-readable lock.
- Preserve exact commits, archive and source hashes, license conclusions, and
  the complete native test gate.
- Make commit-only and vendored discovery explicit to reviewers.

### Non-Goals

- Publishing a new Apple xcframework from an unreviewed dependency proposal.
- Treating a transitive release as compatible before Ghostty consumes it.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI/daemon on macOS arm64 | Targeted | This is the supported native candidate and owns the Apple artifact and packaging gate. |
| CLI/daemon on Linux | Targeted for source verification | Existing exact-source validation remains available without changing rollout scope. |
| iOS and macOS apps | Metadata consumers | Their shared Swift package consumes the same checksum-pinned Apple artifact, but this change adds no app behavior. |

## Proposals

### Proposal 0: Do Nothing

Continue relying on ad-hoc audits and the Go manager. This leaves non-Go pins
invisible and permits wrapper-only automerge, so it does not meet the provenance
boundary.

### Proposal 1: Canonical lock plus generated projections (Recommended)

Add `libghostty-native.lock.json` as the canonical dependency record. Renovate
regex managers read only annotated version or digest fields in that file.
Package rules place those matches and go-libghostty in a named dependency unit,
disable automerge, and suppress the generic gomod update for the wrapper.

The wrapper's `CMakeLists.txt` exact `GIT_TAG` is the default compatibility
authority for Ghostty. A wrapper update follows that tested pair; an explicitly
approved Ghostty proposal may move ahead only through the native compatibility
suite. For either primary update, the update command derives Zig, uucode,
Highway, and vendored simdutf from the selected Ghostty tree instead of choosing
their independent latest releases. Transitive-only release proposals remain
dependency-dashboard discovery signals and fail generation until a compatible
Ghostty commit consumes them.

The command resolves the wrapper commit to its Go pseudo-version and sum,
checks out the selected Ghostty commit, hashes exact sources and licenses,
synchronizes committed headers, refreshes the Apple artifact URL/checksum,
updates `go.mod`/`go.sum`, and renders the SPDX and generated notice inventory.
It fails if the exact Apple artifact has not yet been published. The release
metadata must name the full Ghostty commit and checksum, and the bytes must match
GitHub's release-asset digest. This is an explicit trust boundary: the update
consumes a separately built and reviewed artifact and does not rebuild or
publish it. Those failures are review signals, not a reason to weaken
verification.

License conclusions are never inferred from a version string. Each conclusion
is bound to the exact license and embedded-notice hashes by a review fingerprint.
Generation can expose changed evidence in a reviewable PR, but the merge gate
stays red until a maintainer inspects it and explicitly refreshes that binding.
The SPDX validator is the one automatic exception to dashboard approval because
it is update tooling, not compiled content; it remains in the same non-automerge
unit and its GitHub release digest and official validation are checked.

An offline verification mode compares all projections to the lock on every
native-relevant PR. Generation rolls all managed files back after a late error,
so it never leaves a partially rotated worktree. The existing native workflow
remains the required gate for wrapper tests, compatibility, race/fuzz,
packaging, SPDX, linkage, privacy, and supported platforms. When the fallback
workflow creates a generated commit, it explicitly dispatches every protected
workflow at that exact branch head rather than relying on statuses from the
pre-generation SHA.

The trade-off is that a Renovate proposal for a transitive release can remain
red until a compatible Ghostty commit and reviewed Apple artifact exist. That
is intentional: Renovate discovers candidates; it does not assert native ABI or
license compatibility.

### Proposal 2: Let Renovate rewrite every occurrence

Regex managers could replace matching strings in shell, Swift, JSON, Markdown,
and workflow files. They cannot safely derive checksums, Go sums, headers, or
license conclusions, and independent prose replacement would create additional
sources of truth. This approach is rejected.

## Other Notes

### References

- [Issue #1496](https://github.com/d0ugal/graith/issues/1496)
- [Native backend design](2026-07-18-libghostty-daemon-backend.md)
- `renovate.json5`
- `scripts/libghostty-native.sh`

### Testing

Unit tests cover lock validation, wrapper/Ghostty compatibility selection,
transitive reconciliation, deterministic notice/SPDX rendering, and stale
projection failures. A pinned Renovate validator and local lookup dry run prove
the repository config finds all seven managed pins and assigns the dependency
unit rule. Shell syntax, the offline dependency verifier, Go tests, workflow
lint, and the existing native workflow complete local and CI verification.
