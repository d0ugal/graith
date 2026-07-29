---
title: "Design Doc: Terminal Query and OSC Ownership"
authors: Codex
created: 2026-07-29
status: Implemented (v1)
reviewers: (none yet)
informed: graith-maintainers, native-owners
issue: https://github.com/d0ugal/graith/issues/1826
---

# Terminal Query and OSC Ownership

graith now treats the daemon-side terminal model, backed by the native helper
when available, as the only authority that may answer terminal queries from a
child PTY. Attached clients render filtered visible output and must not let the
host terminal answer the same query or apply unsafe OSC/image side effects.

## Background

PTY sessions already fan child output to three places: persistent scrollback,
the daemon's terminal model for previews/snapshots/reconstruction, and attached
clients. Terminal-owned attach added a second presentation path
where the client repaints daemon snapshots rather than streaming raw child
control traffic directly to the host terminal.

Before this slice, the terminal model drained native query responses so writes
would not deadlock, while ordinary raw attach could still forward the query to a
host terminal. Detached sessions therefore did not get the same child-facing
answers as attached sessions, and terminal-owned attach risked evolving toward a
split-brain model where both the daemon and an attached client might answer.

## Problem

Terminal queries and OSC side effects are not just display bytes. DA, DSR,
DECRQSS, XTVERSION, and size reports send bytes back to the child. OSC title,
clipboard, hyperlinks, notifications, and image protocols affect state outside
the rendered text grid. If more than one component owns those effects, children
can see duplicate or state-dependent replies; if no component owns them while
detached, previews, automation, and reconstruction diverge from attached
behavior.

## Goals

- Make query replies deterministic whether a session is detached, attached,
  read-only attached, or reconnecting.
- Keep the daemon's terminal model authoritative for preview, snapshot, and
  reconstruction state.
- Prevent attached host terminals from answering daemon-owned queries.
- Fail closed for unsafe OSC and image side effects.
- Keep this slice independent from mouse routing, history, dirty rendering, and
  chrome layout.

### Non-Goals

- Implement mouse, focus, bracketed-paste, or keyboard mode routing.
- Add user-facing clipboard, notification, hyperlink, or image rendering
  support.
- Preserve host-terminal side effects from raw attach when they conflict with
  daemon ownership.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | `gr attach` is the first attached view and now filters daemon-owned terminal traffic. |
| iOS | Planned | Remote/mobile views should follow the same daemon-owned query contract when they render PTY output. |
| macOS | Planned | Native app views should render snapshots/output without answering child terminal queries themselves. |

## Proposals

### Proposal 0: Do Nothing

Leaving query replies to whichever terminal happens to be attached keeps raw
attach behavior familiar, but detached sessions still lack consistent answers
and terminal-owned attach remains vulnerable to duplicate replies as it grows more
terminal-aware.

### Proposal 1: Daemon-Owned Replies with Attach Filtering (Recommended)

The daemon terminal model captures native write-pty effects and queues live
reply bytes back to the PTY master. Reply writes are bounded and best-effort so
child-input backpressure cannot block the PTY read loop. Replay and
reconstruction writes discard any generated replies so old scrollback queries
are not answered again after daemon restart or helper recovery.

Attached clients receive an output stream filtered by the daemon. The filter is
UTF-8 aware, drops daemon-owned query sequences, all OSC strings, all
DCS/APC/PM/SOS strings, and ENQ. Visible text around those controls is
preserved, so hyperlink labels remain readable even though host-terminal
hyperlink state is stripped.

OSC policy in v1:

- Window title changes may update the daemon model, but are not forwarded to
  attached host terminals.
- Clipboard writes are denied by the native helper and stripped from attach
  output; clipboard reads/malformed writes produce no host interaction.
- OSC 8 hyperlinks are not forwarded as host hyperlinks; their visible label
  text remains.
- Desktop notifications and progress side effects are ignored.
- Kitty/APC, sixel/DCS, and related image protocols are disabled or stripped.

Trade-off: raw attach no longer passes these side effects through to the user's
terminal. That is deliberate: correctness and safety require one owner, and the
daemon is the only owner present while detached.

### Proposal 2: Client-Owned Queries While Attached

The daemon could answer only when detached, leaving attached host terminals to
answer while a client is present. This keeps historical raw attach closer to a
plain terminal, but reconnect timing and read-only attach state would change the
child-visible terminal identity. It also makes terminal-owned attach depend on
client-specific terminal emulation, which is the split authority this issue is
intended to remove.

## Other Notes

### References

- Issue: https://github.com/d0ugal/graith/issues/1826
- Parent epic: https://github.com/d0ugal/graith/issues/1824
- Experimental attach slice: https://github.com/d0ugal/graith/pull/1817
- PTY terminal contract: `internal/pty/terminal.go`
- Attach output filtering: `internal/pty/terminal_authority.go`
- Daemon attach streaming: `internal/daemon/handler.go`

### Testing

The v1 proof covers representative DA/DSR/XTVERSION/DECRQM-style filtering,
DECID and kitty/sixel query filtering, UTF-8 text preservation, OSC
title/clipboard/hyperlink/notification stripping, image-protocol stripping,
daemon attach tail/live-output filtering, bounded helper write-reply payloads,
live terminal reply writes back to the PTY master, and replay-time reply
discard.
