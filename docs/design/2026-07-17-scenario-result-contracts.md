---
title: "Design Doc: Declared Scenario Result Contracts"
authors: OpenAI Codex
created: 2026-07-17
status: Accepted
reviewers: (pending implementation review)
informed: Graith maintainers and scenario users
issue: https://github.com/d0ugal/graith/issues/1380
---

# Declared Scenario Result Contracts

Scenario members may declare named text, Markdown, or JSON results that they
must publish before their work counts as complete. Publication is authenticated
as the calling member, writes the artifact into the existing shared document
store, and persists validation and availability metadata in scenario state so a
later fan-in member can consume exact, stable keys.

## Background

Scenarios persist member identity, role, task, lifecycle, and todo-derived
progress in `ScenarioState`. At start, the daemon sends each member a topology
manifest through its inbox and writes the same manifest to the shared document
store. Members otherwise coordinate with ordinary `gr msg` and `gr store`
commands. Scenario completion is derived from assigned todo items: all tracked
members must finish their work and no member may be errored. The same
authoritative completion edge drives configured completion actions and
lifecycle cleanup.

Message publication already forces sender identity from the authenticated
connection. The document store already validates keys, serializes writes,
writes atomically, and commits history, but direct store writes do not carry a
scenario-level declaration or completion signal. Control frames are bounded at
4 MiB, and store reads reserve framing overhead below that ceiling.

## Problem

A prompt may ask a worker to publish a review, but the scenario cannot say which
artifact is expected, where it belongs, or whether it passed format validation.
A worker can finish every todo while omitting a result, writing it under a typo,
publishing malformed JSON, or merely telling a topic that it finished. Fan-in
members must infer success from conventions and cannot consume outputs
deterministically after a daemon restart.

## Goals

- Validate stable result names, supported formats, and scenario-scoped store
  destinations before any scenario member is launched.
- Authenticate publication as the calling member without accepting a target
  member identity from the client.
- Persist pending, available, invalid, and failed metadata through restart and
  member stop/resume.
- Make required results participate in member and aggregate scenario completion
  while optional results remain observational.
- Reuse the shared document store and its size/key policy, with exact resolved
  destinations visible to fan-in consumers.
- Preserve direct messaging, direct store use, and scenarios with no contracts.

### Non-Goals

- JSON Schema evaluation; JSON syntax validation is sufficient.
- A tribunal-, review-, or verdict-specific result type.
- Replacing the shared store, attaching arbitrary blobs, or treating an
  unregistered direct store write as successful publication.
- Allowing peers, humans, or orchestrators to publish on a member's behalf.

## Proposals

### Proposal 0: Do Nothing

Prompts and topic conventions could continue to name expected files. This has
no authenticated success record, cannot distinguish a typo from an omitted
artifact, and lets todo completion overstate scenario completion. It does not
meet the fan-in or restart requirements.

### Proposal 1: Authenticated Named Store Results (Recommended)

Each `[[sessions]]` entry may contain result declarations:

```toml
[[sessions.results]]
name = "review"
format = "markdown"
store = "{session_name}/review.md"
required = true
```

Names are lowercase alphanumeric with internal hyphens and are unique within a
member. Formats are exactly `text`, `markdown`, or `json`. `store` is a relative
template whose only substitutions are `{scenario_id}`, `{scenario_name}`,
`{session_id}`, `{session_name}`, and `{result_name}`. The daemon resolves it
under `scenarios/<scenario-id>/results/`, validates the complete key with the
existing store validator, and rejects any two declarations that resolve to the
same key. Scoping is therefore structural even when a template is constant.

The CLI publishes with:

```text
gr scenario result put <name> [body]
gr scenario result put <name> --file <path>
```

Standard input is accepted through the existing body reader. `--scenario`
selects a scenario when a shared member belongs to more than one; otherwise the
CLI uses `GRAITH_SCENARIO_NAME` and the daemon may resolve a unique membership.
The control payload carries a scenario selector, result name, and body, but no
member or destination. The daemon accepts only an authenticated session, uses
the token-derived session ID, and finds that member's own declaration. A peer
can publish only its own declaration even when two members use the same result
name.

Publication is serialized separately from the session-manager state lock. The
daemon snapshots the declaration under the state lock, validates the body and
writes the shared store outside that lock, then records metadata under the lock
and persists state. Text and Markdown must be non-empty; JSON must also pass
`json.Valid`. Bodies are capped below the existing control-frame ceiling so a
successfully published artifact can also be returned through the store control
API. Validation failures become `invalid`; store failures become `failed`; a
later successful publication replaces either with `available`.

Every member manifest lists declared results for itself and siblings, including
the resolved shared-store key. `gr scenario status --json` returns the same
result metadata. Human status renders compact `name=status` summaries. A direct
`gr store put` remains legal but cannot mutate result metadata, so it cannot
forge contract success.

A member with tracked todos or required results is complete only when all its
tracked todos are done and every required result is available. Members with no
tracked todos and no required results retain existing behavior. Aggregate
scenario completion requires every tracked member to be complete and no member
to be errored. Optional results never enter this predicate. Completion actions
and lifecycle cleanup use this combined todo-and-result predicate, and every
result publication hints the authoritative reconciler so a successful result
can close an epoch and a later invalid replacement can reopen it.

The trade-off is a new control message plus additive protocol and Swift status
fields. Result content remains in the existing store, while only small metadata
is duplicated into `state.json`.

### Proposal 2: Infer Results from Store Paths or Message Topics

The daemon could poll declared store paths or topics and mark them present. A
path proves only that somebody with filesystem access wrote bytes; a topic
message has the same peer-forgery problem unless a second authenticated contract
layer is added. Polling also makes validation and state transitions racy. An
explicit self-publication operation gives one authorization and validation
boundary while still reusing the underlying primitives.

## Other Notes

### References

- Issue [#1380](https://github.com/d0ugal/graith/issues/1380)
- `docs/design/2026-06-22-scenarios.md`
- `docs/design/2026-06-22-agent-auth.md`
- `docs/design/2026-05-21-shared-document-store.md`
- `internal/daemon/scenario.go` — start, manifests, status, and completion
- `internal/daemon/authmatrix.go` — remote fail-closed control policy
- `internal/store/store.go` — validated, atomic, git-backed document writes

### Implementation Notes

The new fields are additive and omitted for scenarios without declarations, so
existing state and clients remain valid without a state-version migration.
Required Swift scenario status models gain optional/defaulted result fields;
publication remains CLI-only. Scenario start and programmatic `scenario_add`
both run the same declaration compiler. Manifest republishing uses persisted
declarations rather than re-reading the source TOML. Scenario completion
automation reads persisted result state under the session-manager lock rather
than trusting publication hints.

### Testing

Cover TOML/protocol round trips, name/format/template/collision validation,
empty and malformed JSON, oversize input, missing and misnamed results,
scenario misrouting, human and peer authorization, store failure, retry to
available, optional-versus-required completion, direct-store non-recognition,
state reload, stop/resume metadata retention, completion-action gating and
reopening, manifest destinations, CLI body and response handling, remote policy
completeness, integration publication, and race coverage around concurrent
publication. Regenerate protocol and capability fixtures, then run focused Go,
integration/race, Swift shared, docs, vet, and lint checks.
