---
title: "Design Doc: Remove Command Policy"
authors: Dougal Matthews
created: 2026-07-22
status: Accepted
reviewers: Issue #1576 decision
informed: Graith users configuring command_policy
issue: https://github.com/d0ugal/graith/issues/1576
---

# Remove Command Policy

Graith removes its optional `command_policy` shell filter in one explicit
breaking change. Agent-native approvals and sandboxes remain under each agent's
control, while Graith's separately configured OS sandbox remains a capability
boundary. Users needing semantic command filtering must configure an
agent-native hook or an external policy tool directly.

## Background

`command_policy` is a global, opt-in hook which evaluates proposed shell
commands in the daemon. Graith generates Claude, Codex, and latent Cursor hook
configuration which calls a hidden `gr command-policy-check` worker. The worker
sends a synchronous `command_policy_check` control message to the daemon, which
evaluates either Graith's clean-room localmost-compatible engine or an external
localmost process and returns `command_policy_decision`.

The feature is independent of native approval prompts and Graith's OS sandbox.
Its useful property is narrower and different: it can deny a semantic command
shape, such as `gh api`, even when a broader executable remains available and
native prompts have been disabled. It covers shell-hook calls only and is not a
host containment boundary.

Policy configuration is stored globally and a copy is persisted in each
session's creation snapshot for staleness reporting. Decisions and queues are
not persistent. Policy-only Codex sessions receive inline hook overrides and
the process-wide `--dangerously-bypass-hook-trust` flag; old Claude sessions
refer to generated settings below the per-session data-directory hook path.

## Problem

The feature's security value does not justify its boundary ambiguity and
maintenance cost. Enforcement depends on mutable agent hook contracts, omits
non-shell tools, and can be escaped through sufficiently powerful allowed
commands. The built-in parser has previously failed open on empty `unless`
rules and hidden substitutions and needed explicit resource-exhaustion bounds.
Backend availability, config reload, rule paths, nested deadlines, and
CLI/daemon skew can instead turn a healthy session into deny-all behavior.

Removing the code mechanically would create a new security downgrade. A
policy-enabled old session can survive an exec daemon upgrade with its generated
hook still installed. If the removed command or handler silently allowed, the
operator's configured guarantee would disappear; if it failed closed forever,
the session would wedge. Likewise, silently ignoring an old config would make a
new session appear protected when it is not.

## Goals

- Remove the config schema, engines, CLI commands, hook worker, daemon path,
  protocol messages, authorization row, and persisted creation snapshot.
- Hard-fail legacy `[command_policy]` configuration with a concise migration
  diagnostic which never reproduces policy values or secrets.
- Make policy-enabled live sessions cross the removal boundary through a clean,
  explicit restart rather than continuing with an old hook.
- Remove policy-only generated artifacts and Codex's hook-trust bypass while
  preserving independent lifecycle hooks and their existing injection.
- Preserve native agent controls, Graith OS sandbox enforcement, authentication,
  state backup atomicity, and downgrade refusal.
- State plainly that centralized semantic shell denial independent of native
  prompts is the guarantee being removed.

### Non-Goals

- Enabling Graith's sandbox by default or changing its configured behavior.
- Answering, weakening, or replacing agent-native approval prompts or sandboxes.
- Adding another policy DSL, advisory mode, compatibility handler, or daemon
  authorization service.
- Claiming that an OS sandbox or a direct agent hook exactly reproduces the
  removed semantic policy.
- Removing generic lifecycle hooks, status reporting, or inbox integration.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted removal | The CLI owns configuration loading, policy subcommands, the hidden worker, session launch, and migration diagnostics. |
| iOS | Targeted generated-protocol update only | The removed messages were intentionally not modelled in Swift and no policy UI exists; the shared fixture must still reflect the Go registry. |
| macOS | Targeted generated-protocol update only | As on iOS, there is no product policy surface, but protocol conformance must record the removal. |

## Proposals

### Proposal 0: Do Nothing

Retain the optional filter and continue maintaining its parser, external
backend, hook contracts, deadlines, protocol, authorization, lifecycle, and
documentation. This preserves centralized semantic denial but also preserves a
large security-adjacent surface whose guarantees are easy to overstate and
whose failure modes can be either fail-open or workflow-wide deny-all. It does
not resolve the product-boundary mismatch.

### Proposal 1: Immediate removal with a clean session boundary (Recommended)

Delete the feature in one release. Configuration loading performs a narrow raw
TOML presence check before ordinary validation. Any `[command_policy]` table is
rejected by name with guidance to use agent-native hooks or an external policy
tool for semantic filtering and Graith's OS sandbox for capability confinement.
The diagnostic never formats the table or any child value. The previously
removed `[approvals]` diagnostic is updated so it no longer recommends
`command_policy`.

The protocol remains at major version 2 because removal does not change any
required remote-client shape and compatibility is enforced at the affected
session boundary instead. The local/internal `command_policy_check` and
`command_policy_decision` structs, registry annotations, handler case, and auth
matrix row disappear. An old caller receives the ordinary bounded unknown-
message error; no compatibility handler returns allow or keeps the evaluator
alive.

State version 28 is allocated after issue #1575's version 27 migration. During
decoding, a migration-only reader detects a non-empty legacy policy backend in
the old creation snapshot without retaining or re-serializing its values. The
v27-to-v28 migration removes that snapshot and marks each affected live session
non-adoptable. During exec handoff the existing ownership guard drains inherited
descriptors, terminates the exact recorded process identity, and refuses daemon
publication if cleanup cannot be proven. Cold startup applies the same exact-
identity cleanup before serving. Only after process cleanup does Graith remove
the affected session's generated hook directory and ownership metadata and
commit migrated state. Affected sessions remain stopped with an actionable
summary; an explicit `gr resume` regenerates only currently configured generic
hooks. Unaffected sessions retain the normal adoption path.

Migration is intentionally two-step because the replacement must reject the
old configuration. Before upgrading, the operator archives and removes the
`[command_policy]` table and, if needed, configures a direct agent hook or
external policy. The old daemon may hot-reload the now-disabled policy, but its
already-generated hook remains installed until the upgrade cleanly stops that
session. The operator then upgrades and resumes each affected session. A
capacity probe or startup against an unmodified old config fails before any
daemon or session ownership changes.

State migration uses the existing atomic save and pre-migration backup path.
An upgrade directly from state v26 through the predecessor migration to v28
keeps the exact v26 file as `state.json.v26.bak`; an upgrade from v27 keeps a
v27 backup. A v27 or older binary refuses v28 state. Downgrading therefore
requires stopping the daemon, restoring the matching state backup, restoring
the archived old config, and then starting the older release. Restoring only
the binary/state does not restore policy because the new config no longer
contains it.

This deliberately loses centralized deterministic shell-command denial that
is independent of native prompts. Native controls and OS sandboxing remain
useful complementary layers, but neither is represented as a semantic
replacement.

### Proposal 2: Deprecate or default off before removal

Keep the evaluator for one release, warn configured users, and remove it at a
later boundary. This lowers immediate migration pressure but retains the full
security and maintenance surface. The feature is already opt-in and default
off, so another inactive phase adds little discovery value. Issue #1576
explicitly chooses an immediate, visible break instead.

## Other Notes

### References

- Issue [#1576](https://github.com/d0ugal/graith/issues/1576), the removal
  decision and acceptance criteria.
- [Remove Graith interactive approvals](2026-07-18-non-interactive-sandbox-policy.md),
  the now-superseded decision to retain command policy.
- [Pluggable Approvals Backends](2026-07-03-pluggable-approvals-backends-design.md),
  historical rationale for the parser and backend model.
- [Nono sandbox design](2026-07-02-nono-sandbox-design.md), the independent OS
  capability boundary which remains supported.

### Implementation Notes

Issue #1575 removes Graith-owned MCP support first and owns state version 27.
This change rebases after it, owns version 28, composes both removed-key
diagnostics without exposing values, and regenerates protocol, Swift,
capability, and package dependency artifacts from the combined tree.

Policy-specific sandbox grants disappear, but sandbox selection and enforcement
remain fail closed. Hook generation is simplified around the remaining
`AgentHooks` lifecycle choice: no hook request means no hook arguments and no
Codex trust-bypass flag; a lifecycle-hook request still emits its existing
event configuration and trust flag. Generated Claude settings no longer have a
policy-only `PreToolUse` group.

Historical design records retain their rationale and are marked superseded by
this document rather than deleted.

### Alternatives considered

A one-release tombstone handler could return an explicit deny to old hooks. It
would keep every affected session wedged and preserve a security-sensitive wire
surface without a recovery path. Returning allow would be a silent downgrade.
The clean-restart migration resolves the process instead, so no bridge is kept.

Removing only the built-in parser or only the external backend would leave most
of the cross-cutting hook, timeout, config, protocol, state, and lifecycle
machinery. Moving the parser into another Graith-owned helper would retain the
same product guarantee and maintenance obligation. Both are rejected.

### Testing

- Config tests cover any legacy table shape, mixed removed namespaces, an
  actionable fixed diagnostic, and secrets in keys/values which must not appear
  in errors.
- Hook tests prove lifecycle-only Claude and Codex injection still works,
  policy-only injection disappears, and Codex receives no hook-trust bypass
  when no generic hook was requested.
- State tests start from exact pre-removal JSON, verify v28 removal, backup
  bytes, migration idempotence, and older-binary downgrade refusal.
- Upgrade coverage starts with the exact old-current-main config, creation
  snapshot, generated hook, and a live process identity. It verifies config
  must be migrated first, the process cannot be adopted, bounded cleanup
  completes, policy-only artifacts disappear, failures refuse publication, and
  a later resume uses only current lifecycle configuration.
- Protocol generation and tests prove the registry and Swift fixture no longer
  expose either message. Daemon auth-matrix completeness proves the dispatch
  and policy row were removed together and unknown messages remain denied.
- Focused Go tests are followed by daemon/protocol race tests, tagged
  integration coverage, shared Swift tests, docs/package-graph checks, and the
  full repository test suite.
