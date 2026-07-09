---
title: "Design Doc: Universal (iOS + macOS) App with Tailscale Remote Control"
authors: Dougal Matthews
created: 2026-07-07
status: Draft
reviewers: (none yet)
informed: (TBD)
issue: 628
---

# Universal (iOS + macOS) App with Tailscale Remote Control

## Background

graith is **Unix-socket + TUI only**. The daemon (`graithd`) listens on a Unix
socket (`internal/daemon/server.go`, `net.Listen("unix", …)`, chmod `0700`),
the CLI (`gr`) dials that socket (`internal/client/client.go`), and all
interaction is terminal-native: `gr attach` drops you into a session's PTY,
`gr dashboard` / the overlay picker (ctrl+b w) render in the terminal. There is
no way to monitor or drive sessions from a phone, or from any host other than
the one the daemon runs on.

Two existing artifacts are directly relevant:

- **Issue #628** originally proposed a web/HTTP control surface plus a mobile
  PWA. This document **re-frames #628**: we want a **native app** (not a web
  app / PWA), built on **Ghostty** for terminal rendering, connecting over
  **Tailscale**, able to manage **multiple daemons on multiple hosts**. The web
  surface is explicitly dropped. The target is a **universal iOS + macOS app**
  from one codebase (see "A universal app" below): iOS is the new front-end, and
  macOS supersedes the gui-poc by swapping its transport for the shared protocol
  client.

- The **`d0ugal/gui-poc` branch** contains a working native **macOS** GUI under
  `gui/` — a SwiftUI app that renders terminals with Ghostty's `libghostty-vt`
  (VT parsing, grid state, key/mouse encoding, selection) plus a hand-written
  CoreText **and** Metal renderer. It reaches the daemon indirectly by
  **fork/exec'ing `gr attach`** PTY subprocesses and polling `gr list --json`.
  See `gui/design-doc.md` on that branch. This design **evolves** gui-poc rather
  than sitting beside it: its renderer core is reused, but its fork/exec
  transport is replaced by the same network/protocol client the iOS app uses —
  which additionally lets the macOS app be App-Sandboxed (fork/exec was the
  blocker gui-poc documented).

- **Issue #615** (native remote attach — render remote sessions as local
  terminal tabs) wants exactly the same primitive this doc introduces: a
  network transport + auth model so a client can reach a daemon on another host.
  #628 and #615 are two front-ends over one shared remote-control substrate.

### What the gui-poc branch gives us — and where it stops for iOS

The gui-poc rendering work is valuable, but **less of it is drop-in-portable
than a first glance suggests**. The Swift is written for AppKit; the truly
platform-neutral core is small. A realistic reuse assessment:

| Component | iOS reuse | Notes |
|-----------|-----------|-------|
| `GhosttyTerminalState` (libghostty-vt wrapper) | **Portable** | VT parsing / grid / key + mouse encoding — no platform ties |
| `Session` model | **Portable** | Codable data model; may need field updates |
| `MetalTerminalRenderer` | **Port required** | Metal runs on iOS, but this file `import`s AppKit and uses `NSFont`/`NSFontManager`/`NSColor` and `.managed` Metal storage (macOS-oriented). It is a strong porting *base*, not shared code |
| `TerminalSearchState` | **Port required** | Currently lives in a file importing SwiftUI + AppKit |
| `Theme` | **Port required** | `import`s AppKit (`NSColor`) |
| `KeyMapping` | **Rewrite** | Maps macOS virtual key codes; iOS hardware-keyboard input uses a different path |
| `BaseTerminalNSView` (+ CoreText backend) | **Rewrite** | `NSView` / `NSTextInputClient`; the iOS input layer (`UIKeyInput`/`UITextInput`) is a *different protocol*, so this is a rewrite (~800 lines), not a port. CoreText backend dropped (Metal-only on iOS) |
| `SessionStore` (`gr list --json` polling) | **Replace** | Subprocess model |
| PTY attach via `forkpty()` + `execve("gr attach")` | **Replace** | iOS has no `fork`/`exec` and is sandboxed |

The load-bearing point holds: **the reusable core is the VT/render logic; the
transport must be replaced.** gui-poc works because on macOS it shells out to the
real `gr` client. iOS cannot: no `fork`/`exec`, no child processes, hard app
sandbox. So the iOS app must **speak graith's framed protocol directly over a
network socket** — the daemon-side network surface that #628 is about. This doc
is therefore two things:

1. A **daemon network + auth surface** (reusable by iOS *and* by `gr attach
   --remote` for #615).
2. A **native iOS app** that ports the gui-poc rendering stack to UIKit and
   drives sessions through that network surface.

### The protocol is transport-agnostic — the handler is not fully multiplexed

`internal/daemon/server.go` is encouraging. `Server` wraps *any* `net.Listener`
and dispatches each accepted `net.Conn` to a handler:

```go
func NewServer(l net.Listener, handler func(ctx, conn), log) *Server
func (s *Server) Serve(ctx) error   // loops on s.listener.Accept()
```

`HandleConnection` (`internal/daemon/handler.go`) reads the 5-byte framed
protocol off the `net.Conn` — channel `0x00` for JSON control envelopes,
channel `0x01` for raw PTY bytes — and never assumes a Unix socket. The control
envelope already carries a `Token` (`internal/protocol/messages.go`), and the
message set already covers what a remote client needs: `list`, `attach`,
data/keystrokes, `resize`, `logs`, `screen_snapshot`, `msg_*`, `approval_*`,
`status`, `diagnostics`. **We do not need a new protocol.**

Two caveats that shaped the rest of this design:

- **The handler is not a fully multiplexed RPC layer.** Several operations take
  over the connection in nested read loops: `logs` with `Follow`
  (`handler.go:621-646`), `msg_inbox`/`msg_sub` (`handler.go:704-728`),
  `approval_request` (blocks, `handler.go:848-893`), `mcp_connect`
  (`handler.go:1185-1242`). A robust iOS app therefore uses **multiple
  connections per host** — one for short-lived control RPCs, one per attached
  terminal, one for event/approval subscription — rather than assuming a single
  connection can carry everything (see C.2).

- **The daemon currently owns exactly one listener.** It starts one listener
  (`daemon.go:4694`, `4719-4723`) and the self-upgrade path type-asserts it to
  `*net.UnixListener` (`daemon.go:4779-4783`). Running a second, network-facing
  listener is real lifecycle work (startup, shutdown coordination, upgrade fd
  handling), not "just call `Serve` twice" (see A.1).

### Why auth is the hard part

This is the finding that most reshaped the design. `internal/daemon/auth.go`
treats an **empty token as the human CLI** with broad rights, and this
assumption is **not confined to `checkTarget`** — it is scattered across many
handler paths and helper predicates that key off `auth.authenticated` directly.
Concrete examples that are *unauthenticated-permissive today*:

- `checkMsgPub` (`auth.go:108`) unconditionally returns `nil`;
  `handler.go:669-673` lets an unauthenticated caller supply its own `SenderID`.
  Over a network
  surface this is **`msg_pub` sender-spoofing from any tailnet peer with no
  token** — a remote agent could impersonate any session.
- `checkScenarioOp` (`auth.go:123`) begins `if !ac.authenticated { return nil }`
  — an unauthenticated caller may **stop / delete / add** scenarios.
- Message types with no target check today (`list`, `create`, `approval_list`,
  `diagnostics`, `reload`, `upgrade`) and human-only ops implemented as "must be
  *un*authenticated" (`approval_respond`, `reload`, `upgrade` at
  `handler.go:896-899`, `1038-1041`, `1425-1428`) all inherit "the socket is the
  gate."

All of this is safe *only because the Unix socket is `0700`* — reaching it
already proves you are the local user. The moment the daemon accepts network
connections, "no token ⇒ human" is a full remote takeover. **The auth work is
not "rewrite the four `authRule` cases"; it is an origin-aware boundary
rejection plus a per-message audit of every handler path.** Part B specifies
this in full.

## Problem

Users running AI agent fleets want to **watch and drive those sessions from
their phone**, and from any host — approve a tool call from the sofa, unstick a
stopped agent, kick off a session, read output — with the same fidelity as
sitting at the terminal. Today that is impossible: the daemon is only reachable
locally, over an unauthenticated socket, through a terminal-only client.

## Goals

- A **universal native app** (Swift, one codebase) for **iOS and macOS** that
  renders live sessions with Ghostty. macOS reaches the local daemon over its
  Unix socket and remote daemons over the tailnet; iOS is tailnet-only.
- **Tailscale** as the network substrate for remote hosts: remote daemons are
  reachable only over the tailnet; the app connects to their MagicDNS names.
- **Multiple daemons on multiple hosts**: the app manages a registry of daemons
  and presents an aggregated, per-host view of all sessions.
- A **fail-closed remote auth model**: Tailscale identity as the outer gate,
  explicit device pairing minting a revocable per-device credential as the inner
  gate, and an audited per-message authorization matrix.
- **Reuse** of the `gui-poc` rendering *core* (libghostty-vt + Metal) across both
  platforms, with a UIKit input view added for iOS (honest reuse boundary in the
  table above).
- A network + auth substrate **factored so #615's `gr attach --remote` reuses
  it**.
- Off by default: the network surface is opt-in via config; existing
  Unix-socket behaviour is untouched when disabled.

The **interaction target is full interactive attach** — typing into a session,
resize, scroll, select/copy. Note the shipping order in Phasing: a **read +
approvals** milestone lands first, with full attach as the immediately following
milestone, because the UIKit input layer is the single largest piece of work.

## Non-Goals

- **No web app / PWA.** Explicitly dropped from the #628 scope.
- **No new *wire* protocol / no parallel REST or gRPC surface.** We reuse the
  existing framed protocol and message types. New *control messages* are added
  only for pairing/device management (B.5) — that is additive, not a second
  protocol.
- **No Android / other mobile platforms** in this iteration.
- **No embedded Tailscale on iOS in v1.** We rely on the official Tailscale iOS
  app to provide the tunnel; the graith app dials tailnet addresses. Embedding
  the Tailscale NetworkExtension is a future enhancement (and note: iOS permits
  only one active packet-tunnel provider, so an embedded tunnel could not
  coexist with the standalone Tailscale app).
- **No automatic daemon discovery in v1 (backlogged).** Hosts are added manually
  by MagicDNS name, then paired. Auto-discovery is a future enhancement. Options
  considered: (a) **Tailscale admin API + a `tag:graith` device tag** — the app,
  given a tailnet API credential entered once, lists tagged daemons as pair-ready
  entries (cleanest, but requires a tailnet-level credential); (b) a lightweight
  **naming-convention probe** (try `graith-*` / previously-used names, no
  credential); (c) **daemon-advertised siblings** — a paired daemon tells the app
  about other graith daemons it knows, bootstrapping discovery from the first
  host. Ruled out: mDNS/Bonjour (Tailscale doesn't forward multicast across the
  tailnet) and reading Tailscale's on-device LocalAPI (iOS sandboxes it from
  other apps). v1 stays manual since a user typically has a handful of hosts.
- **No background push notifications in v1.** Approval prompts surface while the
  app is connected/foregrounded. APNs-based background alerts need a push relay
  and are deferred — **but see the value risk in Open Questions:** the headline
  "approve from your phone" use case is materially weaker without them.
- **Not replacing the CLI.** The CLI remains the primary interface; the apps are
  additional front-ends. (The macOS gui-poc *is* superseded — it becomes the
  macOS half of this universal app, evolving its transport.)
- **Not a Linux/Windows GUI.** The universal app targets Apple platforms
  (Ghostty's macOS/iOS libghostty path). Other platforms keep the CLI.
- **No changes to session/worktree lifecycle semantics.** Single-attach-per-
  session still holds (see Security & Constraints).

## Proposed Design

### Overview

```
  iPhone / iPad                          Tailnet (WireGuard mesh)          Host A
 +---------------------------+                                       +-------------------------+
 |  graith iOS app           |                                       |  graithd (daemon)       |
 |                           |                                       |                         |
 |  HostRegistry             |        MagicDNS: graith-a.ts.net      |  Unix socket (0700) ----+-- local gr
 |   - graith-a  [token]  ===+====== TLS over WireGuard ===========> |  tailnet listener  <----+   (unchanged)
 |   - graith-b  [token]     |                                       |   (tsnet | interface)   |
 |                           |                                       |   + ConnOrigin + WhoIs  |
 |  GraithProtocolClient     |                                       |   + client-token auth   |
 |   (framed protocol,       |                                       |         |               |
 |    multiple conns/host)   |                                       |   HandleConnection      |
 |        |                  |                                       |    (origin-gated auth)  |
 |  BaseTerminalUIView       |   <---- channel 0x01: PTY bytes ----- |         |               |
 |   (libghostty-vt + Metal) |   ----- channel 0x01: keystrokes ---> |     PTY sessions        |
 |   + on-screen key row     |   ----- channel 0x00: control ------> |                         |
 +---------------------------+                                       +-------------------------+
        |                                                                    Host B
        +------------------- graith-b.ts.net -------------------------> (another graithd)
```

The design has three parts: **(A)** the daemon network surface, **(B)** the
auth & pairing model, **(C)** the iOS app.

---

### Part A — Daemon network surface

#### A.1 A second listener, same handler, with connection origin

Add an optional tailnet-facing listener alongside the Unix socket. Both feed the
same `HandleConnection`, so every existing control/data message is *transported*
remotely for free — **but authorization is not free** (see Part B). The daemon
runs both listeners concurrently; the Unix socket remains the default and is
unaffected when the remote surface is disabled.

Two concrete pieces of work beyond "wrap a `net.Listener`":

1. **Thread connection origin into the handler.** `HandleConnection` currently
   takes a bare `net.Conn`. It must receive a `ConnOrigin` so auth can
   distinguish local from remote and recover the tailnet identity *before the
   first control frame is processed*:

   ```go
   type ConnOrigin struct {
       Remote   bool             // false = Unix socket (local user)
       Identity *TailnetIdentity // WhoIs result for remote conns; nil for local
   }
   // HandleConnection(ctx, conn, origin, sm, log)
   ```

2. **Generalize listener lifecycle.** The daemon must own more than one listener,
   coordinate shutdown across both, and fix the self-upgrade path that today
   type-asserts the single listener to `*net.UnixListener` (`daemon.go:4779`).
   The remote listener's behaviour across `gr daemon restart` / self-upgrade must
   be designed (re-open on the new process vs. drop remote clients with a
   reconnect). Config changes to `[remote]` under the existing config watcher
   should define whether listeners start/stop live or require a daemon restart
   (v1: **restart required**, to keep listener lifecycle simple and fail-closed).

#### A.2 Two ways onto the tailnet (both supported, tsnet default)

Config selects how the daemon exposes itself — **support both, tsnet as the
default**:

**Mode `tsnet` (default)** — embed `tailscale.com/tsnet`. `graithd` joins the
tailnet as its own node with a stable MagicDNS name and its own identity, with
**no host `tailscaled` required**:

```go
ts := &tsnet.Server{
    Hostname:  cfg.Remote.Hostname,   // e.g. "graith-<host>"
    AuthKey:   cfg.Remote.AuthKey,    // or auth_key_file / OAuth client
    Ephemeral: false,
}
ln, err := ts.Listen("tcp", fmt.Sprintf(":%d", cfg.Remote.Port))
lc, _ := ts.LocalClient()            // for WhoIs identity
```

- Pros: self-contained, per-daemon identity/ACL target, works on hosts without
  Tailscale installed, stable MagicDNS name.
- Cons: **larger binary** — bundles the gVisor userspace netstack (a heavy
  dependency for an otherwise lean daemon; there is no Tailscale dep in `go.mod`
  today). Needs an auth key / OAuth client provisioned per daemon on first run;
  with no key and `Ephemeral: false`, only a login URL in `daemon.log` unblocks
  it — a first-run UX cliff for a headless daemon (see Open Questions).

**Mode `interface`** — for hosts already running `tailscaled`. Bind a TCP
listener to the host's tailnet IP (resolved via the Tailscale `LocalClient`,
**never `0.0.0.0` and never a public interface**), and use the same
`LocalClient` for `WhoIs`:

```go
lc := &tailscale.LocalClient{}
ip := tailnetIP(lc)                          // 100.x.y.z (helper: handle v4/v6, multiple addrs)
ln, err := net.Listen("tcp", net.JoinHostPort(ip, port))
```

- Pros: lighter daemon, reuses existing tailnet membership and MagicDNS.
- Cons: requires host `tailscaled`. **Fail closed if the tailnet IP can't be
  resolved** — do not fall back to a wildcard bind.

Both modes converge on the same contract handed to the handler: **a
`net.Listener` on the tailnet + a `WhoIs(ctx, remoteAddr)` capability + a
`ConnOrigin{Remote: true, Identity: …}`.**

`[remote]` config is validated at load: `mode` is an enum (`tsnet`/`interface`);
`interface` mode must resolve a tailnet IP or the daemon fails to start the
remote listener (Unix socket still comes up); tsnet fields (`auth_key`/`tags`)
are ignored/rejected in `interface` mode.

#### A.3 Transport encryption and endpoint identity

WireGuard already provides **transport confidentiality** end to end, so socket
bytes are private on the wire. TLS is layered on for **endpoint identity and
defense in depth** (the accurate framing — not "TLS instead of WireGuard"):

- `tsnet` mode: use `ts.ListenTLS` with a tailnet HTTPS cert for the MagicDNS
  name. This requires the tailnet to have **HTTPS certs / MagicDNS enabled** (an
  admin feature) and first-run cert provisioning; call this out as an
  operational precondition.
- `interface` mode: a self-signed cert generated on first run.
- **Pinning:** prefer **SPKI / public-key pinning** over pinning the leaf
  certificate — a pinned `tsnet` Let's Encrypt leaf will break on renewal. For
  `interface`-mode self-signed certs, TOFU: the app pins the fingerprint at
  pairing time, and `gr pair` shows the fingerprint locally so the human can
  confirm it. Host rename / re-key requires re-pairing.

#### A.4 Config

New `[remote]` block, **disabled by default**:

```toml
[remote]
enabled       = false
mode          = "tsnet"          # "tsnet" | "interface"
hostname      = "graith-laptop"  # tsnet node name / MagicDNS label
port          = 4823
auth_key_file = "~/.config/graith/ts-authkey"   # tsnet only
tags          = ["tag:graith"]   # tsnet ACL tags
# pairing / auth
allow_tailnet_users = ["you@example.com"]  # WhoIs allowlist (Gate 1)
require_pairing     = true                 # default; false is UNSAFE (see B.2)
pair_request_rate   = "5/min"              # anti-flood on pending pairings
```

---

### Part B — Auth & pairing model

**Tailscale identity + device pairing** (belt and suspenders). Two gates, both
must pass for a remote client to get human-level rights, *and* every message is
checked against an explicit role.

#### B.1 Gate 1 — Tailscale identity (WhoIs)

On accept, for remote connections the daemon calls
`LocalClient.WhoIs(ctx, remoteAddr)` **once** to resolve the tailnet **user** and
**node**, and stores it in `ConnOrigin.Identity`. Connections from identities not
permitted by `[remote].allow_tailnet_users` (and by the tailnet's own ACLs) are
refused at handshake, before any control message is processed. Decide policy for
**tagged nodes** (WhoIs may return a tagged-node identity with no user) — v1:
tagged nodes are *not* allowed unless explicitly listed, so tag-authed peers
don't silently pass.

#### B.2 Gate 2 — device pairing → client token

Reaching the daemon is necessary but not sufficient. Each device completes a
one-time **pairing** that mints a long-lived, revocable **client token**
distinct from session tokens:

1. **Pre-auth pairing lane.** A brand-new device has no token. The only thing a
   `roleNone` remote connection may do (after Gate 1) is send `pair_request`
   (plus `handshake`). Everything else is rejected. This lane is explicit so
   "handshake refuses unknown identities" (B.1) and "first launch sends
   `pair_request` without a token" (B.2) don't contradict.
2. `pair_request` carries a device label **and a device public key**. The daemon
   surfaces a pending pairing to the **local human** — via the overlay/dashboard
   and a new `gr pair` CLI (`gr pair list` / `gr pair approve <id>` / `gr pair
   revoke <id>`). Approval is a **local, out-of-band human action**.
3. On approval the daemon records the device (public key, WhoIs user+node, label)
   and mints a client token, returned once via `pair_response`; the app stores it
   in the iOS **Keychain**.
4. **Proof-of-possession (explicit wire exchange).** The device public key is not
   decorative, and the envelope's single `Token` field is not enough to carry a
   signature — so PoP needs its own messages. On every **remote** connection,
   immediately after `handshake`, the daemon sends `auth_challenge{nonce}`; the
   client replies `auth_proof{device_id, signature}` signing the nonce with its
   device private key. The connection stays `roleNone` (Gate-A lane only) until a
   valid `auth_proof` is received, so a leaked bearer token alone is insufficient.
   **Channel binding (issue #886).** The signed material is not the bare nonce
   but `"graith-pop-v1:" + nonce + ":" + spki`, where `spki` is the base64
   SHA-256 SPKI pin of the server certificate this TLS connection actually
   terminates on. The daemon signs against its own pin (`remoteTLSPin`); each
   client signs against the pin it observed/pinned — never one the peer reports.
   A man-in-the-middle who relays the handshake terminates TLS with a different
   certificate (different SPKI), so a proof it captures will not verify against
   the honest daemon's pin, and it cannot obtain the session token. The exact
   construction is `protocol.PoPSigningInput`, shared verbatim by the daemon
   (`verifyPoP`), the Go client (`completeRemotePoP`), and the Swift client
   (`DeviceKeySigner.proof(forNonce:channelBinding:)`).
   (If we later decide PoP is more than v1 warrants, we **delete
   `auth_challenge`/`auth_proof` and the keypair** and treat the token as
   bearer-bound-to-WhoIs — but the design and the wire must stay in lockstep; the
   doc must not describe a keypair it never uses. v1 decision: **keep PoP**, since
   the device already generates a keypair and it closes the stolen-token window.)
5. **Bind to identity every connection.** On every connection the daemon compares
   the current WhoIs user/node to the record stored at pairing (using stable
   Tailscale identifiers, not display names). A token presented from a different
   identity is rejected.

**`require_pairing = false`** trusts Gate 1 alone. This is **not merely "lower
friction" — it is unsafe**: it makes compromise of a tailnet account/node
equivalent to full local-human control. If retained it is documented as unsafe
and (v1) restricted to **read-only** operations.

**Pairing approval is local-only.** `pair_approve` and `pair_revoke` require the
**local Unix socket** (`roleLocalHuman`), *not* "local socket or any paired
device" — otherwise a compromised paired phone could approve further devices
remotely, defeating the out-of-band property. Pairing requests are rate-limited
(`pair_request_rate`) with a pending-queue cap and per-request expiry so a
permitted tailnet user can't flood the local human with prompts.

#### B.3 Role model — replacing "authenticated bool" with an origin-aware role

Today `authContext` is `{sessionID, authenticated}` and predicates key off
"token present or not." We refactor to an explicit **role that includes origin**,
so "no token" never again means "human":

```go
type authRole int
const (
    roleNone         authRole = iota // remote, unpaired/invalid  -> only handshake + pair_request
    roleLocalHuman                   // local Unix socket (the 0700 trust boundary)
    roleRemoteHuman                  // valid paired client token, WhoIs identity matches record
    roleSession                      // per-session agent token
    roleOrchestrator                 // session token whose SystemKind == orchestrator
)
```

- **`roleLocalHuman` vs `roleRemoteHuman` are distinct** (not collapsed into one
  "human"). Local retains everything it has today, including local-only admin
  operations. Remote human is granted an *explicit* subset (B.4).
- **`roleRemoteGuest`** is the role for a device paired while
  `require_pairing = false` (the unsafe, WhoIs-only mode): a **read-only** subset
  only (list/status/logs/screen peek), no mutation. If `require_pairing = false`
  is not shipped, this role is unused.
- **`roleNone` is the default for any remote connection without a valid
  credential**, and it is **rejected at the connection boundary** — the very
  first thing that happens for a remote conn is: resolve origin → WhoIs (Gate 1)
  → PoP challenge/response → require a valid paired token (Gate 2); until all
  pass, the only permitted messages are `handshake`, `pair_request`, and
  `auth_proof`. This must be enforced *before* message dispatch, not only inside
  the four `authRule` predicates.
- **`resolveAuth` becomes origin-aware.** A client token resolves to
  `roleRemoteHuman` (or `roleRemoteGuest`) **with an empty `sessionID`**. This
  changes the meaning of `authContext.sessionID == ""` — it no longer implies
  "unauthenticated human"; every site that treats empty `sessionID` as "human
  CLI" must switch to checking the role (see B.4). **Human roles are the fleet
  operator, not a session**, so they are *not* subject to the session
  self/descendant check — the self/descendant rule applies only to `roleSession`;
  human roles are authorized directly by role.

#### B.4 Per-message authorization matrix (the actual audit)

The refactor's real surface is **every message type and every `auth.authenticated`
call site**, not just `checkTarget`. The design commits to this matrix. It is
**not abridged in intent** — the implementation must produce a row (and a test)
for **every** `case` in the `HandleConnection` switch, plus a completeness test
that fails when a new `case` is added without a matrix entry. Representative rows
(`RG` = `roleRemoteGuest`, the read-only `require_pairing=false` role):

| Message(s) | `roleLocalHuman` | `roleRemoteHuman` | `RG` | `roleSession` | `roleNone` |
|-----------|-----------|-----------|------|-----------|-----------|
| `handshake`, `pair_request`, `auth_proof` | ✓ | ✓ | ✓ | ✓ | ✓ (only these) |
| `list`, `status`, `logs`, `screen_preview`, `screen_snapshot`, `repo_list` | ✓ | ✓ | ✓ | self/descendant | ✗ |
| `attach`, data channel, `resize` | ✓ | ✓ | ✗ | self/descendant | ✗ |
| `approval_list` | ✓ | ✓ | ✓ | ✗ | ✗ |
| `approval_respond`, `approval_subscribe` (C.6) | ✓ | ✓ | ✗ | ✗ | ✗ |
| `create`, `delete`, `stop`, `resume`, `restart`, `rename`, `star`, `fork`, `migrate`, `update` | ✓ | ✓ | ✗ | self/descendant | ✗ |
| `msg_pub` | ✓ (sender forced) | ✓ (sender forced) | ✗ | ✓ (sender forced) | ✗ |
| `msg_inbox`, `msg_sub`, `msg_ack`, `msg_conversation`, `msg_topics` | ✓ | ✓ | ✗ | self/descendant | ✗ |
| `scenario_*` | ✓ (operator) | ✓ (operator) | ✗ | orchestrator/descendant | ✗ |
| `mcp_connect` | ✓ | ✗ (session-only) | ✗ | self | ✗ |
| `pair_approve`, `pair_list`, `pair_revoke` | ✓ | **✗ (local-only)** | ✗ | ✗ | ✗ |
| `upgrade`, `reload` | ✓ | **✗ (local-only)** | ✗ | ✗ | ✗ |
| `diagnostics` | ✓ (full) | scoped (redacted) | ✗ | ✗ | ✗ |

**`scenario_*` resolves the empty-`sessionID` question:** scenario ops are
authorized for **humans as the operator** (local or remote — no `sessionID`
needed, they are not sessions) *or* for a `roleSession` that is the scenario's
orchestrator/descendant. So `checkScenarioOp` becomes: allow if `ac.isHuman()`,
else apply the orchestrator/descendant session rule; reject `roleNone`/`RG`.

Specific fixes this bakes in (all currently unauthenticated-permissive):
`checkMsgPub` must reject `roleNone` and force `SenderID` from the authenticated
identity for *all* roles (kills the spoofing vector); `checkScenarioOp` must not
early-return for "not authenticated"; `list`/`create`/`approval_list`/
`diagnostics` must require ≥ `roleRemoteHuman`; the human-only ops implemented as
"must be unauthenticated" (`approval_respond`, `upgrade`, `reload`) must switch to
role checks, with `upgrade`/`reload`/pairing being **local-only**.

#### B.5 New protocol messages, state, revocation

- Messages (`internal/protocol/messages.go`): `PairRequestMsg`,
  `PairResponseMsg`, `PairApproveMsg`, `PairListMsg`, `PairRevokeMsg`,
  `AuthChallengeMsg`, `AuthProofMsg` (PoP — see B.2.4), `ApprovalSubscribeMsg`,
  and `RepoListMsg`/`RepoListResponseMsg` (remote-create repo picker — see C.4).
  Handler cases in `daemon/handler.go`.
- State (`daemon/state.go`): a `PairedDevices` table — `{id, label, pubkey,
  tailnet_user, tailnet_node, token_hash, created_at, last_seen_at}` — persisted
  in `state.json` (schema version bump + migration), listable/revocable, surfaced
  by `gr doctor`. Client tokens are high-entropy random, stored as a keyed hash
  (HMAC/SHA-256; password hashing is unnecessary), compared in constant time.
- **Revocation drops live connections.** Auth is resolved per *control* message,
  but keystrokes flow on channel `0x01` guarded only by `IsAttachedClient` with
  no per-frame token re-check (`handler.go:1446-1452`). So `gr pair revoke` must
  **track connections by device/principal and force-close active connections**
  for that device — otherwise a revoked (e.g. stolen) phone keeps typing into an
  attached session until it disconnects. This is stated as a hard requirement,
  not "takes effect on next message."
- **Audit logging.** Remote security-sensitive actions (pair request/approve,
  attach + takeover, create/delete, approval decisions) are logged with the
  paired **device ID and WhoIs identity**, not just "human" — the `0700` socket
  needed no audit trail; a network surface does.

---

### Part C — The apps (universal iOS + macOS)

Native Swift app (Xcode project + a shared Swift package factored out of
`gui-poc`). **No web tech.**

#### C.0 One codebase, two platforms

The iOS and macOS apps are **one universal SwiftUI app** built from a shared
core. The layering makes this clean:

```
                     +-------------------------------------+
                     |  SwiftUI app shell (shared)         |
                     |  HostRegistry, sidebar, sheets,     |
                     |  approvals UI, session store         |
                     +-------------------------------------+
                        |                         |
        +---------------------------+   +---------------------------+
        | GraithProtocolClient      |   | GraithTerminalCore        |
        | (shared) transport-abstr. |   | (shared) GhosttyTerminal- |
        |  - Unix socket (local)    |   |  State + Metal renderer   |
        |  - NWConnection/TLS (net) |   +---------------------------+
        +---------------------------+                 |
                                          +-----------+-----------+
                                          |                       |
                                +------------------+   +--------------------+
                                | BaseTerminalUIView|   | BaseTerminalNSView |
                                | (iOS: UITextInput)|   | (macOS: NSText-    |
                                |                   |   |  InputClient)      |
                                +------------------+   +--------------------+
```

- **Shared, unchanged across platforms:** `GraithProtocolClient`,
  `GraithTerminalCore` (`GhosttyTerminalState`, Metal renderer core, `Session`,
  `TerminalSearchState`, `Theme`), `HostRegistry`, the SwiftUI shell (sidebar,
  new-session sheet, approvals, multi-host aggregation), and — critically — the
  entire **transport + auth + pairing** story.
- **Platform-specific (thin):** the terminal *input view* behind a common
  protocol. macOS keeps gui-poc's `BaseTerminalNSView`/`NSTextInputClient` (which
  already works); iOS gets the new `BaseTerminalUIView`/`UITextInput` (C.3). Both
  drive the same shared `GhosttyTerminalState` and Metal renderer.

The unification comes from the **transport abstraction** in
`GraithProtocolClient` (C.2): a *host* is either **local** (the daemon's Unix
socket, reached directly — no fork/exec, no `gr` subprocess) or **remote** (a
tailnet MagicDNS endpoint over `NWConnection`+TLS). The macOS app therefore
gains everything the iOS design adds — **multi-host**, remote daemons, the
subscribe-based approval queue — for free, while dropping the subprocess model.

**Recommended packaging:** a native **SwiftUI multiplatform** app (single
project, `#if os(macOS)/os(iOS)` only around the two input views), *not* Mac
Catalyst — this preserves gui-poc's native-AppKit terminal on macOS and keeps
each platform's input handling first-class. Mac Catalyst (run the iOS UIView
everywhere, one input layer, less mac-native feel) is a considered alternative
(see Open Questions).

#### C.1 Shared code with gui-poc

Extract the platform-neutral core from `gui-poc` into a shared package,
`GraithTerminalCore`: `GhosttyTerminalState` and the `Session` model are directly
portable; `MetalTerminalRenderer`, `TerminalSearchState`, and `Theme` move in as
**porting bases** with their AppKit dependencies (`NSFont`/`NSColor`/`.managed`
storage) replaced by UIKit/`CoreText`/`.shared` equivalents; `KeyMapping` is
rewritten for iOS input. Both the macOS gui-poc and the iOS app depend on the
shared package.

`libghostty-vt` must be rebuilt as an **iOS (arm64) + simulator** slice (an
`.xcframework`), pinned to a Ghostty commit SHA — the branch ships an unpinned
macOS-arm64 `.a` (confirmed: `lipo -info` reports a non-fat arm64 archive; a
macOS `.a` cannot link into an iOS target regardless). **Treat this as a
feasibility spike with an acceptance test** ("the Swift package links and renders
a smoke VT transcript on device *and* simulator"), not a routine extraction —
Ghostty's own docs still warn libghostty is not yet a stable standalone API, and
an iOS build may need Zig build-system/libc/symbol-visibility work.

#### C.2 The transport swap — native protocol client, multiple connections/host

Replace fork/exec + `gr list --json` with a **native Swift implementation of the
framed protocol**, `GraithProtocolClient`. The frame format is simple: channel
byte + big-endian `uint32` length + payload. The client is **transport-abstract**
so the same code serves both platforms and both host kinds:

- **Local host** (macOS, the daemon on this machine): connect to the Unix socket
  directly — no fork/exec, no `gr` subprocess, no tailnet needed.
- **Remote host** (any tailnet daemon; the only kind on iOS): `NWConnection` +
  TLS to the MagicDNS endpoint.

Both speak the identical framed protocol; only the underlying `NWConnection`
endpoint (`.unix(path:)` vs `.hostPort(...)` with TLS) differs.

Because the daemon handler is not fully multiplexed (see Background), the app
opens **multiple connections per host**:

- a **control** connection for short-lived RPCs (`list`, `status`, `create`,
  `stop`, `resize`, …),
- one **attach** connection per active terminal (channel `0x01` both ways),
- an **event/approval** connection that issues `approval_subscribe` and reads
  push notifications.

Each connection handshakes, answers the PoP `auth_challenge` with an `auth_proof`
signed by the device key, and presents the client token from the Keychain (B.2).
There is **no local PTY** on iOS: the app renders channel `0x01` bytes through
`GhosttyTerminalState` and sends keystrokes back as channel `0x01` bytes — exactly
what `HandleConnection`'s attach path expects. The **MCP channel** (assigned
dynamically by `mcp_connect`, not a fixed channel number) is simply never opened
by the app — the iOS client does not proxy MCP.

#### C.3 Full interactive attach

Full attach mirrors `gr attach`, and is the largest single body of iOS work:

- **Render**: attach → stream channel `0x01` → `GhosttyTerminalState.write` →
  Metal renderer (60 Hz coalesced redraw, same model as gui-poc).
- **Input (a rewrite, not a port)**: a `BaseTerminalUIView` (`UIView` +
  `UIKeyInput`/`UITextInput` for hardware keyboards & IME marked text;
  `pressesBegan`/`pressesEnded` for modifiers; gestures for selection & scroll).
  This does **not** carry over from `BaseTerminalNSView` — `NSTextInputClient`
  and `UITextInput` are different protocols. Budget it as new code (~800 lines).
- **On-screen key accessory row**: iOS soft keyboards lack esc/ctrl/alt/arrows/
  tab; an input-accessory bar (Blink/a-Shell style) supplies them plus sticky
  `ctrl`/`alt`. Essential for real attach on a phone.
- **Resize**: no `TIOCSWINSZ` (that's for local PTYs). The app computes cols/rows
  from the view + cell metrics and sends a `resize` **control message**; the
  daemon resizes the real PTY. (The `resize` handler already exists,
  `handler.go:965-975`.)
- **Reattach / backgrounding**: on data-channel EOF (session stopped / attach
  kicked), show a detached overlay with reattach. Design the
  "detached-while-backgrounded" and reconnect UX early (iOS suspends sockets when
  backgrounded).

#### C.4 Multiple daemons on multiple hosts

A `HostRegistry` (persisted; tokens in Keychain) holds one entry per daemon:
`{label, magicdns_name, port, client_token, device_key, tls_pin, profile,
last_seen}`. Note **`profile`**: the handshake carries a profile and the daemon
rejects on mismatch (`handler.go:110-137`), so the host entry must record the
daemon's profile (encoded in the pairing exchange / QR). The app:

- **Adds hosts manually** (v1): enter a MagicDNS name, pair, done. Tailscale
  **peer auto-discovery** (enumerating peers advertising `tag:graith`) is
  deferred — a sandboxed iOS app can't enumerate tailnet peers without the
  Tailscale SDK.
- **Aggregates sessions across hosts**: the sidebar groups by host → repo →
  session tree, reusing gui-poc's hierarchical layout with a host tier on top.
  Session IDs are **per-daemon unique, not global**, so the UI/state model
  namespaces by `host/session` (see Open Questions for cross-host feeds).
- **Remote `create` has no local cwd.** Unlike the CLI, the app can't pass the
  current directory. `create` uses a **repo picker** populated from the daemon
  (configured repo list / recent repos), not a free-text path field.

#### C.5 Tailscale on the device (v1)

v1 **relies on the official Tailscale iOS app** providing the tunnel: once the
phone is on the tailnet, the system resolver serves MagicDNS app-wide, so
`NWConnection` to `graith-a.ts.net:4823` works. The app must not *assume* the
tunnel is up — surface a clear "not connected to tailnet" state and, optionally,
a Shortcuts hand-off to bring Tailscale up. Embedding the NetworkExtension is a
future enhancement (Non-Goals).

#### C.6 Approvals on mobile — subscribe, don't rely on attach

The approval queue is the highest-value mobile use case, and the current push
path does **not** serve it as-is: `broadcastApprovalNotification`
(`approvals.go:147-168`) fans notifications only to **attached** clients
(`sm.attachedClients`, populated by `SetAttachedClient` during `attach`).
**Connected ≠ attached** — an app browsing the multi-host sidebar would receive
nothing unless it attached to a session (kicking the desktop, per single-attach).

Fix: add an **`approval_subscribe`** control message that registers a
`sendControl` callback in a **separate subscriber registry** (no PTY attach, no
desktop kick). Approval notifications broadcast to attached clients *and*
subscribers. The app subscribes on its event connection (C.2), shows a native
prompt with the tool + input, and sends `approval_respond`. Passive fleet
overview can also use `screen_snapshot`/`screen_preview` (non-attaching) to peek
without kicking a desktop attach.

**Background push (APNs)** remains out of v1 (needs a relay). This is the biggest
open product risk — foreground+subscribed approvals work, but "approve while the
app is closed" does not (see Open Questions).

---

### Relationship to #615 (shared substrate — but not "zero client work")

Parts A and B are a general **remote-control substrate**, not iOS-specific. #615's
`gr attach --remote <host>/<session>` is the *same idea* in the Go client. It
reuses the tailnet listener + auth with **no new server-side protocol**, but it is
**not** "point the existing client at a remote endpoint" for free — `internal/
client/client.go` is local-daemon-specific today: `New` always calls
`EnsureDaemon` (auto-starts the local daemon), the version-mismatch path
auto-upgrades and `probeDaemonVersion` dials the **Unix socket**, and the
handshake sends the local `Profile` (rejected on mismatch). #615 needs a **remote
client constructor** that skips local auto-start/upgrade, dials a tailnet
endpoint, loads a paired client token, applies TLS/pinning, and handles the
remote daemon's profile. Real but bounded client work; **zero new daemon work**.

## Security & Constraints

- **Fail closed at the boundary.** Empty/invalid token on a remote connection ⇒
  `roleNone` ⇒ only `handshake`/`pair_request`, enforced before dispatch. Remote
  surface off by default. `interface` mode binds only to the resolved tailnet IP
  or does not start (never wildcard).
- **Two independent gates + per-message audit.** WhoIs identity *and* out-of-band
  local pairing, plus the B.4 matrix. Neither gate alone grants access; the
  matrix ensures no handler path silently inherits socket trust.
- **Revocable, identity-bound, least-privilege credentials.** Per-device tokens,
  hashed at rest, bound to WhoIs identity + device key, revocable
  (`gr pair revoke`) — and revocation **force-closes live connections** for that
  device.
- **Attack surface acknowledged:** social-engineering the local human into
  approving a rogue device; theft of the device + Keychain (mitigated by revoke +
  identity binding); tsnet auth-key leakage (attacker can *become* a daemon node
  but still must pair to act as a client to *other* daemons); supply-chain on the
  `libghostty-vt` xcframework build (mitigated by SHA-pinned, scripted,
  reproducible builds).
- **Single-attach is double-edged.** Takeover is real (`KickAttachedClient` +
  `SetAttachedClient`, `handler.go:281-295`): an iOS attach kicks a desktop attach
  and vice versa. Beyond the UX note ("make takeover legible"), a
  misbehaving/compromised paired device can *repeatedly* evict a local user — an
  attention-DoS. Surface takeover as an audit event and consider rate-limiting
  reattach. The iPad **split-screen / two-pane** UI must not show the same session
  twice (it would fight itself over the single attach).
- **No new trust on the local socket.** Local Unix-socket behaviour and rights are
  byte-for-byte unchanged when `[remote].enabled = false`; when enabled, local is
  `roleLocalHuman` and behaves exactly as today. (Note this is a *superset* story,
  not "byte-for-byte unchanged while also granting remote humans the same
  rights" — remote humans get the explicit B.4 subset, not local-only ops.)

## Testing Strategy

- **Substrate-first, Go-client-driven.** Before any Swift work, point the existing
  `gr` client at the tailnet listener and exercise the full message set. This
  validates Parts A+B without iOS in the loop.
- **Auth matrix tests (one per B.4 row).** Explicit tests that: a remote
  empty-token conn is denied every message except `handshake`/`pair_request`; a
  paired remote human can attach/list/approve but **cannot** `upgrade`/`reload`/
  `pair_approve`; a session token keeps self/descendant limits; `msg_pub` sender
  is forced and unauthenticated spoofing is rejected; `scenario_*` denies
  `roleNone`; a **revoked token force-closes an active attach**; a WhoIs mismatch
  rejects an otherwise-valid token.
- **Origin injection over TCP.** tsnet is awkward in CI; test the boundary logic
  by running the handler over a plain TCP listener with an injected
  `ConnOrigin{Remote: true, Identity: …}`, so the auth/matrix logic is covered
  without a real tailnet.
- **Local-unchanged regression.** Assert Unix-socket behaviour is identical with
  `[remote]` enabled and disabled.

## Phasing

1. **Substrate (server):** `[remote]` config + validation, tsnet + interface
   listeners, `ConnOrigin` threading, WhoIs gate, the role/matrix refactor
   (B.3–B.4), pairing messages/state + `gr pair`, `approval_subscribe`,
   revocation-closes-connections, audit logging. Verified with the Go client and
   the auth-matrix tests above. **Largest risk lives here.**
2. **Shared Swift package + `GraithProtocolClient`:** extract `GraithTerminalCore`
   from gui-poc; build the transport-abstract protocol client (Unix socket +
   `NWConnection`/TLS); build the SHA-pinned `libghostty-vt` `.xcframework`
   (feasibility spike with acceptance test).
3. **macOS transport swap (early, low-risk):** point the *existing* gui-poc
   renderer/`NSView` at `GraithProtocolClient` over the local Unix socket instead
   of fork/exec + `gr list --json`. The macOS terminal already works, so this is a
   contained change that validates the shared client on a known-good renderer and
   unblocks App Sandboxing. Then add `HostRegistry` + remote hosts to macOS.
4. **iOS read + approvals (first mobile milestone):** `HostRegistry`, pairing UX,
   multi-host aggregated sidebar, log/`screen_snapshot` peek, `approval_subscribe`
   prompts. **No attach, no desktop kick** — ships mobile value early, defers the
   heavy UIKit input work. Reuses the SwiftUI shell now proven on macOS.
5. **iOS full attach:** `BaseTerminalUIView` (Metal render + `UITextInput`/IME
   rewrite), on-screen key row, resize-over-control-message, reattach/backgrounding
   UX.
6. **#615:** the remote Go client constructor onto the same substrate.

The interaction *goal* is full attach (Goals); the *shipping boundary* puts the
macOS transport swap (step 3) and iOS read+approvals (step 4) first because iOS
full attach (step 5) is the largest and riskiest piece. Doing macOS first is
deliberate: it exercises the shared protocol client against gui-poc's
already-working renderer before the iOS input rewrite lands.

## Alternatives Considered

- **Web app / PWA (original #628).** Rejected per direction: a native app gives
  real Ghostty rendering, hardware-keyboard/IME fidelity, Keychain, and a proper
  installable mobile experience.
- **New REST/JSON or gRPC mapping over the protocol.** Rejected: interactive
  attach is bidirectional raw-byte streaming; the framed protocol models it
  exactly, and a REST layer would need a parallel streaming path anyway. Reusing
  the protocol also makes #615's server side free.
- **A single multiplexed connection per host.** Rejected: the handler enters
  blocking per-operation read loops (`logs -f`, `msg_sub`, `approval_request`,
  `mcp_connect`), so one connection can't concurrently carry control RPCs, attach,
  and event subscription. Multiple connections per host is simpler and matches the
  handler as built (C.2).
- **Trust the tailnet alone (WhoIs, no pairing).** Available via
  `require_pairing = false`, but not the default and marked unsafe: device pairing
  adds a revocable, out-of-band, identity-bound gate so a single compromised
  tailnet identity isn't game over.
- **`interface`-only (require host tailscaled).** Rejected as the sole mode; tsnet
  makes the daemon self-contained. `interface` kept as an option.
- **Collapse local + remote humans into one role.** Rejected: local-only ops
  (`upgrade`, `reload`, `pair_approve`) must stay off the network even for trusted
  paired devices, so `roleLocalHuman` and `roleRemoteHuman` are distinct.
- **Adopt a higher-level libghostty terminal surface (e.g. Termini) instead of
  porting gui-poc.** Rejected for v1: porting the proven gui-poc renderer core
  keeps control of input/selection/scroll; revisit if maintaining the hand-written
  renderer becomes a burden.

## Open Questions

- **Background approval push (value risk, not just a feature gap).** APNs needs a
  push provider holding the app's key; the daemon can't reach APNs directly.
  Foreground+subscribed approvals ship in v1, but "approve while the app is
  closed" — arguably the headline use case — does not. Options: a small
  first-party push relay, a Tailscale-hosted relay, or NSE background connections.
  Is a relay in scope, and who operates it?
- **tsnet provisioning UX.** Auth key vs OAuth client vs interactive login URL in
  `daemon.log` on first run — least-friction default for standing up a daemon?
- **Proof-of-possession vs bearer token.** v1 keeps device-key signing; confirm
  the added complexity is worth it vs. a bearer token bound to WhoIs identity
  alone.
- **Cross-host feeds.** Session IDs are per-daemon; a unified "needs attention"
  feed or deep links need a `host/session` composite identity and possibly a
  cross-host event model. In scope for v1 or later?
- **libghostty-vt iOS build provenance.** Scripted, SHA-pinned `.xcframework`
  (macOS + iOS + simulator) is a prerequisite before either front-end ships.
- **App Store vs developer distribution.** TestFlight/ad-hoc for v1; App Store
  review implications of a terminal + (future) network-extension are unresolved.
- **Universal packaging: SwiftUI multiplatform vs Mac Catalyst.** Recommendation
  is native multiplatform (keep gui-poc's AppKit terminal on macOS, add a UIKit
  one for iOS behind a common protocol). Catalyst would let one `UITextInput`
  view run everywhere (single input layer, less code) at some cost to macOS
  native feel and by discarding gui-poc's working `NSView`. Confirm before
  committing — it affects how much of gui-poc's input code survives.

  > **Decision (apple-macos track, 2026-07-08): native SwiftUI multiplatform,
  > not Catalyst.** Now empirically supported by the Phase 2/3 work. The shared
  > `GraithTerminalCore` (Task 15) already carries the entire renderer
  > (`GhosttyTerminalState` + the CoreText/Metal renderer) cross-platform behind
  > `PlatformFont`/`#if os()`, and the transport swap (Task 16) removed the last
  > macOS-only escape hatch (fork/exec). So the *only* platform-divergent code is
  > the terminal input view — `BaseTerminalNSView`/`NSTextInputClient` (retained,
  > working) on macOS and `BaseTerminalUIView`/`UITextInput` (Task 20) on iOS —
  > behind one common protocol. That is exactly the shape multiplatform models
  > best, and it preserves gui-poc's proven `NSView` keycode/IME path (which
  > Catalyst would discard for a weaker `UITextInput`-on-Mac). One SwiftPM
  > package, two thin input views. No reason to pay Catalyst's macOS-fidelity
  > cost.
- **macOS App Sandbox scope.** Dropping fork/exec makes sandboxing feasible;
  reaching the local daemon's Unix socket from a sandboxed app still needs a
  decision (security-scoped bookmark, a shared group container for the socket
  path, or connecting over the loopback/tailnet endpoint instead).

  > **Investigation + decision (apple-macos track, 2026-07-08).** With fork/exec
  > gone (Task 16), the only remaining sandbox obstacle is socket reachability.
  > The daemon's socket lives at `~/Library/Application Support/<appName>/
  > graith.sock`. **Under App Sandbox, `~/Library/Application Support` is
  > redirected into the app's private container**, so a sandboxed app literally
  > cannot see the daemon's socket at the real path. The three options are not
  > equal:
  > - *(a) security-scoped bookmark to the socket* — the user would have to grant
  >   access to a socket file via an open-panel; awkward UX and fragile.
  > - *(b) shared App Group container* — clean, but the socket would have to live
  >   in the group container, which requires a **daemon-side change** to relocate
  >   its socket (and the daemon is neither sandboxed nor a member of the app
  >   group). Out of scope for the app track; a daemon decision.
  > - *(c) connect over a loopback/tailnet endpoint* — the sandboxed app uses the
  >   **same `.remote` transport** the client already has (Task 14), with only
  >   `com.apple.security.network.client`. No socket-in-sandbox problem at all.
  >
  > **Decision:** v1 ships the macOS app **unsandboxed** (as gui-poc did), which
  > keeps the fast, zero-config `.unix(path:)` local transport. **If/when
  > sandboxing is required, use (c)** — a loopback endpoint + network-client
  > entitlement — not (a)/(b). Concretely: the `.unix` local transport is only
  > viable for an *unsandboxed* macOS build; a sandboxed build must switch that
  > host's transport to `.remote` against a daemon-exposed loopback listener.
  > (This also means the App-Sandbox path reuses the exact remote transport iOS
  > uses, so there is no third code path.)
