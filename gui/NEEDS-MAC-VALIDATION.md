# NEEDS MAC VALIDATION (#628 — Apple universal app)

Everything below was implemented and self-reviewed in a headless macOS
environment with **Swift 6.3.2 + Xcode Command Line Tools only** (no full
Xcode, no iOS SDK, no device/simulator). Each item needs a human on a Mac with
full Xcode to build/run/validate.

Owner of this track: `apple-macos` (Phase 2 Tasks 13–15, Phase 3 Tasks 16–17).

---

## What WAS verified here (real `swift build` / `swift test`)

- `gui/shared` + `gui/macos` SwiftPM packages build: `GraithProtocol`,
  `GraithTerminalCore` (shared) and `GraithGUI` (macOS app, depends on
  `../shared`) all compile — `make -C gui/shared build && make -C gui/macos build`.
- **25 unit tests pass** across 4 suites — `make -C gui/shared test`
  (or `test-clt` on a CLT-only machine):
  - framing (round-trip, chunked, multi-frame, oversize),
  - control envelope + wire-key (snake_case) codable, `SessionInfo`
    reconciliation (drops POC `cost_usd`/`context_percent`, keeps PR/CI/etc.),
  - full **handshake → PoP `auth_challenge`/`auth_proof` → `list`** over an
    in-memory mock daemon, attach data-streaming, daemon-`error` surfacing,
  - libghostty-vt **VT write / resize / key-encode** (Enter→CR, Ctrl+C→ETX,
    printable→UTF-8) — proves the current macOS `libghostty-vt.a` links + works.

### Environment quirks (not bugs — but a full-Xcode Mac won't hit them)

- SwiftPM compiles its `Package.swift` manifest in a nested `sandbox-exec`,
  which fails inside an already-sandboxed environment → we pass
  `--disable-sandbox` (all `build`/`test` Makefile targets pass it).
- Under CLT (no Xcode) the swift-testing framework module + its runtime dylib
  (`lib_TestingInterop.dylib`) aren't on the default search paths, so the
  shared Makefile's `test-clt` target adds `-F`/`-rpath` for
  `/Library/Developer/CommandLineTools/Library/Developer/{Frameworks,usr/lib}`.
  **With full Xcode, plain `swift build` / `swift test` should work and these
  flags are unnecessary.**

---

## Task 13 — libghostty-vt xcframework (BLOCKED here; script written)

`gui/scripts/build-libghostty.sh` is written and its build command is verified
against the real Ghostty tree (it uses Ghostty's own
`zig build -Demit-lib-vt=true -Demit-xcframework=true -Doptimize=ReleaseFast`,
which emits the universal macos+ios+ios-sim xcframework). **Pinned to Ghostty
commit `91f66da24527fa02d92b5fd0b41cd020f553a64c`.**

It could NOT be run to completion here, for two independent reasons:

1. **Zig version scissors.** Ghostty at the pinned SHA requires **Zig 0.15.2**
   (`build.zig.zon minimum_zig_version`). The environment ships **Zig 0.16.0**,
   which Ghostty's `build.zig` rejects (it uses the 0.15.x 3-arg
   `std.Io.Dir.readFileAlloc`; 0.16 made it 4-arg). Downloading the exact
   Zig 0.15.2 and retrying got *much* further (it compiled the build runner)
   but then **failed to link `libSystem`** (`undefined symbol: _fork`,
   `_getcwd`, `_malloc_size`, …): Zig 0.15.2 predates the **macOS 26 SDK** in
   this environment and can't resolve its libSystem stub. Net: no single Zig
   here satisfies both Ghostty (needs 0.15.2) and the macOS 26 SDK (needs newer).
2. **`xcodebuild -create-xcframework` needs full Xcode.** The CLT `xcodebuild`
   errors: *"tool 'xcodebuild' requires Xcode"*. The iOS + iOS-simulator SDKs
   are also Xcode-only.

**To finish on a Mac with full Xcode:**
- Install Zig 0.15.2 (`https://ziglang.org/download/0.15.2/`).
- `sudo xcode-select -s /Applications/Xcode.app/Contents/Developer`.
- Run `gui/scripts/build-libghostty.sh` → produces
  `gui/Libraries/libghostty-vt.xcframework`.
- Then switch `Package.swift`'s `CGhosttyVT` from the current
  `.unsafeFlags(["-LLibraries"]) + .linkedLibrary("ghostty-vt")` (unpinned
  macOS-only `.a`) to a **`binaryTarget`** on the `.xcframework`, and drop the
  unsafe linker flags. Re-run `swift-test.sh` to confirm the VT tests still pass
  against the pinned artifact, on **both** device and simulator (the design's
  acceptance test).
- The current `gui/Libraries/libghostty-vt.a` is **unpinned** and macOS-arm64
  only (`lipo -info` = non-fat arm64); it works for the macOS app today but
  cannot link into an iOS target — hence the xcframework is a prerequisite
  before the iOS app can build/run.

---

## Task 14/15 — shared packages (built + tested; validate on device)

- `GraithProtocol` and `GraithTerminalCore` build for the **macOS** slice here.
  They have **not** been compiled for **iOS/simulator** (blocked on Task 13's
  xcframework for `CGhosttyVT`; `GraithProtocol` itself is pure
  Foundation/Network/CryptoKit/Security and should build for iOS once it's a
  consumable product).
- `MetalTerminalRenderer` was ported off AppKit to CoreText + `PlatformFont` and
  `.shared` textures so it compiles cross-platform, but its **rendering output
  on iOS is unvalidated** (no simulator here). Validate glyph metrics, Retina
  scaling, cursor styles, and bold/italic variants on a real iOS device/sim.
- `TLSPinning.leafMatchesSPKI` (SPKI cert pinning for the remote transport) is
  **unvalidated** and has an open dependency: the exact SPKI hashing formula
  must be reconciled with the daemon's TLS task (design Task 7) once it lands,
  so both sides hash identical bytes. The **local Unix-socket transport does
  not use TLS**, so this does not gate macOS v1.
- `NWByteStream`'s **Unix-socket path** (`NWEndpoint.unix` + `.tcp` params) is
  not exercised against a live daemon here. Validate that `NWConnection` to the
  daemon's `0700` Unix socket connects and streams frames on a real Mac.

---

## Protocol / core drift found vs current daemon (per design-628 heads-up)

Reconciled while porting; recorded here for the reviewer:

- `SessionInfo`: the gui-poc had `cost_usd` / `context_percent` which are **not**
  on the wire (`protocol.SessionInfo`) — **dropped**. Added the fields the POC
  lacked: `parent_id`, `exit_signal`, `unpushed_count`, `sandboxed`,
  `shared_worktree`, `in_place`, `yolo`, `model`, `tool_name`, `includes`
  (`IncludedRepoInfo`), `config_stale`, `starred`, `system_kind`, `scenario_id`,
  `scenario_name`, `summary_text`, `summary_faded`, `last_output_at`,
  `migrated_from`, `pull_request` (`PRInfo`), `ci` (`CIInfo`), `agent_status`.
- Canonical `SessionInfo` now lives in **`GraithProtocol`** (a wire concern),
  not `GraithTerminalCore` as the plan's Task 15 text suggested. Agreed with
  design-628. The macOS app's old `Session` model (`Session.swift`) is still
  present and will be replaced by `GraithProtocol.SessionInfo` in Task 16.
- Frame constants verified against `internal/protocol/frame.go`: channels
  `0x00` control / `0x01` data / `0x02` MCP; 5-byte header `[channel][len:4 BE]`;
  `MaxPayload` 4 MiB. The app **never opens channel 0x02** (MCP is
  dynamically assigned by `mcp_connect`; the app doesn't proxy MCP).
- PoP contract verified against `internal/daemon/pairing.go` `verifyPoP`: pubkey
  = base64-std of raw 32-byte ed25519; signature = base64-std of raw 64-byte
  ed25519; nonce is signed **verbatim** (UTF-8 bytes of the challenge string,
  not base64-decoded). Client token stored daemon-side as hex(HMAC-SHA256).
- The daemon issues `auth_challenge` on **remote** connections only; the local
  Unix-socket path gets **no** PoP challenge. `GraithProtocolClient` matches
  this (`if transport.isRemote { … }`).
- `approval_subscribe` handler is not yet landed on design-628 (its Go Task 8);
  the `ApprovalSubscribeMsg` type exists and the client codes against it. Whether
  the daemon sends an initial approval snapshot on subscribe is unconfirmed — the
  client does not synthesize one.

---

## Task 16 — macOS transport swap (built + grep-verified; runtime needs a Mac)

Done at the code level and the package builds; **runtime needs a live daemon**
on a Mac with the GUI running:

- Verify the app resolves the socket path correctly and `list` populates the
  sidebar with **no `gr` child process** (`ps` / Activity Monitor: the app
  spawns nothing; grep confirms no `forkpty`/`execve`/`Process()`/`gr` remain).
- Verify **attach** renders a session, **types** (incl. IME), **resizes**
  (control message → daemon resizes PTY), scrolls, and copy/paste works — all
  over the Unix-socket `GraithProtocolClient`, no PTY.
- Verify **detach/EOF/kick** finishes the output stream → detached overlay →
  **reattach** re-opens the attach connection.
- Verify the socket-path resolver matches the running daemon's actual socket
  (`~/Library/Application Support/<appName>/graith.sock`, honoring
  `XDG_RUNTIME_DIR`/`GRAITH_PROFILE`). If a profile is in use, confirm the app
  reads `GRAITH_PROFILE` from its environment (GUI apps don't inherit the shell
  env — may need a config or launch-arg).
- `NWConnection` to a Unix `NWEndpoint` with `.tcp` parameters is **unvalidated**
  against a real socket here — this is the single most important thing to
  confirm on a Mac. If it doesn't connect, fall back to a POSIX `AF_UNIX` socket
  wrapped in `DispatchIO` behind the same `ByteStream` protocol (the transport
  is abstracted for exactly this reason).

## Task 17 — multi-host + App Sandbox + packaging decision

- **Decisions recorded** in the design doc's Open Questions: (1) native SwiftUI
  multiplatform over Mac Catalyst; (2) macOS App Sandbox — v1 unsandboxed with
  the `.unix` local transport, and if sandboxing is later required, switch the
  local host to a `.remote` loopback endpoint (the `.unix` path is not reachable
  from inside an App Sandbox container). See that section for the full rationale.
- `HostRegistry.swift` is the **macOS foundation** (host model, persistence,
  per-host client factory). **Remaining Mac work** (large; intended to unify
  with the iOS track's shared `HostRegistry` at the Phase 2 merge):
  - host-tier sidebar UI (host → repo → session) and a host picker/add sheet;
  - the pairing flow for remote hosts (`pair_request` → local `gr pair approve`
    → persist token + SPKI pin + daemon profile);
  - a Keychain-backed `DeviceKeySigner` (ed25519) for remote PoP — currently the
    signer is an injection point, not implemented on the macOS side;
  - moving client tokens out of the `hosts.json` Codable into the Keychain
    (`clientTokenRef` is a placeholder reference key today).
