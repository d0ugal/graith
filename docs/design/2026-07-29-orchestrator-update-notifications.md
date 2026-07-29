---
title: "Design Doc: Orchestrator Update Notifications"
authors: Codex
created: 2026-07-29
status: Implemented
reviewers: Codex, Claude
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1845
---

# Orchestrator Update Notifications

When the daemon observes that the running Graith build differs from the build it last recorded, it records a durable system-event outbox item for the enabled orchestrator. Delivery uses the existing daemon-authored inbox notification path, so the event is visibly automated and survives a stopped orchestrator.

## Background

Graith already has daemon-authored inbox messages through `notifyFromDaemon`: they use the synthetic `graith:system` sender, are marked as system messages by the message store, and auto-resume stopped inbox targets. The daemon also persists session state in `state.json`, while message history lives in the message store.

The update path already knows the active and replacement versions during exec upgrades, but cold starts and daemon restarts need the same behavior. The durable comparison point therefore has to live in daemon state, not only in the upgrade request.

## Problem

An enabled orchestrator has no reliable way to learn that Graith changed. It may continue operating with stale assumptions about commands, configuration, triggers, or skills until a human notices. A direct notification is also easy to get wrong: firing on every restart would spam the orchestrator, while firing only when the orchestrator is running would lose events during upgrades that stop or recreate it.

## Goals

- Notify only enabled orchestrators about detected Graith build transitions.
- Include previous version, new version, detection time, commit metadata, and a transition classification.
- Avoid notification storms across ordinary daemon restarts and repeated delivery attempts.
- Preserve delivery for a stopped or not-yet-created orchestrator.
- Keep the notice visibly daemon-authored, not a normal agent message.

### Non-Goals

- Fetching release notes automatically.
- Guaranteeing detection when both old and new binaries expose no useful version or commit metadata.
- Adding GUI-specific update surfaces.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | The CLI daemon owns startup, state, and orchestrator inbox delivery. |
| iOS | Excluded | Native clients consume daemon state; they do not run orchestrator control logic. |
| macOS | Excluded | The macOS app may display the resulting inbox state later, but detection and delivery are daemon behavior. |

## Proposals

### Proposal 0: Do Nothing

The orchestrator remains unaware of updates. This avoids state changes, but it leaves the workflow that most needs to adapt to new control-plane capabilities without a signal.

### Proposal 1: Durable Daemon Outbox (Recommended)

Persist the last observed Graith build in daemon state. On startup or upgrade adoption, compare the current build identity with the persisted one. If it changed and `[orchestrator] enabled = true`, append a pending update notice with a deterministic event ID for that observed transition. The notice is delivered through `notifyFromDaemon` after orchestrator reconciliation finds or creates the orchestrator session.

The message uses a deterministic thread ID that includes the previous and current build observations, so retries of one persisted notice dedupe while a later real transition through the same version pair remains distinct. Before publishing, delivery checks whether that thread already exists in the orchestrator inbox; this prevents duplicate messages if publication succeeded but clearing the pending state failed. Pending state is cleared only after the message exists.

This approach gives at-least-once durable delivery with practical deduplication, reuses the existing system-message semantics, and avoids a new transport.

### Proposal 2: Publish Immediately During Upgrade

The upgrade handler could publish directly before `exec`. That path has access to target metadata, but it cannot handle cold replacement, ordinary startup, or an orchestrator that is stopped or absent during the handoff. It also couples notification delivery to the fragile descriptor handoff window.

## Consensus

Independent review agreed with the durable outbox and system-sender transport. Review changed two details before merge: event IDs include observation timestamps so repeated transitions through the same version pair remain distinct, and pending notices drain from orchestrator reconciliation and successful supervisor restart paths rather than only the startup hook.

## Other Notes

### References

- https://github.com/d0ugal/graith/issues/1845
- `internal/daemon/update_notification.go` — update detection, formatting, outbox delivery, and deduplication.
- `internal/daemon/orchestrator.go` — orchestrator startup, reconciliation, and restart delivery hooks.
- `internal/daemon/notify.go` — daemon-authored system inbox notification transport.
- `internal/daemon/state.go` — persisted build baseline and pending update notice state.
- `internal/version/update.go` — version comparison helper.

### Implementation Notes

The added state fields are optional and do not require a state-version bump. Older binaries ignore them; if an older binary rewrites state, the next new binary treats that as a fresh baseline rather than synthesizing a possibly wrong event.

When version comparison succeeds, the daemon classifies the transition as `upgrade`, `downgrade`, or `restart_without_version_change`. If either version is unavailable or unparsable, it reports `version_metadata_unavailable` and still includes commit fields when present.

Version comparison keeps the existing `internal/version` numeric parsing behavior, including prerelease suffix normalization. A prerelease-to-final replacement with the same numeric version may therefore appear as `restart_without_version_change`; commit metadata remains in the notice to distinguish the build.

### Testing

Unit tests cover version comparison, transition classification, first-start baseline behavior, disabled orchestrators, repeated version-pair transitions, delivery deduplication, failed-delivery retention, unavailable metadata formatting, persisted state clear, and orchestrator reconciliation/restart delivery hooks. Focused daemon race tests cover the update notification paths without running the full integration suite.
