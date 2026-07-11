<!--
  Design-doc template for graith.

  To start a new design doc:
    1. Copy this file to docs/design/YYYY-MM-DD-<slug>.md
       - date = the day you start it (creation date, not last-edited)
       - slug = short-kebab-case topic, e.g. `2026-07-11-triggers-design.md`
    2. Delete every HTML comment (like this one) as you fill each section in.
    3. Keep the section order below — it is the house style. Drop sections that
       genuinely don't apply (e.g. `Consensus` before any review has happened),
       but don't reorder or rename the ones you keep.

  The doc is the argument for a decision, not a spec of the finished code. Write
  it before you build, and update `status` as it moves through review and
  implementation. Prose over bullet-dumps; cite `file.go:line` when you mean a
  specific place in the code.
-->
---
title: "Design Doc: <Short Title>"
authors: <Your Name>
created: <YYYY-MM-DD>
status: Draft
reviewers: (none yet)
informed: (TBD)
# issue: https://github.com/d0ugal/graith/issues/<n>   # optional; omit if none
---

<!--
  status vocabulary (advance it as the doc progresses):
    Draft                     — proposal under discussion, not yet agreed
    Accepted                  — approach agreed, not yet built
    Implemented               — shipped; doc kept as the record of why
    Implemented (v1)          — first slice shipped; later phases still deferred
    Superseded by <doc>       — replaced; leave a pointer to the successor
  You may append a short parenthetical, e.g.
    `status: Draft (revised after an independent review — see Consensus)`
-->

# <Short Title>

<!-- One short paragraph: what this changes and why it matters, in plain terms.
     A reader should grasp the whole idea from this paragraph alone. -->

## Background

<!-- The context a reader needs before the problem makes sense. How things work
     today, which components are involved, what the reader has to know. No
     proposal yet — just the lay of the land. -->

## Problem

<!-- The specific pain this doc addresses. Be concrete: what breaks, what's
     awkward, who hits it, what it costs. If there's a triggering issue or a
     real-world report, cite it. Keep it to the problem — not the fix. -->

## Goals

<!-- What a good solution must achieve — the bar success is measured against.
     Bullets are fine here. -->

- <goal>
- <goal>

### Non-Goals

<!-- Explicitly out of scope. Naming these prevents scope creep and pre-empts
     "but what about…" review comments. A stated non-goal is a decision, not an
     omission — if you later reverse one, that's a doc update worth noting. -->

- <non-goal>

## Proposals

<!--
  Lay out the options considered, not just the winner — the rejected paths are
  the most valuable part of the doc later. House conventions:
    - Number them. Start with "Proposal 0: Do Nothing" (the status quo /
      baseline you're arguing against).
    - Mark the one you're advocating with "(Recommended)" in its heading.
    - For each: what it is, how it works, and its trade-offs (pros/cons).
  For a small, single-obvious-approach change you may collapse this to a single
  `## Proposal` section instead — but still say what you rejected and why.
-->

### Proposal 0: Do Nothing

<!-- The baseline. What happens if we ship nothing? Why isn't that acceptable?
     (Sometimes it is — a doc that concludes "do nothing" is a valid outcome.) -->

### Proposal 1: <Name> (Recommended)

<!-- The recommended approach. Explain the mechanism concretely — data flow,
     new types/fields, config surface, CLI surface, state/protocol changes.
     Use subsections (### How it works, ### Implementation, ### Config, etc.)
     as the design warrants. Trade-offs and risks, honestly stated. -->

### Proposal 2: <Name>

<!-- A serious alternative and why it lost to Proposal 1. -->

## Consensus

<!-- OPTIONAL — add this only after the doc has been reviewed. Summarise where
     reviewers agreed and disagreed, which findings changed the design, and how
     open disagreements were resolved. This is how a revised doc records that it
     was scrutinised. Omit entirely until a review has actually happened. -->

## Other Notes

<!-- Supporting material that doesn't belong in the argument above. Use the
     subsections that apply; drop the rest. -->

### References

<!-- Related design docs, issues, PRs, and the key code paths this touches
     (`internal/daemon/daemon.go` — `Create()`, …). -->

### Implementation Notes

<!-- Practical build notes: state migrations, backward-compatibility, phasing,
     ordering constraints, gotchas discovered while designing. -->

### Alternatives considered

<!-- Smaller alternatives not big enough to be full Proposals, and why they
     were set aside. -->

### Testing

<!-- How the change will be proven correct: unit coverage, integration tests,
     edge/error cases, and any regression test that locks a fixed bug closed.
     Remember the repo's coverage bar and `-race` requirement. -->

### Open questions

<!-- Decisions still outstanding for reviewers to weigh in on. Move these into
     the design (or into Non-Goals) as they're resolved. -->
