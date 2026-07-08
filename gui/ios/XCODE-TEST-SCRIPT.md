# graith iOS — Mac/Xcode manual test script (issue #628, Tasks 18–20)

A concrete, ordered validation script for a human on a Mac with **full Xcode**,
an **iOS Simulator**, and (for the terminal/attach parts) a **real device on the
tailnet**. It covers everything that cannot be verified in the CLT-only graith
worktree (see `../NEEDS-IOS-VALIDATION.md` for the rationale and the integration
adapter mapping).

Conventions: **[SIM]** = works in the Simulator; **[DEV]** = needs a physical
device (Keychain/Secure-Enclave realism, hardware keyboard, real IME, camera-less
tailnet); **[2×]** = needs two daemons or two windows. Check each box; note the
build/OS in the header when you run it.

> Tested on: Xcode ___ · iOS ___ · device(s) ___ · date ___

---

## Phase 0 — Setup / integration (do first)

- [ ] **Build the `libghostty-vt.xcframework`** (Task 13): macOS-arm64 +
  iOS-arm64 + iOS-simulator slices, pinned to a Ghostty commit SHA. Replace the
  stale `gui/Libraries/libghostty-vt.a`. Acceptance: a SwiftPM test links it and
  renders a known VT transcript to a grid snapshot on **both** device and sim.
- [ ] **Create the universal SwiftUI app target** (SwiftUI multiplatform, *not*
  Mac Catalyst — design §C.0). Add `.package(path: "..")` for the shared
  `GraithProtocol` + `GraithTerminalCore` products and a dependency on this
  package's `GraithMobileUI` + `GraithTerminalUIKit`.
- [ ] **Write the 6 integration adapters** (mapping table in
  `../NEEDS-IOS-VALIDATION.md`): `ProtocolClientAdapter: GraithHostClient`,
  `AttachSessionAdapter: TerminalAttachSession` (drain `.events` → `.detached`
  reason), `PairingAdapter: GraithPairing`, `ClientFactoryAdapter:
  HostClientFactory`, `extension GhosttyTerminalState: TerminalCoreDriving`,
  `MetalTerminalRenderer` → `TerminalRenderer` seam.
- [ ] App root wires `AppModel(registry:identity:reachability:factory:pairingBackend:)`
  with the real adapters and shows `RootView(model:)`.
- [ ] Confirm all targets compile for **iOS 16+** and the app launches [SIM].
- [ ] Add the Keychain entitlement; confirm no iCloud-Keychain sync
  (`kSecAttrSynchronizable = false`).

## Phase 1 — App shell + no-hosts state (Task 18) [SIM]

- [ ] Fresh install → detail pane shows **"No daemons paired"** with an Add Host
  button.
- [ ] With Tailscale **off/absent**, the sidebar shows the **"Not connected to
  the tailnet"** banner; with a network but no reachable host it reads
  *notOnTailnet*; fully offline reads *offline*.
- [ ] Toolbar shows Approvals (badge hidden at 0), New Session (disabled with no
  hosts), Add Host.

## Phase 2 — Pairing round-trip (Task 18) [DEV, needs a daemon]

Prereq: a daemon with `[remote] enabled=true` on the tailnet; device on the
tailnet via the Tailscale app.

- [ ] Add Host → enter label + MagicDNS name + port → **Pair**. UI enters
  *awaiting approval*.
- [ ] On the daemon host: `gr pair list` shows the pending device (label +
  WhoIs identity); `gr pair approve <id>`.
- [ ] App transitions to **paired**; the **SPKI fingerprint** shown matches what
  `gr pair` printed locally (TOFU).
- [ ] Kill + relaunch the app → it reconnects **without re-pairing** (PoP
  `auth_challenge`→`auth_proof` runs; token loaded from Keychain). [DEV: verify
  the ed25519 key survived in the Keychain.]
- [ ] `gr pair revoke <id>` on the host → the app's live connection drops and
  reads unauthorised; re-pair works.
- [ ] Rejected/rate-limited `pair_request` renders a sensible error.

## Phase 3 — Multi-host read (Task 19) [2×][DEV]

Prereq: pair **two** daemons.

- [ ] Sidebar aggregates both: **host → repo → session** tree; each host shows a
  connection dot (green/yellow/red) and errors inline.
- [ ] Session rows show status dot, name, agent, and badges: PR (#, state,
  conflicting), CI (pass/fail/pending), star, yolo (bolt), sandbox (shield),
  approval bell.
- [ ] Pull-to-refresh reloads the session lists.
- [ ] Select a session → detail header shows status/agent/model/branch/PR/CI.
- [ ] **Peek** tab renders a `screen_snapshot` (monospaced, selectable); refresh
  updates it. Confirm this does **not** kick a desktop attach.
- [ ] **Logs** tab renders a log tail; refresh updates it.
- [ ] Lifecycle actions: Stop a running session (→ stopped), Resume (→ running),
  Restart, Interrupt — each reflects in the sidebar after refresh.

## Phase 4 — Remote create (Task 19) [DEV]

- [ ] New Session → pick a host → repo picker is populated from `repo_list`
  (recent repos flagged). No free-text path field.
- [ ] Choose repo + name + agent + optional prompt → Create → the new session
  appears in the sidebar under the right host/repo.
- [ ] Create with an empty name / no repo is blocked (Create disabled).
- [ ] A daemon-side create error surfaces in the sheet.

## Phase 5 — Approvals (Task 19) [DEV] — the headline mobile use case

- [ ] Trigger a tool that needs approval in a session on a paired daemon.
- [ ] The Approvals badge increments **without attaching** to the session
  (subscribe-only). Open Approvals → the card shows tool name, input, agent,
  repo, session.
- [ ] **Allow** → the agent proceeds; the card disappears from all devices.
- [ ] Trigger another → **Deny** with no reason → agent is denied.
- [ ] [2×] Approvals from **both** daemons appear, grouped by host.
- [ ] Confirm approving on the phone does **not** detach a desktop attach.

## Phase 6 — Interactive attach (Task 20) [DEV]

Prereq: `libghostty-vt.xcframework` + renderer adapter wired.

- [ ] Open a session's attach → terminal renders live output; cursor visible.
- [ ] **Soft keyboard**: type printable text → appears in the session. Return,
  Backspace work. On-screen key row shows esc/ctrl/alt/tab/arrows.
- [ ] **Sticky modifiers**: tap `ctrl`, then `c` → sends Ctrl-C (interrupts a
  running command); the sticky clears after one use. Same for `alt`.
- [ ] esc opens vim/less command mode; arrows navigate; tab completes.
- [ ] **Hardware keyboard** (iPad + BT/Magic Keyboard): plain keys type; Ctrl-C,
  Ctrl-D, Cmd-based chords route correctly; arrows/function keys work. Confirm
  **no double-emit** of Return/Tab/arrows (pressesBegan vs insertText — see
  NEEDS doc risk note).
- [ ] **Scroll**: one-finger drag scrolls into scrollback; releasing / new output
  returns to bottom.
- [ ] **Selection**: long-press + drag selects; the copy menu appears; Copy puts
  text on the pasteboard; Paste inserts (bracketed-paste framing when the app
  has it enabled — test in vim `:set paste` vs a shell).

## Phase 7 — IME (Task 20) [DEV]

- [ ] **US English**: accented input via long-press keys commits correctly.
- [ ] **Japanese** (Romaji): type `nihongo` → conversion candidates appear inline
  (marked text) → commit → the composed 日本語 is sent to the session as one
  commit (not per-keystroke). Cancel mid-composition sends nothing.
- [ ] Emoji picker inserts correctly.
- [ ] Dead keys / multi-stage input don't leak partial bytes to the PTY before
  commit.

## Phase 8 — Resize, reattach, backgrounding, single-attach (Task 20)

- [ ] **Resize**: rotate the device / resize the iPad window → `vim`/`less`
  reflow to the new cols/rows (a `resize` control message fired; no local
  TIOCSWINSZ).
- [ ] **Detach on stop**: stop the session from another client → the attach view
  shows the detached overlay with a **Reattach** button + the reason from
  `.events`.
- [ ] **Reattach** → streams resume.
- [ ] **Backgrounding**: background the app during an attach → a "paused" state;
  foreground → it reattaches automatically (iOS suspends sockets when
  backgrounded).
- [ ] **Single-attach guard** [2×]: iPad split-view / two windows — open the
  **same** session in a second pane → the second shows **"Already attached in
  another window"** and does not open a second attach. Different sessions in two
  panes work simultaneously.
- [ ] **Takeover legibility**: attaching from the phone kicks a desktop attach
  (and vice-versa) with a clear message on the kicked side.

## Phase 9 — Robustness

- [ ] Tailscale toggled off mid-session → clear disconnected state, recovers on
  reconnect.
- [ ] Daemon restarted (`gr daemon restart`) → remote clients reconnect.
- [ ] TLS pin mismatch (re-key the daemon) → app refuses with a re-pair prompt,
  does not silently trust.
- [ ] No crashes on session delete/stop while attached or peeking.
- [ ] Accessibility: VoiceOver reads sidebar rows (name/status/agent); Dynamic
  Type doesn't break the sidebar/approval cards.
