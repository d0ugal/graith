---
weight: 36
title: CI policy manifest
description: Validate the CI north-star policy manifest
icon: shield-check
toc: true
---

# CI policy manifest

The CI north-star P1 policy manifest is the checked-in capability policy built
from the accepted P0 baseline inventory. It is data, not workflow logic:
`internal/cipolicy/manifest.json` records policy version, digest, source and
event identity, trust tiers, capabilities, modes, platforms, cost classes,
requiredness, owners, unsupported decisions, and evidence references.

The manifest traces each current repository-owned workflow/job coordinate back
to `internal/cibaseline/inventory.json` and its inventory digest. It does not
replace the P0 inventory or re-describe workflow YAML as a second inventory
language.

## Validate the policy

The checked-in manifest is validated by the Go package tests:

```bash
go test ./internal/cipolicy
```

The manifest drift tests fail closed for stale generated data, unknown
references, duplicate mode IDs or coordinates, missing owners, ambiguous
requiredness, unsupported silent passes, invalid trust tiers, invalid
platforms, invalid cost classes, invalid source/event identity, malformed
evidence references, and P0 evidence that is not bound to the manifest's
baseline inventory digest.

Regenerate the committed manifest from the current P0 inventory through the
package-owned update gate:

```bash
go test ./internal/cipolicy -run TestCommittedManifestMatchesInventory -update
```

## Regenerate from P0

Regenerate the manifest only as part of a reviewed policy change that also
updates the matching P0 inventory. The package generator canonicalizes ordering
and serialization, so the same semantic policy has one digest. Manual edits
that do not match the P0-derived manifest fail validation as stale drift.

After regeneration, run:

```bash
go test ./internal/cibaseline ./cmd/cibaseline ./internal/cipolicy ./cmd/cipolicy
```

`cmd/cipolicy plan` remains a checked-in workflow helper. The dev-release
pull-request classifier runs it from the trusted base checkout so unknown paths,
policy edits, generated input drift, lockfiles, release metadata, and detector
errors expand to the safe dev-release superset instead of narrowing silently.
Manifest generation and validation remain package owned.

## Downstream use

`mode.trust_tiers` is an aggregate across the mode's `source_events`. A policy
evaluator must intersect the selected event with that event identity's
`trust_tiers` before authorizing credentials or publication. Treating the mode
aggregate as a Cartesian authorization weakens the trust boundary.

`trusted-publication` is limited to an explicit protected main, tag, or
environment publication context whose credentials are unavailable to
pull-request-controlled code. Write-scoped `GITHUB_TOKEN` permissions,
repository location, check/comment publication, and `proof_type` do not upgrade
a mode to publication trust.

Plan narrowing is allowed only when the changed-file list is exact and every
path is known to the detector. Unknown paths, CI policy or evaluator changes,
generated inputs, lockfiles, release metadata, and detector errors all expand
to the safe superset. Blank changed-file rows and leading or trailing path
whitespace are treated as invalid changed paths and also expand to the safe
superset. A plan with no required jobs is rejected instead of passing silently.
Plan expiry is bounded to the policy TTL, and plans whose creation time is too
far in the future are rejected as invalid.

Ref classification is driven by the manifest's event ref patterns. Push events
must match an explicit protected branch or tag pattern; missing push ref
patterns and bare wildcard patterns fail closed. Pull-request and
workflow-dispatch narrow plans include the current required native and sandbox
gates even when the changed path detector narrows other capabilities, because
the P1 manifest traces those legacy gates as required for those events. Any
required mode for an event must be covered by that event's universal capability
floor, so an omitted or tampered detected-capability list cannot drop required
coordinates.

For fork pull requests and pull requests that change policy code or workflow
metadata, run plan and gate evaluation from the trusted base ref or from a
pinned released policy tool. Pull-request-controlled policy code is input to be
proved, not authority for narrowing itself.

Expensive pull-request workflows use PR-scoped GitHub Actions concurrency groups.
A newer push to the same pull request cancels the superseded core CI, sandbox,
and libghostty-native runs. Non-PR runs use unique groups and do not opt into
that cancellation path, so main, release, scheduled, manual, and publication
work cannot be cancelled by pull-request activity.

Native and release artifact boundaries remain enforced by the current workflow
and libghostty-specific checks. `internal/cipolicy` no longer exposes a
supported artifact manifest or producer-result contract.

## External observed modes

The current P1 manifest explicitly records the GitHub-generated
`dynamic/dependabot/update-graph` observation as an unsupported external mode
with owner, rationale, and P0 evidence references. It is not a
repository-owned current proof mode and cannot silently pass or enter dual-run
sampling until a reviewed policy change gives it an explicit implementation or
retirement decision. Elapsed calendar time alone does not invalidate the row.
