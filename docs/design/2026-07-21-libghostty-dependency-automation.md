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

- Publishing a new native artifact from an unreviewed dependency proposal.
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
synchronizes committed headers, refreshes the Apple and Linux artifact
URLs/checksums, updates `go.mod`/`go.sum`, and renders the SPDX and generated
notice inventory. It fails if the exact native artifacts have not yet been
published. The release metadata must name the full Ghostty commit, the Apple
metadata must include the SwiftPM checksum, and the bytes must match GitHub's
release-asset digest. This is an explicit trust boundary: normal generation
consumes separately built and reviewed artifacts and never accepts stale,
placeholder, or locally guessed artifact checksums.

Dependency source URLs are a second trust boundary. Every Ghostty-manifest
dependency must be served from `deps.files.ghostty.org` with its own filename
prefix. The single exception is uucode: newer Ghostty manifests point it at
`https://github.com/jacobsandlund/uucode/archive/<commit>.tar.gz`, so for uucode
only, that exact shape is accepted (https, host `github.com`, that owner and
repository, a full lowercase 40-character commit, no query, fragment, userinfo,
port, or path traversal; matched against the raw string). Highway and any other
dependency keep the Ghostty-hosted rule. A GitHub-sourced archive is also bound
to its URL: every member must sit under the `uucode-<commit>/` root that GitHub
generates, and a pax global header commit, when present, must match. The
archive SHA-256 is recorded in the lock and the Zig hash stays pinned for the
build, so a URL serving different bytes fails offline verification and the Zig
fetch. Revisit this exception, and remove it, if upstream restores uucode to
`deps.files.ghostty.org`.

Wuffs, which newer Ghostty commits compile into libghostty-vt for Kitty graphics,
is served from `deps.files.ghostty.org` (`wuffs-<commit>.tar.gz`, the Ghostty
mirror of one commit of `google/wuffs-mirror-release-c`), so it uses the ordinary
Ghostty-hosted rule and needs no URL exception. Its entry is optional in the
lock. The generator tracks Wuffs exactly when the selected Ghostty's
`src/build/GhosttyZig.zig` wires it into the libghostty-vt module, and removes
the entry when it does not; Wuffs that is only present in the Ghostty tree for
the full application is not part of the closure. Ghostty's own `pkg/wuffs`
manifest supplies the URL, commit and Zig hash, and the compiled C source file
(`release/c/wuffs-vX.Y.c`). The version string, SPDX declaration, source hash,
archive hash and the LICENSE, LICENSE-APACHE and LICENSE-MIT hashes come from
the fetched archive. Wuffs has no Renovate manager of its own: it is a
vendored mirror whose version is chosen by Ghostty, so it can only move with a
Ghostty update.

A new Wuffs entry never arrives reviewed. The generator copies the declared
license as a proposed conclusion and writes a pending review digest that can
never equal a real fingerprint, so generation produces the usual deliberately
red review branch and the merge gate stays red until a human inspects the
evidence and runs `accept-license-reviews`. Later changes to any Wuffs license
hash, the declaration, or the conclusion invalidate the review the same way as
for Highway and simdutf.

The audited static archive closure is lock-driven. The base is the 11 objects
Graith has always audited. Only when the lock tracks Wuffs does the expected set
gain exactly one object, named from the lock's C source (`wuffs-v0.4.o` for
`release/c/wuffs-v0.4.c`). The comparison remains exact set equality with no
wildcards, so an untracked Wuffs object, a missing one, or a different release
series all fail the audit, and a malformed C source path in the lock fails
closed. Offline verification also requires the lock's Wuffs tracking to agree
with the selected Ghostty's build graph and the SPDX package count to be 7
rather than 6 when Wuffs is tracked.

Same-repository lock-only Renovate PRs use a trusted workflow to fill that
publication step. A maintainer reviews the native proposal, including
go-libghostty's tested Ghostty pin, and approves the PR; the approving review is
the authorization (there is no label to apply). The workflow triggers on
`pull_request_review` (submitted). Because that event runs the workflow file from
the PR merge ref, the `authorize` job treats the event as a hint only and
re-reads state from the API: the review must still be `APPROVED`, its
`commit_id` must equal the PR's current head SHA (so the approval is bound to
exactly what is built and any new push needs a fresh approval), the reviewer must
have `write` or `admin` permission on the repository, no reviewer may have an
outstanding changes request, the PR must be open, same-repository, and change
only `libghostty-native.lock.json`, and the head must not have moved by the end
of the checks. Later jobs use the SHAs verified by `authorize`, never the event
payload. A same-repository branch author already has write access, so running the
merge-ref copy of the workflow grants nothing new, and a lock-only Renovate PR
cannot change the workflow and stay eligible; forks get a read-only token without
secrets and are rejected. A dismissed or changes-requested review never builds,
and dismissal after a build has nothing to revoke because published releases are
immutable. A separate no-checkout `guidance` job on `pull_request_target`
(opened, synchronize, reopened) upserts one PR comment, found by a hidden marker
and authored by `github-actions[bot]`, explaining that an approval is required
and what to check first. The artifact jobs run only base-branch repository code, overlay the PR lock as data,
build the Apple and Linux artifacts in read-only jobs, publish immutable
releases from no-checkout/no-build write-token jobs keyed by the Ghostty,
go-libghostty, and Zig inputs, run strict generation against those releases,
and pass only an allowlisted generated commit bundle to an isolated
`RELEASE_TOKEN` push job. Forks, mixed-code PRs, stale approvals, missing
credentials, branch races, existing asset mismatches, and unbuildable selected
pins fail or skip without exposing maintainer credentials to unreviewed code.

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
packaging, SPDX, linkage, privacy, and supported platforms. Generated commits
are pushed with the repository's workflow-triggering token from an isolated job,
so the normal pull-request synchronize event reruns required checks at the
generated branch head.

The trade-off is that a Renovate proposal for a transitive release can remain
red until a compatible Ghostty commit and reviewed native artifacts exist. That
is intentional: Renovate discovers candidates; it does not assert native ABI,
buildability, or license compatibility.

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
