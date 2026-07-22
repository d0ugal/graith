---
title: "Design Doc: Focus the native GUIs on interactive session work"
authors: Dougal Matthews
created: 2026-07-17
status: Implemented (v1)
reviewers: (none yet)
informed: (TBD)
---

# Focus the native GUIs on interactive session work

> **MCP removal amendment (2026-07-22):** MCP management was removed from every
> surface by [Remove Graith-Owned MCP Support](2026-07-22-mcp-removal.md), so it
> is no longer a CLI-only capability.

The native apps should excel at interactive session work: finding sessions,
attaching to terminals, exchanging direct messages, and reading useful state.
Operational control planes, agent automation primitives, and
configuration-heavy workflows will remain in the CLI and orchestrator instead
of becoming partial native interfaces.

## Background

The capability matrix contains several iOS/macOS `planned` rows inherited from
an assumption that CLI features should eventually reach every frontend. The
platform-scope policy now distinguishes a targeted gap (`planned`) from a
deliberate product exclusion (`n/a`) linked to a design decision.

Some current rows also combine two different product shapes. Messaging combines
direct conversations with topic publish/subscribe, even though the apps only
implement direct messages. Document-store access combines a useful read-only
browser with agent-oriented put/append/remove mutations.

## Problem

Treating every daemon operation as future native-app work creates an indefinitely
growing settings and administration surface. Many of these operations are
streaming, configuration-heavy, security-sensitive, or primarily invoked by
agents and scripts. A phone UI is especially poorly suited to them, while a
desktop-only implementation would weaken native parity and still duplicate a
better terminal workflow.

The over-broad capability rows also obscure what the apps genuinely support.
For example, a direct-message composer should not imply topic-subscription UI,
and a document viewer should not imply arbitrary store mutation.

## Goals

- Define a coherent native-app boundary centered on interactive session work.
- Convert unsuitable planned work into explicit, design-linked exclusions.
- Split bundled capabilities where a useful native subset already exists.
- Keep protocol conformance aligned with the chosen native scope.
- Preserve CLI, daemon, orchestrator, and agent behavior unchanged.

### Non-Goals

- Removing any excluded capability from the CLI or daemon.
- Removing direct messages, diagnostics, configuration viewing, or read-only
  document browsing from the native apps.
- Preventing future reconsideration through a new design decision.
- Building the targeted read-only todo overview in this change; this decision
  only separates its scope from todo mutation.

## Platform support

The following capability scope is intentional:

| Capability | CLI | iOS | macOS | Rationale |
|------------|-----|-----|-------|-----------|
| Interactive Graith approval queue and responses | Removed | Removed | Removed | Issue [#1392](https://github.com/d0ugal/graith/issues/1392) removed Graith's queue; native agent approval TUIs remain agent-owned, while sandbox and command policy are independent configuration. |
| Manage paired devices | Targeted | Excluded | Excluded | Management is local-human-only and remote-denied; apps pair with a daemon but do not administer its trust list. |
| Direct messages | Targeted | Targeted | Targeted | A focused human-to-session conversation fits both native apps. |
| Topic publish/subscribe | Targeted | Excluded | Excluded | Topics are an agent/orchestrator coordination primitive rather than a human chat UI. |
| Jailed PR comments | Targeted | Excluded | Excluded | Inspecting and releasing quarantined untrusted input is a security moderation workflow. |
| Todo list/progress view | Targeted | Targeted | Targeted | Read-only progress is useful native context and remains planned for both apps. |
| Todo mutation | Targeted | Excluded | Excluded | Add/claim/assign/transition/remove operations belong to agents and orchestrators; no partial native editor is planned. |
| Trigger management | Targeted | Excluded | Excluded | Trigger lifecycle is an automation control plane. |
| Document mutation | Targeted | Excluded | Excluded | Put/append/remove are agent and scripting primitives; native read-only browsing remains supported. |
| Send notifications | Targeted | Excluded | Excluded | Agents and scripts send notifications; native apps are notification recipients and presenters. |
| Sandbox introspection | Targeted | Excluded | Excluded | Explain/watch output is diagnostic and streaming, making the terminal the natural interface. |

Capability rows excluded here link to this section. Direct-message and document
browsing rows remain supported on all three surfaces.

## Proposals

### Proposal 0: Keep every current `planned` row

This preserves maximum theoretical parity but treats the native apps as generic
administration clients. It leaves no product rule for deciding which future
daemon operations deserve native presentation.

### Proposal 1: Interactive native boundary (Recommended)

Mark the operational capabilities above `n/a` on both native platforms and link
them to this decision. Split messaging into direct send/read (supported) and
topic publish/subscribe (excluded). Split todo into a targeted read-only overview
and excluded mutation. Narrow document mutation to put/append/remove while
retaining the existing browse/read capability.

This decision originally removed the queue, response actions, subscriptions,
and Swift wire models only from the native apps. Issue
[#1392](https://github.com/d0ugal/graith/issues/1392) subsequently removed the
same workflow from the CLI, daemon, and protocol, replacing it with mandatory
sandbox enforcement and an optional subtractive command policy.

Reclassify Swift protocol expectations from `planned` or `required` to `na` when
the corresponding wire type has no remaining native consumer. Delete now-unused
Swift management models and conformance probes. Shared wire types that still
serve a supported subset remain required: for example, `MsgPubMsg` carries both
direct inbox sends and topic publications, so direct messaging keeps it in the
Swift surface even though topic UI is excluded. Store list/get types similarly
remain required for the native document browser.

### Proposal 2: Desktop administration app, mobile session app

Keep these controls planned for macOS but exclude them on iOS. Desktop space
makes some presentations possible, but it would permanently split the native
product model and still duplicate strong CLI workflows. Rejected.

## Other Notes

### References

- `internal/capabilities/capabilities.json` — frontend support policy.
- `internal/protocol/manifest.go` — Swift protocol expectations.
- `docs/design/2026-07-17-platform-scope-policy.md` — design-linked exclusion
  mechanism.
- `website/content/docs/capabilities.md` — generated public matrix.

### Implementation Notes

Most affected capabilities are not implemented in the apps, so their product
change is a manifest and protocol-backlog correction rather than UI deletion.
Pair-management protocol types previously modelled speculatively in Swift can
be removed as long as the separate pairing-request/authentication flow retains
its required messages. Approval subscription and response types likewise become
not applicable after their last native consumer is removed.

### Testing

- Capability validation requires this design link on every excluded row.
- Generated docs show direct-message, read-only todo, and document-browser
  support separately from excluded topic and mutation operations.
- Protocol conformance rejects stale Swift probes for types reclassified `na`.
- Shared Swift tests and both app builds prove pairing and supported features
  still compile after unused management models are removed.
- Go protocol and capability tests prove fixtures remain current.
