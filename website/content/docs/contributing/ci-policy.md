---
weight: 36
title: CI policy manifest
description: Validate the P1 CI north-star policy manifest and digest
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

From the repository root:

```bash
go run ./cmd/cipolicy validate
```

Validation prints the deterministic policy digest on success. It fails closed
for stale generated data, unknown references, duplicate mode IDs or
coordinates, missing owners, ambiguous requiredness, unsupported silent
passes, invalid trust tiers, invalid platforms, invalid cost classes, invalid
source/event identity, malformed evidence references, and P0 evidence that is
not bound to the manifest's baseline inventory digest.

To print the checked-in digest without comparing against the P0 inventory:

```bash
go run ./cmd/cipolicy digest
```

## Regenerate from P0

Regenerate only after reviewing the matching P0 inventory change:

```bash
go run ./cmd/cipolicy -output internal/cipolicy/manifest.json generate
go run ./cmd/cipolicy validate
go test ./internal/cipolicy ./cmd/cipolicy
```

The generator canonicalizes ordering and serialization, so the same semantic
policy has one digest. Manual edits that do not match the P0-derived manifest
fail validation as stale drift.

The digest covers the canonical semantic policy. The `validate` command also
compares the checked-in canonical semantic manifest with a fresh P0-derived
manifest so equivalent-looking but stale generated data cannot hide drift.

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

## External observed modes

The current P1 manifest explicitly records the GitHub-generated
`dynamic/dependabot/update-graph` observation as an unsupported external mode
with owner, rationale, expiry, and P0 evidence references. It is not a
repository-owned current proof mode and cannot silently pass or enter dual-run
sampling until a later policy package gives it an explicit mode or retirement
decision. Its current expiry is `2026-08-31`; validation fails after that date
until the owner renews, promotes, or retires the row.
