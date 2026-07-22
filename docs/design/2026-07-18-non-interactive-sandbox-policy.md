---
title: "Design Doc: Remove Graith interactive approvals"
authors: Dougal Matthews
created: 2026-07-18
status: Implemented
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1392
---

# Remove Graith interactive approvals

Graith removes its human tool-approval loop. Agent-native approval prompts remain
available and are owned entirely by the agent's TUI. Graith's OS sandbox is
enabled by default but may be explicitly disabled when the operator relies on
native controls, an external sandbox, or a VM. An optional localmost-compatible
command policy remains as an independent synchronous deny layer: it may stop a
shell command, but an allow never grants a capability.

## Background

Graith currently has two overlapping security mechanisms. The OS sandbox wraps
the whole agent process and constrains filesystem, process, signal, socket, and
network access. Separately, generated agent hooks call `gr approve-request`,
which asks an approvals backend and may enqueue a request for a human. Pending
requests are daemon state and are exposed through protocol messages, terminal
overlays, status counts, notifications, and historical native-client models.

The approvals backend set mixes different concepts. `prompt` is the human
queue, `auto` and per-session `Yolo` always allow, `command` delegates trust to
an arbitrary executable, while `builtin` and `localmost` implement a useful
deterministic shell-command restriction. Headless sessions have a separate
`can_use_tool` bridge because they have no attached human.

Supported agents also have their own permission prompting and, in some cases,
their own sandbox. Those native layers can pause independently of Graith's
queue or impose a different capability boundary from Graith's configured OS
sandbox.

## Problem

The human queue makes unattended sessions, scenarios, triggers, remote
sessions, and resumes capable of stalling on a person who may never be present.
It also creates an unsafe ambiguity: an automated “allow” can be mistaken for a
permission grant even though only the OS sandbox should decide what the process
can actually access. Failures in hook transport have historically needed a
choice between failing open and stranding a session.

Keeping the queue requires protocol and lifecycle state whose only purpose is
to pause execution. Agent-native prompts are different: the agent already owns
their presentation and response path, so Graith treats time spent in that TUI as
ordinary running state.

## Goals

- Ensure no session can wait for a Graith-owned tool-approval response.
- Allow operators to retain or disable each agent's native approval UI.
- Default to Graith's enforceable OS sandbox while allowing explicit opt-out
  with a startup warning and `gr doctor` diagnostic.
- Retain the built-in and native localmost command evaluators only as optional,
  bounded, fail-closed restrictions on shell commands.
- Make deny, ask, malformed input/output, timeout, missing backend, and runtime
  evaluation failure deterministic and immediately visible to the agent.
- Remove approval queues, responses, wire types, UI, status, notifications,
  configuration, per-session bypasses, and obsolete backend code.

### Non-Goals

- Preserving old `[approvals]` configuration, client protocol compatibility,
  or the `--yolo` flag.
- Letting command policy grant filesystem, process, signal, socket, or network
  access.
- Replacing OS sandbox configuration with a command allowlist.
- Detecting or attesting an external sandbox or VM.
- Adding command-policy editing to the iOS or macOS applications.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | The CLI launches agents, exposes `gr sandbox policy check/validate`, and reports sandbox or policy startup failures. |
| iOS | Targeted removal only | Approval state, Yolo controls, counts, glyphs, and wire fields disappear; command policy is host configuration rather than mobile interaction. |
| macOS | Targeted removal only | The macOS app removes the same approval and Yolo surfaces and continues to consume ordinary session state. |

## Proposals

### Proposal 0: Do Nothing

Keep the queue and make unattended callers choose `yolo` or an auto backend.
This preserves a workflow that can pause indefinitely, keeps an allow-all path,
and leaves Graith with two competing permission models. It does not meet the
automation or security goals.

### Proposal 1: Independent native prompts, sandbox, and deny policy (Recommended)

Treat three controls independently. First, `non_interactive_args` is an optional
launch prefix. Bundled definitions retain unattended defaults, while clearing
the list preserves the agent's native approval TUI; Graith does not answer,
count, or map those prompts to a workflow status. Second, the merged Graith
sandbox defaults on. When enabled it must select an available backend and every
requested primitive fails closed; when explicitly disabled the process starts
with a one-time warning and a doctor diagnostic. Third, command policy can be
enabled with either sandbox setting and only subtracts from the resulting
capabilities.

OpenCode's TUI non-prompting mode is `--auto`: it approves requests that would
otherwise ask while preserving explicit native denies. Cursor's force mode is
non-interactive, but its current hook runner can drop a fast synchronous deny,
so Cursor sessions are rejected when command policy is configured until an
upstream version provides a verified deny contract.

Replace `[approvals]` with optional `[command_policy]`. An empty backend means
the feature is disabled. The only backends are `builtin` and `localmost`; both
are limited to shell commands and have a bounded evaluation timeout. A
configured policy forces installation of a shell-tool hook even when lifecycle
hooks are disabled. Agent definitions without a supported synchronous blocking
hook fail at session start when policy is configured rather than running
without enforcement.

The hook invokes a renamed hidden `gr command-policy-check` command. It sends a
single authenticated `command_policy_check` request to the daemon and receives
one `command_policy_decision` response. There is no request identifier, queue,
subscription, notification, deadline owned by a human, or durable state. The
daemon evaluates the full command input immediately. The hook command is a
hard-deadline supervisor around a child worker: worker crash, signal, malformed
output, or timeout is rendered as an agent-native deny, while failure to start
the supervisor is converted to the hook runner's blocking exit-2 contract. The
agent runner timeout is deliberately longer than the supervisor deadline, so a
runner timeout cannot turn a policy transport failure into an allow:

| Result | Command-policy meaning |
|--------|-------------------------|
| allow | Continue without granting permissions; any enabled Graith, native, or external controls still apply. |
| deny | Emit an immediate deny with the rule reason. |
| ask/defer | Deny immediately because no human decision path exists. |
| malformed tool input or backend output | Deny immediately with a diagnostic reason. |
| timeout or evaluation error | Deny immediately with a diagnostic reason. |
| backend unavailable at create/resume | Fail session startup before spawning the agent. |
| non-shell tool | Do not invoke policy; proceed directly to normal agent execution. |

Agent-native permission events are no longer mapped to `agent_status=approval`.
For interactive PTY sessions they leave status unchanged and remain in the
agent's TUI. A headless `can_use_tool` request is denied immediately and marks
the driver degraded because there is no TUI in which a human can respond.

Remove the old approval wire messages and fleet count, daemon maps and
subscribers, human responders, terminal overlay and keybindings, notification
setting, `gr approvals`, `gr approve-request`, arbitrary command approvers,
prompt/auto backends, Yolo state, and the user-selectable Codex approval flag.
Move localmost parser code to `internal/commandpolicy` and retain its focused
unit and CLI validation coverage.

The trade-off is explicit responsibility: disabling Graith's sandbox may expose
daemon tokens, sibling worktrees, and host credentials unless another boundary
protects them. Graith warns but cannot verify external isolation. A configured
sandbox or command-policy backend still fails closed rather than silently
downgrading.

### Proposal 2: Require Graith's sandbox for every session

This gives Graith a uniform capability boundary but prevents deliberate use in
an already-sandboxed container, VM, or host policy and prevents manual native-
approval workflows. Rejected in favour of an explicit, diagnosed opt-out.

## Other Notes

### References

- Issue [#1392](https://github.com/d0ugal/graith/issues/1392).
- `docs/design/2026-07-02-nono-sandbox-design.md` for sandbox availability and
  fail-closed enforcement.
- `docs/design/2026-07-03-pluggable-approvals-backends-design.md` for the
  localmost-compatible parser that this decision retains and narrows.
- `docs/design/2026-07-13-headless-stream-json-design.md` for the headless
  control-channel lifecycle that no longer carries permission decisions.

### Implementation Notes

No compatibility shim is added. Old approval wire messages become unknown,
old `[approvals]` configuration is rejected, and removed Yolo fields are not
restored from persisted state. The historical state migration chain remains
structurally readable but clears approval-era runtime agent status. The wire
protocol major version advances to 2 so an old
approval-aware client cannot connect under false assumptions. This is an
intentional breaking transition rather than a staged deprecation.

The first protocol-1 to protocol-2 upgrade is therefore a clean security-boundary
restart, not an exec adoption. The protocol-2 client never sends the old daemon a
preserve request: it identifies the exact Unix-socket peer and its process start
time, asks that daemon to stop gracefully while it still owns all PTY and headless
agents, and proves both that process identity and socket are gone before starting
protocol 2. Failure to identify or stop the old daemon, or a socket that remains,
fails closed without starting a competing daemon. Current-format protocol-2
upgrades may use the identity-bearing manifest described below.

Policy evaluation occurs in the daemon, outside the agent process, but its
result only controls whether the agent's proposed shell command continues. The
agent process executes the command under whatever Graith sandbox, native policy,
or external confinement was selected. Native localmost execution bounds output,
the direct child, and pipe-holding descendants so a backend cannot extend the
synchronous check.

Current-format live daemon upgrade also fails closed. The manifest records every live process,
including headless drivers and PTYs whose fd cannot be transferred. An inherited
PTY is adopted only when persisted state and the manifest prove its PID
identity. Sandbox and native-prompt choices are preserved, not adoption gates.
Headless processes cannot transfer a PTY and are identity-checked and terminated
during handoff. Replacement startup
arms identity-verified cleanup immediately after reading the manifest, before
configuration loading, path initialization, state-version, authentication, or
adoption work, so an earlier startup error cannot strand an inherited process.
Pre-transition and failed-adoption processes
are likewise terminated and recorded as stopped; an unknown or unverifiable
inherited session, or a process that cannot be terminated, aborts replacement-
daemon startup after any partially adopted processes are also stopped.

### Testing

- Table-test built-in and native policy allow, deny, ask, malformed input,
  malformed output, timeout, and execution errors.
- Test hook output for Claude and Codex, fail-closed hook transport, and
  startup rejection for agents without a verified synchronous deny contract.
- Test create, resume, fork, headless, scenario, and trigger paths with sandbox
  enabled and explicitly disabled, while configured enforcement still fails closed.
- Test that PTY native permission requests leave Graith status unchanged and
  headless requests are denied immediately because they cannot be serviced.
- Regenerate and test Go/Swift protocol and capability manifests.
- Run focused package tests, daemon and protocol tests with the race detector,
  tagged integration tests, shared Swift tests, app builds, the documentation
  build, and the full Go suite.
