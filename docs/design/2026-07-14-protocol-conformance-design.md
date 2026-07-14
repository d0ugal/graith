---
title: "Design Doc: Cross-language protocol conformance"
authors: Dougal Matthews
created: 2026-07-14
status: Implemented
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1129
---

# Cross-language protocol conformance

The wire protocol is defined twice — once in Go (`internal/protocol/messages.go`,
~119 structs) and once, by hand, in Swift
(`gui/shared/Sources/GraithProtocol/Messages.swift`, ~32 structs). Nothing kept
them in step, so divergence was only ever caught by human review. This design
makes the Go side the single source of truth for the *shape* of the protocol: it
reflects every wire struct into a language-neutral manifest, commits that
manifest as a fixture, and asserts against it from both languages so drift fails
a test rather than slipping through.

## Background

graith's daemon and its three clients (CLI, iOS, macOS) all speak one framed
control protocol. The Go structs in `messages.go` are canonical — the daemon
marshals/unmarshals them directly. The Swift app re-declares a subset of those
structs by hand with explicit `CodingKeys` so the JSON keys line up with the Go
`json:"…"` tags.

There is prior art for a "completeness guard" in this codebase:
`daemon/authmatrix.go` classifies every control message with a `remotePolicy`,
and `TestRemoteMatrixCompleteness` parses `handler.go` and fails if a `case` is
added without a matching policy row (or vice versa). It fails *closed*: a new
message with no classification is a test failure, forcing a deliberate decision.
This design reuses that pattern.

## Problem

Nothing enforces that Swift's `Messages.swift` keeps up with Go's `messages.go`.
When a Go PR adds a field or a whole message, the Swift side silently falls
behind until someone notices at review time — and reviewers miss things. The
divergence (119 vs 32 structs) is real today but invisible: there is no artifact
that says which of those gaps are intentional (server-internal messages a GUI
never needs) versus accidental (a client-facing message nobody ported).

A naive "make the Swift test red on any Go change" has a fatal wrinkle: **gui/
Swift CI only runs on `gui/**` changes** (macOS runner-minutes bill ~10× Linux —
see `gui-ci.yml`). A Go-only PR that adds a message would therefore never run the
Swift test. So a one-sided Swift guard is not enough; the drift must also be
caught on the Go side, which runs on every PR.

## Goals

- A committed, machine-readable manifest of every wire type and its JSON fields.
- A Go test that fails if the manifest is stale vs `messages.go` — on every PR.
- A Swift test that fails if `Messages.swift` can't satisfy the manifest.
- Make the current gaps explicit and reviewable (intentional vs accidental).
- Both guards wired into CI on their respective change paths.

### Non-Goals

- **Generating Swift from Go.** Codegen would eliminate the hand-written Swift,
  but it's a much larger change, couples the Swift build to a Go toolchain step,
  and throws away the deliberate consolidation the Swift side does (one
  `SessionScopeMsg` for stop/delete/restart, etc.). Out of scope; the manifest
  is a conformance check, not a code generator.
- **Byte-level encoder equivalence.** We check that Swift can *decode* a
  manifest-shaped instance of each required type (including a required-only form),
  not that Go and Swift produce identical bytes for every value. This structural
  decode check is a strong proxy — matching field names/types/optionality — but
  it does not exercise a divergent *custom encoder* on an outbound message. A
  full encode-round-trip assertion is a reasonable future strengthening; the
  hand-written Swift structs use synthesized `Codable` today, so decode symmetry
  covers them in practice.
- **Runtime protocol negotiation.** The manifest is a build/test-time artifact,
  not shipped or exchanged at runtime.

## Proposals

### Proposal 0: Do Nothing

Keep relying on review. Rejected: the 119-vs-32 gap is proof review doesn't catch
this, and every new message widens it silently.

### Proposal 1: Reflection manifest + fixture asserted from both sides (Recommended)

`internal/protocol/manifest.go` holds a `registeredTypes` slice (one zero value
per wire struct) and a `swiftAnnotations` map classifying each type's Swift
expectation: `required` (Swift models it), `planned` (a known, acknowledged gap),
or `na` (a GUI/remote client never needs it — hook-CLI↔daemon internals, MCP
proxy transport, local-only doctor/diagnostics/upgrade). `BuildManifest()`
reflects over the registry and emits, per type, its JSON field list (name, a
language-neutral type descriptor, optional flag), sorted for a stable diff.

The manifest is committed as
`gui/shared/Tests/GraithProtocolTests/Fixtures/protocol_manifest.json`.

Three guards:

1. **`TestManifestRegistryComplete`** (Go) parses `messages.go` with `go/ast` and
   fails if any exported struct is missing from `registeredTypes`, if a stale
   entry lingers, or if a registered type lacks a `swiftAnnotations` row. This is
   the fail-closed discipline from `authmatrix.go`: a new wire struct forces a
   registration + classification decision in the same change.
2. **`TestManifestUpToDate`** (Go) regenerates the manifest and diffs it against
   the committed fixture; `-update` rewrites it. Go CI has no paths filter, so
   this runs on every PR — closing the gui-CI-is-gated gap.
3. **`ManifestConformanceTests`** (Swift) decodes the same fixture and, for every
   `required` type, verifies a Swift decoder is registered, that its Swift type
   matches the manifest's `swift_type`, and that it accepts two JSON instances
   synthesized from the manifest: a *full* one (every field present — catches a
   Swift-required field the wire never sends) and a *required-only* one (optional
   keys omitted — catches a field Go marks `omitempty` but Swift models
   non-optional, a real runtime decode bug). Array elements and nested objects
   are synthesized, so element-type drift is exercised. A reverse check flags a
   Swift decoder whose type is no longer `required`.

**What conformance means (and doesn't).** Swift deliberately models a *subset* of
each Go type — only the fields the apps use, and several Go types consolidate
onto one Swift shape (`Messages.swift:9`). So the guard does **not** assert Swift
mirrors every Go field (Swift tolerates unknown keys by design); a Go-only
optional field like `DeleteMsg.purge` is fine. What it guarantees is: no whole
`required` type is missing, Swift's required-field set is a subset of Go's, and
array/nested-object shapes decode. Field *additions* to an existing required type
are not ratcheted — promoting a `planned` type to `required` is. The `Envelope`
is `required` (Swift's `ControlEnvelope`) with a dedicated `decodeControl`-based
probe, since a rename of its `type`/`payload`/`token` would break every message.

**Why the fixture lives under `gui/`, read by Go via a relative path.** A single
committed copy avoids two-files-drift. SwiftPM can only bundle a test resource
that lives inside the test target's directory, so the fixture must live under
`gui/`; the Go test reaches it by a path relative to its package dir (which
`go test` sets as the working directory). A happy side effect: regenerating the
fixture from a Go change *touches a `gui/` file*, which trips the paths-filtered
gui/ Swift CI — so on a Go PR that adds a `required` message the Swift job runs
and goes red while `Messages.swift` is behind, even though that job is normally
skipped on Go-only PRs. (gui-ci is deliberately *not* a required status check —
see `gui-ci.yml` — so this surfaces the failure rather than hard-blocking the
merge; the always-on Go `TestManifestUpToDate` is the real gate.)

**Consolidation.** Several Go types map to one Swift type (`SessionScopeMsg` for
stop/delete/restart; `SessionIDMsg` for the bare `{session_id}` requests;
`EmptyMsg` for no-payload requests). The conformance test synthesizes wire
fields and decodes; since Swift models optionals leniently, a Go-only optional
field (e.g. `DeleteMsg.purge`) doesn't break conformance — only a whole missing
type, a Swift-required field the wire never sends, or a Go-optional field Swift
models non-optional does.

Trade-offs: the `swiftAnnotations` map and the Swift decoder registry are
hand-maintained, but both are guarded (a new type fails the Go completeness test;
a stale probe fails the Swift reverse check), so they can't silently rot.

### Proposal 2: JSON Schema per message

Emit a JSON Schema document per message and validate Swift's decoders against it.
Rejected: heavier machinery for no extra signal here — we don't need
value-level validation, just structural conformance, and Schema tooling in Swift
would be a new dependency. The bespoke manifest is smaller and purpose-fit.

## Other Notes

### Current gaps (2026-07-14)

Of 119 wire types: **53 required** (all satisfied by `Messages.swift` /
`ControlEnvelope` today), **53 planned** (client-relevant, not yet modelled —
messaging, scenarios, triggers, MCP management, jail, notify, tokens, and various
response types), and **13 n/a** (hook-CLI↔daemon, MCP proxy transport, local-only
doctor/diagnostics/upgrade). The `planned` set is the reviewable backlog the
parity effort works through; promoting one to `required` (and modelling it in
Swift) is the ratchet.

### References

- Issue #1129 (this work); depends on #1128 (capability matrix — shares the
  "committed manifest as fixture" idea; not blocking).
- `internal/daemon/authmatrix.go` + `authmatrix_test.go` — the completeness-guard
  pattern this reuses.
- `internal/protocol/manifest.go`, `internal/protocol/manifest_test.go`.
- `gui/shared/Tests/GraithProtocolTests/ManifestConformanceTests.swift`.
- `gui/shared/Tests/GraithProtocolTests/Fixtures/protocol_manifest.json`.
