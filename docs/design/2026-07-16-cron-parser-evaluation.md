---
title: "Design Doc: Cron parser abstraction and gronx evaluation"
authors: Dougal Matthews
created: 2026-07-16
status: Implemented
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1213
---

# Cron parser abstraction and gronx evaluation

graith parsed cron in two separate places with two separate `robfig/cron`
parser instances that could silently drift, and depended on `robfig/cron/v3` —
unmaintained since January 2021. Issue #1213 asked whether to replace it with
`github.com/adhocore/gronx`. This doc records the evaluation: we built a single
graith-owned schedule abstraction (`internal/cronx`), pinned down the grammar
graith actually supports, ran a differential corpus against gronx — and **kept
robfig**, because gronx v1.20.0 (the latest) computes wrong next-fire times for
several ordinary schedules.

## Background

A `[trigger.schedule]` with a `cron` field is parsed twice:

- `internal/config/trigger.go` validated it at config-load time with a
  `cron.NewParser(...)` instance (`cronSpecParser`).
- `internal/daemon/trigger.go` parsed it again at runtime with a *second*
  `cron.NewParser(...)` instance (`cronParser`) and called `Schedule.Next` on
  each tick to compute the next fire; the parsed schedules were stored in
  `triggerState.cron` as `map[string]cron.Schedule`.

Both instances used the same field flags, so they agreed today — but nothing
enforced that. A change to one and not the other would let config validation
accept an expression the runtime couldn't fire, or vice-versa.

Only three pieces of robfig's API were ever used: the parser, `Schedule.Next`,
and the field/descriptor flag constants. Its scheduler and job runner are
unused.

## Problem

1. **Two parsers, one grammar, no guarantee they match.** The drift risk above.
2. **The grammar was never written down.** The docs promised "5-field cron, or
   `@hourly`/`@daily`/`@weekly`/`@monthly`", but robfig quietly accepted more:
   `@yearly`, `@annually`, `@midnight`, `@reboot`, `@every <dur>`, and (with the
   right flags) seconds/year fields. What graith *actually* supported was
   whatever robfig happened to parse, not what was documented.
3. **robfig is unmaintained** (last commit January 2021), so #1213 asked whether
   `gronx` — actively maintained — should replace it.

## Goals

- One parser, used by both config validation and the daemon, so they cannot
  accept different grammars.
- The supported grammar written down explicitly, as a single source of truth.
- A differential test corpus comparing robfig and gronx across the cases that
  matter (descriptors, wildcards, lists/ranges/steps, named fields, Sunday 0/7,
  DOM/DOW union, leap days, IANA timezones, DST spring-forward gaps and
  fall-back repeats, non-hour DST offsets, repeated `Next` across years,
  unreachable schedules).
- A recorded keep-or-replace decision, backed by that corpus.

## Non-Goals

- Supporting seconds-resolution or year fields. graith fires at minute
  granularity and has no need for either.
- Preserving robfig's undocumented descriptor extensions. See the decision on
  tightening below.

## Proposals

### Proposal 0: Do nothing

Keep the two parsers and the unwritten grammar. Rejected: it leaves the drift
risk and the documentation gap, and doesn't answer #1213.

### Proposal 1: Centralize behind `internal/cronx`, keep robfig (Recommended, adopted)

Introduce `internal/cronx` as graith's single owned schedule abstraction:

- `cronx.Parse(spec) (Schedule, error)` — the one parser. Enforces the grammar
  (below), then delegates to a single shared robfig parser instance.
- `cronx.Validate(spec) error` — used by config validation; same code path as
  `Parse`, so validation and runtime cannot diverge.
- `Schedule.Next(time.Time) time.Time` — delegates to robfig.
- `cronx.Grammar` — the exported, documented grammar string, the source of
  truth the docs point at.

Config validation calls `cronx.Validate`; the daemon stores
`map[string]cronx.Schedule` and calls `cronx.Parse`/`Schedule.Next`. robfig
becomes an implementation detail of one package.

**Grammar — tightened to exactly the documented set.** graith accepts a
five-field expression (minute hour day-of-month month day-of-week, with `*`,
lists, ranges, steps, and three-letter English names for month/weekday), or one
of `@hourly`/`@daily`/`@weekly`/`@monthly`. Everything robfig accepted beyond
that is now rejected with a clear error: six/seven-field forms, the
undocumented descriptors (`@yearly`/`@annually`/`@midnight`/`@reboot`/`@every`),
and Quartz/gronx extensions (`?`, `L`, `W`, `#`). We tighten rather than
preserve because the whole point is a precise, testable contract — "what the
docs say" — and no config or test in the repo used the extras. Sunday stays `0`
(robfig bounds day-of-week `0-6`); `7` is rejected, matching graith's existing
behaviour.

### Proposal 2: Replace robfig with gronx

Swap the engine under `internal/cronx` for gronx and drop robfig. **Rejected on
correctness.** The differential corpus (`internal/cronx/cronx_diff_test.go`)
found that gronx v1.20.0 — the latest release — returns wrong next-fire times
for ordinary schedules, and returns them *silently* (no error; a time its own
`IsDue` rejects):

| Expression | Reference | robfig (correct) | gronx v1.20.0 |
|---|---|---|---|
| `0 0 31 * *` | 2026-11-01 00:30 UTC | 2026-12-31 | **2026-12-02** (not day 31) |
| `0 0 29 2 *` | 2026-01-01 UTC | 2028-02-29 | **2026-03-04** (not Feb 29) |
| `0 0 1 * *` | 2026-11-01 00:30 America/New_York | 2026-12-01 | **2026-11-02** (not day 1) |

The root causes are a per-field iteration cap in gronx's `bumpUntilDue` (it gives
up crossing months that lack the target day, so day-of-month 29/30/31 and
leap-day Feb 29 misfire) and a day-search corruption around DST fall-back
transitions (a plain `@monthly`-style schedule misfires if the reference instant
lands on a fall-back day). These are not defensible "compatibility differences"
to document and accept — they are the schedule firing on the wrong day. Migrating
would mean triggers silently fire at the wrong time.

On the *safe* corpus (no day-of-month overflow, no DST-transition reference) the
two engines agree exactly, so the divergences above are the whole of the
difference — and they're disqualifying.

Note: `go-co-op/gocron/v2` was explicitly out of scope in #1213 because it
depends on the same robfig/cron.

## Consensus

Not separately reviewed; the decision follows directly from the corpus evidence,
which is committed and reproducible (`go test ./internal/cronx`).

## Other Notes

- **gronx stays as a test-only dependency.** `cronx_diff_test.go` keeps the
  live comparison as a tripwire: if a future gronx fixes these bugs, its
  `TestGronxKnownBugs` "engines must still disagree" assertions fail, prompting a
  re-evaluation; if gronx ever diverges on the safe corpus, `TestEnginesAgree`
  fails. It is not on any runtime path.
- **Unreachable-schedule fix.** While centralizing, we fixed a latent
  fire-storm: an impossible cron like `0 0 30 2 *` (February never has a 30th)
  makes `Next` return the zero time, which `dueSchedules` previously treated as
  "due now" and re-fired every tick. `armSchedule` now records an error and
  leaves a zero cursor, and `dueSchedules` treats a zero cursor as dormant
  (regression test: `TestArmSchedule_UnreachableCronIsDormant`).
- If robfig ever needs replacing for supply-chain reasons, the abstraction means
  only `internal/cronx` changes — but the replacement must pass the differential
  corpus first.
