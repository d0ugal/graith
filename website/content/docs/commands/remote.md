---
weight: 445
title: "Remote access commands"
description: "Pair, administer, list, and attach to remote graith daemons."
icon: "lan"
toc: true
draft: false
---

Pair devices and connect to daemons over a Tailscale tailnet. Configure the
listener first — see [Orchestrator & remote access]({{< relref "/docs/configuration/access.md#remote-access" >}}).

## Pair a client

### `gr remote pair <host>`

Request pairing with a remote daemon; blocks until a local human on the host runs
`gr remote pairings approve <request-id>`.

| Flag | Description |
|------|-------------|
| `--port <port>` | Remote daemon port (default `4823`) |
| `--profile <name>` | Named profile used by the remote daemon |
| `--label <label>` | Device label shown to the approving human |

## Administer paired devices

These connect through the local Unix socket and need the local-human credential;
the daemon rejects them remotely.

### `gr remote pairings list`

List pending requests and paired devices. `--json` emits `pending` and `paired`
arrays.

### `gr remote pairings approve <request-id>`

Approve a pending request, returning the device ID and TLS SPKI pin. The waiting
client receives its credential.

### `gr remote pairings revoke <device-id>`

Revoke a paired device, force-closing its live connections.

## List and attach

### `gr remote list`

List remote hosts paired by this client — the client-side counterpart to
`gr remote pairings list`.

### `gr remote attach <host>/<session>`

Attach to a session on a paired remote daemon.
