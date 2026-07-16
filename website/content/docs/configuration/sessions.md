---
weight: 320
title: "Session behavior"
description: "Headless sessions, delete retention, the launch throttle, and git pull."
icon: "tune"
toc: true
draft: false
---

## Headless sessions

**Experimental.** A headless session runs the agent in Claude Code's stream-json mode instead of an interactive PTY — for fire-and-forget work such as review judges and one-shot helpers. graith parses the typed event stream, so `gr logs -f` renders it and the run's cost/token usage is captured from the result envelope. v1 is Claude-only, one-shot, requires a prompt, and is **incompatible with the sandbox** (headless requires `sandbox.enabled = false`, or a per-agent sandbox that resolves to disabled).

```toml
[headless]
experimental = false        # master gate: headless is inert unless this is on
default = false             # once enabled, whether new sessions go headless by default
max_line_bytes = 16777216   # cap on a single stream-json line (16 MiB)
control_timeout = "30s"     # how long a control request waits for its response
interrupt_timeout = "5s"    # interrupt round-trip before falling through to SIGINT
preview_bytes = 16384       # scrollback tail rendered by the overlay / screen_preview (16 KiB)
```

`experimental` is the master switch. While it is off, headless is inert and sessions use the PTY driver. It is gated because the underlying control protocol is undocumented and may change between Claude Code releases. Trigger-spawned headless sessions are not implemented yet; see [Automation → Headless session actions]({{< relref "automation.md#headless-session-actions-planned" >}}).

v1 is **Claude-only** and **one-shot** (one prompt, run to completion, exit). Only agents marked `headless_capable = true` can run headless — see [Agent definitions]({{< relref "agents.md#agent-definitions" >}}). Requesting headless for an agent that can't do it is an error, not a silent fallback to PTY.

`gr attach` on a headless session offers to convert it to an interactive PTY,
preserving the conversation, worktree, branch, and environment. Pass `--yes` to
skip the confirmation. Use `gr logs -f` to inspect a headless session read-only
without converting it (see [Session Lifecycle → Headless sessions]({{< relref "/docs/sessions.md#headless-sessions" >}})).

The remaining keys are processing limits that rarely need tuning. `max_line_bytes` caps a single stream-json line (large tool outputs and base64 images exceed the 64 KiB scanner default). `control_timeout` bounds how long a synchronous control request waits for its response; `interrupt_timeout` is the much shorter interrupt round-trip before graith falls back to SIGINT. `preview_bytes` bounds the scrollback tail rendered by the overlay preview and the `screen_preview` control message. A zero or non-positive value falls back to the default shown.

A headless session drives Claude Code over its stdin control protocol, giving graith a clean interrupt and inline tool-approval handling. It has no human to answer prompts, so its approval policy must be **non-blocking**: `yolo` auto-allows, a non-blocking `[approvals]` backend decides, and anything that would queue for a human is denied (escalated once to the orchestrator inbox). See [Session Lifecycle → Headless sessions]({{< relref "/docs/sessions.md#headless-sessions" >}}).

## Delete retention

```toml
[delete]
retention = "24h"            # how long soft-deleted sessions are kept before purge
purge_startup_delay = "30s"  # delay before the first purge sweep after the daemon starts
purge_interval = "10m"       # how often the purge sweep runs thereafter
```

`gr delete` is a soft delete: it stops the agent and hides the session but keeps its worktree, branch, and state for this window, so `gr restore` can bring it back. A background loop hard-deletes sessions once their retention expires. Setting `retention = "0"` disables soft delete, so `gr delete` refuses and directs the user to `gr purge`; it never silently becomes destructive. `gr purge` is the only immediate, destructive verb.

`purge_startup_delay` and `purge_interval` tune only the sweep cadence, not whether a session is recoverable: a session's recovery deadline is frozen when it is deleted (`retention` at delete time), so no timing value can turn soft delete into an immediate hard delete. The cadence is deliberately coarse because the retention window is measured in hours. Both must be positive durations; a `gr reload` retunes the running purge timer on its next tick. `gr doctor` reports the effective cadence and the last/next sweep times.

## Orphan garbage collection

```toml
[gc]
orphan_min_age = "5m"  # minimum age before an orphaned worktree/scratch dir is GC-eligible
```

`gr gc` reclaims worktree and scratch directories left behind by sessions that are no longer in state. `orphan_min_age` is the minimum age a directory must reach before GC will remove it, so a directory belonging to an in-flight `gr new` (created before the session is committed to state) is never destroyed. Setting `orphan_min_age = "0"` opts out of the age floor and makes directories eligible immediately — only safe when no sessions are being created concurrently.

## Git pull

```toml
[git_pull]
enabled  = false  # periodically fast-forward maintenance repos' default branches
interval = "1h"   # how often to pull (minimum: 1 minute)
```

When enabled, the daemon fetches and fast-forward merges the default branch of each repo registered with `git maintenance`. The first pull runs shortly after the daemon starts, then on the configured `interval` — so a daemon restart doesn't leave repos stale for a full interval before the next pull.

Sessions run in their own worktrees on feature branches, which share only the object store with the source checkout, so fast-forwarding the default branch cannot disturb them — those sessions do **not** block the pull. A repo is only skipped when a session works directly on the source checkout (in-place) or has the default branch itself checked out in its worktree. This keeps default branches up to date for future session creation without ever pulling into an active worktree.

## Launch throttle & startup watchdog

Launching several sessions at once can overwhelm the machine: heavyweight agent runtimes (Claude Code loads a ~400MB Node process) all initialise simultaneously and the tail of the burst can stall for minutes — or hang indefinitely at ~9MB RSS (only the sandbox wrapper loaded), producing no output and never connecting. The `[launch]` block bounds this and recovers stuck launches automatically.

```toml
[launch]
max_concurrent     = 3        # max agent spawns in their startup window at once (< 1 => default 3)
startup_timeout    = "3m"     # kill + restart a session stuck with no output past this ("0" disables the watchdog)
settle_timeout     = "10s"    # how long a launch holds its throttle slot waiting for first output ("0" => release right after spawn)
max_restarts       = 3        # consecutive watchdog restarts before a stuck session is errored (< 1 => default)
watchdog_interval  = "15s"    # how often the watchdog scans for stuck sessions (next daemon start)
slot_poll_interval = "100ms"  # how often a held launch slot polls a fresh session for first output
```

**Throttle.** A launch acquires one of `max_concurrent` slots just before spawning the agent and holds it across the risky startup window, releasing it as soon as the session produces its first output or `settle_timeout` elapses (whichever comes first). This bounds how many agents are *initialising* at once — the actual source of the stampede — so a burst starts cleanly in sequence rather than all at once. `gr new` still returns promptly; the slot is released in the background. `slot_poll_interval` is how often a held slot re-checks a fresh session for its first output.

**Watchdog.** A background loop, ticking every `watchdog_interval`, looks for sessions that are running but have never produced output, sit at `agent_status: unknown`, and have been up longer than `startup_timeout`. Each is killed and restarted fresh (the restart uses a fresh `--session-id` rather than resuming a conversation that was never persisted). `max_restarts` caps consecutive watchdog restarts for a permanently-broken session before it is marked errored; the counter resets once the session produces output. To disable the watchdog entirely set `startup_timeout = "0"` (not `max_restarts = 0`, which falls back to the default).

`max_concurrent`, `startup_timeout`, `settle_timeout`, `max_restarts`, and `slot_poll_interval` are re-read on config reload (`SIGHUP` / edit-and-save). `watchdog_interval` sets the scan loop's ticker at startup, so changing it requires a `gr daemon restart`.

## Session lifecycle & PTY policy

The `[lifecycle]` block gathers the lower-level signal-escalation waits, teardown grace, adopted-PTY babysit timing, scrollback hydration, terminal-input pacing, default launch geometry, and the per-session log cap that were previously fixed constants across the daemon and PTY layers. The **signal-escalation order** (interrupt → `SIGTERM` → `SIGKILL`) is a code invariant; only the waits between steps are tunable.

```toml
[lifecycle]
convert_settle_timeout     = "5s"      # interrupt->settle wait before a converting headless session escalates to SIGTERM
convert_kill_timeout       = "3s"      # SIGTERM step before the final SIGKILL during convert
convert_force_kill_timeout = "3s"      # final wait after SIGKILL so a wedged process can't stall the convert
mass_exit_window           = "2s"      # window over which many near-simultaneous exits are flagged as an external signal
mass_exit_threshold        = 5         # exits within mass_exit_window that trigger the OOM/jetsam warning (< 1 => default)
process_kill_grace         = "5s"      # wait after SIGTERM before SIGKILL when killing a session's process group
adopted_timeout            = "24h"     # safety deadline for the adopted-PTY babysit loop when identity can't be verified
adopted_poll_interval      = "1s"      # how often the adopted-PTY babysit loop polls for process exit
scrollback_hydration_bytes = 131072    # bytes of scrollback tail replayed into an adopted session's screen (< 0 => default; "0" disables)
input_delay                = "50ms"    # pause between typed text and the submit carriage return (non-positive => default)
default_cols               = 80        # default terminal columns for daemon launch paths with no client geometry (< 1 => default)
default_rows               = 24        # default terminal rows for daemon launch paths with no client geometry (< 1 => default)
max_log_bytes              = 104857600 # per-session scrollback log cap in bytes (< 0 => default; "0" => unlimited)
```

**Escalation waits.** `convert_settle_timeout`, `convert_kill_timeout`, and `convert_force_kill_timeout` bound the three steps of stopping a headless process when converting it to interactive (`gr attach` on a headless session). `process_kill_grace` is how long a session's process group is given after `SIGTERM` before `SIGKILL`. (The headless control-channel interrupt round-trip is tuned separately by `[headless]` `interrupt_timeout`.)

**Mass-exit detection.** When `mass_exit_threshold` sessions exit within `mass_exit_window`, the daemon logs a warning — this usually means an external signal (the OOM killer or macOS jetsam) is killing processes, not a graith bug.

**Adoption & PTY.** After a daemon upgrade the daemon re-attaches to surviving agents; `adopted_timeout` and `adopted_poll_interval` tune the loop that watches an adopted process for exit, and `scrollback_hydration_bytes` is how much of the recent scrollback is replayed into the reconstructed screen. `input_delay` is the pause `gr type` inserts between the text and the Enter key so a TUI doesn't treat them as a paste.

**Geometry & log cap.** `default_cols`/`default_rows` are the terminal geometry used by daemon launch paths that have no attaching client (the watchdog restart, the orchestrator, scenarios, triggers, and adoption); an attaching client immediately resizes to its real geometry. `max_log_bytes` caps the per-session scrollback log file (`"0"` means unlimited).

Empty or non-positive duration values fall back to the defaults shown (a zero wait would either escalate instantly or busy-loop a poll); an out-of-range or unparseable value is rejected at config load. **Geometry, hydration, and the log cap apply only to sessions launched (or adopted) after the change** — a running session keeps the geometry and caps it started with. The escalation waits and `input_delay` are read at each use, so a config reload takes effect on the next operation.

## Detection & status classification

The daemon decides each session's `agent_status` (`active`, `ready`, `approval`, `unknown`) from two sources: authoritative reports from agent hooks, and, when no fresh hook report exists, a periodic scrape of the PTY scrollback. The `[detection]` block exposes the timing policy behind both so you can tune how often the daemon looks and how long each signal is trusted.

```toml
[detection]
scan_interval        = "500ms"  # how often PTY scrollback is scanned for agent status
fetch_interval       = "5m"     # how often `git fetch` refreshes remote tracking refs
fetch_timeout        = "30s"    # per-repo `git fetch` timeout (a slow remote can't stall the pass)
silent_threshold     = "20s"    # zero PTY output past this warns the session is silent
adopted_grace        = "60s"    # after a daemon upgrade, keep the prior status this long ("0" disables)
recent_output_window = "3s"     # recent PTY output alone implies "active" when scraping is inconclusive ("0" disables)
hook_start_window    = "5s"     # a SessionStart hook stays authoritative this long
hook_activity_window = "30s"    # tool-use hooks (prompt / pre / post) stay authoritative this long
hook_terminal_window = "30m"    # ready / approval hooks (Stop, idle, permission) stay authoritative this long
```

**Scanning.** `scan_interval` is the scrollback poll cadence — the shortest window in which a status change can be noticed from PTY text. `fetch_interval` governs the much slower background `git fetch` that keeps `origin/<base>` fresh so the diverged-from-base count doesn't go stale after remote merges, and `fetch_timeout` bounds each repo's fetch so one slow remote can't stall the pass for other sessions.

**Hook authority.** When an agent hook reports a status, that report is trusted over PTY scraping until its window elapses. `hook_start_window`, `hook_activity_window`, and `hook_terminal_window` are the windows for start, tool-use, and terminal (ready/approval) events respectively — longer for terminal states, which are stable, and short for the noisy activity events.

**Scraping fallbacks.** `silent_threshold` is how long a running session may produce zero PTY output before the daemon logs a "silent session" warning. `recent_output_window` treats a session that produced output very recently as `active` even when the scraped text is inconclusive; `adopted_grace` keeps a session's previous status for a short window after a daemon upgrade re-attaches to a surviving agent, so a freshly adopted PTY isn't misread as `unknown`. Setting either window to `"0"` disables that fallback.

Empty or non-positive values fall back to the defaults shown above. These values are read live on each detection pass (and each hook report), so a config reload (`SIGHUP` / edit-and-save) takes effect without restarting the daemon; `scan_interval` and `fetch_interval` set the loop tickers at startup, so changing those two requires a `gr daemon restart`.

## Token accounting

The daemon periodically re-derives each session's token usage from its agent's on-disk transcript, surfaced by `gr tokens`. A fingerprint cache means an idle fleet does almost no work between ticks. The `[token_accounting]` block tunes the loop's cadence and how much work it does per tick.

```toml
[token_accounting]
poll_interval = "30s"  # how often per-session token usage is re-derived from transcripts
startup_delay = "5s"   # first-tick delay after a daemon (re)start so `gr tokens` isn't blank ("0" polls immediately)
batch_size    = 8      # max sessions (re)parsed per tick (bounds work on a large fleet with big transcripts)
```

`batch_size` bounds how many sessions with *changed* transcripts are re-parsed in a single tick, so a large fleet with big transcripts can't stall the loop; unchanged transcripts are skipped by the fingerprint cache and don't count against it. `batch_size` is read live each tick, so a reload takes effect immediately; `poll_interval` and `startup_delay` are read once when the loop starts, so changing them requires a `gr daemon restart`. An empty or non-positive `poll_interval` falls back to the default (a zero cadence would busy-loop); `startup_delay` honours an explicit `"0"` to poll immediately; `batch_size < 1` uses the default.

## Resource monitor

The daemon periodically snapshots each live session's process-group memory, CPU, and open file descriptors so sustained growth is visible in the diagnostic report logged when a session exits abnormally. Sampling is deliberately coarse and its failures never affect session operation. The `[resource_monitor]` block tunes the cadence and how many samples are retained.

```toml
[resource_monitor]
sample_interval = "30s"  # how often each session's process group is snapshotted
sample_history  = 5      # how many recent samples are retained per session
```

`sample_interval` is also the per-session spacing that keeps a launch-burst kick from replacing an established session's history. `sample_history` is the number of recent samples kept per session (the window shown in an abnormal-exit report). Both are read live on each sampling pass; `sample_interval` also sets the loop ticker at startup, so changing it requires a `gr daemon restart` to alter the loop cadence. An empty or non-positive `sample_interval` falls back to the default; `sample_history < 1` uses the default.

## Output & display limits

graith truncates output in several user-visible places so a huge log, a runaway tool input, or a long final message never floods a view or an inbox nudge. These caps were formerly scattered as unrelated constants across the daemon and CLI; the `[limits]` block gathers them so one change updates every surface. Each value is a plain count and the unit is in the key name (lines, bytes, runes); a value less than 1 falls back to the default shown.

```toml
[limits]
log_lines              = 300      # default trailing lines for `gr logs`, `gr mcp logs`, attach replay, and MCP log reads (when no -n)
wait_scan_lines        = 500      # scrollback lines `gr wait --contains` scans for an already-present match
wait_buffer_bytes      = 65536    # partial-line buffer cap in the live `gr wait` matcher (64 KiB)
mcp_log_read_bytes     = 1048576  # max trailing bytes read from an MCP log file before splitting into lines (1 MiB)
approval_display_bytes = 500      # tool input shown in the approval overlay (backends still evaluate the full input)
last_message_runes     = 2000     # agent's final Stop message the status hook forwards (counted in runes)
inbox_preview_bytes    = 1000     # unread-inbox preview injected into a session's SessionStart hook context
```

`log_lines` is the shared default used whenever a `--lines`/`-n` count is omitted: `gr logs` and `gr mcp logs` now send `0` by default, so the daemon applies this value. Because the daemon owns the resolution, the graphical clients' log peeks pick up the same server-side default too. `wait_scan_lines` and `wait_buffer_bytes` bound how much history `gr wait --contains` scans and how much unterminated output the live matcher retains. `approval_display_bytes`, `last_message_runes`, and `inbox_preview_bytes` cap what is *shown* — the untruncated values are still evaluated (approval backends) or recoverable (`gr msg inbox --all` shows the full message body). The byte caps are counted in bytes but never split a multi-byte character mid-rune, and `last_message_runes` is counted in whole runes.

## Update check

`gr list` and `gr doctor` check GitHub for a newer graith release and cache the result. The `[updates]` block makes this configurable — turn it off for downstream forks, packaged or offline installs, or if you simply don't want the network call.

```toml
[updates]
enabled    = true            # check GitHub for newer graith releases
repository = "d0ugal/graith"  # GitHub "owner/repo" whose latest release is checked
interval   = "1h"            # how long a cached result stays fresh
timeout    = "5s"            # HTTP request timeout for the release check
```

Setting `enabled = false` disables all update-check network activity: `gr list` skips it silently and `gr doctor` reports the check as disabled rather than claiming you are up to date. The check never runs for a `dev` build regardless of this setting. Point `repository` at a fork to track its releases instead. An empty `interval` or `timeout` falls back to the built-in defaults (1h and 5s); a value that is set but unparseable, or a `repository` not in `owner/repo` form, is rejected at config load. The cached result is scoped to the repository that produced it, so changing `repository` takes effect on the next check rather than serving the previous fork's release for the rest of the interval.

## External tool executables

graith shells out to a handful of external binaries. By default each is resolved by its conventional name on `PATH` (or, for `ps` and `lsof`, its conventional absolute path). The `[tools]` block lets you override any of them — useful for Nix or custom-`PATH` installs, wrapper binaries, or an alternate shell.

```toml
[tools]
git       = "git"             # git executable
gh        = "gh"              # GitHub CLI executable
shell     = "sh"              # shell for notification/trigger commands (run as `<shell> -c ...`)
osascript = "osascript"       # macOS desktop-notification helper
ps        = "/bin/ps"         # process-listing binary
lsof      = "/usr/sbin/lsof"  # open-files listing binary (macOS FD sampling)
```

A value may be a **bare command name** resolved on `PATH` (`"git"`, `"hub"`) or an **absolute/relative path** to a specific binary (`"/run/current-system/sw/bin/git"`). Only fields you set are validated at config load: an explicit path must exist and be executable, and a bare name must be found on `PATH`. Fields you leave unset keep the defaults above and are resolved lazily when first used, so the macOS-only `osascript` default is never an error on Linux.

Only the executable is configurable. The subcommands graith runs (`git rev-parse`, `gh api …`) and sandbox backend flags stay fixed in code — this is not a general command-substitution hook.

The same resolved tools are used by the daemon, the document store, and CLI paths such as `gr store` repo discovery, so an override applies everywhere graith runs that binary.

## Git operation timeouts

The `[git]` block bounds the individual git operations graith runs during session lifecycle. Slower repositories, large fetches, or high-latency remotes can legitimately exceed the defaults. This is distinct from `[git_pull]`, which controls the background maintenance-pull loop.

```toml
[git]
fetch_timeout    = "2m"   # bounds a single `git fetch` on session create/fork and the pull loop
merge_timeout    = "2m"   # bounds a fast-forward merge in the git-pull loop
username_timeout = "15s"  # bounds GitHub-username discovery (may invoke `gh`)
```

An unset field keeps its built-in default (2m / 2m / 15s). A value that is set but unparseable, or that is zero or negative, is rejected at config load.

## Connection deadlines

The `[connection]` block tunes the deadlines and retry cadence the stateless `gr` client applies when talking to a daemon. Slow machines, high-latency links, or a remote daemon on a constrained network can legitimately exceed the defaults. These values are read once per `gr` invocation, so a change takes effect on the next command.

```toml
[connection]
dial_timeout             = "500ms"  # a single Unix-socket dial to the local daemon
handshake_timeout        = "5s"     # the local-daemon handshake exchange
start_timeout            = "5s"     # wait for a freshly spawned daemon to answer
start_poll_interval      = "50ms"   # re-probe cadence while the daemon starts
reconnect_timeout        = "10s"    # attach disconnect-recovery before giving up
reconnect_interval       = "250ms"  # re-probe cadence while reattaching
remote_dial_timeout      = "10s"    # TCP dial to a paired remote daemon
remote_handshake_timeout = "15s"    # remote handshake + proof-of-possession
remote_pairing_timeout   = "11m"    # wait for the remote human to approve `gr pair`
```

An unset field keeps its built-in default (shown above). A value that is set but unparseable, or that is zero or negative, is rejected at config load. The `remote_*` fields apply only to remote-daemon connections (see [Orchestrator & remote access]({{< relref "/docs/configuration/access.md" >}})); the others apply to the local daemon and attach recovery.

## Migration

`gr migrate` hands a session's conversation to a different agent in place. After starting the target agent, the daemon waits `health_window` to confirm it survived startup before declaring the migration successful; if the new agent exits immediately (a bad auth/config), the migration reverts to the original agent. Raise the window to tolerate a slow agent boot, lower it to revert faster.

```toml
[migration]
health_window = "1.5s"  # startup-health confirmation window for the migrated-to agent
```

An empty, unparseable, or non-positive value falls back to the default (1.5s); a set-but-unparseable or non-positive value is rejected at config load.

## Transcript reading

Migration and cross-agent fork build a neutral Markdown context document from the source agent's on-disk transcript and hand it to the target agent. The `[transcript]` block bounds both the rendered document size and the scanner buffers used to read the transcript files. These rarely need tuning; raise them for unusually large transcripts or individual lines.

```toml
[transcript]
max_context_bytes = 262144         # size budget for the rendered context; older turns elided to fit (256 KiB)
max_tool_output_bytes = 4096       # cap on each rendered tool-output block (4 KiB)
max_line_bytes = 16777216          # scanner buffer cap for a transcript line when reading turns/usage (16 MiB)
max_metadata_line_bytes = 4194304  # scanner buffer cap for Codex rollout metadata scans (cwd/id) (4 MiB)
```

A zero or non-positive value falls back to the default shown; a negative value is rejected at config load. The scanner buffer caps (`max_line_bytes`, `max_metadata_line_bytes`) are applied process-wide and re-read on config reload, so a change takes effect without a daemon restart.
