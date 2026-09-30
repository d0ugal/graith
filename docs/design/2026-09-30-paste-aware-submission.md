---
title: "Design Doc: Paste-aware PTY submission"
authors: Graith contributors
created: 2026-09-30
status: Accepted (implementation awaiting merge)
reviewers: (none yet)
informed: Graith maintainers
---

# Paste-aware PTY submission

Use the application's advertised bracketed-paste mode for injected text, followed
by the existing single submit key. This removes the dependency on how quickly a
paste-aware application drains its PTY input.

## Background

Inbox notifications (`internal/daemon/notify.go`) and `gr type` share
`Session.WriteInputAndSubmit`. It serializes text, a configured sleep, and CR
under `writeMu`. Attached sessions first wait for human-input inactivity; detached
sessions skip that wait. Resume notifications have a separate startup delay and
cooldown; ordinary inbox notifications do not coalesce. A successful PTY write,
and the subsequent SIGWINCH poke, do not acknowledge application submission.

PR [#2493](https://github.com/d0ugal/graith/pull/2493) raised the default sleep
from 50 to 150 ms. Configuration reload updates live drivers atomically; an
in-flight submission retains its loaded delay. Explicit old values persist. New/create/resume/fork/orchestrator paths pass the
configured delay; adopted PTYs start with a zero atomic delay and use the built-in
fallback until a later delay-changing reload. That separate adoption inconsistency
is not the cause of failures at the current 150 ms default.

## Problem

Codex's paste heuristic measures time when processing key events, not when Graith
writes them. A stalled consumer can receive both writes together regardless of
the writer's sleep. Enter becomes a newline and extends suppression; several
notifications can accumulate as an unsent multiline draft. Raising the delay
cannot establish a processing boundary.

## Goals

- Preserve explicit text/submit boundaries even when the consumer is delayed.
- Emit exactly one submit key and preserve serialization with other writers.
- Respect terminal capabilities and retain compatibility with raw input.

### Non-Goals

- Guarantee delivery to a composer when a modal or user draft is present.
- Retry submissions or infer acknowledgement from screen contents.
- Add a Codex-only transport in this change.

## Platform support

CLI, iOS and macOS benefit from daemon-generated notifications. No frontend
capability changes are needed. Interactive frontend keystrokes are unchanged.

## Proposals

### Proposal 0: Do Nothing

Retains known failures. Longer sleeps move the failure threshold but cannot
prove that an application has processed the text.

### Proposal 1: Negotiated bracketed paste (Recommended)

Read the terminal's bracketed-paste mode under the screen lock. When enabled,
frame nonempty plain text with ESC[200~ and ESC[201~. Retain the configured
text-to-CR delay and exactly one CR. When unavailable or disabled, preserve raw
input. Input containing control keys (other than LF line breaks) retains raw semantics
rather than risking
an embedded terminator escaping the paste frame. `gr type --no-newline` stays raw.

Codex's explicit paste handler clears its heuristic state. Framing therefore
survives delayed reads and also works with explicit older delays. This is standard
terminal capability negotiation, not screen scraping or an agent-name guess.
Bounded scrollback replay after adoption or parser recovery can lose the startup
mode announcement. In that case the safe raw fallback remains until the agent
re-advertises its modes (for example on restart). Persisting terminal modes
through handoff is a separate lifecycle improvement; assuming them from agent
names would risk wrapping input to an application that has disabled paste.

There remains a mode-transition race inherent in terminal input; no timer or
extra key is added after the ordinary submission.

### Proposal 2: Additional detached Return keys

Rejected: an empty composer is not the only possible input target. Return may
accept a modal, act on a selected history entry or submit newly arrived user
text. Terminal output is neither a draft acknowledgement nor reliable evidence
of submission. Bounded retries and cancellation reduce exposure but cannot make
these actions safe without an application-level contract.

### Proposal 3: Application acknowledgement

Preferred longer-term delivery contract. Local Codex 0.159.0 provides
`codex queue` / `thread/queue/add`, including a client message ID and queue ID
response. It requires the correct shared app-server endpoint and thread identity;
embedded (`--no-daemon`) sessions explicitly cannot use that route. Graith's
existing headless driver is Claude stream-JSON, not a Codex transport. A separate
adapter should persist idempotency IDs, bind the exact server/thread lifecycle,
probe capability, and never fall back to PTY after an ambiguous acknowledgement.
It cannot safely replace all existing PTY sessions as a small bug fix.

## Other Notes

### References

- [Original submission issue #187](https://github.com/d0ugal/graith/issues/187)
- [Repeated delivery issue #309](https://github.com/d0ugal/graith/issues/309)
- [Live delay reload #1363](https://github.com/d0ugal/graith/pull/1363)
- Local Codex source revision `44fe510ce3ee61c8ef623adcbf89b901c73ddd61`:
  `tui/src/bottom_pane/paste_burst.rs`, `chat_composer/reconnect.rs`,
  `chat_composer/paste_input.rs`, and `tui/src/session_queue_commands.rs`.
  The suppression interval remains 120 ms; suppressed Enter extends it.

### Testing

Exercise actual PTY bytes, delayed consumption, consecutive notifications,
capability-disabled and unavailable terminals, literal escape input, empty
submissions, and concurrent writers. Use an isolated Codex configuration and
loopback-only provider for live reproduction; never modify live agent settings.

### Live reproduction

An isolated Codex 0.159.0 PTY with a fresh CODEX_HOME and loopback-only provider
reproduced the failure. SIGSTOP paused only the test TUI while two raw inbox
hints and their CRs were written with 150 ms sleeps. After SIGCONT both hints
appeared as composer lines with blank lines below and no Working transition.
An explicit bracketed paste followed by CR cleared the composer and displayed
Working. A fresh run framing both notifications also cleared the composer and
entered Working after the same stop/write/resume sequence. This proves the delayed-consumer mechanism, not its frequency in live
sessions. No live daemon or user configuration was changed.

A Claude Code 2.1.285 smoke test used a fresh CLAUDE_CONFIG_DIR, bare mode,
a synthetic key, and a refused loopback endpoint. The same stopped-consumer
framed pair cleared its composer and reached the request path. Claude coalesced
the two hints into one submitted message (confirmed in its isolated history);
this transport does not promise one agent turn per notification. A subsequent
framed multiline input was queued while that request was active. No additional
Return was required.
