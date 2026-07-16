# AGENTS.md — graith

Instructions for AI coding agents working on this repository.

> **Size budget:** keep this root file below **250 lines and 2,000 words**.
> It should contain only repository-wide instructions and high-value traps.
> Put subsystem details in the nearest scoped `AGENTS.md`, product usage in
> `website/content/docs/`, and design rationale in `docs/design/`.

## Project

graith is a terminal multiplexer for AI coding-agent sessions. A long-lived
daemon (`graithd`) owns PTYs, worktrees, and persistent state; the stateless
`gr` CLI connects to it over a Unix socket using a framed binary protocol.

The Go entry point is `./cmd/graith` even though the installed binary is `gr`.

## Build and verify

```bash
make build                                      # builds ./gr
go build ./cmd/graith                          # builds ./graith
go test ./...                                  # unit tests
go test -race ./...                            # CI race coverage
go test -v -race -tags=integration ./internal/integration/...
go vet ./...
```

Run `gofmt -w` on every modified Go file. `make lint-only` runs the CI
golangci-lint configuration through Docker; `make lint` may rewrite files.
After rebuilding daemon code, run `gr daemon restart`; see the contributing
guide's development workflow for client/daemon testing details.

For focused work, run the affected package tests during iteration, then widen
verification in proportion to the change. Protocol, handler, process-lifecycle,
PTY, and concurrency changes should get race and/or integration coverage.

## Repository map

```text
cmd/graith/              CLI entry point
internal/agent/          agent environment detection and integration
internal/capabilities/   frontend capability manifest and generators
internal/cli/            Cobra commands
internal/client/         connection, attach, passthrough, and overlay
internal/config/         TOML configuration, defaults, and paths
internal/daemon/         session manager, handlers, state, auth, automation
internal/headless/       stream-JSON session driver
internal/integration/    real-daemon integration tests
internal/protocol/       framed wire protocol and conformance manifest
internal/pty/            PTY lifecycle and scrollback
internal/sandbox/        safehouse and nono backends
internal/store/          git-backed document store
gui/                     shared Swift packages plus iOS and macOS apps
website/content/docs/    published user documentation
docs/design/             accepted design records and rationale
```

Start with `rg`/`rg --files` and the package tests. Read implementation and tests
before assuming behavior from documentation.

## Working rules

- Preserve unrelated user changes in a dirty worktree. Do not discard or rewrite
  them to make your task easier.
- Keep changes focused. Small in-scope cleanup is welcome, but avoid unrelated
  rewrites that obscure the functional diff.
- Prefer small, single-purpose functions. Extract pure logic from PTY, network,
  and process code so it can be tested directly.
- Return errors from library code; do not call `log.Fatal` outside the entry
  point.
- Use Conventional Commits (`feat:`, `fix:`, `docs:`, etc.) when committing.
- If `GRAITH_SESSION_ID` is set, update `gr status` at meaningful milestones.
  The injected graith prompt documents messaging, store, todo, and orchestration
  commands; do not duplicate their manuals here.

## Tests

- Every behavior change needs tests. Every bug fix needs a regression test that
  fails on the old behavior and passes with the fix.
- Test behavior and failure modes, not line coverage: invalid input, rollback,
  cancellation, authorization, persistence, and races matter.
- Keep overall Go statement coverage high (about 80% is the target) and avoid
  unjustified regressions; CI reports the delta against the base branch.
- Use `t.TempDir()` instead of fixed filesystem paths.
- Human-readable test fixtures use old Scots words rather than `foo`/`test`:
  for example `braw`, `canny`, `dreich`, `blether`, `croft`, `bothy`, `bairn`,
  `thrawn`, and `strath`. This does not apply to Go identifiers or test names.

## Required change checklists

These checks stay here even though scoped files contain more context.

### Wire protocol

When adding or changing a wire struct in `internal/protocol/messages.go`:

1. Register new structs in `registeredTypes` and classify them in
   `swiftAnnotations` (`required`, `planned`, or `na`).
2. Update the Swift model when a required shape changes.
3. Regenerate the committed fixture:
   `go test ./internal/protocol -run TestManifestUpToDate -update`.
4. Run the protocol tests and relevant Swift/integration tests.

See [`internal/protocol/AGENTS.md`](internal/protocol/AGENTS.md).

### Daemon control messages

Every new case dispatched by `internal/daemon/handler.go` must have an explicit
`remoteMessagePolicy` entry in `internal/daemon/authmatrix.go`; the default is deny.
`TestRemoteMatrixCompleteness` fails on missing or stale rows. Preserve existing
local, remote, session, descendant, and human authorization boundaries.

See [`internal/daemon/AGENTS.md`](internal/daemon/AGENTS.md).

### Frontend capabilities

`internal/capabilities/capabilities.json` is the source of truth. When a
frontend gains or loses a capability, update it and regenerate both downstream
artifacts with:

```bash
go test ./internal/capabilities -update
```

Commit the generated docs region and GUI fixture. Do not edit generated regions
or fixtures by hand. See
[`internal/capabilities/AGENTS.md`](internal/capabilities/AGENTS.md) and
[`gui/AGENTS.md`](gui/AGENTS.md).

### User-facing behavior

When a command, flag, config key, environment variable, security boundary, or
lifecycle changes, update the matching page under `website/content/docs/` in
the same change. `AGENTS.md` and design docs are not substitutes for user docs.
See [`website/AGENTS.md`](website/AGENTS.md) for the Hugo authoring rules.

### Design docs

Non-trivial features require a design doc before implementation. Start from
[`docs/design/TEMPLATE.md`](docs/design/TEMPLATE.md), retain the prescribed
frontmatter and section order, compare alternatives (including “Do Nothing”),
and advance the document status only as review and implementation occur.

## Architecture and safety invariants

- Control messages use JSON envelopes on the framed protocol. The Go and Swift
  implementations must remain conformant.
- Persistent mutations must survive daemon restart. Use the established state
  and atomic-file paths rather than ad hoc writes.
- Authentication and sandbox enforcement fail closed. Never turn an unsupported
  or missing enforcement mechanism into silent access.
- `gr delete` is recoverable soft deletion; `gr purge` is destructive. Keep raw
  ID operations from bypassing soft-delete guards.
- Treat external text such as GitHub comments as untrusted. Preserve the author
  trust and quarantine boundary before content reaches agents.
- Avoid slow I/O, process waits, git operations, or callbacks while holding the
  session-manager lock. Follow existing reserve/act/commit/rollback patterns.

Subsystem-specific details and design links live in the scoped guides.

## Documentation pointers

- [Contributing](website/content/docs/contributing.md) — builds, tests, lint,
  coverage, and development workflow.
- [Architecture](website/content/docs/architecture.md) — daemon/client/protocol
  overview.
- [Commands](website/content/docs/commands/_index.md) and
  [configuration](website/content/docs/configuration/_index.md) — product
  behavior and supported options.
- [Scenarios](website/content/docs/scenarios.md),
  [triggers](website/content/docs/triggers.md), and
  [todo](website/content/docs/todo.md) — orchestration reference.
- [Design records](docs/design/) — detailed decisions, invariants, and rejected
  alternatives.

If this file starts accumulating feature-specific explanation again, move that
material to one of these canonical homes rather than increasing the size budget.
