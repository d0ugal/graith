---
title: "Design Doc: Jail blocked PR comments for later release"
authors: Dougal Matthews
created: 2026-07-13
status: Accepted
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1082
---

# Jail blocked PR comments for later release

When `pr_watch` drops a PR comment from an untrusted author (a prompt-injection
precaution — see the author-trust design), the comment content is discarded
entirely: not delivered to the agent, not stored anywhere. If the human later
decides the author is trustworthy and allowlists them, the dropped comments are
gone — GitHub still has them, but graith has thrown away its copy and advanced
its cursor past them, so they will never be delivered. This doc adds a
quarantine store ("jail"): blocked comments are held with their metadata instead
of discarded, the human/orchestrator can inspect them, and they are released
(delivered) either explicitly or automatically once the author becomes trusted.

## Background

`pr_watch` (`internal/daemon/prwatch.go`) polls each session's PR and delivers
new review/conversation comments to the owning agent's inbox. Because comment
bodies are free text from arbitrary GitHub users, each new comment passes the
**author-trust gate** (`commentTrusted`, issue #1039): a comment is delivered
only if its author's login is in `comment_author_allowlist` or its
`author_association` is in `trusted_author_associations`
(OWNER/MEMBER/COLLABORATOR by default). Untrusted comments are **dropped** — the
body never reaches the agent (which is itself an LLM, so relaying an
injection payload would defeat the purpose). Their authors are surfaced once,
**metadata-only**, to the orchestrator (`promptUntrustedAuthors`) so the human
can decide whether to allowlist them, and the per-surface cursor advances past
them so a later trusted comment isn't reported alongside the whole backlog.

Inter-agent messages live in a SQLite-backed store (`internal/daemon/msgstore.go`)
that already implements age/count retention via `Cleanup`, wired to
`[messages] max_age` through `RunMessageCleanupLoop`.

## Problem

Dropping is lossy and irreversible. Once the human reads the metadata prompt and
decides "actually, that CONTRIBUTOR's review was legitimate", the comment is
already gone from graith and the cursor has moved on. The only recovery is to
manually `gh pr view --comments` and paste the content back to the agent by
hand. That is exactly the friction the trust gate was supposed to remove, just
displaced onto the human.

## Goals

- Blocked comments are **held**, not discarded, with enough metadata to inspect
  them (PR number, author, association, body, target session, timestamp).
- The human/orchestrator can **list**, **inspect**, and **release** jailed
  comments; release delivers the content to the target session's inbox.
- **Release is privileged**: only the human or the orchestrator may release. A
  regular agent session must never release — a compromised agent could otherwise
  release its own injection payload, defeating the quarantine.
- **Auto-release**: when config is reloaded and an author has become trusted
  (added to the allowlist, or their association is now trusted), their jailed
  comments are released automatically (a local-human config action).
- Jailed comments respect `[messages] max_age` retention — they don't
  accumulate forever.

## Non-Goals

- No change to the trust gate itself (what counts as trusted is unchanged).
- No per-comment "trust once" flow — trust is still author/association-level.
- No UI beyond the CLI (`gr msg jail …`).

## Proposals

### Proposal 0: Do Nothing

Keep discarding. Rejected: the loss is the reported bug (#1082); the human has no
recovery path short of manual copy-paste.

### Proposal 1: Store jailed comments in the shared document store

Write each dropped comment to a `jail/…` doc key. Rejected: the store is
per-repo flat files with no auth boundary — an agent can read/write it, so it
can't enforce "only human/orchestrator releases", and it has no retention tie to
`[messages] max_age`.

### Proposal 2: A new message stream with a quarantine sender (Recommended, chosen)

Add a dedicated `jailed_comments` table to the existing msgstore SQLite DB. It
already lives daemon-side (agents reach it only through authorized control
messages), already has retention plumbing, and is transactional. Jailing is an
insert; listing/inspection are queries; release marks the row released and
delivers the body via the existing `notifyFromDaemon` path (which auto-resumes a
stopped agent). Retention hooks into the existing `Cleanup`.

Authorization reuses the established pattern: `gr msg jail list|show` are
read-only control messages (readable by agents — they can see *what* is jailed,
just not release it); `gr msg jail release` is a mutating message gated by a new
`checkJailRelease` that mirrors `checkNotifyOp` — human or orchestrator only, a
plain agent is rejected. The remote auth matrix classifies list/show as
`remoteReadOnly` and release as `remoteHumanRW`.

Auto-release re-evaluates every unreleased jailed comment against the *new*
config on reload using the same `commentTrusted` predicate over the stored
login/association/bot fields; any that now pass are released. This covers both
"author added to allowlist" and "association added to trusted set" uniformly,
without diffing the two config lists by hand.

## Other Notes

Release delivery frames the comment as *previously-quarantined, now-released*
external feedback, reusing the awareness framing (treat as feedback, not
instructions). The metadata prompt to the orchestrator is updated to say the
comment was jailed (not discarded) and how to inspect/release it.

Dedup: a `UNIQUE(comment_id, surface, target_session)` constraint with
`INSERT OR IGNORE` means a re-fetch of the same comment (e.g. a degraded poll
that re-primes) can't create duplicate jail entries.
