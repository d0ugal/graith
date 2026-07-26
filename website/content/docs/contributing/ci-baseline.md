---
weight: 35
title: CI baseline and inventory
description: Validate the CI north-star static inventory
icon: chart-line
toc: true
---

# CI baseline and inventory

The CI north-star inventory is a checked-in, closed-world snapshot of the
repository's current GitHub Actions proof surface. It is static inventory only:
it does not collect GitHub run history, replay retained evidence, validate
fixed time windows, change required checks, mutate workflows, or publish
artifacts.

The source is `internal/cibaseline/inventory.json`. It records the expected
workflow files, events, permissions, jobs, matrix coordinates, runner
expressions, action references, cache and artifact operations, skip semantics,
proof types, requiredness, owners, legacy coordinate mappings, and repository
policy surfaces. Policy-surface identity includes file digests and stage-zero
Git index modes, so unreviewed content or chmod drift fails closed.

`cmd/cipolicy summary` uses this inventory to render the diagnostic CI shadow
summary. The [CI policy manifest]({{< relref "/docs/contributing/ci-policy.md" >}})
validation path also uses the inventory as the source for current static
workflow and job shape. Current mergeability still comes from the normal GitHub
required checks.

## Validate the inventory

From the repository root:

```bash
go run ./cmd/cibaseline validate
```

Validation fails when the committed inventory is stale or malformed, including
workflow file set drift, job-coordinate drift, duplicate or orphaned mappings,
missing owners, incomplete retirement rows, unjustified new obligations,
unsupported matrix semantics, unsupported policy-surface index entries, and
digest mismatch.

The Go tests run the same static drift check:

```bash
go test ./internal/cibaseline
```

## Refresh the inventory

Regenerate the checked-in inventory only after reviewing the workflow or
policy-surface change that caused drift:

```bash
go run ./cmd/cibaseline -output internal/cibaseline/inventory.json generate
```

Generation is offline and does not refresh GitHub branch protection. If
repository required contexts change, update the reviewed required-context rows
before regenerating.

Then rerun validation and the policy checks that consume the inventory:

```bash
go run ./cmd/cibaseline validate
go test ./internal/cibaseline ./cmd/cibaseline ./internal/cipolicy ./cmd/cipolicy
```

If a workflow or job is renamed intentionally, update the inventory in the same
change so the shadow summary and policy manifest still agree on the static
coordinate set.

## Command surface

`cmd/cibaseline` supports only these commands:

- `generate`: build the static inventory from the local repository.
- `validate`: compare the committed inventory with the local repository.

Supported flags are:

- `-repo`: repository root, defaulting to the current directory.
- `-inventory`: inventory path for `validate`, defaulting to
  `internal/cibaseline/inventory.json`.
- `-output`: output path for `generate`; omit it to write JSON to stdout.
  Written inventory files are created or overwritten with mode `0600`.
