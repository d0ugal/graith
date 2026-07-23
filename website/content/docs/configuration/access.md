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
agent        = ""       # agent to run as orchestrator; empty inherits default_agent. A set value must name a configured agent (built-in or [agents.*]) or config load fails.
model        = ""       # optional model override
idle_timeout = "30m"    # auto-stop if idle
prompt       = "..."    # orchestrator-specific system prompt
prompt_file  = ""       # or read from file
```

See [Orchestrator]({{< relref "/docs/orchestrator.md" >}}) for details.

Disabling a running orchestrator takes effect only once the daemon signals its
session to stop. If signaling fails, the reload is rejected and
`orchestrator.enabled` stays true. `gr daemon reload` returns a field- and
session-specific error; an edit-triggered reload logs the same failure. Fix the
process or driver, then retry. The daemon keeps the entire previous config
generation on failure, so other edits saved alongside the disable apply only
after a successful retry.

### Restart policy

When the orchestrator exits unexpectedly — a crash or startup-watchdog kill, never a user, idle, or shutdown stop — the daemon auto-restarts it with a backoff that grows per failure and resets after a run stays up long enough. Tune with `[orchestrator.restart]`:

```toml
[orchestrator.restart]
schedule              = ["2s", "4s", "8s", "16s", "32s", "60s", "300s"]  # per-attempt delays; the last value repeats. Set to [] to use the geometric knobs below.
initial_backoff       = "2s"   # geometric mode: first restart delay (used only when schedule = [])
max_backoff           = "300s" # geometric mode: cap on the restart delay
multiplier            = 2.0    # geometric mode: each delay = previous × this
stable_reset          = "60s"  # a run lasting at least this long resets the backoff to its first step
fresh_start_threshold = 3      # after this many consecutive restarts, relaunch the agent fresh (new session id)
```

By default the **`schedule`** applies, preserving graith's historical backoff curve; its entries must be positive and nondecreasing. `schedule = []` switches to **geometric** mode. `initial_backoff`, `max_backoff`, and `stable_reset` must be positive; `multiplier` must be finite (`1.0` or less falls back to `2.0`). In geometric mode the effective initial delay can't exceed the effective maximum — the check uses each value's default when the key is omitted, so setting only one can't silently contradict the other. Invalid or contradictory policies are rejected on load and reload with a field-specific error. Defaults leave an unconfigured daemon behaving exactly as before.

## Remote access

An optional control listener reaching the daemon from another device over your [Tailscale](https://tailscale.com) tailnet. **Disabled by default and fail-closed**: when enabled, an invalid `[remote]` block is a hard config-load error, not a silent downgrade.

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

Pairing limits default to the historically-fixed values, clamped to safe bounds: `0`/empty means the default, and an out-of-bounds value in an enabled block is a hard config-load error. `pair_request_rate` is the primary anti-flood knob; when unset, the `pair_fallback_count`/`pair_fallback_window` rate applies.

`pending_pairing_ttl` is frozen when a request is created, so a hot reload affects only later requests — in-flight ones keep their original expiry, so device and daemon never disagree about when a request lapses.

Access is gated in two layers: a WhoIs **allowlist** (`allow_tailnet_users` — who on the tailnet may connect at all) and per-device **pairing** (`require_pairing` — each device proves possession of a paired key before it gets human-level rights).

Initiate pairing on the client with `gr remote pair <host>`. On the daemon host,
find the pending request with `gr remote pairings list`, then approve it with
`gr remote pairings approve <request-id>`. Device administration is local-only;
`gr remote pairings revoke <device-id>` revokes a device and force-closes its
live connections. See [Remote access commands]({{< relref "/docs/commands/remote.md" >}})
for the full CLI reference.

> **Warning:** `require_pairing = false` is **UNSAFE** — it trusts the tailnet identity alone with no per-device proof, so it's restricted to **read-only** access. Leave pairing on for any device that should control sessions.

### Hot reload and revocation

The whole remote surface hot-reloads. Changing `enabled`, `mode`, `hostname`,
`port`, `auth_key_file` (including its contents), `tags`, or the remote TLS
certificate/key closes the listener and all remote connections before the
replacement starts. A hostname change reissues the self-signed certificate with
the existing key — its certificate name changes but its SPKI pin stays stable.
Replacing the TLS key changes the pin; clients that pinned the old key must pair
again.

Policy-only edits don't restart the listener. `allow_tailnet_users` is checked
against the live config on every remote frame, so removing an identity closes
its connections immediately and adding one admits future connections once the
reload succeeds. `enabled = false` likewise closes the listener and every
connection immediately.

Listener replacement is deliberately fail-closed: graith closes the old
generation before binding the new one. If the new port can't bind, an auth-key
file can't be read, TLS setup fails, or another transport step fails, the
reload reports remote access as degraded and closed while unrelated settings
from that config generation still apply. (`gr config show` reads the edited
file directly, so it can display the candidate.) Correct the setting and reload
again; the daemon retries remote preparation and the local Unix socket stays
available throughout.

A tsnet auth key is consumed when its listener generation starts. If an
unchanged `auth_key_file` later becomes unreadable, graith keeps the
already-registered generation for unrelated and policy-only reloads, so an
allowlist revocation is never blocked by a spent key file. Any change that
replaces the listener still needs a readable key and fails closed as above.

`require_pairing` is also a live authority ceiling for connected devices.
Changing `true` to `false` immediately downgrades every paired device to the
read-only guest role, including devices previously allowed to control sessions.
Changing back to `true` restores control only for devices originally approved
for full access. A device approved as read-only while `require_pairing = false`
stays read-only and must be paired again before receiving full access —
preventing a reload from silently elevating a device.

The orchestrator can also be given extra filesystem access scoped to itself via `[orchestrator.sandbox]` (`read_dirs`/`write_dirs`), layered on top of the global and per-agent sandbox config. See [Authentication & remote access]({{< relref "/docs/auth.md" >}}) for the full authorization model, token lifecycle, and pairing flow.
