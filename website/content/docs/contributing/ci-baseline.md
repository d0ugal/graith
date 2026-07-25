---
weight: 35
title: CI baseline and inventory
description: Validate and replay the P0 CI north-star evidence baseline
icon: chart-line
toc: true
---

# CI baseline and inventory

The CI north-star P0 inventory is a checked-in, closed-world snapshot of the
repository's current GitHub Actions proof surface. It remains explicitly
`p0-observation-in-progress`: the inventory and collector do not change
required checks, workflow behavior, permissions, or publication.

The source is `internal/cibaseline/inventory.json`. It enumerates all 18
workflow files and their events, permissions, jobs and expanded static matrix
coordinates, runner expressions, action references, cache and artifact
operations, conditional skip semantics, proof types, current requiredness, and
owners. File digests make unmodelled workflow changes visible. It also
inventories every repo-owned helper and contract test under
`.github/workflows/scripts/` and every file under root `scripts/`, with an
owner, executable compatibility contract, and retirement criterion. The
checked-in actionlint, golangci-lint, GoReleaser, Release Please, and Renovate
configuration inputs consumed by current workflow proof are inventoried with
the same metadata and file digests. The native dependency lock is an explicit
`native-owners` supply-chain surface because current producer and package
workflows consume its pinned commit, URLs, and SHA-256 values. Each policy
surface also records its
stage-zero Git index mode; conflicts, symlinks, other non-regular entries, and
chmod-only drift fail validation. The root `Makefile` and `gui/ios/Makefile`
are also explicit executable entry points because current workflows invoke
their targets directly. Policy-surface discovery reads the Git index, so
untracked and ignored dependency trees such as `node_modules/` are not
misclassified as repository-owned policy.

The current required contexts are a reviewed snapshot from the main-branch
protection API at the timestamp recorded in the inventory. Generation is
deliberately offline and does not refresh repository settings, so owners must
refresh that reviewed input when branch protection changes. This is
observation, not a repository settings change.

## Validate the inventory

From the repository root:

```bash
go run ./cmd/cibaseline validate
```

Validation fails for workflow or job-coordinate drift, missing owners, missing,
duplicate, or orphaned legacy mappings, incomplete retirement rows,
unjustified new obligations, and a non-deterministic or stale digest. Refresh
the snapshot only after reviewing the workflow and repository-settings change:

```bash
go run ./cmd/cibaseline -output internal/cibaseline/inventory.json generate
```

Each legacy coordinate currently maps to an observation-only mode prefixed
with `legacy/`. A later package may replace that destination or add an owned
retirement row, but it must not delete the legacy obligation implicitly.
The committed inventory test runs this validation under the existing Go test
workflow; no required check was added or changed.

## Collect and replay evidence

Local tests use only checked-in fixtures and require neither a token nor
network access:

```bash
go test ./internal/cibaseline
go run ./cmd/cibaseline \
  -input internal/cibaseline/testdata/braw-snapshot.json \
  -output /tmp/braw-evidence.json collect
go run ./cmd/cibaseline -input /tmp/braw-evidence.json replay
```

The `fetch` command is a read-only adapter for GitHub Actions metadata. A token,
when needed for a private or rate-limited repository, requires only repository
metadata and Actions read access:

```bash
GITHUB_TOKEN=... go run ./cmd/cibaseline \
  -repository d0ugal/graith -since 28 \
  -max-elapsed 10m -max-requests 10000 -max-retries 3 \
  -output /tmp/graith-ci-evidence.json fetch
```

Every live fetch has three finite collection limits. The defaults shown above
allow 10 minutes elapsed time, 10,000 HTTP requests including retry attempts,
and three rate-limit retries across the whole fetch. A future scheduler can
lower these with `-max-elapsed`, `-max-requests`, and `-max-retries`; negative
values are rejected. Explicit zero flag values and zero-valued
collector-library fields select the same finite defaults. Disabling retries is
not supported; the minimum explicit retry bound is one.

GitHub primary and secondary limits are retried only within all three budgets.
The adapter recognizes `429` and `403` responses carrying primary/secondary
rate-limit signals. `Retry-After` is honored for either kind. A primary limit
with `X-RateLimit-Remaining: 0` also honors the later
`X-RateLimit-Reset` boundary with a one-second cushion; a secondary limit does
not substitute that primary-quota reset for its `Retry-After`. A rate-limit
response without usable timing metadata, including a zero or already elapsed
`Retry-After`, receives a conservative 60-second delay, doubled once per prior
rate-limit retry in the same fetch. Waiting is cancellation-aware. Malformed
timing metadata, structurally incomplete success responses, exhausted retries,
a required wait that cannot fit in the remaining elapsed budget,
request-budget exhaustion, elapsed-budget exhaustion, caller cancellation, and
request transport failures produce distinct errors.

The adapter requests completed runs only, paginates run, job, artifact, and
cache lists, records authoritative workflow-run, attempt, per-attempt job,
per-run artifact, and repository-cache counts, and requires exact cardinality
at each scope. Each list requiring more than one page is fetched a second time
and must return the same ordered identity sequence and authoritative count.
Both passes and any retries consume the same request, retry, and elapsed
budgets. Over-delivery beyond an authoritative count stops immediately rather
than accumulating until the request budget expires. The adapter fails closed
on an incomplete or changing paginated response or the GitHub 1,000-result
search ceiling. Evidence
records queue and execution durations separately; every run attempt, including
startup failures and concurrency cancellations with zero jobs; every job
attempt and the first outcome; raw passed, failed, skipped, and cancelled
outcomes; separately labelled inferred supersession and its basis; runner and run-scoped
billable-minute cost inputs; repository cache-retention observations; run-level
artifact names, sizes, and API-reported digests; and canonical inventory
coordinates. Retry attempts must retain the same workflow path/ref, event, head,
branch, and pull-request identity. An unexpanded matrix row may fan out to its
known coordinates only when GitHub reports the synthetic row as completed and
skipped; replay retains that synthetic marker and requires the exact complete
coordinate set for the one raw job ID. Artifact and cache identities, sizes,
timestamps, duplicates, and any
reported artifact digest are validated before evidence is accepted. Skipped
jobs remain skipped when GitHub omits or returns non-monotonic execution
timestamps. Their measured queue and execution durations are zero rather than
fabricated. `Retry` is an
attempt-level fact (`run_attempt > 1`), while `recovered_after_retry` requires
a failed first observation for that exact coordinate. A coordinate first
materialized on a later attempt is accepted only when every earlier contiguous
attempt had zero total job observations; otherwise collection fails closed
rather than guessing whether GitHub omitted a job. The deprecated run-usage endpoint is recorded as
unavailable when GitHub returns `404` or `410` instead of discarding the rest
of the run. The cache API does not expose per-job hit or miss data, so this
foundation does not claim cache-hit coverage.

Inventory, offline snapshot, and retained evidence formats declare schema
version 2. Collection and replay reject other snapshot or evidence versions
before strict field decoding; there is no silent migration or defaulting.
Replay uses the checked-in inventory, requires the evidence's recorded
inventory digest to match it, verifies completeness and the deterministic
evidence digest, and rejects cross-workflow, unknown, or duplicate coordinates
and inconsistent retry/timestamp/outcome or supersession data. It also rejects
a skipped result represented as passed. Evidence schema version 2 is the first
format that carries authoritative counts and raw run/job identities; replay
rejects other versions before strict field decoding. The digests detect accidental
corruption and bind replay to the reviewed inventory revision; they are not
signatures or proof of authenticity. `-output` creates or overwrites evidence
files with mode `0600`; omitting it continues to write JSON to standard output.

## Four-week window and retention decision

No scheduled collector is added in P0 foundation. A repository workflow would
need a durable retention destination and an explicit owner for the read-only
Actions token and evidence retention policy. Committing evidence from a
scheduled workflow would add write permission, while ordinary Actions
artifacts do not by themselves establish the requested four-week durable
baseline. That credential, settings, and retention choice must be approved
before wiring collection rather than inferred in workflow YAML.

The adapter now supplies bounded, tested primary and secondary rate-limit
handling and finite collection defaults, but the retained 28-day collection
still requires maintainers to approve the request/time values for the
operational environment. Any exhausted budget, exhausted retry allowance,
cancellation, malformed or incomplete response, changing pagination count, or
cardinality mismatch fails the entire fetch and returns no snapshot for
evidence writing. Shorter-window fetches remain suitable for diagnostics and
offline replay validation. The approved operational budget, durable
destination, and token owner are all clock-start prerequisites; the example
`-since 28` command does not start or certify the retained window by itself.

Consequently, the retained live measurement window has **not started**. Record
its exact UTC start and immutable evidence location when that decision is made.
The earliest P0 acceptance is 28 full days after that timestamp, and only after
all owner sign-offs and replay review below are complete. P1 release remains
blocked on that P0 acceptance; this foundation does not claim the exit
criterion.

## Owner sign-off and merged-change replay

Owners review the rows for their workflows and policy surfaces:

- `graith-maintainers`: general Go, workflow policy, regeneration, and commits
- `gui-owners`: macOS and iOS GUI proof
- `native-owners`: libghostty producer and consumer proof
- `release-owners`: development, stable, and release-please proof
- `security-owners`: dependency, CodeQL, scorecard, and secret scanning
- `docs-owners`: Pages publication and pull-request previews

Record sign-off in the P0 tracking issue or pull request with the inventory
digest, reviewed rows, reviewer, and UTC timestamp. A sign-off becomes stale
when the digest changes.

For the acceptance replay, select merged changes spanning Go-only, protocol,
GUI, native, docs, generated, sandbox, release, dependency, and workflow-policy
surfaces. Fetch their retained runs, replay each evidence file, and compare the
observed coordinates with the inventory mappings. Every observed mode needs an
inventory row and owner; every expected coordinate needs a passed, failed,
skipped, cancelled, or superseded observation. An unexplained mode or an
implicit skip is an inventory failure, not an assumption to defer to cutover.
