---
weight: 320
title: "Session behavior"
description: "Headless sessions, delete retention, the launch throttle, and git pull."
icon: "tune"
toc: true
draft: false
---

## Headless sessions

**Experimental.** A headless session runs the agent in Claude Code's stream-json mode instead of an interactive PTY — for fire-and-forget work like review judges and one-shot helpers. graith parses the typed event stream, so `gr logs -f` renders it and cost/token usage comes from the result envelope. Sandbox settings apply as with a PTY session.

```toml
[headless]
experimental = false        # master gate: headless is inert unless this is on
default = false             # once enabled, whether new sessions go headless by default
max_line_bytes = 16777216   # cap on a single stream-json line (16 MiB)
control_timeout = "30s"     # how long a control request waits for its response
interrupt_timeout = "5s"    # interrupt round-trip before falling through to SIGINT
preview_bytes = 16384       # scrollback tail rendered by the overlay / screen_preview (16 KiB)
```

Headless is gated because the control protocol is undocumented and may change between Claude Code releases; while `experimental` is off, sessions use the PTY driver. Trigger-spawned headless sessions aren't implemented yet; see [Automation → Headless session actions]({{< relref "automation.md#headless-session-actions-planned" >}}).

v1 is **Claude-only**, **one-shot** (one prompt, run to completion, exit), and needs a prompt. Only agents marked `headless_capable = true` can run headless — see [Agent definitions]({{< relref "agents.md#agent-definitions" >}}); requesting it for an agent that can't is an error, not a silent PTY fallback.

`gr attach` on a headless session offers to convert it to an interactive PTY,
preserving the conversation, worktree, branch, and environment; pass `--yes` to
skip confirmation. Use `gr logs -f` to inspect one read-only without converting
(see [Session Lifecycle → Headless sessions]({{< relref "/docs/sessions.md#headless-sessions" >}})).

The remaining keys rarely need tuning; `max_line_bytes` covers large tool outputs and base64 images past the 64 KiB scanner default. A zero or non-positive value falls back to the default shown.

Headless drives Claude Code over the stdin control protocol, giving graith a clean interrupt. Native permission requests are denied immediately and mark the driver degraded — there's no human-response path. Optional `[command_policy]` rules synchronously restrict shell commands before agent execution, whether or not Graith's sandbox is enabled.

## Delete retention

```toml
[delete]
retention = "24h"            # how long soft-deleted sessions are kept before purge
purge_startup_delay = "30s"  # delay before the first purge sweep after the daemon starts
purge_interval = "10m"       # how often the purge sweep runs thereafter
```

`gr delete` is a soft delete: it stops the agent and hides the session but keeps its worktree, branch, and state, so `gr restore` can recover it within the window. A background loop hard-deletes sessions once retention expires. `retention = "0"` disables soft delete: `gr delete` refuses and points to `gr purge` rather than silently turning destructive. `gr purge` is the only immediate, destructive verb.

`purge_startup_delay` and `purge_interval` tune only the sweep cadence, not recoverability: a session's recovery deadline is frozen at delete time (`retention` then), so no timing value can turn soft delete into an immediate hard delete. Both must be positive durations; `gr reload` retunes the running timer on its next tick. `gr doctor` shows the effective delay and interval in a **Purge** section with last and next sweep times.

## Orphan garbage collection

```toml
[gc]
orphan_min_age = "5m"  # minimum age before an orphaned worktree/scratch dir is GC-eligible
```

`gr gc` reclaims worktree and scratch directories left behind by sessions no longer in state. The age floor protects a directory belonging to an in-flight `gr new` (created before the session is committed to state). `orphan_min_age = "0"` makes directories eligible immediately — only safe when no sessions are being created concurrently.

## Git pull

```toml
[git_pull]
enabled  = false  # periodically fast-forward maintenance repos' default branches
interval = "1h"   # how often to pull (minimum: 1 minute)
```

When enabled, the daemon fetches and fast-forward merges the default branch of each repo registered with `git maintenance`. The first pull runs shortly after the daemon starts, then on `interval` — so a restart doesn't leave repos stale for a full interval.

Sessions run in their own worktrees on feature branches, sharing only the object store with the source checkout, so fast-forwarding the default branch can't disturb them — they do **not** block the pull. A repo is skipped only when a session works directly on the source checkout (in-place) or has the default branch checked out in its worktree.

## Launch throttle & startup watchdog

Launching many sessions at once can overwhelm the machine: heavyweight runtimes (Claude Code loads a ~400MB Node process) all initialise together and the tail can stall for minutes or hang at ~9MB RSS (only the sandbox wrapper loaded), producing no output. The `[launch]` block bounds this and auto-recovers stuck launches.

```toml
[launch]
max_concurrent     = 3        # max agent spawns in their startup window at once (< 1 => default 3)
startup_timeout    = "3m"     # kill + restart a session stuck with no output past this ("0" disables the watchdog)
settle_timeout     = "10s"    # how long a launch holds its throttle slot waiting for first output ("0" => release right after spawn)
max_restarts       = 3        # consecutive watchdog restarts before a stuck session is errored (< 1 => default)
watchdog_interval  = "15s"    # how often the watchdog scans for stuck sessions (next daemon start)
slot_poll_interval = "100ms"  # how often a held launch slot polls a fresh session for first output
```

**Throttle.** A launch grabs one of `max_concurrent` slots just before spawning and holds it across the startup window, releasing on first output or `settle_timeout` (whichever comes first). This bounds how many agents initialise at once, so a burst starts cleanly in sequence. `gr new` still returns promptly; the slot is released in the background. `slot_poll_interval` sets how often a held slot re-checks a fresh session for first output.

**Watchdog.** A loop ticking every `watchdog_interval` finds sessions that are running but have never produced output, sit at `agent_status: unknown`, and have been up longer than `startup_timeout`. Each is killed and restarted fresh (using a new `--session-id` rather than resuming a never-persisted conversation). `max_restarts` caps consecutive restarts of a permanently-broken session before it's errored; the counter resets on output. Disable the watchdog with `startup_timeout = "0"` (not `max_restarts = 0`, which falls back to the default).

`max_concurrent`, `startup_timeout`, `settle_timeout`, `max_restarts`, and `slot_poll_interval` are re-read on config reload (`SIGHUP` / edit-and-save). `watchdog_interval` sets the scan loop's ticker at startup, so changing it needs a `gr daemon restart`.

## Session lifecycle & PTY policy

The `[lifecycle]` block gathers lower-level PTY and teardown timing constants. The **signal-escalation order** (interrupt → `SIGTERM` → `SIGKILL`) is a code invariant; only the waits between steps are tunable.

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

**Escalation waits.** The three `convert_*` timeouts bound stopping a headless process during conversion to interactive (`gr attach` on a headless session); the control-channel interrupt round-trip is tuned separately by `[headless]` `interrupt_timeout`. `mass_exit_threshold` exits within `mass_exit_window` log a warning — usually an external signal (OOM killer or macOS jetsam), not a graith bug.

**Adoption & PTY.** After a daemon upgrade the daemon re-attaches to surviving agents; `adopted_timeout`/`adopted_poll_interval` tune the watch loop and `scrollback_hydration_bytes` how much is replayed into the reconstructed screen. `input_delay` is the pause `gr type` inserts so a TUI doesn't treat text and Enter as a paste. `default_cols`/`default_rows` serve daemon launch paths with no attaching client (watchdog restart, orchestrator, scenarios, triggers, adoption); an attaching client immediately resizes to its real geometry.

Empty or non-positive durations fall back to the defaults shown; an out-of-range or unparseable value is rejected at config load. **Geometry, hydration, and the log cap apply only to sessions launched or adopted after the change** — a running session keeps what it started with. Escalation waits and `input_delay` are read at each use, so a reload takes effect on the next operation.

## Detection & status classification

The daemon derives each session's `agent_status` (`active`, `ready`, `error`, `unknown`) from two sources: authoritative agent-hook reports, and — absent a fresh hook report — a periodic scrape of the PTY scrollback. The `[detection]` block tunes how often it looks and how long each signal is trusted.

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
hook_terminal_window = "30m"    # ready/error hooks (Stop, idle, unexpected permission) stay authoritative this long
```

`scan_interval` is the shortest window in which a PTY-text status change is noticed. `fetch_interval`'s background `git fetch` keeps `origin/<base>` fresh so the diverged-from-base count doesn't go stale after remote merges. A hook-reported status is trusted over PTY scraping until its window elapses (`hook_start_window`, `hook_activity_window`, `hook_terminal_window` cover start, tool-use, and terminal events). `adopted_grace` keeps the previous status briefly after a daemon upgrade re-attaches to a surviving agent, so a freshly adopted PTY isn't misread as `unknown`.

Empty or non-positive values fall back to the defaults shown. Values are read live on each detection pass and hook report, so a reload (`SIGHUP` / edit-and-save) takes effect without a restart; `scan_interval` and `fetch_interval` set the loop tickers at startup, so changing those two needs a `gr daemon restart`.

## Token accounting

The daemon periodically re-derives each session's token usage from its agent's on-disk transcript, surfaced by `gr ls --wide`, `gr ls --tokens`, and `gr ls --json`. A fingerprint cache means an idle fleet does almost no work between ticks.

```toml
[token_accounting]
poll_interval = "30s"  # how often per-session token usage is re-derived from transcripts
startup_delay = "5s"   # first-tick delay after restart so token columns aren't blank ("0" polls immediately)
batch_size    = 8      # max sessions (re)parsed per tick (bounds work on a large fleet with big transcripts)
```

`batch_size` counts only sessions with *changed* transcripts; unchanged ones are skipped by the fingerprint cache and don't count against it. `batch_size` is read live each tick (reload takes effect immediately); `poll_interval` and `startup_delay` are read once at loop start, so changing them needs a `gr daemon restart`. An empty or non-positive `poll_interval` falls back to the default; `startup_delay` honours an explicit `"0"` to poll immediately; `batch_size < 1` uses the default.

## Resource monitor

The daemon periodically snapshots each live session's process-group memory, CPU, and open file descriptors, so sustained growth shows in the diagnostic report logged on abnormal exit. Sampling failures never affect session operation. `[resource_monitor]` tunes the cadence and retained sample count.

```toml
[resource_monitor]
sample_interval = "30s"  # how often each session's process group is snapshotted
sample_history  = 5      # how many recent samples are retained per session
```

`sample_interval` also sets the per-session spacing that keeps a launch-burst kick from replacing an established session's history; `sample_history` samples are the window shown in an abnormal-exit report. Both are read live on each pass; `sample_interval` also sets the loop ticker at startup, so changing the cadence needs a `gr daemon restart`. An empty or non-positive `sample_interval` falls back to the default; `sample_history < 1` uses the default.

## Output & display limits

graith truncates output in several user-visible places so a huge log or long final message never floods a view or inbox nudge. The `[limits]` block gathers these formerly-scattered caps so one change updates every surface. Each value is a plain count with the unit in the key name (lines, bytes, runes); a value less than 1 falls back to the default shown.

```toml
[limits]
log_lines              = 300      # default trailing lines for `gr logs` and attach replay (when no -n)
wait_scan_lines        = 500      # scrollback lines `gr wait --contains` scans for an already-present match
wait_buffer_bytes      = 65536    # partial-line buffer cap in the live `gr wait` matcher (64 KiB)
last_message_runes     = 2000     # agent's final Stop message the status hook forwards (counted in runes)
inbox_preview_bytes    = 1000     # unread-inbox preview injected into a session's SessionStart hook context
```

`log_lines` is the shared default when a `--lines`/`-n` count is omitted: `gr logs` and attach scroll mode send `0`, so the daemon applies its current value per request — reconnecting attach sessions and graphical clients' log peeks pick up the same server-side default after a reload. `last_message_runes` and `inbox_preview_bytes` cap what's shown; the full message stays available via `gr msg inbox --all`. Byte caps never split a multi-byte character mid-rune, and `last_message_runes` is counted in whole runes.

## Update check

`gr list` and `gr doctor` check GitHub for a newer graith release and cache the result. The `[updates]` block makes this configurable — turn it off for downstream forks, packaged or offline installs, or to avoid the network call.

```toml
[updates]
enabled    = true            # check GitHub for newer graith releases
repository = "d0ugal/graith"  # GitHub "owner/repo" whose latest release is checked
interval   = "1h"            # how long a cached result stays fresh
timeout    = "5s"            # HTTP request timeout for the release check
```

`enabled = false` disables all update-check network activity: `gr list` skips it silently and `gr doctor` reports the check as disabled rather than claiming you're up to date. The check never runs for a `dev` build regardless. Point `repository` at a fork to track its releases; the cached result is scoped to its source repository, so changing `repository` refreshes on the next check rather than reusing the old release. An empty `interval` or `timeout` falls back to the built-in defaults (1h and 5s); a set-but-unparseable value, or a `repository` not in `owner/repo` form, is rejected at config load.

## External tool executables

graith shells out to a handful of external binaries, each resolved by its conventional name on `PATH` (or, for `ps` and `lsof`, its conventional absolute path). The `[tools]` block overrides any of them — for Nix or custom-`PATH` installs, wrapper binaries, or an alternate shell.

```toml
[tools]
git       = "git"             # git executable
gh        = "gh"              # GitHub CLI executable
gcx       = "gcx"             # Grafana Cloud CLI executable (gcx trigger source)
shell     = "sh"              # shell for notification/trigger commands (run as `<shell> -c ...`)
ps        = "/bin/ps"         # process-listing binary
lsof      = "/usr/sbin/lsof"  # open-files listing binary (macOS FD sampling)
```

A value may be a **bare command name** resolved on `PATH` (`"git"`, `"hub"`) or an **absolute/relative path** to a specific binary (`"/run/current-system/sw/bin/git"`). Only fields you set are validated at load: an explicit path must exist and be executable, a bare name must be found on `PATH`. Unset fields keep the defaults above and resolve lazily on first use.

A **relative** path (e.g. `"./bin/git-wrapper"`) resolves against the directory containing your `config.toml`, not graith's working directory (which is set to a session's repository or worktree). It's normalized to absolute once at load, and the same normalized path validates and runs the override, so a wrapper that validates always executes from the same location.

Only the executable is configurable. The subcommands graith runs (`git rev-parse`, `gh api …`, `gcx irm …`) and sandbox backend flags stay fixed in code — this isn't a general command-substitution hook.

The same resolved tools are used by the daemon, the document store, and CLI paths such as `gr store` repo discovery, so an override applies everywhere graith runs that binary.

## Git operation timeouts

The `[git]` block bounds the individual git operations graith runs during session lifecycle. It's distinct from `[git_pull]`, which controls the background maintenance-pull loop.

```toml
[git]
fetch_timeout    = "2m"   # bounds a single `git fetch` on session create/fork and the pull loop
merge_timeout    = "2m"   # bounds a fast-forward merge in the git-pull loop
username_timeout = "15s"  # bounds GitHub-username discovery (may invoke `gh`)
```

An unset field keeps its built-in default (2m / 2m / 15s). A set-but-unparseable, zero, or negative value is rejected at config load.

## Connection deadlines

The `[connection]` block tunes the deadlines and retry cadence the stateless `gr` client uses when talking to a daemon. Values are read once per `gr` invocation, so a change takes effect on the next command.

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
remote_pairing_timeout   = "11m"    # wait for `gr remote pairings approve` on the host
```

An unset field keeps its built-in default (shown above). A set-but-unparseable, zero, or negative value is rejected at config load. The `remote_*` fields apply only to remote-daemon connections (see [Orchestrator & remote access]({{< relref "/docs/configuration/access.md" >}})); the others apply to the local daemon and attach recovery.

`start_timeout` and `start_poll_interval` set the aggregate budget and re-probe cadence for every daemon readiness and lifecycle wait — auto-starting a daemon, waiting for a `gr daemon restart` exec upgrade, and waiting for a stopped daemon's socket to disappear. Each probe uses the smaller of its own `dial_timeout`/`handshake_timeout` and the remaining budget, so a socket that accepts then stalls can't overrun `start_timeout`. Upgrade readiness is generation-aware: the daemon returns a per-process boot nonce, and a restart/upgrade is ready only once a *new* generation answers with the wanted version — an inherited old listener or a same-version rebuild that keeps the pre-upgrade nonce falls back to a clean restart instead of a false success.

`reconnect_timeout` bounds the recovery window: no new reattach starts at or after it, and the `reconnect_interval` wait before each attempt is capped to the remaining budget (so `reconnect_interval` larger than `reconnect_timeout` can't overshoot before the first probe). An attempt that starts before the deadline still completes under its own `remote_dial_timeout`/`handshake_timeout` bounds; those aren't clipped to the remaining budget.

## Migration

`gr migrate` hands a session's conversation to a different agent in place. After starting the target, the daemon waits `health_window` to confirm it survived startup before declaring success; if the new agent exits immediately (bad auth/config), the migration reverts to the original. Raise the window for a slow boot, lower it to revert faster.

```toml
[migration]
health_window = "1.5s"  # startup-health confirmation window for the migrated-to agent
```

An empty, unparseable, or non-positive value falls back to the default (1.5s); a set-but-unparseable or non-positive value is rejected at config load.

## Transcript reading

Migration and cross-agent fork build a neutral Markdown context document from the source agent's on-disk transcript for the target agent. The `[transcript]` block bounds the rendered document size and the scanner buffers used to read transcript files; raise them for unusually large transcripts or lines.

```toml
[transcript]
max_context_bytes = 262144         # size budget for the rendered context; older turns elided to fit (256 KiB)
max_tool_output_bytes = 4096       # cap on each rendered tool-output block (4 KiB)
max_line_bytes = 16777216          # scanner buffer cap for a transcript line when reading turns/usage (16 MiB)
max_metadata_line_bytes = 4194304  # scanner buffer cap for Codex rollout metadata scans (cwd/id) (4 MiB)
```

A zero or non-positive value falls back to the default shown; a negative value is rejected at config load. The scanner buffer caps (`max_line_bytes`, `max_metadata_line_bytes`) apply process-wide and are re-read on config reload, so a change takes effect without a daemon restart.
