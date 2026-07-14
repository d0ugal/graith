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

The daemon logs to `~/.local/share/graith/daemon.log` in JSON format (slog). Tail it:

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
  OS-level signal forensics. When present, `peak_rss_mb` is labelled with
  `peak_rss_proc` (`agent` or `sandbox-wrapper`) so a small wrapper RSS isn't
  mistaken for the agent's footprint. When Claude reports a clean shutdown via
  its `SessionEnd` hook, a process-ending reason (`logout` / `prompt_input_exit`)
  is attributed as `user` rather than falling back to `crash`; `/clear` and
  `/resume` are logical-session transitions that don't end the process, and any
  other (or unobserved) reason still falls back to `crash`.

Trace one session end to end:

```bash
jq 'select(.id == "<session-id>" or .session_id == "<session-id>")' \
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
