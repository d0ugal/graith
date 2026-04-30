# GRAITH_PROFILE — Isolated Dev/Test Environments

**Date:** 2026-06-10
**Status:** Draft

## Problem

When developing graith, changes to the daemon, protocol, or client need testing against a running instance. Currently there is exactly one daemon per user — running a dev build risks corrupting state or disrupting active sessions.

## Solution

A `GRAITH_PROFILE` environment variable that namespaces all graith paths. When set, the daemon and client operate in a fully isolated environment (config, state, socket, logs, worktrees, messages DB) with no overlap with the default instance.

## Design

### Environment variable

| Value | Effective app name | Behavior |
|-------|-------------------|----------|
| unset / empty | `graith` | Default — current behavior, no change |
| `dev` | `graith-dev` | Fully isolated instance |
| `experiment` | `graith-experiment` | Fully isolated instance |

### Path resolution

`ResolvePaths()` in `internal/config/paths.go` currently uses a hardcoded `appName = "graith"`. The change:

1. Read `GRAITH_PROFILE` from the environment.
2. If non-empty, validate it (see below) and set `appName = "graith-" + profile`.
3. All downstream paths derive from `appName` — no other code changes needed for isolation.

Example paths for `GRAITH_PROFILE=dev`:

```
~/.config/graith-dev/config.toml
~/.local/share/graith-dev/state.json
~/.local/share/graith-dev/daemon.log
~/.local/share/graith-dev/logs/
~/.local/share/graith-dev/messages.sqlite
~/.local/share/graith-dev/worktrees/...
$XDG_RUNTIME_DIR/graith-dev/graith.sock
$XDG_RUNTIME_DIR/graith-dev/graith.pid
```

### Profile name validation

Since the profile name becomes a directory path component, it must be validated at CLI startup before any daemon interaction.

Rules:
- Must match `^[a-zA-Z0-9-]+$` (alphanumeric plus hyphens)
- Maximum 32 characters
- Cannot be `default` (reserved — the unnamed profile is the default)

Invalid names produce an immediate error:

```
Error: invalid profile name "foo/bar": must be alphanumeric with hyphens, max 32 characters
```

### Handshake profile check

The client includes its resolved profile name in the handshake message. The daemon compares it against its own profile. On mismatch, the daemon rejects the connection.

**Wire format change** — add `Profile` field to `HandshakeMsg`:

```go
type HandshakeMsg struct {
    Version      string    `json:"version"`
    ClientID     string    `json:"client_id"`
    TerminalSize [2]uint16 `json:"terminal_size"`
    Cwd          string    `json:"cwd"`
    Profile      string    `json:"profile,omitempty"`
}
```

Daemon-side check in the handshake handler:
- If the client's profile doesn't match the daemon's profile, respond with `HandshakeErrMsg{Reason: "profile mismatch: client has \"dev\" but daemon is default"}`.
- Empty string and absent field both mean "default" — they are equivalent.

This is defense-in-depth. Path isolation already prevents cross-profile connections under normal circumstances, but this catches edge cases (e.g., manually specifying a socket path).

### Agent environment propagation

The daemon already sets `GRAITH_SESSION_ID` and `GRAITH_SESSION_NAME` in every agent process. When running under a non-default profile, the daemon also sets:

```
GRAITH_PROFILE={profile}
```

This ensures that any `gr` commands an agent runs inside a session automatically target the correct daemon. For the default profile, `GRAITH_PROFILE` is not set (preserving current behavior).

### UI indicators

Profile indicators are shown only for non-default profiles.

**Overlay (ctrl+b w):**

The title line changes from `Sessions` to `Sessions [dev]`. The profile label uses a dimmer style than the title to avoid visual noise.

**`gr list`:**

A line is printed before the session table:

```
Profile: dev

NAME    REPO    AGENT   STATUS  ...
```

Omitted entirely for the default profile.

**No changes to:**
- Passthrough view
- Session names
- Shell prompt
- Log output format

The profile is a daemon-level concept, not a session-level one.

### What is NOT in scope

- **Profile management commands** (`gr profile list`, `gr profile create`, etc.) — profiles are created implicitly when you first run a command with `GRAITH_PROFILE` set. No explicit lifecycle management.
- **Profile-specific binary paths** — agents use whatever `gr` is on their `PATH`. The profile env var routes them to the right daemon regardless of which binary they use.
- **Cross-profile session migration** — sessions belong to exactly one profile. No moving them between profiles.
- **Default profile name override** — the unnamed/default profile is always `graith`, never configurable.

## Testing

- Unit test for `ResolvePaths()` with and without `GRAITH_PROFILE` set.
- Unit test for profile name validation (valid names, invalid characters, too long, reserved names).
- Unit test for handshake profile mismatch rejection.
- Integration test: start two daemons (default + profiled), verify they use separate sockets and state, verify cross-profile connection is rejected.
- Verify `GRAITH_PROFILE` appears in agent process environment for non-default profiles.
