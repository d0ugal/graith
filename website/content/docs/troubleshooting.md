---
weight: 1600
title: "Troubleshooting"
description: "Diagnose and fix common problems."
icon: "healing"
toc: true
draft: false
---

## Diagnostics

Run `gr doctor` to check the health of your graith installation:

```bash
gr doctor
```

It checks:

- **Version:** CLI and daemon version match, update availability
- **Environment:** config file, data dir, daemon log size, state file, messages DB, sandbox availability, agent prompt
- **Daemon:** connectivity, PID file freshness, uptime
- **Sessions:** zombie processes (PID not alive but status running), missing worktrees, config drift, scrollback saturation
- **Storage:** scrollback files, orphaned scrollback logs, orphaned worktree directories, tmp dir size, legacy share dir

Use `--autofix` to automatically fix common issues (remove stale sockets, truncate large logs, clean orphaned files):

```bash
gr doctor --autofix
```

## Daemon management

### Updating after a rebuild

After rebuilding graith, the daemon is still running the old binary. Pick up the new one:

```bash
make build
gr daemon restart    # preserves live sessions via exec
```

The client binary in your shell also needs a fresh build. If you installed to PATH, rebuild and restart your shell or re-source your profile.

### Force restart

If sessions are wedged, a force restart kills all running sessions and starts fresh:

```bash
gr daemon restart --force
```

### Reload config

Apply config changes without restarting the daemon or disrupting sessions:

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

`gr doctor` reports "Version mismatch: CLI=X, daemon=Y" when the CLI binary and daemon are different versions. Fix with:

```bash
gr daemon restart
```

## Session issues

### Session stuck in "running" but agent is dead

`gr doctor` detects this ("PID not alive but status is running"). Fix:

```bash
gr daemon restart    # restarts daemon, which reconciles session states
```

### Worktree missing

If a session's worktree was deleted outside graith:

```bash
gr delete <session> -f
```

### Config drift

If you changed agent config after creating a session, `gr doctor` warns about config drift. The session continues with its original config. To pick up new config:

```bash
gr restart <session>
```

### Scrollback saturation

When a session's scrollback file hits the size limit, old output is lost. Check with `gr doctor`. If scrollback is routinely saturating, the agent is producing excessive output.

### Orphaned worktrees

Worktrees left behind from crashed or improperly deleted sessions waste disk space. `gr doctor --autofix` removes them (skipping any with uncommitted changes).

```bash
gr doctor --autofix
```

To see how much disk space the data dir, tmp repos, and orphaned worktrees are using, add `--disk`. This is off by default because measuring sizes means walking the whole tree, which is slow on large installs:

```bash
gr doctor --disk
```

### Cannot delete starred session

Starred sessions are protected from deletion and skipped by batch operations. Unstar first:

```bash
gr unstar <session>
gr delete <session>
```

Direct `gr stop` still works on starred sessions -- the protection applies to `gr delete` and batch flags like `--stale` and `--stopped`.

## Sandbox issues

Sandbox denials are one of the most confusing failures to debug — an agent (or a
command it runs) fails with a bare "permission denied" and no hint about *which*
path or operation the kernel refused. Two commands turn that guesswork into a
concrete answer. Run them from your **normal shell** — `/usr/bin/log` refuses to
run inside a sandboxed session. See [Diagnostics &
limitations]({{< relref "/docs/sandbox/debugging.md" >}}) for the full guide.

### See what the sandbox actually blocked (`gr sandbox watch`, macOS)

`gr sandbox watch` taps the macOS unified log and shows the real Seatbelt
denials — the exact path and operation that were refused. Reproduce the failure
while it live-tails, or ask for what was just denied:

```bash
# What did the sandbox deny in the last few minutes? (aggregated, most frequent first)
gr sandbox watch --recent

# Live-tail while you reproduce the failure (Ctrl-C to stop)
gr sandbox watch

# Narrow to one session's process tree, or a process name
gr sandbox watch my-session
gr sandbox watch --proc node
```

A typical line — `42× file-read-data /Users/you/.aws/credentials [node]` — tells
you exactly what to grant (or deliberately keep denied). This works for both the
`safehouse` and `nono` backends on macOS.

### Check whether an access would be allowed (`gr sandbox explain`, nono)

When you're about to change the policy and want to confirm the effect *before*
launching an agent, `gr sandbox explain` asks the backend's policy oracle:

```bash
gr sandbox explain --path ~/.ssh/id_rsa --op read     # denied (deny_credentials)
gr sandbox explain --path ~/Code/shared --op write    # denied on a read-only grant
gr sandbox explain --host github.com --port 443        # network reachability
```

This needs a policy oracle, which today only the `nono` backend has; on a
`safehouse` config it points you at `gr sandbox watch` instead.

### "safehouse not found"

Sandbox requires `safehouse` on PATH. Install it:

```bash
brew install eugene1g/tools/agent-safehouse
```

### Sandbox path does not exist

`gr doctor` warns when configured sandbox read/write paths do not exist. Either create the directory or remove it from your config.

### Mirror session fails

`--mirror` requires sandbox to be enabled. Without it, session creation fails closed.

```toml
[sandbox]
enabled = true
```

## Messaging issues

### Messages not arriving

Check that the topic name matches exactly between publisher and subscriber:

```bash
gr msg topics    # list all topics with message counts
```

### Stale messages on --wait

If `gr msg sub --topic X --wait` returns immediately with old messages, the subscriber position was not advanced. Use `--ack` to mark messages as read:

```bash
gr msg sub --topic X --all --ack    # read and acknowledge all
gr msg sub --topic X --wait         # now waits for new messages
```

## Store issues

### "key contains invalid characters"

Store keys must be valid file paths. Rejected characters: control characters, backslashes, `*`, `?`, `[`, `:`. Spaces are technically allowed but discouraged.

### "--shared and --repo are mutually exclusive"

Pick one scope. `--shared` accesses the global store. `--repo` accesses a specific repo's store. Omit both to auto-detect from the current directory.

## Common operations

### Clean up stale sessions

Remove sessions that have been idle for a week:

```bash
gr delete --repo my-project --stale 7d -f
```

Remove all stopped sessions for a repo:

```bash
gr delete --repo my-project --stopped -f
```

### Check daemon logs

The daemon log is the first place to look when a session stops unexpectedly. By
default it is `~/.local/share/graith/daemon.log` (JSON/slog); if `data_dir` is
set to `~/.graith`, it is `~/.graith/daemon.log`. `gr doctor` prints the active
data directory. Tail the default log with:

```bash
tail -f ~/.local/share/graith/daemon.log | jq .
```

If the log file grows large, `gr doctor --autofix` truncates it to ~1 MB.

#### Diagnosing why a session stopped

Every session lifecycle transition is logged so a stop is fully diagnosable from
the log alone:

- **`session spawned`** / **`resume: pty spawned`** — a session (re)started.
  Includes `pid`, `pgid` (the process group graith signals), and `sandboxed`.
- **`pty first output`** — the agent produced its first byte;
  `since_launch_ms` is the launch→first-output gap.
- **`session active`** — the agent reported it is running (hook `SessionStart`);
  `since_launch_ms` is the launch→active gap. A large gap here with output
  flowing is a slow start; no `session active` at all is a stuck start.
- **`stopping session`** — emitted the instant before a daemon-initiated
  SIGTERM, carrying `reason` (`user`, `idle`, `shutdown`, `delete`, `watchdog`,
  …), `initiator` (the code path: `idle-loop`, `user-stop`, `restart`,
  `watchdog-restart`, `shutdown`, `delete`, …), and `pid`/`pgid`. Orphaned-
  process reaps (a recorded PID with no live PTY, e.g. after a daemon restart)
  log the same line with an `-orphan` initiator suffix.
- **`session exited`** — the process is gone. `stop_reason` attributes the exit
  (`crash`, `user`, `idle`, `shutdown`, `watchdog`), and `pid`/`pgid` support
  OS-level signal forensics. `exit_category` separates a non-zero exit from a
  signal, and `signal_source` says whether graith had a matching, PID-bound
  signal request or whether the sender is external or unknown. When present,
  `peak_rss_mb` is labelled with
  `peak_rss_proc` (`agent` or `sandbox-wrapper`) so a small wrapper RSS isn't
  mistaken for the agent's footprint. When Claude reports a clean shutdown via
  its `SessionEnd` hook, a process-ending reason (`logout` / `prompt_input_exit`)
  is attributed as `user` rather than falling back to `crash`; `/clear` and
  `/resume` are logical-session transitions that don't end the process, and any
  other (or unobserved) reason still falls back to `crash`.
- **`session abnormal exit report`** — a single high-density record emitted for
  a crash. `resource_samples` contains up to five 30-second process-group
  snapshots (`rss_mb`, `cpu_percent`, `open_fds`, `process_count`, and
  `top_process`), so it includes the agent and tools below a sandbox wrapper.
  `fds_partial: true` means at least one short-lived or inaccessible process
  could not be counted. The report also records `last_output_age_ms`,
  `observed_lifetime_ms`, `sandbox_backend`, `sandbox_diagnostic`, attachment
  state, pending approvals, unread messages, and the health of session-scoped
  MCP processes.

The most useful exit fields are:

| Field | Interpretation |
|---|---|
| `stop_reason` | Lifecycle intent. `crash` means no clean or daemon-controlled stop reason was observed; it does not by itself identify the killer. |
| `exit_code` | The ordinary process exit status. A non-zero value with no `signal` usually indicates an agent or wrapper error. |
| `signal` | Signal reported by `wait(2)`, such as `terminated` (SIGTERM) or `killed` (SIGKILL). |
| `exit_category` | `signal-after-graith-request`, `signal-external-or-unknown`, `exit-nonzero`, or `exit-clean`. |
| `signal_source` | `graith-requested` only when the signal and process generation match a logged daemon request; otherwise `external-or-unknown`. The OS does not normally expose the sender through `wait(2)`. |
| `signal_request_initiator` | Daemon code path that requested the matching signal, for example `user-stop`, `idle-loop`, `restart`, `delete`, or `shutdown`. |
| `peak_rss_mb` / `peak_rss_proc` | Peak RSS for graith's direct child. With a sandbox this can be only the wrapper; use `resource_samples[].rss_mb` for the whole process group. |

Common patterns:

- `signal=terminated`, `signal_source=graith-requested`, and a preceding
  `stopping session` record means graith requested the stop. The `reason` and
  `initiator` fields explain why.
- `signal=terminated` with `signal_source=external-or-unknown` and no preceding
  `stopping session` means the daemon did not record sending SIGTERM. Check the
  host's process manager, administrator actions, and OS logs. Multiple sessions
  ending at nearly the same time strongly suggests a shared external event.
- `signal=killed` plus rapidly rising process-group `rss_mb` is consistent with
  resource exhaustion, but is not proof of OOM. Check Linux kernel/OOM logs or
  macOS memory-pressure logs to confirm it.
- `exit_category=exit-nonzero` with no signal usually means the agent or sandbox
  wrapper exited itself. Inspect the session scrollback and nearby daemon log
  entries for its error.
- A sandboxed crash whose `sandbox_diagnostic` points at Seatbelt may be a policy
  denial. On macOS, correlate the timestamp with
  `gr sandbox watch --recent 5m <session>`; safehouse has no separate structured
  wrapper exit-reason API.
- A large `last_output_age_ms` with flat resource samples points toward a hung
  or idle process; high CPU, increasing file descriptors, or a growing process
  count points toward runaway work or a leak.

Trace one session end to end:

```bash
jq 'select(.id == "<session-id>" or .session_id == "<session-id>")' \
  ~/.local/share/graith/daemon.log
```

For a custom `data_dir`, replace the path above (for example with
`~/.graith/daemon.log`). To compare crashes that happened together:

```bash
jq 'select(.msg == "session exited" or .msg == "session abnormal exit report")' \
  ~/.local/share/graith/daemon.log
```

Control requests (`stop`, `delete`, `restart`, `scenario_stop`,
`scenario_delete`) log the authenticated caller identity at **debug** level
(`control request`), before the authorization check, so raise the log level if
you need to see who issued (or attempted) a stop.

### Reset config to defaults

```bash
gr config reset --force
```
