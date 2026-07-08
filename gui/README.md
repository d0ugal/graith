# graith universal app (`gui/`) — iOS + macOS (#628)

A native **universal** app (iOS + macOS) that connects to one or more graith
daemons **over Tailscale** and drives their sessions: a multi-host session
sidebar, a Ghostty-backed terminal, approvals, and device pairing. Built on the
same daemon, protocol, and auth substrate as the `gr` CLI — no web surface.

See the design doc: [`docs/design/2026-07-07-native-ios-app-design.md`](../docs/design/2026-07-07-native-ios-app-design.md)
and the implementation plan: [`docs/plans/2026-07-07-universal-app-remote-control.md`](../docs/plans/2026-07-07-universal-app-remote-control.md).

## Layout

Three sibling SwiftPM packages, each with its own `Makefile`; the top-level
`gui/Makefile` aggregates and delegates (`make <pkg>-<target>`, e.g. `make
macos-run`).

```
gui/
  shared/   Cross-platform core, consumed by both apps:
              GraithProtocol      transport-abstract framed-protocol client
                                  (TLS + SPKI pinning, PoP, wire messages)
              GraithTerminalCore  libghostty-vt wrapper + Metal renderer + keymap
              CGhosttyVT          C shim over libghostty-vt (Libraries/*.a)
  macos/    GraithGUI — the macOS app (AppKit/SwiftUI + Metal). Depends on ../shared.
  ios/      The iOS app + modules. Depends (soon) on ../shared:
              GraithMobileApp     @main SwiftUI App (the launchable app)
              GraithMobileUI      RootView, sidebar, session detail, pairing, approvals
              GraithMobileKit     device identity, host registry, tailnet reachability, Keychain
              GraithTerminalUIKit UIKit terminal surface (UITextInput/IME, key accessory)
              GraithClientAPI     boundary protocol (to be unified onto ../shared — see below)
              GraithMobileMock    in-memory mocks for previews/tests/dev
```

## Build & run

Full Xcode is required for iOS; point at it via `DEVELOPER_DIR` (defaults to
`/Applications/Xcode.app`). `build`/`smoke` targets are safe inside a graith
session sandbox; `test`/`sim`/`run` shell out to `xcodebuild`/`simctl` and must
run from a normal terminal (or a session with `[sandbox] enabled = false`).

```bash
# macOS app
make -C gui macos-build          # compile
make -C gui macos-run            # launch GraithGUI (needs a display)

# iOS app
make -C gui ios-app              # build + assemble a launchable .app (sandbox-safe)
make -C gui ios-run              # build, install, and LAUNCH on the simulator
make -C gui ios-sim              # just boot + open the Simulator
make -C gui ios DEVICE="iPhone Air" ios-run   # pick a device

# shared core
make -C gui shared-build
make -C gui shared-test          # unit tests (run outside the sandbox; test-clt for CLT-only)

make -C gui build                # all three; `make -C gui help` for everything
```

The iOS app has **no `.xcodeproj`** (and this repo assumes no xcodegen/tuist).
`ios/build-ios-app.sh` builds the SwiftPM product for the `iphonesimulator` SDK
and hand-assembles an ad-hoc-signed `.app`; `simctl` installs + launches it.
For a Keychain-backed, distributable build, sign with a real identity and
`ios/Resources/GraithMobile.entitlements` (see that script's header).

## Connecting over Tailscale

The apps (and `gr remote`) reach a daemon over the tailnet. There is **no
account/password** — trust is Tailscale identity (WhoIs) plus a one-time device
pairing. Off by default; enabling it is fail-closed.

### 1. Enable the remote listener on the host daemon

In `~/.config/graith/config.toml` on the machine you want to reach:

```toml
[remote]
enabled = true
mode = "tsnet"                      # embedded Tailscale node (default); or "interface"
hostname = "graith-ben"             # tsnet node name / MagicDNS label
auth_key_file = "~/.config/graith/ts-authkey"   # tsnet mode only
# tags = ["tag:graith"]             # tsnet ACL tags (optional)
allow_tailnet_users = ["you@example.com"]        # Gate 1: WhoIs allowlist
# pair_request_rate = "5/min"       # anti-abuse on the pre-auth pairing lane
```

- **`mode = "tsnet"`** brings up an embedded Tailscale node (via `tsnet`) that
  joins your tailnet using `auth_key_file`. **`mode = "interface"`** binds to an
  existing `tailscaled`'s tailnet IP instead.
- Restart the daemon (`gr daemon restart`). It now serves a **TLS** listener on
  the tailnet alongside the local Unix socket, sharing the same handler.

### 2. Get the device onto the tailnet

Install the official **Tailscale** app on the iPhone/Mac and log into the same
tailnet. v1 relies on the system tunnel — the app surfaces a "Checking tailnet
connection…" / "not connected to tailnet" banner (the state you see on first
launch) and does not embed its own tunnel.

### 3. Pair the device (one-time, human-approved)

A brand-new device has no token; the only thing it may do after Gate 1 (WhoIs)
is send a `pair_request`. Approval is a deliberate, out-of-band human action on
the host.

```bash
# On the device / client:
gr remote pair graith-ben            # sends a pair_request over the tailnet

# On the host (local human):
gr pair list                         # show pending requests + paired devices
gr pair approve <request-id>         # mint a client token, return the TLS SPKI pin
# gr pair revoke <device-id>         # revoke a device (force-closes its connections)
```

In the app, "Add Host" runs the same flow via the pairing sheet. On approval the
daemon records the device (ed25519 public key + WhoIs user/node + label) and
returns a long-lived, revocable **client token**, stored in the Keychain.

### 4. Connect

Every remote connection then does: `handshake` → daemon `auth_challenge{nonce}`
→ client `auth_proof{device_id, signature}` (ed25519 proof-of-possession, so a
leaked token alone is useless) → authorized. Identity is re-bound to the pairing
record on every connection.

```bash
gr remote list                       # paired hosts
gr remote attach graith-ben/my-session   # attach a session over the tailnet (#615)
```

The app connects to **all** paired hosts and shows their sessions in one
sidebar. Approvals use a non-attaching `approval_subscribe` so the app is
notified without kicking a desktop attach.

## Status & deferred work

Done: shared core (protocol + terminal + tests), macOS app (`GraithGUI`, builds
and runs), iOS app (`GraithMobileApp`, **launches in the simulator** against
real identity/registry/reachability with mock hosts), remote daemon substrate +
auth/pairing, `gr remote`/`gr pair` CLI, the Makefile/build tooling.

Deferred / not yet done (tracked in
[`NEEDS-IOS-VALIDATION.md`](NEEDS-IOS-VALIDATION.md) /
[`NEEDS-MAC-VALIDATION.md`](NEEDS-MAC-VALIDATION.md)):

- **Live terminal render on iOS.** The iOS app is unified onto `../shared`'s real
  `GraithProtocolClient` (connects + drives sessions), but the terminal *render*
  adapter (`GraithMobileRealTerminal`) isn't linked into the app yet — it needs
  the libghostty-vt `.xcframework` below.
- **libghostty-vt `.xcframework`.** `shared/Libraries/libghostty-vt.a` is
  unpinned + macOS-only; `shared/build-libghostty.sh` produces a SHA-pinned
  universal xcframework but needs Zig 0.15.2 + full Xcode (Task 13).
- **Automatic daemon discovery (backlog).** Hosts are added manually by MagicDNS
  name today. Future: Tailscale admin API + a `tag:graith` device tag (cleanest),
  a naming-convention probe (no credential), or daemon-advertised siblings. mDNS
  and reading Tailscale's on-device API are ruled out (see the design Non-Goals).
- **Distributable builds / signing.** The iOS dev bundle is ad-hoc-signed, so it
  falls back to an in-memory device identity (no Keychain). A signed build (real
  identity + entitlements, ideally a proper Xcode project) is needed for
  persistent identity and distribution. The macOS app runs via `swift run` but
  has no packaged `.app`.
- **Simulator XCTest suites** (`make -C gui/ios test`) and running them in CI.
- **APNs background push for approvals** (foreground/subscribed works; background
  is the biggest open product risk).
- **Security review** of the auth/pairing model before it ships.
```
