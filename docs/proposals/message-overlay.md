# Proposal: Message overlay (`ctrl+b m`)

> **Implementation status (v1 shipped).** The read-only v1 cut is implemented:
> backend `msg_conversation` (with the `checkTarget`/attached-session auth model
> and the `sender_id` index), `Conversation(self, limit)` storage query with
> deterministic ordering and pagination, the `ctrl+b m` keybinding, and a
> two-pane bubbletea overlay (per-peer rail + thread pane, both directions, 2s
> poll). **Deferred to follow-ups:** inline reply, push-based live-follow,
> explicit mark-thread-read/ack, unified-timeline & topics view modes, and
> fleet/operator mode.

## Summary

Add a second full-screen overlay — alongside the session picker (`ctrl+b w`) —
that shows the messages **to and from the current session**, rendered like a
chatroom. It is opened with a new prefix-key binding, **`ctrl+b m`** (m for
*messages*).

## Motivation

graith agents already talk to each other constantly via `gr msg send` (direct,
1:1) and `gr msg pub --topic` (broadcast). Today the only way a human can see
that traffic is to drop into a session's shell and run `gr msg inbox --all` or
`gr msg sub --topic ... --follow` — a CLI workflow that shows raw, one-shot
output and is awkward to keep an eye on.

The real driver is **human oversight**. Agents coordinate, delegate, and
occasionally overstep — a sibling agent recently tried to assert authority over
the human operator. When that happens, the human needs to *see* it, in context,
without disrupting the session. A persistent, chatroom-style view of who is
saying what to whom turns inter-agent messaging from an invisible side-channel
into something the operator can supervise at a glance and intervene on.

Secondary benefit: it's also just a nicer way for an agent (and the human
driving it) to read their own inbox than scrolling raw CLI output.

### Validation from live use

A sibling session (`cmux-learnings`) that had just coordinated ~4 child agents
gave field feedback that shaped this design:

- Traffic is **~100% 1:1 direct messages** (parent↔child request/response),
  essentially zero topic pub/sub, and **bursty** (quiet, then 3–5 messages when
  an agent reports in). Optimise for *many concurrent 1:1 threads*, not broadcast.
- Inbox-only is "genuinely painful" — every conversation is half-visible.
  **Per-conversation threads** (me↔one peer) are the primary need; a unified
  timeline is "noise" with many parallel children and belongs as a secondary
  overview mode. Showing your own sent messages is "important, not optional."
- The overlay serves **two audiences**: a *human* watching an agent's
  conversations (wants the chatroom UI) and *agents* (already fine with the
  structured CLI). So it is primarily a **human affordance** over the same store
  — which is exactly the oversight need above.

## Goals

- Show the current session's messaging as a chatroom, **organised by
  conversation** (one thread per peer), with a left rail of peers showing
  unread counts and a thread pane rendering both directions inline.
- Cover **both directions** — messages received *and* messages this session
  sent — not just the inbox. This is the single most important property.
- Live updates / follow: new messages appear without a manual read+ack loop.
- Human-readable rendering (sender, time, body), not raw JSON.
- Zero-friction open/close, mirroring the `ctrl+b w` overlay UX.
- A unified-timeline mode as a secondary overview, and (stretch) a fleet-wide
  mode for the operator to watch *all* inter-agent traffic at once.

## Non-goals (v1)

- A general message search engine.
- Composing brand-new threads to arbitrary sessions (the picker covers
  discovery). Replying *within* a visible conversation is in scope — see below.

## Key architectural finding: "to and from" is not free

This is the crux of the design and worth stating up front.

Direct messages are stored as a **publish into the recipient's inbox stream**.
When session A sends to session B (`internal/cli/msg.go`), the daemon publishes
to stream `inbox:<B>` with `sender_id = A` (`internal/daemon/msgstore.go:114`,
handler at `internal/daemon/handler.go:570`). Consequences:

- **Messages *to* the current session** = the `inbox:<self>` stream. Easy:
  `MsgStore.Read("inbox:"+self, ...)` already exists and `msg_inbox` already
  serves it (`handler.go:605`).
- **Messages *from* the current session** live in *other* sessions' inbox
  streams (and in topic streams), keyed only by `sender_id = self`. There is
  **no query today that fetches messages by sender** — `Read` filters by
  `stream`, and `ListStreams` groups by stream (`msgstore.go:185`, `:277`).

So a true chatroom ("to AND from") requires one new storage query and one new
control message. Everything else is UI.

### Three rendering models, in order of cost

| Model | What it shows | Backend work |
|-------|---------------|--------------|
| **A. Inbox-only** | Only messages received (`inbox:self`) + topics subscribed | None — reuse `msg_inbox` |
| **B. Conversation** | Per-peer thread: my inbox-from-X interleaved with my sends-to-X | New "by sender" query |
| **C. Unified timeline** | Every message involving me, both directions, one stream | New "by sender" query |

Recommendation: target **B (per-conversation threads) as the primary view**,
with **C (unified timeline) as a secondary overview mode**. Both B and C need
the same single backend addition (a by-sender query), so there is no reason to
ship the half-visible inbox-only model A as the user-facing default — live use
confirms it is the main pain point.

## Design

### Backend additions

1. **Storage query** in `internal/daemon/msgstore.go`. Fetch the *unified*
   both-directions DM set in one call and group by peer client-side (B and C
   share this single query — see note below on why we don't take a `peer` arg):

   ```go
   // Conversation returns every direct message involving `self`, both
   // directions: messages delivered to self's inbox (stream = inbox:self,
   // peer = sender_id) and messages self sent to any peer's inbox
   // (sender_id = self AND stream LIKE 'inbox:%', peer = stream minus prefix),
   // ordered by created_at. Topic messages are out of scope here (handled via
   // the existing ListStreams/Read path).
   func (s *MsgStore) Conversation(self string) ([]Message, error)
   ```

   ```sql
   SELECT ... FROM messages
   WHERE stream = 'inbox:' || ?                       -- received by self
   UNION ALL
   SELECT ... FROM messages
   WHERE sender_id = ?                                -- sent by self
     AND stream GLOB 'inbox:*'                        -- DM streams only
     AND stream <> 'inbox:' || ?                      -- avoid double-counting self-msgs
   ORDER BY created_at ASC, id ASC
   ```

   The client derives each message's *peer* (sender_id for received,
   stream-minus-`inbox:` for sent), groups into per-peer threads for the left
   rail (view B), and shows them merged for the Timeline mode (view C) — no
   second query.

   > **Indexing / ordering (tribunal, both judges):** the earlier draft's claim
   > that "`idx_messages_created_at` covers the sort" is misleading — it covers
   > only the `ORDER BY`, not the `WHERE`. There is **no `sender_id` index**
   > today (`msgstore.go:74-76`), so the sent-branch is a full scan, and SQLite's
   > default `LIKE` is case-insensitive so `stream LIKE 'inbox:%'` won't use the
   > stream index. Fixes folded in above: use `GLOB 'inbox:*'` (case-sensitive,
   > index-friendly) and add `CREATE INDEX idx_messages_sender ON
   > messages(sender_id)` (a migration). At ~38k rows today it's only a few ms,
   > but don't bake the false index claim into the design. Verify with `EXPLAIN
   > QUERY PLAN` on realistic data.
   >
   > **Ordering tie-breaker (both judges):** `seq` is *per-stream*, so the only
   > cross-stream key is `created_at` (RFC3339Nano). Ties are possible and would
   > render a reply before its parent — hence `ORDER BY created_at ASC, id ASC`
   > for stable (if not causally exact) ordering. A globally-monotonic column
   > would be needed for exact cross-stream order; out of scope for v1.
   >
   > **Pagination (both judges):** `Conversation` must take a `LIMIT`/since-cursor
   > and load most-recent-first — a long-lived parent can accumulate thousands of
   > messages, and re-loading all of them on every poll (below) is wasteful.
   >
   > **Self-messages:** `UNION ALL` would duplicate a row matching both branches,
   > so the sent-branch excludes `inbox:self` explicitly (above). Define a UI rule
   > for the resulting self-thread.

   > **Signature note (resolving an earlier contradiction):** an earlier draft
   > had `Conversation(self, peer)` but a SQL body that ignored `peer`. We
   > deliberately drop the `peer` argument: the query returns the unified set
   > and per-peer filtering is a client concern. (The server-side alternative —
   > `WHERE (sender_id=self AND stream='inbox:'||peer) OR (sender_id=peer AND
   > stream='inbox:'||self)` — is left as an optimisation if a single very busy
   > thread ever needs it; v1 does not.)

2. **Control message** `msg_conversation` in `internal/protocol/messages.go` +
   a `case` in `internal/daemon/handler.go`. Returns `[]Message`.

   > **⚠ CRITICAL — auth model (both tribunal judges, independently).** The
   > earlier draft said to mirror `msg_inbox`: *require* `auth.authenticated` and
   > *force* `self = auth.sessionID`. **This breaks the proposal's own primary
   > audience.** The overlay is opened from the **human** `gr attach` client,
   > which runs *outside* any agent PTY and therefore has **no `GRAITH_TOKEN`**
   > (the token is only set inside an agent's env; `client.go` reads it from the
   > environment). So a human-attached overlay sends *unauthenticated* control
   > messages — and `msg_inbox` explicitly rejects those (`handler.go:611-615`).
   > Mirroring `msg_inbox` would make the human operator see *nothing*. A second
   > bug from the same root: forcing `self = auth.sessionID` keys off the *token*
   > identity, not the *attached* session — so an agent attached to session B
   > would see session A's conversations.
   >
   > **Correct model — mirror `status`, not `msg_inbox`** (`handler.go:871-877`):
   > the request carries a target `SessionID` (the *attached* session, which the
   > client already has), and the handler authorises with
   > `auth.checkTarget(sm, sessionID, authSelfOrDescendant)` (`auth.go:78-92`).
   > Under that rule: an **unauthenticated human passes** (intended — operator
   > oversight); an **agent** may target itself or a **descendant**; the
   > **orchestrator** may target anyone. The by-sender query then uses the
   > authorised target as `self`.
   >
   > **Decision required — ancestor→descendant read is a NEW capability.**
   > `authSelfOrDescendant` lets an ancestor agent read a descendant's full inbox
   > + sent messages. No path exists today for one agent to read another's inbox
   > *messages* (only `status` exposes unread *counts*). This may be desirable
   > (hierarchical oversight) but is a deliberate privilege expansion — if we want
   > only self + human + orchestrator, a custom rule is needed instead. **Lock the
   > choice with a test** so a later "hardening" can't silently change it.
   >
   > **Why the cross-inbox scan is still safe.** The query reads non-owned
   > `inbox:<peer>` streams filtered to `sender_id = target`. For the human this
   > grants nothing new — an unauthenticated caller can *already* `msg_sub
   > inbox:<anyone>` (the inbox rejection is inside `if auth.authenticated`,
   > `handler.go:628-636`). For an authenticated agent, `msg_pub` force-sets
   > `sender_id = auth.sessionID` (`handler.go:576`), so a `sender_id = self`
   > filter returns only content that session actually authored. **Caveat
   > (Codex):** unauthenticated local callers *can* publish arbitrary `sender_id`
   > (`handler.go:588-592`), so `sender_id` is not cryptographic proof of
   > authorship — fine under the "local human is trusted" model, but don't claim
   > more than that.

3. **Live updates — poll, don't subscribe, for v1 (both judges).** The earlier
   draft said "we notify on the sender side too so outbound messages echo in" —
   **no such mechanism exists.** `Subscribe` is per-stream and in-memory
   (`msgstore.go:335`); `Publish` only fans out to subscribers of the *published*
   stream and `notifyInbox` notifies the *recipient*, not the sender
   (`notify.go:53`). Streaming *outbound* live would require subscribing to every
   peer's inbox (unbounded, and blocked for authenticated agents) or new
   sender-side plumbing in `Publish`. **v1 plan:** use a **2s poll** with a
   cursor/watermark, exactly like the session picker (`overlay.go:922` →
   `freshClient` one-shot at `attach.go:711`) — which also fits the
   fresh-`ConnectPassive`-per-poll model the unauthenticated human uses. Promote
   true push-follow (the `f` toggle) to future work.

### Client / overlay

New bubbletea model in `internal/client/` (e.g. `msgoverlay.go`), structured
like `overlay.go`. Two-pane chatroom layout:

- **Left rail — conversations.** One row per peer (derived from `Conversation`
  results grouped by counterpart), most-recent first, each with a
  per-conversation **unread count**. This is the primary navigation, matching
  how an agent juggling parallel children actually thinks ("what's the state of
  my thread with X?"). **Peer-name resolution (both judges):** *received* rows
  carry `sender_name`, but *sent* rows only carry the peer's session ID (from the
  stream) — the rail must resolve id→name via the session list and render a
  graceful "unknown / deleted peer" fallback when the peer no longer exists.
  Consider having the server return a DTO (`peer_id`, `peer_name`, `direction`,
  `unread`) rather than reimplementing stream parsing on every client (Codex).
- **Thread pane — the selected conversation, both directions inline:**
  - **Received** messages left-aligned with `sender_name`;
  - **Sent** messages right-aligned (or marked `→`) as "me";
  - relative timestamps, a subtle separator on day/gap boundaries;
  - **system/auto notifications** visually distinguished. **Caveat (both
    judges):** the inbox auto-resume "pokes" are injected as **PTY input text,
    not stored messages** (`notify.go:67`), so they won't appear in the
    conversation at all. What *is* persistent: `_system.*` streams
    (`notify.go:16`, filtered in `ListStreams` at `msgstore.go:293`) and
    orchestrator manifest DMs (`scenario.go:358`). Classify by the `_system.`
    convention / known sender, and don't claim to surface pokes.
- **Inline reply (high priority).** A reply line at the bottom of the thread
  pane composes back to the selected peer via `msg_pub`. **Identity must be
  explicit (both judges):** a human reply goes over an *unauthenticated*
  connection, where the daemon takes `SenderID` from the payload
  (`handler.go:588-592`) — so to appear "as" the attached session it must set
  `SenderID = attachedSessionID`, which is a spoof the daemon permits for the
  trusted local human but should be a *first-class, deliberate* operation, not
  accidental. (An authenticated agent's sender is force-set, `handler.go:576`.)
  Decide whether the reply is authored *as the attached session* or *as the
  operator*.
- **Live follow.** v1 = the 2s poll above (see backend §3); the `f` push-follow
  toggle is deferred — true outbound live-echo has no backing mechanism today.
- **Explicit, toggleable ack.** Ack-on-view is *opt-in*, not silent: agents
  deliberately use `--ack` for triage, so default to a "mark read" key and offer
  a "keep unread / mark unread" mode. **Per-peer ack on a shared stream (both
  judges):** all inbound from every peer lands in the *single* `inbox:self`
  stream, while unread is tracked per-`(subscriber, stream)` (`msgstore.go:78`).
  So per-peer unread must be computed by splitting `inbox:self` by `sender_id`
  client-side, and "mark this thread read" must ack **only the matching received
  seqs** via `AckMessages` (per-seq, `msgstore.go:236`) — **not** `AckLatest`,
  which would clear *every* peer's unread. Outbound rows (in peers' inboxes) are
  *never* acked by self — that's the peer's unread state. Topic acking stays
  separate. This needs a test so the overlay can't clear another session's
  unread.
- View modes cycled with `◂ ▸` (mirroring the picker's `viewAll/...`):
  **Conversations** (default, model B), **Timeline** (unified, model C),
  **Topics**. Note (Codex): there is no persistent subscription registry —
  "Topics" means *topic streams that have messages* (via `ListStreams`), with a
  per-subscriber read cursor, not "subscribed topics".
- `/` to filter by sender name or text. Footer help bar consistent with the
  picker.

### Wiring (passthrough + attach)

- Add `ResultMessageOverlay` to the `PassthroughResult` enum
  (`internal/client/passthrough.go:17`).
- Bind it under the prefix key — **`m`** is currently free — at the dispatch
  switch (`passthrough.go:408`) and add `m messages` to the help bar
  (`passthrough.go:136`). (Correction from tribunal: the full default-bound set
  is larger than an earlier draft stated — beyond `a c d f l n o p r s w`, the
  config also binds `x`, `R`, `,`, `/`, `[` at
  `default_config.toml:106-119`. None collide with `m`, so the pick stands.
  Like `d/w/s/a/r`, `m` would be hardcoded in the switch rather than
  config-rebindable.)
- Handle `ResultMessageOverlay` in the attach loop
  (`internal/cli/attach.go`), calling a new `RunMessageOverlay(...)` that
  issues `msg_conversation` and then reattaches when the overlay exits — exactly
  how `ResultOverlay` is handled today.

## Keyboard shortcut

**`ctrl+b m`** — mnemonic *messages*, and `m` is unbound in the current prefix
map. Help bar gains: `... s shell  m messages  r restart`.

(Alternatives considered: `i` for *inbox* — but the view is broader than the
inbox; `g` — no mnemonic. `m` is the clear pick.)

## Oversight / fleet mode (stretch)

The motivating incident — an agent overstepping the human — argues for a mode
where the operator watches **all** inter-agent traffic, not just one session's.
The building blocks exist and create **no new hole**: the unauthenticated human
CLI can already read any inbox (`msg_sub`) and list every stream
(`ListStreams`), so an operator view merging all inboxes + topics into one
timeline grants the human nothing they lack. With the `checkTarget` auth model
above, this falls out naturally for the human and the orchestrator. Risks here
are **scale and UI** (≈1500 streams today — must paginate), not auth. Highest-value
oversight feature, largest scope; a follow-up once the per-session overlay lands.

Note: the orchestrator's `authSelfOrDescendant` access via `checkTarget` only
applies to handlers that *call* `checkTarget`; if fleet mode should work from the
orchestrator *session* (vs. the tokenless human CLI), `msg_conversation` must
route through `checkTarget` for that to hold (Codex).

## Testing

Both judges flagged the absence of a test plan; the auth exception especially
needs a test that *asserts* the intended behaviour so a future "hardening" can't
silently break "to and from". Per repo convention, fixtures use Scots words.

- **Storage:** `Conversation` includes inbound-from-peer and outbound-to-peer;
  excludes third-party DMs and topics; self-message appears once; deterministic
  order on equal `created_at`; malformed `inbox:`/empty `sender_id` don't panic
  grouping; intended index used (`EXPLAIN QUERY PLAN`).
- **Handler/auth:** unauthenticated human can read a target session's
  conversation; an agent sees only its own authored rows + own inbox; a sibling
  is denied; descendant/orchestrator behaviour is whatever we deliberately
  choose — *locked by a test*.
- **Client:** `ctrl+b m` dispatch + help bar; attach loop reattaches after exit;
  grouping/unread/self-message/deleted-peer/long-multiline bodies; "mark thread
  read" acks only the selected peer's received seqs.
- **Integration:** send A→B, open B's conversation, reply, verify both
  directions; ack survives daemon restart (cursors persist).

## Protocol / versioning

`msg_conversation` is a new control message (`protocol/messages.go` +
`handler.go`). An old daemon returns "unsupported control message"
(`handler.go` default case); the client should surface a clear "restart the
daemon after upgrading" error. Compatibility checks only the major version
(currently `1.0`), so no major bump is required.

## Recommended v1 cut (post-tribunal)

Given the scope is large for a first overlay, both judges suggest landing a
tighter v1 and deferring the rest:

1. Backend `msg_conversation` with the **`checkTarget`/attached-session** auth
   model, pagination, the `sender_id` index, and deterministic ordering.
2. Read-only conversation rail + thread pane (per-peer, both directions).
3. The **2s poll** (no push-follow).
4. Explicit "mark thread read" for received rows only.

Then add inline reply (once reply-identity is decided), push-follow, unified
timeline, topics, and fleet mode.

## Future work

- Inline reply (if not in v1), push-based live-follow (the `f` toggle), unified
  timeline, topics view, fleet/operator mode.
- Jump-to-reply / thread collapsing using the `reply_to` / `thread_id` fields
  already on `Message` (`msgstore.go:23`).
- Per-peer unread badges surfaced in the session picker itself.

## Open questions

1. **Auth scope — DECIDED.** `msg_conversation` uses the `checkTarget` /
   attached-`SessionID` model (not force-self). An **ancestor agent may read a
   descendant's conversation** — `authSelfOrDescendant` is the intended rule
   (confirmed by the maintainer); hierarchical oversight is wanted. Locked with a
   test (`msg_conversation_test.go`) so a later "hardening" can't silently
   narrow it.
2. **Reply identity:** does a human reply post *as the attached session* (a
   daemon-permitted spoof for the trusted local human) or *as the operator*?
3. **v1 scope:** ship the read-only v1 cut above, or include inline reply
   immediately (live use ranks it #1)?
4. **Fleet/operator mode** — in scope now, or a separate proposal?
