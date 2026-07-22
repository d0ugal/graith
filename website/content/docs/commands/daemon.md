---
weight: 450
title: "Daemon & other commands"
description: "Daemon lifecycle, config, completion, and internal commands."
icon: "dns"
toc: true
draft: false
---

## Daemon management

### `gr daemon start`

Start the daemon — normally automatic and rarely needed.

### `gr daemon stop`

Stop the daemon gracefully. On a supported packaged macOS installation this
leaves the Graith user service registered but dormant. It does not restart at
login or after a crash; the next ordinary `gr` command starts it on demand.
Stopping a daemon is not the same as removing its background-item registration.

### `gr daemon restart`

Restart the daemon, preserving live sessions via exec.

| Flag | Description |
|------|-------------|
| `--force` | Clean stop/start that kills running sessions |

After rebuilding `gr`, run `gr daemon restart` to pick up the new daemon binary.
Crossing from the approval-era protocol to the non-interactive Graith protocol is
an intentional breaking restart: rather than preserving them, graith gracefully
stops the old daemon and all its PTY and headless sessions once it confirms the
exact old socket peer has exited and its socket has disappeared — if either check
fails, no competing daemon starts. Resume stopped sessions to relaunch under the
new security model.

Before it hands off any live terminal, graith checks that the exact replacement
binary can adopt every session and understands the current helper handoff. It
refuses the attempt while session creation or another lifecycle launch is in
progress. Such a refusal leaves the existing daemon and agents running; retry
after the reported work finishes. If preparation or exec fails, inheritable
terminal descriptors are restored and the existing daemon continues serving.
The replacement may use a newer state schema when it has the forward migrations
needed to load the running daemon's exact snapshot. A replacement with an older
state schema is rejected as a downgrade, while manifest and adoption protocol
versions must still match exactly. Compatibility errors report the bounded
numeric target and running versions for each mismatched boundary.

When upgrading from a release with Graith-owned MCP support, first remove
`[[mcp_servers]]`, `[agents.<name>.mcp_servers.*]`, and
`limits.mcp_log_read_bytes` from `config.toml`; the new daemon rejects these
obsolete lifecycle and security keys instead of silently ignoring them. The old
daemon drains its managed MCP children, sockets, stderr pipes, and reconnect
loops before exec. A live PTY can still be adopted, but any proxy injected into
that already-running agent is permanently unavailable. Restart or resume the
session to relaunch it without Graith-generated MCP arguments or files.

Live adoption requires persisted state and the handoff manifest to prove the
exact process identity; the manifest records every live process, not just
transferable PTYs. Whether that process uses Graith's sandbox or the agent's
native approval TUI is preserved, not an adoption requirement. Sessions from
pre-transition releases — and headless sessions, which have no adoptable PTY —
are identity-checked, terminated, and marked stopped rather than left unmanaged.

The replacement arms cleanup before loading configuration, paths, state, or
authentication, so an early failure still identity-checks and terminates
inherited agents. If preserve is accepted but the replacement isn't ready after
the configured startup wait, graith rechecks the live daemon and falls back to a
clean start only after proving the exact process that answered the pre-upgrade
handshake has exited — a stale PID file isn't enough — then checks that result
for the requested version and a fresh daemon generation. Otherwise it leaves the
possible in-progress replacement alone: retry once startup finishes, or use
`--force` to kill the sessions intentionally.

### `gr daemon reload`

Reload configuration without restarting the daemon. Invalid settings or a
runtime apply failure return an error and leave the previous config generation in
place. Remote transport replacement closes the old listener first and stays
closed if the replacement fails — fix the setting and reload again through the
local socket. See [remote hot reload]({{< relref "/docs/configuration/access.md#hot-reload-and-revocation" >}}).

## macOS user service

On macOS 13 or newer, signed Homebrew and release-tarball installations run the
foreground daemon as an app-associated service for the logged-in user. Activity
Monitor and **System Settings → General → Login Items** can therefore attribute
it to **Graith**, rather than to the terminal that ran the first command. The
headless `Graith.app` has no Dock icon or menu-bar UI. It is never installed as
a root or system daemon.

The service is demand-started: install and login do not launch it, and there is
no unconditional crash restart. Closing Terminal does not stop a healthy
daemon. A crash, logout, reboot, or intentional stop leaves the registered job
dormant until the next eligible command. If Login Items says Graith requires
approval or is disabled, startup fails with guidance and never bypasses that
choice with a Terminal-owned process.

The default profile has its own service label. Named profiles lease distinct
static labels, so their labels, sockets, PIDs, config, state, tokens, and
lifecycle controls cannot collide. Up to 64 named profiles may remain
registered at once (running or dormant); remove a dormant profile service to
free a slot. A supported package at capacity fails instead of falling back to a
directly spawned daemon.

A Homebrew `graith-dev` package that includes the signed `Graith.app` uses the
same service identity but an isolated `dev` profile and service slot, so it can
coexist with stable Graith. A transitional dev package without that bundle
retains the previous direct-spawn behavior rather than installing an unsigned
service app. After upgrading a managed dev package, run `gr-dev daemon restart`;
before uninstalling it, run `gr-dev daemon service remove`. macOS 11/12, source
builds, `go install`, and Linux keep the direct-spawn behavior. `gr doctor`
reports which mode is active. The dev and stable channels do not publish
separately named rollback archives; see the
[native dev canary guidance]({{< relref "/docs/installation.md#native-graith-dev-canary" >}})
for supported platform artifacts and channel changes.

### `gr daemon service status`

Show the active profile's service label, slot, lease state, Service Management
status, launchd state, PID, and registered/running bundle generations.

| Flag | Description |
|------|-------------|
| `--all-profiles` | Include the default, every named-profile lease, and quarantined slots |

Use `--json` for structured output.

### `gr daemon service remove`

Stop the exact profile daemon, unregister its user service, and release a named
profile's slot only after launchd confirms the job is gone. Config, state,
worktrees, tokens, messages, and logs are preserved.

| Flag | Description |
|------|-------------|
| `--all-profiles` | Remove the default and every registered named-profile service |

Run `gr daemon service remove --all-profiles` before uninstalling a macOS
package. An ordinary upgrade must not remove the services; Graith keeps signed,
versioned app generations and rotates each dormant registration safely.

### `gr daemon service repair`

Validate the owner-only service receipt and its backup, inspect only Graith's 65
exact compiled labels and signed cached apps, and repair state that is proven
dormant. A valid backup restores the primary. Unknown live, disabled, or
indeterminate jobs are quarantined rather than killed or reassigned. Reinstall a
signed package first if `gr` or `Graith.app` was deleted before service removal;
then run this command instead of using wildcard `launchctl` cleanup.

The managed service starts with a deliberately small environment projection.
See [`[daemon_service]`]({{< relref "/docs/configuration/_index.md#macos-daemon-service-environment" >}})
before upgrading if agents depend on `SSH_AUTH_SOCK`, cloud credentials, or API
keys inherited from your terminal.

## Other commands

### `gr config show`

Print the effective (merged) configuration.

### `gr config diff`

Show changes from built-in defaults.

### `gr config reset`

Write built-in defaults to the config file.

| Flag | Description |
|------|-------------|
| `--force` | Overwrite without confirmation |

### `gr completion <shell>`

Generate a shell completion script. Supported shells: `bash`, `zsh`, `fish`, `powershell`.

### `gr version`

Print version information.

## Hidden/internal commands

Used by graith internally, not intended for direct use:

| Command | Purpose |
|---------|---------|
| `gr report-status` | Report agent status (used by hooks) |
| `gr check-inbox` | Check unread inbox messages (used by hooks) |
| `gr command-policy-check` | Perform a bounded synchronous shell-policy check |
