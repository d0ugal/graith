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

When an agent hits a confusing "permission denied", there are two different
questions you might be asking — and `gr sandbox` has a subcommand for each:

| Question | Command | Needs |
|----------|---------|-------|
| *Would* this access be allowed? (predictive) | `gr sandbox explain` | a policy **oracle** → `nono` |
| What *did* the sandbox actually deny? (retrospective) | `gr sandbox watch` | an OS **denial log** → macOS |

Neither launches an agent or changes anything. The split is by capability, not
by backend: a future backend with an oracle joins `explain`, and a future OS
audit log joins `watch`.

### `gr sandbox explain` — the policy oracle (predictive)

`explain` tells you whether a given access *would* be allowed or denied under
your configured policy, before you run anything. It builds the exact profile
graith would generate from your config and asks the backend's policy oracle,
then renders the decision.

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

`explain` needs a **policy oracle**, which today only the `nono` backend
provides. On a `safehouse` config it errors and points you at `gr sandbox
watch`, since a Seatbelt policy can only be inspected by observing what it
actually blocked.

The answer reflects graith's generated profile (including the
`environment.allow_vars` allowlist), so it is a faithful preview of what a real
session would enforce. Note that a read-only `read_dirs`/`read_files` grant
at or under `/tmp` or `$TMPDIR` is rejected up front (see the
[default-writable caveat]({{< relref "how-it-works.md#the-tmp-default-writable-caveat" >}})), so `gr sandbox
explain` returns that config error rather than a decision — another reason to
keep read-only sandbox paths outside `/tmp`.

### `gr sandbox watch` — the denial log (retrospective)

`watch` shows the denials the OS actually recorded. The macOS Seatbelt sandbox
logs every denial to the unified log; `watch` taps it and shows you exactly
which paths and operations were blocked. This is the practical way to debug a
confusing "permission denied", and the **only** way under `safehouse` (which has
no oracle).

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

`watch` **live-tails by default on a terminal**; `--recent` switches to an
aggregated window view (identical denials collapse into one row with a repeat
count, so a loop hammering the same blocked path reads as `  42× file-read-data
/path [node]` rather than 42 lines). Passing `--since` implies `--recent`. Add
`--json` for machine-readable output: live mode emits one NDJSON object per
denial as it arrives; `--recent` emits the aggregate.

When output is **not a terminal** (piped, or in `--json`/agent mode), `watch`
defaults to `--recent` instead of live — otherwise a non-interactive caller
would tail forever with no way to stop it. Pass `--follow` (`-f`) to force a
live tail there.

Passing a **session name** scopes denials to that session's process tree:

- Live (the default on a terminal), membership is re-checked against a
  short-lived process-tree snapshot, so a subprocess the agent spawns *after*
  you start watching is attributed once the snapshot catches it. This scopes
  more reliably than `--recent`, but it isn't perfect: a child that is denied
  and exits before the snapshot sees it can't be attributed (the macOS log
  carries no session identity), and over a long watch a recycled PID could be
  misattributed.
- With `--recent`, graith can only match against the process tree as it exists
  **now**. Denials from children that have since exited are therefore missing,
  and a recycled PID can be misattributed. `gr sandbox watch <session> --recent`
  prints a note to this effect; prefer `--proc` (or a live `watch`) when scoping
  matters.

`watch` is **macOS-only** — it relies on Seatbelt and unified logging. On Linux,
where `safehouse` isn't available at all, use `gr sandbox explain` (the `nono`
oracle) instead. The denial log reflects what the kernel *actually* blocked, so
`watch` is also useful under `nono` on macOS (which uses Seatbelt underneath).

> **Note:** `watch` reads the unified log via `/usr/bin/log`, which refuses to
> run *inside* a sandbox (`log: Cannot run while sandboxed`). Run it from your
> normal shell, not from within a sandboxed agent session.

## Limitations

- `safehouse`: macOS only; requires safehouse installed separately; cannot
  filter network egress.
- `nono`: requires the nono binary installed separately; Linux needs kernel
  5.13+ (Landlock) for filesystem enforcement and **6.7+ (Landlock ABI v4)** for
  network filtering. Credential injection is not wired up yet.
- `process-control` is a no-op under nono unless `signal_mode = "isolated"` is
  set (see [feature caveats]({{< relref "configuration.md#feature-gate-caveats" >}})).
