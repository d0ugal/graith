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

### Profile resolution

A new `ResolveProfile()` function in `internal/config` reads `GRAITH_PROFILE`, validates it, and returns the canonical profile name (empty string for default) and the effective app name. This is the single source of truth — used by `ResolvePaths()`, config loading, and handshake construction.

`ResolvePaths()` calls `ResolveProfile()` internally. Its signature changes to return an error so invalid profiles are impossible to ignore:

```go
func ResolvePaths() (Paths, error) { ... }
```

The `Profile` and effective `AppName` are stored on the `Paths` struct so downstream code (overlay, list, doctor, handshake) can access the canonical value without re-reading the environment.

Hidden hook commands (`report_status`, `approve_request`, `check_inbox`) also call `ResolvePaths()` directly, so the validated profile flows through them automatically.

### Path resolution

`ResolvePaths()` in `internal/config/paths.go` currently uses a hardcoded `appName = "graith"`. The change:

1. Call `ResolveProfile()` to get the effective app name.
2. If profile is non-empty, `appName = "graith-" + profile`.
3. All downstream paths derive from `appName`.

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

Note: the `--config` flag remains an explicit escape hatch. If a user passes `--config /path/to/shared.toml`, that overrides the profile's config path. This is intentional — it's an explicit opt-in to sharing config.

### Profile name validation

Validation happens inside `ResolveProfile()`, before any path computation or config loading.

Rules:
- Must match `^[a-z0-9][a-z0-9-]*$` (lowercase alphanumeric plus hyphens, no leading/trailing hyphens)
- Maximum 32 characters
- Cannot be `default` (reserved — the unnamed profile is the default)

Lowercase-only avoids case-insensitive filesystem collisions on macOS (where `graith-dev` and `graith-Dev` would resolve to the same directory).

Invalid names produce an immediate error:

```
Error: invalid profile name "foo/bar": must be lowercase alphanumeric with hyphens, max 32 characters
```

### Handshake profile check

The client includes its resolved profile name in the handshake message. The daemon compares it against its own profile. On mismatch, the daemon rejects the connection and closes it.

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

**Daemon-side:**
- If the client's profile doesn't match the daemon's profile, respond with `HandshakeErrMsg` and **close the connection**. Currently the daemon doesn't close after `handshake_err` — this must be fixed.
- Empty string and absent field both mean "default" — they are equivalent.

**Client-side:**
- All client connect paths (`Connect`, `ConnectFast`, `ConnectForApproval`) must check that the handshake response type is `handshake_ok`. If it's `handshake_err`, decode the reason into a Go error and return it. Currently the client doesn't check the response type.

**Shared handshake builder:**
All handshake construction must go through a single helper that includes the profile. Currently there are direct `HandshakeMsg{}` constructions in:
- `client.Connect` / `client.Handshake`
- `client.probeDaemonVersion`
- `cli.probeDaemonVersion` (in `daemon.go`)
- `cli.doctor`

All of these must use the shared builder, or they will be rejected by non-default daemons.

**Backwards compatibility:**
- `omitempty` means old clients send no profile field → treated as default → accepted by new default daemons.
- Old daemons ignore the unknown `profile` field → no rejection.
- New profiled client → old daemon: the old daemon ignores `GRAITH_PROFILE` and listens on the default socket. The new client looks for the profiled socket, doesn't find it, auto-starts a new daemon (which inherits `GRAITH_PROFILE`). No conflict.

### Agent environment propagation

The daemon already sets `GRAITH_SESSION_ID` and `GRAITH_SESSION_NAME` in every agent process. When running under a non-default profile, the daemon also sets:

```
GRAITH_PROFILE={profile}
```

This must be done explicitly (not via environment inheritance) in all three places where agent env is built:
- `CreateSession` (~line 408)
- `ForkSession` (~line 603)
- `ResumeSession` (~line 783)

For the default profile, `GRAITH_PROFILE` is not set (preserving current behavior).

**Sandbox considerations:** Because the sandbox env-key allowlist is built from the `env` map after hook injection, adding `GRAITH_PROFILE` to the env map is sufficient — it will be included in `WrapOpts.EnvKeys` automatically.

**Hook commands:** Agent hooks invoke `gr` directly. The hook process inherits the agent's environment, so `GRAITH_PROFILE` propagation to the agent is sufficient for hooks to target the correct daemon.

### Legacy daemon cleanup

`cleanupLegacyDaemon()` scans old pre-v0.11 runtime dirs (`$TMPDIR/graith-{uid}`, `/tmp/graith-{uid}`) and stops any orphaned daemon found there.

For non-default profiles, this cleanup must be skipped. Legacy paths only exist for the default profile — a profiled daemon should never touch them.

### Upgrade/adoption

The exec-upgrade path (`ExecUpgrade`) uses `syscall.Exec` with `os.Environ()`, so `GRAITH_PROFILE` naturally propagates to the new daemon process.

The `UpgradeManifest` should include a `Profile` field. On adoption (`--adopt-from`), the daemon validates that the manifest's profile matches its own. This prevents accidental cross-profile adoption if `--adopt-from` is ever invoked manually with the wrong environment.

### UI indicators

Profile indicators are shown only for non-default profiles. The canonical profile value comes from `Paths.Profile`, not from re-reading the environment.

**Overlay (ctrl+b w):**

The title line changes from `Sessions` to `Sessions [dev]`. The profile label uses a dimmer style than the title to avoid visual noise.

**`gr list`:**

A line is printed before the session table:

```
Profile: dev

NAME    REPO    AGENT   STATUS  ...
```

Omitted entirely for the default profile.

**`gr doctor`:**

Print the active profile near the path diagnostics output.

**No changes to:**
- Passthrough view
- Session names
- Shell prompt
- Log output format
- Notification titles
- MCP server names

The profile is a daemon-level concept, not a session-level one.

### Known limitations

**Git branches are not profile-scoped.** Worktree directories are isolated because they live under the profile's data dir, but branches are created in the source repository's namespace. Two profiles working on the same repo share the branch namespace. Collisions are unlikely (session IDs are random) but possible in theory.

**No automatic config bootstrap.** A fresh profile has no `config.toml`. The user must create one manually or copy from the default profile. This is intentional — the whole point is isolated config for testing unreleased features.

### What is NOT in scope

- **Profile management commands** (`gr profile list`, `gr profile create`, etc.) — profiles are created implicitly when you first run a command with `GRAITH_PROFILE` set. No explicit lifecycle management.
- **Profile-specific binary paths** — agents use whatever `gr` is on their `PATH`. The profile env var routes them to the right daemon regardless of which binary they use.
- **Cross-profile session migration** — sessions belong to exactly one profile. No moving them between profiles.
- **Default profile name override** — the unnamed/default profile is always `graith`, never configurable.

## Testing

- Unit test for `ResolveProfile()`: valid names, invalid characters, uppercase rejection, too long, reserved names, empty/unset.
- Unit test for `ResolvePaths()` with and without `GRAITH_PROFILE`: verify all paths change.
- Unit test for handshake profile mismatch rejection, including connection close.
- Unit test for handshake builder: verify all construction sites include the profile.
- Unit test for `ConnectFast` and `ConnectForApproval` handling of `handshake_err`.
- Integration test: start two daemons (default + profiled), verify separate sockets and state, verify cross-profile connection is rejected.
- Verify `GRAITH_PROFILE` appears in agent process environment for non-default profiles (including sandboxed sessions).
- Verify legacy cleanup is skipped for non-default profiles.
- Verify exec-upgrade preserves profile, and adoption validates profile match.
