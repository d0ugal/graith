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

Graith removes its opt-in `command_policy` shell filter in one breaking
change. Native agent controls and Graith's independently configured OS sandbox
remain; users needing semantic filtering must configure an agent-native hook or
an external policy tool directly.

## Background

The global feature generated Claude, Codex, and latent Cursor hooks which called
a hidden CLI worker and synchronous daemon evaluator. It supported a built-in
localmost-compatible parser or external localmost backend, and persisted a copy
of its configuration in each session's creation snapshot.

Its useful guarantee was narrow but real: Graith could centrally deny a shell
command shape such as `gh api`, independently of native prompts. It covered
hook-mediated shell calls only and was never a host containment boundary.

## Problem

This guarantee does not justify the parser, backend, hook-contract, timeout,
protocol, authorization, state, and lifecycle surface. The parser has had
fail-open and resource-exhaustion defects, while backend or version skew can
instead produce deny-all behavior. Simply deleting the handler would also leave
a live old hook either silently unprotected or indefinitely denied.

## Goals

- Remove the schema, engines, commands, hooks, evaluator, protocol messages,
  authorization row, and persistent creation-config field.
- Reject old configuration without printing rules, commands, paths, or secrets.
- Force policy-enabled live sessions across a clean restart boundary and retire
  only Graith-owned policy artifacts.
- Preserve native controls, generic lifecycle hooks, Graith sandboxing,
  authentication, atomic state backups, and downgrade refusal.
- Explicitly acknowledge loss of centralized semantic shell denial independent
  of native prompts.

### Non-Goals

- Enabling Graith sandboxing by default or changing native agent safeguards.
- Adding a replacement DSL, advisory mode, daemon service, or compatibility
  evaluator.
- Claiming direct hooks or OS sandboxing reproduce the removed semantic rules.
- Removing generic lifecycle hooks.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted removal | It owns configuration, commands, launch hooks, and diagnostics. |
| iOS | Generated update only | No policy UI or required Swift message existed. |
| macOS | Generated update only | No policy UI or required Swift message existed. |

## Proposals

### Proposal 0: Do Nothing

Retain the optional evaluator and its centralized semantic-denial guarantee.
This also retains a disproportionately large security-adjacent surface whose
limits are easy to overstate and whose failures can be fail-open or deny-all.

### Proposal 1: Immediate removal with a clean session boundary (Recommended)

Delete the feature in one release. Raw TOML detection rejects any legacy
`[command_policy]` table with fixed guidance to use a native hook or external
policy for semantics and Graith `[sandbox]` for OS isolation. The diagnostic
never formats child keys or values.

Remove the internal wire messages, dispatch, and remote-auth row together. Old
calls receive the ordinary bounded unsupported-message response; no bridge can
return allow or keep sessions wedged.

Issue #1575 owns state v27, so this removal owns v28. A migration-only decoder
recognizes a non-empty old backend without retaining it. The v27-to-v28
migration marks affected running sessions for exact-process cleanup, prevents
adoption, removes policy-only generated artifacts after process identity is
resolved, and leaves the session explicitly resumable. Unaffected sessions keep
normal adoption. Generic hooks are regenerated only when still configured, so
policy-only Codex sessions lose `--dangerously-bypass-hook-trust`.

The existing fail-closed migration path writes an exact v26 or v27 backup before
any mutation. Older binaries refuse v28. Downgrade therefore requires stopping
the daemon, restoring the matching `state.json.v<oldVersion>.bak`, restoring
the archived old configuration, and starting the old binary. Restoring only the
binary or state does not restore the former policy.

This deliberately loses centralized deterministic shell-command denial
independent of native prompts. Native controls and OS sandboxing are useful
complementary layers, not equivalent replacements.

### Proposal 2: Deprecate or default off before removal

Warn for a release and remove later. The feature is already opt-in and
default-off, so this adds little discovery value while retaining all of the
security and maintenance surface. Issue #1576 explicitly chooses immediate
removal.

## Other Notes

### References

- Issue [#1576](https://github.com/d0ugal/graith/issues/1576).
- [Pluggable approvals backends](2026-07-03-pluggable-approvals-backends-design.md)
  and [non-interactive sandbox policy](2026-07-18-non-interactive-sandbox-policy.md)
  remain unchanged as historical rationale; this record supersedes their
  command-policy decisions.
- [Nono sandbox design](2026-07-02-nono-sandbox-design.md) describes the
  independent OS capability boundary.

### Implementation Notes

The change stacks after #1575, composes both removed-config diagnostics without
value disclosure, retains protocol major 2 for surviving compatible traffic,
and regenerates protocol, Swift, capability, and dependency artifacts from the
combined tree. Policy-specific sandbox grants disappear; sandbox selection and
enforcement remain fail closed.

### Alternatives considered

A tombstone handler returning deny would preserve a permanent wedge; returning
allow would silently weaken security. Keeping only one backend or moving the
parser elsewhere would retain most cross-cutting machinery and the same product
obligation.

### Testing

Unit coverage checks secret-free configuration errors, v27-to-v28 migration and
exact backups, hook/trust-flag behavior, artifact ownership, unsupported old
messages, and auth-matrix completeness. A tagged integration fixture builds the
literal pre-removal commit, starts a policy-enabled live session, upgrades it,
proves bounded cleanup and explicit resume, and verifies downgrade refusal.
Protocol/capability generators, Go race/integration tests, Swift tests, and
documentation checks complete the verification.
