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
An explicit `agent` must name a configured `[agents.<name>]` entry; an empty
value continues to inherit `default_agent`.

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

There are two backoff modes. By default an explicit **`schedule`** lists the delay for each attempt, with the final entry repeating for every attempt beyond its length — this preserves graith's historical backoff curve. Schedule entries must be positive and nondecreasing. Setting `schedule = []` switches to **geometric** backoff computed as `initial_backoff × multiplier` each attempt, capped at `max_backoff`; `initial_backoff`, `max_backoff`, and `stable_reset` must be positive, the initial delay must not exceed the maximum, and `multiplier` must be a finite number (a value of `1.0` or less falls back to the default of `2.0`). Invalid policies are rejected on load or reload. The defaults are chosen so an unconfigured daemon behaves exactly as before.

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
max_pending_pairings = 16           # cap on unapproved pair requests outstanding at once (1-1024)
pending_pairing_ttl  = "10m"        # how long an unapproved pair request lives before it expires (1m-24h)
pair_fallback_count  = 5            # rate count applied when pair_request_rate is unset (1-1000)
pair_fallback_window = "1m"         # rate window applied when pair_request_rate is unset (1s-24h)
```

Pairing policy limits keep the historically-fixed values as defaults and are clamped to safe bounds: a value of `0`/empty means "use the displayed default", and effective config output resolves those sentinels to the values above. An out-of-bounds value in an enabled block is a hard config-load error. `pair_request_rate` is the primary anti-flood knob; when it is unset the `pair_fallback_count`/`pair_fallback_window` rate applies.

Access is gated in two layers: a WhoIs **allowlist** (`allow_tailnet_users` — who on the tailnet may connect at all) and per-device **pairing** (`require_pairing` — each device proves possession of a paired key before it gets human-level rights).

> **Warning:** `require_pairing = false` is **UNSAFE** — it trusts the tailnet identity alone with no per-device proof, so it is restricted to **read-only** access. Leave pairing on for any device that should control sessions.

Remote policy is enforced live. Saving the config or running `gr daemon reload`
immediately applies `enabled` and `allow_tailnet_users` to already-open
connections as well as new ones: disabling remote access closes the listener and
all remote connections, and removing an identity closes that identity's active
connections. Expanding the allowlist admits matching future connections without
disconnecting identities that remain allowed.

Changes to listener-derived settings (`mode`, `hostname`, `port`,
`auth_key_file`, and `tags`) replace the remote listener generation. The daemon
closes the old listener and its connections before it binds the replacement. If
TLS, Tailscale, or bind setup fails, the reload is rejected and remote access
stays off rather than falling back to the old exposure; fix the setting and
reload again. A hostname change reissues the persisted TLS certificate while
preserving its SPKI pin.

Changing `require_pairing` also disconnects remote clients so their role is
re-evaluated. While it is `false`, every allowlisted human connection is a
read-only guest, including devices previously paired for read/write access.
Turning it back on restores read/write access for those full paired devices.
A device enrolled while pairing was off remains read-only after pairing is
re-enabled and must be paired again to gain read/write rights. Remote session
tokens keep their normal session-scoped authorization.

### Pairing completes only after the device confirms receipt

Pairing is a two-way commit: the daemon persists a paired device **only after the
requesting device acknowledges it received its one-time credential**. Approving a
request with `gr pair approve <id>` prints the daemon's TLS SPKI pin right away
and then waits for the device to confirm and store its credential before
reporting `Device paired`. This means an interrupted pairing never leaves a
durable device on the daemon that the requester never received a working token
for (and vice versa — the requesting client stores its credential before
acknowledging, so a crash mid-handshake cannot strand it either).

The pairing client and the daemon must both understand this receipt handshake.
A current `gr` (or GUI) still pairs with an older daemon that predates it, and a
current daemon safely rejects an older client that cannot acknowledge receipt —
so mixed-version fleets during an upgrade keep working.

The orchestrator can also be given extra filesystem access scoped to itself via `[orchestrator.sandbox]` (`read_dirs`/`write_dirs`), layered on top of the global and per-agent sandbox config. See [Authentication & remote access]({{< relref "/docs/auth.md" >}}) for the full authorization model, token lifecycle, and pairing flow.
