---
title: "Design Doc: Todo Dependency Graph"
authors: Dougal Matthews
created: 2026-07-17
status: Implemented
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/641
---

# Todo Dependency Graph

Todo items gain durable same-scope dependency edges. An item whose declared
dependencies are not all done is reported as blocked and cannot be claimed;
completing the final dependency makes it claimable in the same SQLite
transaction and emits an `unblocked` todo event. Scenario files may express the
same graph with member names, which the daemon resolves to the scenario's
seeded assigned todo items.

## Background

The first-class todo subsystem introduced by issue #591 stores durable work in
`todos.sqlite`. It already provides scoped lists, revisions, atomic claims,
assigned scenario items, claim reclamation, retention, and optional
`todo:<scope>` events. A scenario member with a `task` receives one assigned
top-level todo item, and scenario progress is derived from those assigned
items.

Today every unclaimed `todo` item is immediately claimable. Ordering between
items is expressed out of band in prose or messages. The daemon cannot answer
why a downstream item is waiting, and completion of upstream work does not
change downstream availability.

The existing `blocked` status represents a different condition: an owner has
claimed an item and then manually blocked it, usually with a note. Dependency
waiting must compose with that state without erasing its owner or note.

## Problem

Multi-stage work needs a durable readiness condition. In particular, a
scenario synthesis member should not be offered its assigned work until all
declared producer members have completed theirs. Polling `gr todo list`, asking
siblings over messages, or sending a separate "dependency complete" mutation
creates races and duplicate state.

The graph must remain correct across concurrent completion, daemon restart,
claim reclamation, and ordinary todo mutations. It also needs deterministic
answers for cycles, cross-scope edges, reopened work, manual blockers, removal,
and failures between the todo database and the message database.

## Goals

- Store arbitrary acyclic `todo -> dependency` edges durably in the todo
  database.
- Prevent claiming an item until every direct dependency is `done`.
- Commit completion and final-dependency unblocking atomically.
- Reject cycles, self-dependencies, missing dependencies, and cross-scope
  dependencies without changing the old graph.
- Expose declared and currently-unsatisfied dependencies in CLI and JSON
  output.
- Resolve scenario `depends_on = ["member-name"]` references to seeded assigned
  todo IDs.
- Preserve behavior for todos and scenarios that declare no dependencies.
- Specify reopen, manual block, removal, reclamation, retention, and
  partial-failure behavior.

### Non-Goals

- Starting, stopping, or resuming agent processes as graph nodes become ready.
  Continuous traversal (#602) consumes ready todos; this change supplies the
  readiness primitive.
- Limiting the number of simultaneously claimable items. Execution lanes
  (#603) remain responsible for concurrency policy.
- A separate event engine. This change emits through the existing todo event
  path from #109.
- Failure propagation or skip-on-failure policy. The todo state machine has no
  failed terminal state.
- Cross-scope or cross-daemon graphs.

## Proposals

### Proposal 0: Do Nothing

Authors continue to encode ordering in task prose. Agents poll or exchange
messages before beginning downstream work. This keeps two sources of truth and
leaves a read-to-claim race, so it does not meet the issue's atomicity goal.

### Proposal 1: Durable edges with computed dependency blocking (Recommended)

Add a `todo_dependencies(todo_id, dependency_id)` join table to
`todos.sqlite`. Both foreign keys reference `todos`: deleting a dependent item
removes its outgoing edges, while deleting an item that is still required is
restricted. The pair is the primary key, and reverse lookup is indexed for the
completion cascade.

Dependency blocking is an effective state derived from the graph rather than a
second stored blocker flag:

- a stored `todo` with at least one non-`done` dependency is returned as
  `status: "blocked"`, with `blocked_by` listing the unsatisfied IDs;
- a stored `todo` whose dependencies are all done is returned as `todo` and is
  eligible for the existing claim compare-and-set;
- stored `in-progress`, `done`, and manually `blocked` statuses remain
  authoritative. A manual blocker is never silently cleared by dependency
  completion.

The wire item also returns `depends_on`, the complete declared edge list. This
keeps the reason machine-readable and lets human output render a concise
"dependencies: ..." explanation. A manually blocked item continues to use its
note; if it also has unfinished dependencies, both reasons are visible.

Computing the dependency blocker avoids migrating the existing `todos` table's
owner/status check constraint. In particular, the current schema requires a
stored `blocked` item to have an owner, whereas an unclaimed dependency-waiting
item must not invent one. The graph is the durable source of truth, so no
readiness fact can drift from its dependencies after a restart.

#### Mutations and cycle checks

`gr todo add --depends-on <id>` declares edges at creation. `gr todo deps <id>
[dependency-id...]` replaces the edge set, with an empty list clearing it. The
same optional list is available in `todo_update` and MCP.

All dependency replacement happens in one transaction. The daemon normalizes
duplicate IDs, verifies every item exists in the same scope, and uses a
recursive query to reject any edge whose dependency already reaches the
dependent. A validation or write failure rolls the whole transaction back and
leaves presentation fields and the previous graph unchanged.

#### Completion and cascade-unblock

Completing an item runs in one `todos.sqlite` transaction:

1. apply the existing guarded owner/override transition to `done`;
2. find direct dependents stored as `todo` for which no dependency remains
   non-done;
3. bump each newly-ready dependent's revision and timestamp;
4. commit and return the completed item plus the newly-ready items.

The revision bump makes the readiness transition observable even though its
status is computed. Concurrent final-dependency completions serialize under the
store's writer lock and SQLite transaction; each dependent is reported as
newly ready once.

After commit, the daemon emits the ordinary event for the completed item and an
`unblocked` event on the same `todo:<scope>` stream for every newly-ready item.
The todo and message stores are separate databases, so event publication cannot
share the data transaction. Publication therefore retains the established
best-effort contract: a message failure is logged and does not roll back the
authoritative todo commit; consumers reconcile by listing the scope and
revision.

#### State-transition semantics

- **Dependency reopened:** reopening a done dependency immediately makes each
  not-yet-claimed direct dependent effectively blocked again and bumps that
  dependent's revision in the same transaction. Work already `in-progress`,
  manually `blocked`, or `done` is not unwound. This avoids revoking ownership
  or invalidating completed side effects behind an agent's back.
- **Dependency manually blocked:** it does not satisfy an edge, so downstream
  stored-`todo` items remain dependency-blocked. There is no automatic failure
  propagation; an owner or override must resolve, complete, or reopen the
  blocker.
- **Dependency removed:** removal is rejected while any item outside the same
  parent/sub-item deletion set depends on it. Authors first clear or replace
  those edges. Retention likewise keeps done items that are still referenced.
- **Dependency reclaimed after a session stops or a lease expires:** a claimed
  dependency returns to stored `todo` as today. If that item itself has
  unfinished dependencies it is exposed as blocked, not returned to the ready
  pool. Its dependents were already waiting while it was in progress, so no
  extra cascade occurs.
- **Dependency edge changed:** adding an unfinished edge blocks an unclaimed
  item; removing or satisfying the final unfinished edge unblocks it. Started
  or terminal work is not rewound.
- **Partial failure:** SQL validation/write failures roll back the entire
  mutation. Post-commit event failures are logged and reconciled from todo
  state. A manually blocked dependency represents a stalled branch, not a
  partially successful cascade.

#### Scenario authoring

Each `[[sessions]]` entry may declare member names:

```toml
[[sessions]]
name = "synthesis"
task = "Synthesize the findings"
depends_on = ["backend", "frontend"]
```

The parser and daemon both reject unknown names, duplicates, self-dependencies,
references to members without a seeded `task`, dependencies on an entry that
itself has no task, and cycles. After sessions have stable IDs, the daemon
inserts all seeded assigned items and their resolved edges in one todo
transaction. A todo failure creates none of the seed items; scenario start then
rolls back newly-created members under the existing all-or-none lifecycle.

`gr scenario add --depends-on <member>` resolves existing member names to their
original seeded assigned items. The new member cannot introduce a cycle because
existing members cannot depend on a member that did not exist when their graph
was authored.

### Proposal 2: Store dependency waiting as ordinary `blocked`

This makes simple status queries cheap, but it requires either a fake owner or
a rebuild of the constrained `todos` table plus a blocker-kind column. Every
dependency mutation must keep that redundant flag synchronized with the graph,
including crash recovery and future migrations. It also risks automatically
clearing a human blocker. The computed state is simpler and cannot drift.

### Proposal 3: Put the graph only in scenario state

Scenario names would be convenient to author, but ad-hoc todos could not use
dependencies and todo deletion/reopen semantics would split across the atomic
JSON state file and SQLite. This violates the generic primitive and atomic
completion requirements.

## Other Notes

### References

- Issue [#641](https://github.com/d0ugal/graith/issues/641)
- Todo foundation: `docs/design/2026-07-16-todo-list.md`
- Scenario model: `docs/design/2026-06-22-scenarios.md`
- Store implementation: `internal/daemon/todostore.go`
- Todo operations/events: `internal/daemon/todo.go`
- Scenario seeding: `internal/daemon/scenario.go`

### Implementation Notes

Opening an older todo database creates the join table and indexes with `CREATE
TABLE IF NOT EXISTS`; existing rows require no backfill and remain ready because
they have no edges. The protocol additions are optional fields on registered,
Swift-planned structs, preserving older clients' behavior.

### Testing

Store tests cover add/update validation, cross-scope rejection, cycles,
concurrent completion, single and multi-dependency cascades, revision bumps,
restart persistence, reopen/reclaim semantics, protected removal, retention,
and migration from a pre-graph database. Operation tests cover authorization
and emitted cascade events. Scenario parser/daemon tests cover name resolution,
invalid graphs, atomic seeded graphs, compatibility without dependencies, and
seed rollback. CLI and MCP tests cover human explanations and JSON shapes.
