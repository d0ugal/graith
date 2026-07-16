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
agent        = "claude" # agent to run as orchestrator
model        = ""       # optional model override
idle_timeout = "30m"    # auto-stop if idle
prompt       = "..."    # orchestrator-specific system prompt
prompt_file  = ""       # or read from file
```

See [Orchestrator]({{< relref "/docs/orchestrator.md" >}}) for details.

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
```

Access is gated in two layers: a WhoIs **allowlist** (`allow_tailnet_users` — who on the tailnet may connect at all) and per-device **pairing** (`require_pairing` — each device proves possession of a paired key before it gets human-level rights).

> **Warning:** `require_pairing = false` is **UNSAFE** — it trusts the tailnet identity alone with no per-device proof, so it is restricted to **read-only** access. Leave pairing on for any device that should control sessions.

The orchestrator can also be given extra filesystem access scoped to itself via `[orchestrator.sandbox]` (`read_dirs`/`write_dirs`), layered on top of the global and per-agent sandbox config. See [Authentication & remote access]({{< relref "/docs/auth.md" >}}) for the full authorization model, token lifecycle, and pairing flow.

### `tsnet` vs `interface`, and the build footprint

The two modes differ in what they link into the binary:

- **`tsnet`** runs an *embedded* Tailscale node inside the daemon — no host `tailscaled` needed. It links `tailscale.com/tsnet`, which pulls in the full embedded Tailscale dependency graph (the gVisor userspace netstack and WireGuard). This is the convenience default.
- **`interface`** binds the tailnet IP of an *existing* host `tailscaled`. It links only `tailscale.com/client/local` — no netstack.

**The published release binaries include `tsnet`** so both modes work out of the box. The embedded node is a large dependency: it accounts for roughly **17 MB of the binary** (~46 MB → ~29 MB without it) and about a third of the compiled package graph.

For interface-only deployments — a host that already runs `tailscaled` and never needs the embedded node — graith can be built with the **`no_tsnet`** build tag to compile `tsnet` (and its netstack graph) out entirely:

```bash
go build -tags no_tsnet ./cmd/graith
```

Such a build still supports `mode = "interface"` in full. It **fails closed** on `mode = "tsnet"`: the daemon logs a clear error and leaves the remote surface off (the local Unix socket is unaffected) rather than silently downgrading. The measurements and the reasoning for keeping `tsnet` in the default artifacts are recorded in [`docs/design/2026-07-16-tsnet-footprint.md`](https://github.com/d0ugal/graith/blob/main/docs/design/2026-07-16-tsnet-footprint.md).
