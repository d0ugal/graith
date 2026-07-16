---
weight: 360
title: "Orchestrator & remote access"
description: "The orchestrator session and the tailnet remote control listener."
icon: "hub"
toc: true
draft: false
---

## Orchestrator

```toml
[orchestrator]
enabled      = false    # enable the orchestrator session
agent        = ""       # agent to run as orchestrator; empty inherits default_agent
model        = ""       # optional model override
idle_timeout = "30m"    # auto-stop if idle
prompt       = "..."    # orchestrator-specific system prompt
prompt_file  = ""       # or read from file
```

See [Orchestrator]({{< relref "/docs/orchestrator.md" >}}) for details.

### Restart policy

When the orchestrator exits unexpectedly (a crash or startup-watchdog kill — never a user, idle, or shutdown stop) the daemon auto-restarts it with a backoff that grows after each failure and resets once a run stays up long enough. Tune it with `[orchestrator.restart]`:

```toml
[orchestrator.restart]
schedule              = ["2s", "4s", "8s", "16s", "32s", "60s", "300s"]  # per-attempt delays; the last value repeats. Set to [] to use the geometric knobs below.
initial_backoff       = "2s"   # geometric mode: first restart delay (used only when schedule = [])
max_backoff           = "300s" # geometric mode: cap on the restart delay
multiplier            = 2.0    # geometric mode: each delay = previous × this
stable_reset          = "60s"  # a run lasting at least this long resets the backoff to its first step
fresh_start_threshold = 3      # after this many consecutive restarts, relaunch the agent fresh (new session id)
```

There are two backoff modes. By default an explicit **`schedule`** lists the delay for each attempt, with the final entry repeating for every attempt beyond its length — this preserves graith's historical backoff curve. Setting `schedule = []` switches to **geometric** backoff computed as `initial_backoff × multiplier` each attempt, capped at `max_backoff`. The defaults are chosen so an unconfigured daemon behaves exactly as before.

## Remote access

An optional control listener that lets you reach the daemon from another device over your [Tailscale](https://tailscale.com) tailnet. **Disabled by default and fail-closed**: when enabled, an invalid `[remote]` block is a hard config-load error rather than a silent downgrade.

```toml
[remote]
enabled             = false          # expose the remote control listener over the tailnet
mode                = "tsnet"         # transport: "tsnet" (embedded Tailscale) or "interface" (bind an existing tailnet IP)
port                = 4823            # TCP port the listener binds
require_pairing     = true           # require per-device pairing for human-level rights
# hostname          = "graith"       # tsnet node name / MagicDNS label (tsnet mode)
# auth_key_file     = "~/.config/graith/tsnet.key"  # tsnet auth key path (tsnet mode)
# tags              = ["tag:graith"] # tsnet ACL tags applied to the node (tsnet mode)
# allow_tailnet_users = ["you@example.com"]  # WhoIs allowlist; "tag:"-prefixed entries opt tagged nodes in
# pair_request_rate = "5/min"        # anti-flood limit on pending pair requests ("<n>/<sec|min|hour>")
# max_pending_pairings = 16          # cap on unapproved pair requests outstanding at once (1-1024)
# pending_pairing_ttl  = "10m"       # how long an unapproved pair request lives before it expires (1m-24h)
# pair_fallback_count  = 5           # rate count applied when pair_request_rate is unset (1-1000)
# pair_fallback_window = "1m"        # rate window applied when pair_request_rate is unset (1s-24h)
```

Pairing policy limits keep the historically-fixed values as defaults and are clamped to safe bounds: a value of `0`/empty means "use the default", and an out-of-bounds value in an enabled block is a hard config-load error. `pair_request_rate` is the primary anti-flood knob; when it is unset the `pair_fallback_count`/`pair_fallback_window` rate applies.

Access is gated in two layers: a WhoIs **allowlist** (`allow_tailnet_users` — who on the tailnet may connect at all) and per-device **pairing** (`require_pairing` — each device proves possession of a paired key before it gets human-level rights).

> **Warning:** `require_pairing = false` is **UNSAFE** — it trusts the tailnet identity alone with no per-device proof, so it is restricted to **read-only** access. Leave pairing on for any device that should control sessions.

The orchestrator can also be given extra filesystem access scoped to itself via `[orchestrator.sandbox]` (`read_dirs`/`write_dirs`), layered on top of the global and per-agent sandbox config. See [Authentication & remote access]({{< relref "/docs/auth.md" >}}) for the full authorization model, token lifecycle, and pairing flow.
