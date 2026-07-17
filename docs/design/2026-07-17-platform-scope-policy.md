---
title: "Design Doc: Design-linked frontend platform scope"
authors: Dougal Matthews
created: 2026-07-17
status: Implemented
reviewers: (none yet)
informed: (TBD)
---

# Design-linked frontend platform scope

Frontend parity remains the default for graith capabilities, but it should not
force a feature onto a surface where the feature does not make sense. Each
intentional exclusion will be recorded in the capability manifest and linked to
the feature design's platform-support decision. Conformance tests will enforce
parity only across the surfaces that the design targets.

## Background

`internal/capabilities/capabilities.json` records whether each capability is
`supported`, `planned`, or `n/a` on the CLI, iOS, and macOS surfaces. Go tests
validate the manifest and generate the published capability matrix plus a Swift
fixture. `CapabilityConformanceTests.swift` then checks the GUI columns against
compile-anchored shared affordances and requires the iOS and macOS states to be
equal unless the capability ID appears in a separate `knownDivergences` set.

The state model already distinguishes a gap we intend to close (`planned`) from
a feature that deliberately does not apply (`n/a`). However, the reason for an
`n/a` cell is only free-form prose, while an intentional iOS/macOS difference
must also be duplicated in Swift. Neither mechanism points to the design review
where the platform scope was chosen.

## Problem

Unconditional parity turns an architectural default into a product constraint.
A CLI scripting primitive, mobile-only integration, or desktop-only affordance
can fail conformance even after the design process has deliberately excluded a
surface. The current escape hatch is also in the wrong place: a Swift test-local
set says that divergence is allowed but does not explain who decided it or why.

We need an opt-out that is capability-specific, reviewable, and fail-closed. A
blanket test skip, environment variable, or PR label would make accidental drift
indistinguishable from an intentional product decision.

## Goals

- Keep parity as the default for every platform targeted by a feature.
- Let a design deliberately exclude one or more surfaces.
- Make every exclusion traceable to a durable design-doc decision.
- Keep the capability manifest as the sole source of truth for platform scope.
- Reject missing, malformed, or broken design references in tests.

### Non-Goals

- Runtime feature flags or user-configurable platform availability.
- Automatically proving that an app view exposes every shared affordance.
- Deriving protocol-message classifications from capability rows; messages can
  be reused across several capabilities and still require deliberate review.
- Allowing temporarily incomplete work to masquerade as a permanent exclusion.
  A targeted but unfinished surface remains `planned`.

## Platform support

This change affects contributor policy and conformance tests rather than a
runtime frontend, so the CLI, iOS, and macOS applications require no new user
interface. All three surfaces remain represented by the capability manifest.

This design also ratifies the two pre-existing exclusions during migration:

| Capability | CLI | iOS | macOS | Decision |
|------------|-----|-----|-------|----------|
| `session.wait` | Targeted | Excluded | Excluded | Waiting is a shell automation gate; GUIs continuously present live state. |
| `terminal.remote-type` | Targeted | Excluded | Excluded | Attached GUIs already send terminal input directly; the standalone command is a CLI automation convenience. |

Future feature designs must include this section with one row per surface and a
short rationale for every exclusion. Their capability rows link back to that
section rather than to this policy document.

## Proposals

### Proposal 0: Do Nothing

Continue using `n/a` notes and the Swift-only `knownDivergences` set. This keeps
intentional scope decisions split across two sources and provides no durable
link to design review.

### Proposal 1: Design-linked exclusions in the manifest (Recommended)

Add an optional `platform_decision` field to each capability row. It contains a
repository-relative Markdown link of the form
`docs/design/<document>.md#platform-support`. The field is required whenever a
surface is `n/a` or the iOS and macOS states differ. Go validation rejects an
absolute path, parent traversal, a non-design-doc path, or a different anchor;
a repository-level test verifies that the document exists and contains the
`## Platform support` heading.

The three support states retain distinct meanings:

- `supported`: the surface is targeted and the capability is usable.
- `planned`: the surface is targeted but implementation is incomplete.
- `n/a`: the surface is excluded by the linked platform decision.

The generated capability documentation includes the decision link in the row's
footnote, and the GUI fixture carries it so the Swift check remains
self-describing.

GUI parity is then scoped rather than disabled. If both iOS and macOS are
targeted (neither is `n/a`), their states must remain equal. If exactly one is
excluded, the difference is intentional and the manifest's validated decision
replaces `knownDivergences`. A compile-anchored shared affordance must be
`supported` on each targeted GUI surface, but it does not force an excluded
surface into scope. The existing `viewOnlyCapabilities` registry remains: it
describes where implementation lives, not which platforms are targeted.

Protocol annotations follow the same decision manually. Wire types used only by
a capability excluded from both GUIs are normally `na`; a future GUI target is
`planned`, and a shipped GUI path is `required`. This remains a contributor
check rather than an automatic mapping because wire types and capabilities are
not one-to-one.

### Proposal 2: A blanket `skipParity` switch

Add a boolean to a capability, test invocation, or CI job. This is mechanically
simple but loses which surfaces are excluded, why they are excluded, and where
the decision was reviewed. It also permits a temporary implementation gap to be
silenced indefinitely. Rejected.

## Other Notes

### References

- `internal/capabilities/capabilities.json` — capability support source of truth.
- `internal/capabilities/capabilities.go` — schema, validation, and generators.
- `gui/shared/Tests/GraithSessionKitTests/CapabilityConformanceTests.swift` —
  compile-anchored GUI conformance and parity checks.
- `internal/protocol/manifest.go` — related `required` / `planned` / `na`
  classification for Swift protocol models.

### Implementation Notes

The capability manifest and generated GUI fixture advance to version 2 because
the projected schema gains `platform_decision`. Existing exclusions point to
this document's Platform support section; later exclusions point to their own
feature design.

Contributor instructions and the published capability page will define `n/a`
as a deliberate, design-linked exclusion. Regeneration remains
`go test ./internal/capabilities -update`.

### Testing

- Go validation rejects an exclusion or GUI divergence without a decision.
- Go validation rejects malformed decision references.
- A repository test rejects a missing document or missing Platform support
  heading.
- Markdown rendering tests cover decision-only and note-plus-decision footnotes.
- GUI fixture projection tests cover the decision field and version 2.
- Swift tests cover parity across two targeted GUIs, a one-GUI exclusion, and
  missing decision metadata.
- Run focused Go tests and the shared Swift suite after regenerating artifacts.
