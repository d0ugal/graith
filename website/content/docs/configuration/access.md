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

Disabling a running orchestrator takes effect only after the daemon successfully
signals its session to stop. If signaling fails, the reload is rejected and
`orchestrator.enabled` remains true so the config and live process cannot
silently diverge. `gr daemon reload` returns a field- and session-specific error;
an edit-triggered reload records the same failure in the daemon log. Fix the
process or driver problem, then retry the reload.
The daemon retains the entire previous config generation on this failure, so
other edits saved alongside the disable take effect only after a successful
retry.

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

There are two backoff modes. By default an explicit **`schedule`** lists the delay for each attempt, with the final entry repeating for every attempt beyond its length — this preserves graith's historical backoff curve. Schedule entries must be positive and nondecreasing. Setting `schedule = []` switches to **geometric** backoff computed as `initial_backoff × multiplier` each attempt, capped at `max_backoff`; `initial_backoff`, `max_backoff`, and `stable_reset` must be positive, and `multiplier` must be a finite number (a value of `1.0` or less falls back to the default of `2.0`). In geometric mode the effective initial delay must not exceed the effective maximum — the check uses each value's default when the key is omitted, so setting only one of the pair cannot silently contradict the other. Invalid or contradictory policies are rejected on load and reload with a field-specific error. The defaults are chosen so an unconfigured daemon behaves exactly as before.

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

`pending_pairing_ttl` is frozen when a pairing request is created: each request keeps the expiry deadline computed from the TTL that was in effect at that moment. A hot config reload therefore affects only pair requests made after the reload — requests already in flight retain their original expiry, so the waiting device and the daemon never disagree about when a request lapses.

Access is gated in two layers: a WhoIs **allowlist** (`allow_tailnet_users` — who on the tailnet may connect at all) and per-device **pairing** (`require_pairing` — each device proves possession of a paired key before it gets human-level rights).

> **Warning:** `require_pairing = false` is **UNSAFE** — it trusts the tailnet identity alone with no per-device proof, so it is restricted to **read-only** access. Leave pairing on for any device that should control sessions.

### Hot reload and revocation

The whole remote access surface hot-reloads. Changing `enabled`, `mode`,
`hostname`, `port`, `auth_key_file` (including its contents), `tags`, or the
remote TLS certificate/key closes the listener and all remote connections
before graith starts the replacement. A hostname change reissues the
self-signed certificate with the existing key, so its certificate name changes
but its SPKI pin remains stable. Replacing the TLS key changes the pin; clients
that pinned the old key must pair again.

Policy-only edits do not restart the listener. `allow_tailnet_users` is checked
against the live config on every remote frame, and removing an identity closes
its existing connections immediately. Adding an identity admits its future
connections as soon as the reload succeeds. `enabled = false` likewise closes
the listener and every connection immediately.

Listener replacement is deliberately fail-closed. Graith closes the old
generation before binding the new one. If the new port cannot bind, an auth-key
file cannot be read, TLS setup fails, or another transport step fails, the
reload returns an error, the previous config remains visible through
daemon-backed config introspection, and remote access stays closed. (`gr config
show` reads the edited file directly, so it can still display the rejected
candidate.) Correct the setting and reload again (or run `gr daemon restart`);
the local Unix socket remains available.

`require_pairing` is also a live authority ceiling for devices that are already
connected. Changing it from `true` to `false` immediately downgrades every
paired device to the read-only guest role, including devices previously allowed
to control sessions. Changing it back to `true` restores control only for
devices originally approved for full access. A device approved as read-only
while `require_pairing = false` stays read-only and must be paired again before
it can receive full access. This prevents a reload from silently elevating a
device.

The orchestrator can also be given extra filesystem access scoped to itself via `[orchestrator.sandbox]` (`read_dirs`/`write_dirs`), layered on top of the global and per-agent sandbox config. See [Authentication & remote access]({{< relref "/docs/auth.md" >}}) for the full authorization model, token lifecycle, and pairing flow.
