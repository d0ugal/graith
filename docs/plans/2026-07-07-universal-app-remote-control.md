# Universal App + Tailscale Remote Control — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `graithd` an optional, off-by-default network control surface over
Tailscale with a fail-closed auth model, then build a universal (iOS + macOS)
Swift app that drives sessions over it — reusing the `gui-poc` renderer core and
the existing framed protocol. Design doc:
`docs/design/2026-07-07-native-ios-app-design.md`.

**Architecture:** The daemon already dispatches any `net.Listener` through
`HandleConnection`. We add a second, tailnet-facing listener; the real work is
**auth**, because the daemon currently trusts the `0700` Unix socket ("empty
token ⇒ human"). We introduce a connection **origin** and a **role** model,
audit every handler path against an explicit matrix, and add **device pairing**
with proof-of-possession. The Swift side becomes a transport-abstract protocol
client (Unix socket locally, `NWConnection`+TLS remotely) shared by both
platforms.

**Tech Stack:** Go (daemon, Cobra CLI, `tailscale.com/tsnet`), the graith framed
protocol, Swift/SwiftUI (universal app), `libghostty-vt` (C/Zig via an
`.xcframework`), Metal, Network.framework.

**Two-gate ordering (critical).** The pairing table and role model must exist
*before* the auth matrix can be tested with real remote-human states, so Phase 1
is sequenced pairing → role → matrix → **listener**. The security guarantee is
split into two gates:
- **Gate A (before the network listener lands, Task 9):** a remote connection
  with no valid credential (`roleNone`) can reach *only* `handshake`,
  `pair_request`, and the PoP challenge exchange — enforced at the connection
  boundary, proven by the matrix test.
- **Gate B (Phase 1 exit):** the full per-message matrix over every role
  (including `roleRemoteHuman`/`roleRemoteGuest`) is green, exercised via the TCP
  origin-injection harness.

**Guardrails for every task:**
- Build with `go build -o "$TMPDIR/gr-build" ./cmd/graith` (plain
  `go build ./cmd/graith` writes `./graith` into the cwd and **fails in a
  read-only/shared worktree**; `make build` writes `./gr`).
- Run **targeted package tests** for the task's package(s) (e.g.
  `go test -race ./internal/daemon/...`). Run the **full** `go test -race ./...`
  suite at **phase boundaries**, not after every task (it is slow and noisy).
- Nothing in Phase 1 may change behaviour when `[remote].enabled = false`. Task 12
  adds a regression asserting equivalent Unix-socket behaviour enabled vs disabled.
- Run `gofmt -w` on touched files (CI fails on gofmt).
- Fixture strings use Scots words (see AGENTS.md): hosts `ben`/`brae`, devices
  `bairn`/`skelf`, users `speir@…`, persisted-state fixtures `bide`.

---

## Phase 1 — Daemon network + auth substrate (Go, TDD)

### Task 1: `[remote]` config block + validation

**Files:** `internal/config/config.go`, `internal/config/config_test.go`,
`config.sample.toml`

- [ ] **Step 1: Tests first.** `TestRemoteConfigValidation`:
  - defaults: `Enabled=false`, `Mode="tsnet"`, `Port=4823`, `RequirePairing=true`.
  - `mode="wheesht"` → error (enum).
  - `enabled=true, mode="interface"` with tsnet-only fields (`auth_key_file`,
    `tags`) set → error ("tsnet-only field in interface mode").
  - `pair_request_rate` parses ("5/min"); garbage rejected.
  - `allow_tailnet_users` accepts user emails and `tag:` entries; document that a
    bare `tag:` entry opts tagged nodes in (default: tagged nodes disallowed).
- [ ] **Step 2:** Add `RemoteConfig{Enabled, Mode, Hostname, Port, AuthKeyFile,
  Tags, AllowTailnetUsers, RequirePairing, PairRequestRate}` and a `Validate()`
  called from `config.Load`. Validation is **static** and fail-closed (an invalid
  block is a hard load error). **Distinguish** this from *runtime* remote-listener
  provisioning failures (missing tailnet IP, cert issues) which are handled in
  Task 9, not here.
- [ ] **Step 3:** Document the block (commented, disabled) in `config.sample.toml`,
  including the `require_pairing=false` warning (unsafe; read-only — Task 6).

**Verify:** `go test ./internal/config/...`.

### Task 2: `ConnOrigin` + thread it through the handler

**Files:** `internal/daemon/origin.go` (new), `internal/daemon/handler.go`,
`internal/daemon/daemon.go`, handler tests

- [ ] **Step 1: Tests first.** Drive a control message over `net.Pipe()` with
  `ConnOrigin{Remote:false}`; assert current behaviour is unchanged (local
  baseline).
- [ ] **Step 2:** Define:

  ```go
  // internal/daemon/origin.go
  type TailnetIdentity struct {
      User string   // stable Tailscale user id (not display name); "" for tagged nodes
      Node string   // stable node id
      Tags []string
  }
  type ConnOrigin struct {
      Remote   bool
      Identity *TailnetIdentity // set for remote conns only
  }
  ```
- [ ] **Step 3:** Change the signature to
  `HandleConnection(ctx, conn, origin ConnOrigin, sm, log)` and thread `origin`
  to `resolveAuth`. The Unix-socket server passes `ConnOrigin{Remote:false}`.
- [ ] **Step 4:** Update every call site + the `NewServer` handler closure.

**Verify:** `go test -race ./internal/daemon/...` (baseline unchanged).

### Task 3: Pairing + proof-of-possession + repo-list protocol messages

**Files:** `internal/protocol/messages.go`, `internal/protocol/messages_test.go`

- [ ] **Step 1: Tests first.** Round-trip encode/decode for every new message.
- [ ] **Step 2:** Add messages:
  - Pairing: `PairRequestMsg{DeviceLabel, DevicePubKey}`,
    `PairResponseMsg{DeviceID, ClientToken, DaemonProfile, TLSPinSPKI}`,
    `PairApproveMsg{RequestID}`, `PairListMsg{}`, `PairListResponseMsg{...}`,
    `PairRevokeMsg{DeviceID}`.
  - **Proof-of-possession challenge/response** (the wire mechanism the design
    B.2.4 requires — without it, auth degrades to bearer). The daemon issues a
    challenge on every remote connection *before* privileged messages are
    accepted:
    - `AuthChallengeMsg{Nonce}` (daemon → client, right after handshake on remote
      conns),
    - `AuthProofMsg{DeviceID, Signature}` (client → daemon; signs `Nonce` with the
      device private key).
    A remote connection stays `roleNone` until a valid `AuthProofMsg` is received;
    unsigned/mis-signed proofs keep it `roleNone`. (If a later decision drops PoP
    for v1, delete these two messages and treat the token as bearer-bound-to-WhoIs
    — but the design must be updated in lockstep.)
  - `ApprovalSubscribeMsg{}`.
  - Remote create support: `RepoListMsg{}` and
    `RepoListResponseMsg{Repos []RepoEntry}` (design C.4 — the app can't pass a
    local cwd, so it fetches the daemon's configured/recent repos).

**Verify:** `go test ./internal/protocol/...`.

### Task 4: Paired-device state + pairing handlers + PoP + revocation-closes-conns

**Files:** `internal/daemon/state.go` (schema bump 12 → 13 + migration),
`internal/daemon/pairing.go` (new), `internal/daemon/pairing_test.go` (new),
`internal/daemon/handler.go` (pairing/PoP cases),
`internal/daemon/daemon.go` (connection registry by device)

- [ ] **Step 1: Tests first** (`pairing_test.go`):
  - `pair_request` from an allowed WhoIs identity queues a **pending** pairing
    (pending queue is **in-memory**, does not survive restart); `pair_approve`
    (local) mints a token; that token then resolves to a valid device.
  - `pair_request` is rate-limited, pending-queue-capped, and expires.
  - `pair_approve`/`pair_revoke`/`pair_list` from `roleRemoteHuman` are
    **rejected** (local-only) — including `pair_list` (no remote device
    enumeration).
  - **PoP:** a connection presenting a valid token but a **bad `AuthProofMsg`
    signature** stays `roleNone`; a correct signature over the issued nonce
    authenticates.
  - **Migration:** an old `state.json` at version 12 with no `PairedDevices`
    field loads and round-trips (Scots fixture `bide`).
  - **Revocation drops live connections on the real path:** attach a device
    (registered in `connsByDevice` at first successful auth), then `pair_revoke`;
    assert the connection is force-closed **and** channel-`0x01` keystrokes stop
    being delivered (exercise the real `IsAttachedClient` + data path, not just a
    fake registry entry), and the attached-client + subscriber entries are cleared.
- [ ] **Step 2:** Add `PairedDevice{ID, Label, PubKey, TailnetUser, TailnetNode,
  TokenHash, CreatedAt, LastSeenAt}` to state; bump `CurrentStateVersion` 12 → 13
  with a migration adding an empty `PairedDevices` map. Tokens stored as an
  **HMAC-SHA256** keyed hash; **persist the HMAC key** in state (generate once).
  Compare in constant time.
- [ ] **Step 3:** Implement handlers `pair_request`, `pair_approve`, `pair_list`,
  `pair_revoke`, `auth_proof`. PoP verifies `AuthProofMsg.Signature` against the
  device `PubKey` over the issued nonce.
- [ ] **Step 4:** Add a **connection registry keyed by device**
  (`connsByDevice map[deviceID][]net.Conn`). **Registration point:** on the
  *first* successful `roleRemoteHuman`/`roleRemoteGuest` auth for a connection
  (role is stable per connection), removed on connection close. `pair_revoke`
  closes all registered conns for the device and clears their attached-client /
  approval-subscriber entries.
- [ ] **Step 5:** Audit-log (slog) pair request/approve/revoke and remote
  attach/create/approval decisions with **device ID + WhoIs identity**.

**Verify:** `go test -race ./internal/daemon/... -run 'Pair|Migrat|Revoc'`.

### Task 5: Role model — origin-aware, PoP-aware

**Files:** `internal/daemon/auth.go`, `internal/daemon/auth_test.go`

*(Now unblocked: the paired-device table from Task 4 exists.)*

- [ ] **Step 1: Tests first** — role resolution table:
  - local socket, empty token → `roleLocalHuman`.
  - remote, empty token / no PoP → `roleNone`.
  - remote, valid token + valid PoP + WhoIs matches record → `roleRemoteHuman`
    (or `roleRemoteGuest` if the device was paired under `require_pairing=false`).
  - remote, valid token, WhoIs **mismatch** vs stored record → `roleNone`.
  - any origin, valid session token → `roleSession`; orchestrator's →
    `roleOrchestrator`.
- [ ] **Step 2:** Introduce:

  ```go
  type authRole int
  const (
      roleNone authRole = iota
      roleLocalHuman
      roleRemoteHuman
      roleRemoteGuest // paired under require_pairing=false: READ-ONLY subset
      roleSession
      roleOrchestrator
  )
  type authContext struct {
      role      authRole
      sessionID string      // set only for roleSession/roleOrchestrator
      deviceID  string      // set only for roleRemote*
      origin    ConnOrigin
  }
  func (ac authContext) isHuman() bool      // roleLocalHuman || roleRemoteHuman
  func (ac authContext) isLocalHuman() bool // roleLocalHuman
  ```
- [ ] **Step 3:** Rewrite `resolveAuth(sm, token, origin, connState)` to return the
  role, using the Task 4 table (constant-time hash compare + WhoIs binding + PoP
  state on the connection). **Note:** human roles (`roleLocalHuman`/
  `roleRemoteHuman`) carry an **empty `sessionID`** and are *not* subject to the
  session self/descendant checks — they are the fleet operator. Every existing
  site that read `sessionID == ""` as "unauthenticated human" must switch to
  `ac.isHuman()` / `ac.role` (audited in Task 6).

**Verify:** `go test ./internal/daemon/ -run Auth`.

### Task 6: Exhaustive per-message authorization matrix + Gate A boundary

**Files:** `internal/daemon/handler.go`, `internal/daemon/auth.go`
(`checkMsgPub`, `checkScenarioOp`), `internal/daemon/auth_matrix_test.go` (new)

This is the security core. The matrix must cover **every** `case` in the
`HandleConnection` switch — not a curated subset.

- [ ] **Step 1: Enumerate every `case`.** Produce the full list from the switch
  (includes `handshake, list, create, fork, migrate, attach, detach, delete, stop,
  rename, update, star, unstar, set_status, resume, restart, logs, msg_pub,
  msg_inbox, msg_sub, msg_ack, msg_topics, msg_conversation, approval_request,
  approval_respond, approval_list, approval_subscribe, type, resize,
  screen_preview, screen_snapshot, reload, status, status_report, diagnostics,
  mcp_connect, scenario_*, upgrade, pair_*, auth_proof`). Encode the required role
  per message as a table in code.
- [ ] **Step 2: Tests first** (`auth_matrix_test.go`): **one row per `case`**, plus
  a **completeness test** that fails if a new `case` is added to the switch
  without a matrix entry (enumerate via a registry the switch is generated from,
  or a reflection/string scan of handled types). Required regression rows:
  - remote `roleNone` denied **everything** except `handshake`, `pair_request`,
    `auth_proof`.
  - remote `roleNone` `msg_pub` rejected (today `checkMsgPub` returns nil).
  - remote `roleNone` `scenario_stop`/`delete`/`add` rejected (today
    `checkScenarioOp` early-returns for unauthenticated).
  - remote `roleNone` `list`/`create`/`approval_list`/`diagnostics` rejected.
  - `roleRemoteHuman` **denied** `upgrade`, `reload`, `pair_approve`,
    `pair_revoke`, `pair_list` (local-only); **allowed** `attach`,
    `approval_respond`, `approval_subscribe`, `list`, `create`, `msg_pub`
    (sender forced).
  - `roleRemoteGuest` (require_pairing=false) allowed **read-only**
    (`list`/`status`/`logs`/`screen_*`/`approval_list`) and **denied** every
    mutating op (`create`/`attach`-write/`stop`/`approval_respond`/`msg_pub`/…).
  - `roleSession` keeps self/descendant limits; `msg_pub` sender forced.
  - `scenario_*`: allowed for humans (local or remote) and for
    orchestrator/descendant sessions; denied `roleNone`/`roleRemoteGuest`. (This
    resolves the design's empty-`sessionID` issue — humans manage scenarios *as
    the operator*, not via the session descendant check.)
  - `diagnostics`: `roleRemoteHuman` receives a **redacted** payload (see Step 5).
- [ ] **Step 3: Gate A boundary.** At the top of the control switch (after
  `resolveAuth`, before the `msg.Type` dispatch): if `origin.Remote && role ==
  roleNone`, allow only `handshake`/`pair_request`/`auth_proof`, else reply
  `error` and continue.
- [ ] **Step 4: Fix the permissive helpers.** `checkMsgPub`: force `SenderID` from
  the authenticated identity for **all** roles; reject `roleNone`. `checkScenarioOp`:
  drop `if !ac.authenticated { return nil }`; require `isHuman()` **or**
  orchestrator/descendant session.
- [ ] **Step 5: Walk every `case`.** Replace `auth.authenticated` / `sessionID==""`
  checks with role checks per the matrix; grep both to confirm none missed (tag
  each audited). Mark `upgrade`/`reload`/`pair_*` `authLocalHumanOnly`. Add a
  `DiagnosticsForRole(role)` that redacts secrets (paths/tokens/config) for
  `roleRemoteHuman`; test it.

**Verify:** `go test -race ./internal/daemon/...`; the matrix + completeness tests
are Gate A.

### Task 7: Daemon TLS (cert lifecycle, SPKI pinning)

**Files:** `internal/daemon/tls.go` (new), `internal/daemon/tls_test.go` (new),
wired into Task 9's listeners

- [ ] **Step 1: Tests first.** Self-signed cert generation is deterministic given
  a stored key; SPKI fingerprint computation is stable across cert renewal (pin
  the **public key / SPKI**, not the leaf); a client pinning the SPKI accepts a
  renewed cert with the same key and rejects a different key.
- [ ] **Step 2:** `interface` mode: generate a self-signed cert on first run
  (persist the key), expose the **SPKI fingerprint** for TOFU. `tsnet` mode: use
  `ts.ListenTLS` (tailnet HTTPS cert) — document the MagicDNS/HTTPS-certs
  precondition; pin SPKI there too. The fingerprint is returned in
  `PairResponseMsg.TLSPinSPKI` and shown by `gr pair` for local confirmation.

**Verify:** `go test ./internal/daemon/ -run TLS`.

### Task 8: `approval_subscribe` (approvals without attach)

**Files:** `internal/daemon/handler.go`, `internal/daemon/approvals.go`,
`internal/daemon/daemon.go`, `internal/daemon/approval_subscribe_test.go` (new)

- [ ] **Step 1: Tests first.** A conn that sends `approval_subscribe` (no `attach`)
  receives `approval_notification` on a new approval; a merely-connected,
  unsubscribed conn does not. Subscriber requires `isHuman()`. On connection close
  the subscriber entry is removed (no dead-callback leak).
- [ ] **Step 2:** Add an `approvalSubscribers` registry; `broadcastApprovalNotification`
  fans out to attached clients **and** subscribers (dedup). Clean up on disconnect
  and on `pair_revoke` (Task 4 Step 4).

**Verify:** `go test -race ./internal/daemon/... -run Approval`.

### Task 9: tsnet + interface listeners; WhoIs; multi-listener lifecycle

*(Lands only after Gate A — Task 6 matrix — is green.)*

**Files:** `internal/daemon/remote.go` (new), `internal/daemon/remote_test.go`
(new), `internal/daemon/daemon.go`, `go.mod`/`go.sum` (add `tailscale.com`)

- [ ] **Step 1: Tests first (TCP origin injection, no real tailnet).** Run
  `HandleConnection` over a plain `net.TCPListener` with an injected
  `ConnOrigin{Remote:true, Identity:…}` and assert the full matrix (Gate B) holds
  end-to-end over TCP. This is the CI strategy — tsnet's gVisor netstack is not
  spun up in CI.
- [ ] **Step 2:** `RemoteListener` interface: `Listen() (net.Listener, error)` +
  `WhoIs(ctx, remoteAddr) (*TailnetIdentity, error)`. Impls:
  - `tsnetListener` — `tsnet.Server{Hostname, AuthKey, …}`; `WhoIs` via
    `ts.LocalClient().WhoIs(ctx, addr)`. On missing auth key, log the login URL
    (record first-run UX).
  - `interfaceListener` — resolve tailnet IP via `tailscale.LocalClient`
    (handle v4/v6, multiple addrs), `net.Listen("tcp", ip:port)`, **never
    `0.0.0.0`**; **fail closed** (runtime error, Unix socket still comes up) if no
    tailnet IP; `WhoIs` via the same LocalClient.
- [ ] **Step 3:** Generalize the daemon to own **a slice of servers**; on start, if
  `Remote.Enabled`, build the listener, wrap each accepted conn with
  `ConnOrigin{Remote:true, Identity: WhoIs(...)}` and the Task 7 TLS, run it
  alongside the Unix server; coordinate shutdown across both.
- [ ] **Step 4:** Fix the self-upgrade path that type-asserts `*net.UnixListener`
  (`daemon.go` ~4779): only the Unix listener fd crosses exec; the remote listener
  re-opens on the new process (remote clients reconnect). **Document** that
  `[remote]` changes require a daemon restart in v1 — a live config watcher exists,
  so this is a deliberate choice, not a limitation we can't lift.
- [ ] **Step 5:** Gate 1 at accept: reject if WhoIs fails or the user isn't in
  `AllowTailnetUsers`; tagged nodes rejected unless a `tag:` entry allows them.
- [ ] **Step 6 (measurement gate):** record the **binary-size delta** from adding
  `tailscale.com/tsnet` and note first-run provisioning UX. Consider a build tag so
  `tsnet` can be compiled out for `interface`-only builds (decision recorded here).

**Verify:** `go build -o "$TMPDIR/gr-build" ./cmd/graith`;
`go test -race ./internal/daemon/...`. Manual: `mode="interface"`, `gr list` over
the tailnet IP.

### Task 10: `gr pair` CLI + `gr doctor` surfacing

**Files:** `internal/cli/pair.go` (new), `internal/cli/pair_test.go` (new),
`internal/cli/doctor.go`, `internal/cli/root.go`

- [ ] **Step 1: Tests first.** `gr pair list` renders pending + paired;
  approve/revoke send the right messages; JSON in agent mode.
- [ ] **Step 2:** Implement `gr pair list|approve <id>|revoke <id>` over the
  **local socket** (local-only ops). Show the TLS SPKI fingerprint for TOFU
  confirmation on approve.
- [ ] **Step 3:** `gr doctor` reports remote enabled/mode/listen addr, paired
  device count, and WhoIs/cert provisioning problems.

**Verify:** `go test ./internal/cli/... -run Pair`.

### Task 11: Remote `create` repo-list handler

**Files:** `internal/daemon/handler.go` (`repo_list` case),
`internal/daemon/handler_test.go`

- [ ] **Step 1: Tests first.** `repo_list` from `isHuman()` returns configured /
  recent repos; denied `roleNone`/`roleRemoteGuest`-mutation semantics respected.
- [ ] **Step 2:** Implement `repo_list` returning the daemon's known repos so a
  remote client can offer a picker instead of a raw path.

**Verify:** `go test ./internal/daemon/... -run Repo`.

### Task 12: Local-unchanged regression + integration (Gate B / phase exit)

**Files:** `internal/integration/remote_test.go` (new)

- [ ] **Step 1:** Spawn a real daemon with `[remote]` **disabled**; assert the
  existing `gr` flow (create/list/attach/stop) behaves **equivalently** to today.
  Assert *behavioral* equivalence (session set, statuses, attach I/O), **not**
  byte-for-byte transcripts (logs/timestamps drift).
- [ ] **Step 2:** With the TCP origin-injection harness (no tsnet in CI): a remote
  `roleNone` conn is denied everything but handshake/pair_request/auth_proof; a
  paired `roleRemoteHuman` can list/attach/approve/`repo_list` but not
  upgrade/reload/pair; a `roleRemoteGuest` is read-only; `pair_revoke` force-drops
  a live attach.

**Phase 1 exit criteria (Gate B):** the full auth matrix + completeness +
revocation + local-unchanged tests are green **via the TCP origin-injection
harness** (not via the `gr` client, which is Unix-socket-only until Phase 6). The
network listener exists; TLS + pairing + PoP + `approval_subscribe` + `repo_list`
work; nothing changes with the surface disabled.

**Run the full `go test -race ./...` suite now (phase boundary).**

---

## Phase 2 — Shared Swift package + protocol client + libghostty xcframework

**Files (new subtree):** restructure `gui/` into `GraithTerminalCore`,
`GraithProtocol`, and app targets.

### Task 13: `libghostty-vt` iOS + macOS `.xcframework` (feasibility spike)

- [ ] Build `libghostty-vt` for `arm64-apple-macos`, `arm64-apple-ios`, and
  `arm64-apple-ios-simulator`; package as `libghostty-vt.xcframework`, pinned to a
  Ghostty commit SHA; script it (`gui/scripts/build-libghostty.sh`).
- [ ] **Acceptance test:** a SwiftPM test links the framework and renders a known
  VT transcript to a grid snapshot on **both** device and simulator.
- [ ] Replace the unpinned `gui/Libraries/libghostty-vt.a` and the unsafe linker
  flags in `Package.swift`. **Note:** build the macOS slice first so Task 16 (macOS
  swap) is unblocked even if the iOS/simulator slices need extra Zig/libc work.

### Task 14: `GraithProtocolClient` (transport-abstract framed protocol)

- [ ] Swift `FrameReader`/`FrameWriter` for `[channel:1][len:4 BE][payload]`.
- [ ] Control envelope encode/decode `{type, payload, token}`; token from Keychain.
- [ ] **Auth flow:** on remote connections, receive `AuthChallengeMsg`, sign the
  nonce with the device key, send `AuthProofMsg` (PoP) — before privileged RPCs.
- [ ] Transport abstraction: `.unix(path:)` for local hosts; `.hostPort + TLS`
  (SPKI pin) for remote hosts.
- [ ] **Multiple connections per host** (control / attach / event) — the handler's
  blocking read loops (`logs -f`, `msg_sub`, `approval_request`, `mcp_connect`)
  make one multiplexed connection unworkable. Ignore/never-open the dynamically
  assigned MCP channel.
- [ ] **Tests:** encode/decode round-trips + a mock in-process server driving
  handshake → challenge/proof → `list`.

### Task 15: `GraithTerminalCore` extraction

- [ ] Move `GhosttyTerminalState`, `Session` (directly portable) and — ported off
  AppKit — `MetalTerminalRenderer`, `TerminalSearchState`, `Theme` into the shared
  package (`NSFont`/`NSColor`/`.managed` → cross-platform behind `#if os()`).
- [ ] **Tests:** VT write/resize/key-encode unit tests (port gui-poc's).

---

## Phase 3 — macOS transport swap (early, low-risk-*ish*)

*Realistic scope note: this is a substantial refactor of gui-poc, not a one-liner
— budget tests for attach lifecycle, resize, keyboard input, reconnect, and
no-child-process behaviour.*

### Task 16: Point gui-poc's macOS renderer at `GraithProtocolClient`

- [ ] Replace `SessionStore`'s `gr list --json` polling with `GraithProtocolClient`
  `list`/status over the **local Unix socket**.
- [ ] Replace `BaseTerminalNSView`'s `forkpty()` + `execve("gr attach")` with an
  attach connection: stream channel `0x01` into `GhosttyTerminalState`; send
  keystrokes/`resize` back. Remove the PTY/subprocess code.
- [ ] **Acceptance:** the macOS app renders/attaches/types/resizes against the
  local daemon with **no `gr` child processes** (assert programmatically where
  possible, not only via Activity Monitor). Detach/reattach works.

### Task 17: macOS multi-host + App Sandbox + packaging decision

- [ ] Add `HostRegistry` + remote hosts (pairing UX, PoP, SPKI pin) to macOS.
- [ ] Prototype App Sandbox now that fork/exec is gone; decide local-socket access
  (group container / security-scoped bookmark / loopback endpoint) — record in the
  design's Open Questions.
- [ ] **Decision record: SwiftUI multiplatform vs Mac Catalyst** — resolve *before*
  Task 20, since it determines how much gui-poc `NSView` input code survives.

---

## Phase 4 — iOS read + approvals (first mobile milestone)

### Task 18: iOS app shell + pairing

- [ ] Universal SwiftUI shell (host→repo→session tree), `HostRegistry` persisted
  with tokens in Keychain, device keypair generation, "not connected to tailnet"
  state.
- [ ] Pairing: `pair_request` → local human `gr pair approve` → store token + SPKI
  pin + daemon profile; PoP challenge/response on every reconnect.

### Task 19: Read + approvals (no attach)

- [ ] Multi-host aggregated session list; `repo_list`-backed create picker; log
  tail + `screen_snapshot` peek.
- [ ] `approval_subscribe` on the event connection; native prompt →
  `approval_respond`. **No PTY attach, no desktop kick.**
- [ ] **Acceptance:** from a phone on the tailnet, see all sessions across ≥2
  daemons and approve/deny a real tool call without kicking a desktop attach.

---

## Phase 5 — iOS full interactive attach

### Task 20: `BaseTerminalUIView` (the input rewrite)

- [ ] `UIView` + `UIKeyInput`/`UITextInput` (IME marked text),
  `pressesBegan/Ended` modifiers, gestures for selection/scroll; drive shared
  `GhosttyTerminalState` + Metal renderer.
- [ ] On-screen key accessory row (esc/ctrl/alt/arrows/tab + sticky modifiers).
- [ ] Resize via control message; reattach + detached-while-backgrounded UX.
- [ ] iPad split-view must not show the same session twice (single-attach guard).
- [ ] **Acceptance:** full interactive attach on-device incl. IME (US + Japanese),
  vim/less, resize, copy/paste.

---

## Phase 6 — #615 remote CLI attach

### Task 21: Remote Go client constructor

**Files:** `internal/client/client.go`, `internal/cli/attach.go`

- [ ] Add a remote client path that skips `EnsureDaemon`/auto-upgrade, dials a
  tailnet endpoint, loads a paired client token + device key (PoP), applies TLS
  SPKI pinning, and handles the remote daemon's `Profile` (the handshake sends
  `Profile` and the daemon rejects mismatches — `handler.go:110-137`).
- [ ] `gr attach --remote <host>/<session>`: reuse the passthrough loop; store
  remote credentials locally.
- [ ] **Tests:** remote-dial constructor units; integration via the TCP
  origin-injection harness (Task 12).

---

## Risks / watch-items (from design review)

- **Auth completeness + ordering is the whole ballgame.** Pairing/role (Tasks 4–5)
  precede the matrix (Task 6), which is **Gate A** before the listener (Task 9);
  do not reorder. The completeness test prevents new handler cases from silently
  bypassing the matrix.
- **Proof-of-possession needs its wire messages** (Task 3) — otherwise it silently
  degrades to bearer; the design requires PoP for v1.
- **`require_pairing=false` is read-only** (`roleRemoteGuest`, Task 6) or not
  shipped — never full control without pairing.
- **Revocation must drop live connections on the real data path** (Task 4 Step 4).
- **Daemon TLS is a first-class task** (Task 7), SPKI-pinned, not an afterthought.
- **tsnet binary size / cert provisioning** — measured in Task 9 Step 6; possible
  build tag.
- **iOS input is a rewrite, not a port** (Task 20) — why read+approvals ships first.
- **libghostty API stability** — pin the SHA; the xcframework build (Task 13) is a
  spike; build the macOS slice first.
- **macOS transport swap is a real refactor** (Task 16), not a one-liner.
