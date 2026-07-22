---
weight: 1600
title: "Troubleshooting"
description: "Diagnose and fix common problems."
icon: "healing"
toc: true
draft: false
---

## Diagnostics

`gr doctor` checks installation health:

```bash
gr doctor
```

It checks:

- **Version:** CLI and daemon version match, update availability
- **Environment:** config file, data dir, daemon log size, state file, messages DB, sandbox availability, agent prompt
- **macOS daemon service:** managed/fallback mode, projected variable names, receipt health, and named-profile slot usage
- **Daemon:** connectivity, PID file freshness, uptime, active terminal-screen backend
- **Sessions:** zombie processes (PID not alive but status running), missing worktrees, config drift, scrollback saturation
- **Storage:** scrollback files, orphaned scrollback logs, orphaned worktree directories, tmp dir size, legacy share dir

`--autofix` fixes common issues -- stale sockets, large logs, orphaned files:

```bash
gr doctor --autofix
```

## Daemon management

### Updating after a rebuild

After rebuilding, the daemon still runs the old binary:

```bash
make build
gr daemon restart    # preserves live sessions via exec
```

The client binary needs a fresh build too; if on PATH, rebuild and restart your shell or re-source your profile.

### Force restart

For wedged sessions -- kills all running sessions and starts fresh:

```bash
gr daemon restart --force
```

### Reload config

Apply config changes without a daemon restart or session disruption:

```bash
gr daemon reload
```

### Daemon not responding

If `gr` commands hang or return connection errors:

```bash
# Check if the socket exists (path depends on XDG_RUNTIME_DIR)
ls ${XDG_RUNTIME_DIR:-~/.local/share/graith/run}/graith/graith.sock

# Check if the PID file is stale
gr doctor --autofix

# Manual cleanup if doctor can't connect
rm -f ${XDG_RUNTIME_DIR:-~/.local/share/graith/run}/graith/graith.sock \
      ${XDG_RUNTIME_DIR:-~/.local/share/graith/run}/graith/graith.pid
gr daemon start
```

### Version mismatch

`gr doctor` reports "Version mismatch: CLI=X, daemon=Y" when CLI and daemon differ. Fix:

```bash
gr daemon restart
```

A preserve restart accepts a replacement with the same or a newer state schema,
but rejects a replacement that is too old to read the running state. Manifest
and adoption protocol versions must match exactly because state migration cannot
make live terminal descriptors or process ownership compatible. A compatibility
refusal names the target and running numeric versions and leaves the existing
daemon and sessions untouched. Install a target at least as new as the running
state with matching protocols, or use `gr daemon restart --force` only when you
intend to stop every running session and perform a clean restart.

### Graith background service requires approval or is disabled

This applies only to a signed packaged install on macOS 13 or newer. Inspect the
exact profile job, then enable Graith under **System Settings → General → Login
Items**:

```bash
gr daemon service status
gr doctor
```

Run the original command again after approval. Graith intentionally does not
direct-spawn around the setting. If the status and Login Items disagree, keep
the daemon stopped and run `gr daemon service repair`; unknown live or disabled
jobs are quarantined rather than killed.

### Managed package is missing or has an invalid `Graith.app`

A stable Darwin package fails rather than using an unsigned or mismatched app.
Reinstall or upgrade from the official Homebrew tap/release tarball, preserving
`Graith.app` beside a tarball's `gr`, then run:

```bash
gr daemon service repair
gr daemon service status --all-profiles
```

If you deliberately built from source or used `go install`, `gr doctor` should
instead report the explicit direct-spawn fallback.

### All 64 named-profile service slots are leased

List the mappings and remove a dormant profile you no longer need. This removes
only its service registration; profile data is preserved.

```bash
gr daemon service status --all-profiles
GRAITH_PROFILE=old-profile gr daemon service remove
```

Graith never solves capacity by starting a Terminal-owned named daemon. During
an upgrade, a clean restart reserves the service before stopping a still-working
direct daemon, so capacity or approval failure leaves that daemon running.

### macOS privacy access changed after upgrading

The daemon's macOS identity is Graith, not Terminal. Terminal's Full Disk Access,
Automation, keychain, or other TCC grants do not automatically transfer. Grant
the minimum required access to Graith in System Settings. For shell credentials
or sockets, opt the variable name into
[`[daemon_service].inherit_env`]({{< relref "/docs/configuration/_index.md#macos-daemon-service-environment" >}}).
Graith does not fall back to the terminal process to inherit broader access.

## Session issues

### Session stuck in "running" but agent is dead

`gr doctor` detects this ("PID not alive but status is running"). Fix:

```bash
gr daemon restart    # restarts daemon, which reconciles session states
```

### Worktree missing

Session's worktree deleted outside graith:

```bash
gr delete <session> -f
```

### Config drift

`gr doctor` warns of config drift if you change agent config after creating a session; it keeps the original. To adopt the new:

```bash
gr restart <session>
```

### Scrollback saturation

When scrollback hits the size limit, old output is lost -- `gr doctor` flags it. Routine saturation means the agent's producing too much output.

### Orphaned worktrees

Crashed or improperly deleted sessions leave worktrees that waste disk. `gr doctor --autofix` removes them, skipping any with uncommitted changes.

```bash
gr doctor --autofix
```

Add `--disk` to measure data dir, tmp repos, and orphaned worktree sizes (off by default -- walking the whole tree is slow on large installs):

```bash
gr doctor --disk
```

### Cannot delete starred session

Starred sessions are protected from deletion and skipped by batch operations. Clear it first:

```bash
gr update <session> --starred=false
gr delete <session>
```

`gr stop` still works -- protection only applies to `gr delete` and batch flags like `--stale` and `--stopped`.

## Sandbox issues

Sandbox denials give a bare "permission denied" with no hint about *which* path
or operation was refused. Two commands answer that -- run them from your **normal
shell** (`/usr/bin/log` won't run inside a sandboxed session). See [Diagnostics &
limitations]({{< relref "/docs/sandbox/debugging.md" >}}) for the full guide.

### See what the sandbox actually blocked (`gr sandbox watch`, macOS)

`gr sandbox watch` taps the macOS unified log for real Seatbelt denials -- the
exact path and operation refused. Reproduce the failure while it live-tails, or
ask what was denied:

```bash
# What did the sandbox deny in the last few minutes? (aggregated, most frequent first)
gr sandbox watch --recent

# Live-tail while you reproduce the failure (Ctrl-C to stop)
gr sandbox watch

# Narrow to one session's process tree, or a process name
gr sandbox watch my-session
gr sandbox watch --proc node
```

A typical line -- `42× file-read-data /Users/you/.aws/credentials [node]` --
tells you what to grant (or keep denied). Works for both `safehouse` and `nono`
on macOS.

### Check whether an access would be allowed (`gr sandbox explain`, nono)

Check a policy change's effect *before* launching an agent -- `gr sandbox
explain` queries the backend's policy oracle:

```bash
gr sandbox explain --path ~/.ssh/id_rsa --op read     # denied (deny_credentials)
gr sandbox explain --path ~/Code/shared --op write    # denied on a read-only grant
gr sandbox explain --host github.com --port 443        # network reachability
```

This needs a policy oracle, which today only `nono` has; on `safehouse` it
points you at `gr sandbox watch`.

### "safehouse not found"

The sandbox needs `safehouse` on PATH:

```bash
brew install eugene1g/tools/agent-safehouse
```

### Sandbox path does not exist

`gr doctor` warns when configured sandbox read/write paths don't exist. Create the directory or remove it from config.

### Mirror session fails

`--mirror` requires sandbox enabled; without it, session creation fails closed.

```toml
[sandbox]
enabled = true
```

## Messaging issues

### Messages not arriving

Check the topic name matches exactly on publisher and subscriber:

```bash
gr msg topics    # list all topics with message counts
```

### Stale messages on --wait

If `gr msg sub --topic X --wait` returns immediately with old messages, the subscriber position wasn't advanced. Use `--ack`:

```bash
gr msg sub --topic X --all --ack    # read and acknowledge all
gr msg sub --topic X --wait         # now waits for new messages
```

## Store issues

### "key contains invalid characters"

Store keys must be valid file paths. Rejected: control characters, backslashes, `*`, `?`, `[`, `:`. Spaces are allowed but discouraged.

### "--shared and --repo are mutually exclusive"

Pick one scope: `--shared` hits the global store, `--repo` a specific repo's store. Omit both to auto-detect from the current directory.

## Common operations

### Clean up stale sessions

Remove sessions idle for a week:

```bash
gr delete --repo my-project --stale 7d -f
```

Remove all stopped sessions for a repo:

```bash
gr delete --repo my-project --stopped -f
```

### Check daemon logs

The daemon log is the first place to look when a session stops unexpectedly.
Default `~/.local/share/graith/daemon.log` (JSON/slog); with `data_dir`
`~/.graith` it's `~/.graith/daemon.log`. `gr doctor` prints the active data
directory. Tail the default:

```bash
tail -f ~/.local/share/graith/daemon.log | jq .
```

If it grows large, `gr doctor --autofix` truncates it to ~1 MB.

#### Verify the terminal backend

`gr doctor` reports `Terminal backend: charm` for the pure-Go implementation or
`Terminal backend: libghostty-helper` for the process-isolated native
implementation. The supported machine-readable check is:

```bash
gr doctor --json | jq -r .terminal_backend
```

The moving `graith-dev` channel should report `libghostty-helper` on macOS
arm64 and Linux amd64/arm64; its Intel macOS archive reports `charm`. Current
stable artifacts remain pure Go until their separately reviewed native
promotions. There is no separately named Charm rollback archive, so an
unexpected `charm` result on a native dev target means the wrong platform asset
or executable is running. Check `gr-dev version`, the downloaded archive name,
and the daemon executable path reported by `gr-dev doctor`.

Each daemon generation also emits exactly one `terminal backend selected` JSON
record with the same `terminal_backend` value. That record deliberately has no
session ID, filesystem path, or captured terminal output; it proves build-time
selection even when no PTY session is running.

Backend or helper failures remain visible in the existing session-scoped log
records. `terminal screen unavailable during adoption; preserving PTY with
degraded screen` means the live PTY survived but its derived screen could not be
created. `terminal hydration failed during adoption; preserving PTY with empty
screen` and `terminal recovery hydration failed; using empty screen` mean
scrollback replay failed and Graith kept an empty replacement. Runtime parser,
snapshot, and preview failures use `terminal parser failed; screen reset`,
`terminal snapshot failed; screen reconstructed`, and `terminal preview failed;
screen reconstructed`. Each includes `error`; the snapshot and preview records
also include `recovery_error` when reconstruction fails.

#### Diagnosing why a session stopped

Every lifecycle transition is logged; a stop is fully diagnosable from the log:

- **`session spawned`** / **`resume: pty spawned`** — (re)started; includes
  `pid`, `pgid` (the process group graith signals), and `sandboxed`.
- **`pty first output`** — the agent's first byte; `since_launch_ms` is the
  launch→first-output gap.
- **`session active`** — agent reported running (hook `SessionStart`);
  `since_launch_ms` is the launch→active gap. A large gap with output flowing is
  a slow start; no `session active` at all is a stuck start.
- **`stopping session`** — just before a daemon-initiated SIGTERM, carrying
  `reason` (`user`, `idle`, `shutdown`, `delete`, `watchdog`, …), `initiator`
  (code path: `idle-loop`, `user-stop`, `restart`, `watchdog-restart`,
  `shutdown`, `delete`, …), and `pid`/`pgid`. Orphaned-process reaps (a recorded
  PID with no live PTY) log the same line with an `-orphan` initiator suffix.
- **`session exited`** — process gone. `pid`/`pgid` support OS-level signal
  forensics; `stop_reason` (`crash`, `user`, `idle`, `shutdown`, `watchdog`)
  attributes it (table covers `exit_category`, `signal_source`,
  `peak_rss_mb`/`peak_rss_proc`). Via Claude's `SessionEnd` hook, a clean
  process-ending reason (`logout` / `prompt_input_exit`) is attributed `user` not
  `crash`; `/clear` and `/resume` are logical transitions that don't end the
  process; any other or unobserved reason falls back to `crash`.
- **`session abnormal exit report`** — one high-density crash record.
  `resource_samples` holds up to five 30-second process-group snapshots
  (`rss_mb`, `cpu_percent`, `open_fds`, `process_count`, `top_process`) covering
  the agent and tools below a sandbox wrapper; `fds_partial: true` means a
  short-lived or inaccessible process couldn't be counted. Also records
  `last_output_age_ms`, `observed_lifetime_ms`, `sandbox_backend`,
  `sandbox_diagnostic`, attachment state, unread messages, and session-scoped
  MCP process health.

The most useful exit fields:

| Field | Interpretation |
|---|---|
| `stop_reason` | Lifecycle intent. `crash` means no clean or daemon-controlled stop reason was observed; it doesn't identify the killer. |
| `exit_code` | Ordinary exit status. Non-zero with no `signal` usually means an agent or wrapper error. |
| `signal` | Signal from `wait(2)`, e.g. `terminated` (SIGTERM) or `killed` (SIGKILL). |
| `exit_category` | `signal-after-graith-request`, `signal-external-or-unknown`, `exit-nonzero`, or `exit-clean`. |
| `signal_source` | `graith-requested` only when signal and process generation match a logged daemon request; otherwise `external-or-unknown` (the OS doesn't normally expose the sender via `wait(2)`). |
| `signal_request_initiator` | Daemon code path that requested the signal, e.g. `user-stop`, `idle-loop`, `restart`, `delete`, `shutdown`. |
| `peak_rss_mb` / `peak_rss_proc` | Peak RSS for graith's direct child; `peak_rss_proc` is `agent` or `sandbox-wrapper`. With a sandbox this may be just the wrapper, so use `resource_samples[].rss_mb` for the whole group. |

Common patterns:

- `signal=terminated` + `signal_source=graith-requested` + a preceding
  `stopping session`: graith requested the stop; `reason` and `initiator`
  explain why.
- `signal=terminated`, `signal_source=external-or-unknown`, no preceding
  `stopping session`: the daemon didn't record sending SIGTERM. Check the host's
  process manager, admin actions, and OS logs. Multiple sessions ending near the
  same time suggests a shared external event.
- `signal=killed` plus rapidly rising process-group `rss_mb` suggests resource
  exhaustion but isn't proof of OOM. Confirm via Linux kernel/OOM logs or macOS
  memory-pressure logs.
- `exit_category=exit-nonzero` with no signal usually means the agent or wrapper
  exited on its own. Inspect session scrollback and nearby log entries for the
  error.
- A sandboxed crash whose `sandbox_diagnostic` points at Seatbelt may be a policy
  denial. On macOS, correlate the timestamp with
  `gr sandbox watch --recent 5m <session>`; safehouse has no separate structured
  wrapper exit-reason API.
- Large `last_output_age_ms` with flat resource samples points to a hung or idle
  process; high CPU, rising file descriptors, or a growing process count points
  to runaway work or a leak.

Trace one session end to end:

```bash
jq 'select(.id == "<session-id>" or .session_id == "<session-id>")' \
  ~/.local/share/graith/daemon.log
```

For a custom `data_dir`, replace the path. To compare crashes that happened
together:

```bash
jq 'select(.msg == "session exited" or .msg == "session abnormal exit report")' \
  ~/.local/share/graith/daemon.log
```

Control requests (`stop`, `delete`, `restart`, `scenario_stop`,
`scenario_delete`) log the caller identity at **debug** level (`control request`)
before the authorization check -- raise the log level to see who issued (or
attempted) a stop.

### Reset config to defaults

```bash
gr config reset --force
```
