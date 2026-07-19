---
weight: 445
title: "Remote access commands"
description: "Pair, administer, list, and attach to remote graith daemons."
icon: "lan"
toc: true
draft: false
---

The `gr remote` namespace covers both sides of device pairing and connections to
daemons over a Tailscale tailnet. Configure the listener first; see
[Orchestrator & remote access]({{< relref "/docs/configuration/access.md#remote-access" >}}).

## Pair a client

### `gr remote pair <host>`

Request pairing with a remote daemon. The command waits while a local human on
the daemon host approves the request with
`gr remote pairings approve <request-id>`.

| Flag | Description |
|------|-------------|
| `--port <port>` | Remote daemon port (default `4823`) |
| `--profile <name>` | Named profile used by the remote daemon |
| `--label <label>` | Device label shown to the approving human |

## Administer paired devices

These commands always connect through the local Unix socket and require the
local-human credential. The daemon rejects the same administrative messages over
a remote connection.

### `gr remote pairings list`

List pending requests and paired devices. With `--json`, the existing
`pending` and `paired` arrays are emitted for machine consumers.

### `gr remote pairings approve <request-id>`

Approve a pending request and return the device ID and TLS SPKI pin. The waiting
client receives its credential over the pairing connection.

### `gr remote pairings revoke <device-id>`

Revoke a paired device. Revocation force-closes that device's live connections.

## List and attach

### `gr remote list`

List remote hosts paired by this client. Deliberately separate from
`gr remote pairings list`, which administers devices trusted by the local host.

### `gr remote attach <host>/<session>`

Attach to a session on a paired remote daemon.
