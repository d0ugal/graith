---
weight: 1130
title: "Diagnostics & limitations"
description: "gr doctor checks, gr sandbox explain/watch, and backend limitations."
icon: "troubleshoot"
toc: true
draft: false
---

## `gr doctor` checks

`gr doctor` reports, for the configured backend:

- **safehouse:** macOS + binary on `$PATH`.
- **nono:** the `nono` binary on `$PATH`, its version against graith's minimum,
  and the Landlock enforcement state on Linux (`FullyEnforced` /
  `PartiallyEnforced` → degraded / `NotEnforced` → fail).
- A **fail** if the sandbox is enabled with no backend selected.
- Existence of every configured `read_dirs`/`write_dirs`/`read_files`/`write_files` path.

## Debugging the sandbox

A confusing "permission denied" raises one of two questions, each with a
`gr sandbox` subcommand:

| Question | Command | Needs |
|----------|---------|-------|
| *Would* this access be allowed? (predictive) | `gr sandbox explain` | a policy **oracle** → `nono` |
| What *did* the sandbox actually deny? (retrospective) | `gr sandbox watch` | an OS **denial log** → macOS |

Neither launches an agent or changes anything; the split is by capability, not
backend.

### `gr sandbox explain` — the policy oracle (predictive)

`explain` tells you whether an access *would* be allowed or denied under your
configured policy: it builds the exact profile graith would generate, asks the
backend's policy oracle, and renders the decision.

```bash
# Would the agent be able to read your SSH key? (no — deny_credentials)
gr sandbox explain --path ~/.ssh/id_rsa --op read

# Is a write into a read-only read_dir allowed? (no — read grant only)
gr sandbox explain --path ~/Code/shared --op write

# Would an outbound connection to github.com:443 be allowed?
gr sandbox explain --host github.com --port 443

# Resolve the *merged* policy for a specific agent (global + per-agent)
gr sandbox explain --agent codex --path /etc/hosts --op read
```

`--op` is one of `read`, `write`, or `readwrite`. Add `--json` for a
machine-readable decision (`allowed`, `status`, `reason`, `details`, `source`,
`suggested_flag`).

`explain` needs a **policy oracle**, which today only `nono` provides. On
`safehouse` it errors and points you at `gr sandbox watch` — a Seatbelt policy
can only be inspected by observing what it blocked.

The answer reflects graith's generated profile (including the
`environment.allow_vars` allowlist) — a faithful preview of what a real session
enforces. A read-only `read_dirs`/`read_files` grant at or under `/tmp` or
`$TMPDIR` is rejected up front (see the
[default-writable caveat]({{< relref "how-it-works.md#the-tmp-default-writable-caveat" >}})), so `explain`
returns that config error, not a decision.

### `gr sandbox watch` — the denial log (retrospective)

`watch` shows the denials the OS actually recorded: macOS Seatbelt logs every
denial to the unified log, and `watch` taps it to show exactly which paths and
operations were blocked — the **only** way to debug under `safehouse` (no
oracle).

```bash
# Live-tail denials as they happen (Ctrl-C to stop) — the default
gr sandbox watch

# Recent denials instead, aggregated (default window 5m, most frequent first)
gr sandbox watch --recent

# Widen the window (any `log show --last` duration: 30m, 1h, 2d) — implies --recent
gr sandbox watch --since 1h

# Force a live tail even when output is piped (see the default below)
gr sandbox watch --follow

# Scope denials to one session's process tree
gr sandbox watch my-session

# Filter by process-name substring
gr sandbox watch --proc node
```

On a terminal `watch` **live-tails by default**; when output isn't a terminal
(piped, `--json`, or agent mode) it defaults to `--recent`, so a non-interactive
caller can't tail forever with no way to stop. `--recent` shows an aggregated
window where identical denials collapse into one row with a repeat count (a loop
hammering one blocked path reads as `  42× file-read-data /path [node]`, not 42
lines); `--since` implies it. `--follow` (`-f`) forces a live tail anywhere.
`--json` emits one NDJSON object per denial live, or the aggregate under
`--recent`.

A **session name** scopes denials to that session's process tree:

- Live (the terminal default) re-checks membership against a short-lived
  process-tree snapshot, so a subprocess spawned *after* you start watching is
  attributed once the snapshot catches it — more reliable than `--recent`.
- `--recent` matches only the tree as it exists **now**, so denials from
  children that have since exited are missing; `gr sandbox watch <session>
  --recent` prints a note. Prefer `--proc` (or a live `watch`) when scoping
  matters.

Neither is perfect: the macOS log carries no session identity, so a child that
exited before attribution can't be scoped, and over a long watch a recycled PID
can be misattributed.

`watch` is **macOS-only** — it relies on Seatbelt and unified logging; on Linux
(no `safehouse`) use `gr sandbox explain` (the `nono` oracle). The denial log
reflects what the kernel *actually* blocked, so `watch` also helps under `nono`
on macOS (Seatbelt underneath).

> **Note:** `watch` reads the unified log via `/usr/bin/log`, which refuses to
> run *inside* a sandbox (`log: Cannot run while sandboxed`). Run it from your
> normal shell, not a sandboxed agent session.

## Limitations

- `safehouse`: macOS only; requires safehouse installed separately; can't filter
  network egress.
- `nono`: requires the nono binary installed separately; Linux needs kernel
  5.13+ (Landlock) for filesystem enforcement and **6.7+ (Landlock ABI v4)** for
  network filtering. Credential injection isn't wired up yet.
- `process-control` is a no-op under nono unless `signal_mode = "isolated"`
  (see [feature caveats]({{< relref "configuration.md#feature-gate-caveats" >}})).
