---
weight: 1110
title: "How it works"
description: "How graith builds and enforces the sandbox policy."
icon: "engineering"
toc: true
draft: false
---

When `sandbox.enabled = true`, the daemon resolves the merged policy (global +
per-agent), expands `~` and globs to absolute paths, and wraps the agent command
with the selected backend before spawning it under the PTY.

**safehouse** runs `safehouse wrap ... -- <agent>` (macOS `sandbox-exec`):
denies file access by default, allows the worktree (read-write) plus
`read_dirs`/`write_dirs` (single-file `read_files`/`write_files` fold into those
read-only/read-write lists), strips the environment to an allowlist, and gates
`features`.

**nono** generates a per-session JSON profile and runs
`nono run --profile <file> --workdir <dir> -- <agent>`. `--workdir` pins nono's
read-write workdir to the session's worktree/scratch dir, not the process cwd —
important for `--mirror`, where the cwd is the read-only source. The profile:

- `extends: "default"` — inherits nono's audited deny groups (`deny_credentials`,
  `deny_shell_history`, `deny_shell_configs`, browser/keychain denies) and base
  system-read paths, denying credentials and shell history out of the box.
- worktree → `filesystem.allow` (read+write) + `workdir: readwrite`
- `write_dirs` → `filesystem.allow` (read+write). graith avoids nono's
  `filesystem.write`, which is *write-only* (no read-back or delete) and
  would break agents that read files they just wrote.
- `read_dirs` → `filesystem.read` (read-only)
- `write_files` → `filesystem.allow_file` (read+write, single file);
  `read_files` → `filesystem.read_file` (read-only, single file), for individual
  files. An explicit file grant also punches through the inherited
  `deny_credentials` group. See [File grants](#file-grants).
- the env allowlist → `environment.allow_vars` — `PATH`, `HOME`, graith's
  injected `GRAITH_*` vars, plus your configured keys. **Always emitted** for
  nono, even when empty: nono scrubs the environment to exactly the allowlist, so
  any unlisted variable (including host secrets) is stripped. Omitting the block
  would make nono inherit the daemon's entire environment.
- the **agent binary's directory** → `filesystem.read`. nono auto-grants only
  system paths like `/usr/bin`, so graith resolves the agent command via `$PATH`
  and grants its directory.
- feature `ssh` → `filesystem.unix_socket` for `$SSH_AUTH_SOCK` (agent socket)
- feature `ssh-keys` → `filesystem.read` **and** `filesystem.bypass_protection`
  for `~/.ssh` (read-only raw key-file access; opt-in on top of `ssh`, which
  stays agent-socket-only). `~/.ssh` is in nono's required `deny_credentials`
  group, so the read grant alone is a no-op — `bypass_protection` relaxes that
  deny.

The profile lives under graith's runtime dir (readable in the sandbox) for the
session's lifetime, resume included.

### The `/tmp` default-writable caveat

nono's built-in `system_write_linux` group makes `/tmp`, `$TMPDIR`, `/dev/null`,
and `/proc/self/fd` writable by default, and nono **can't** carve a read-only
exception from a writable prefix: on Linux, Landlock has no
deny-under-an-allowed-parent (a deny overlapping the inherited `/tmp` allow is a
hard validation error); on macOS, a Seatbelt `deny` removes read too. Either way,
a read-only grant under `/tmp` or `$TMPDIR` can't be enforced.

graith **rejects** a read-only `read_dirs`/`read_files` grant under those
prefixes and fails closed (creation aborts). Writable paths (the worktree,
`write_dirs`) are exempt. graith's data dir defaults to `~/.local/share/graith`
(not `/tmp`), so this only bites custom configs pointing read-only paths at
`/tmp` or `$TMPDIR`: move them, or grant them as writable.

### File grants

`read_dirs`/`write_dirs` grant whole directories (recursive); `read_files` and
`write_files` grant **individual files** — for single files, most importantly in
`$HOME`, where granting the parent would over-share.

The main case is agent login: Claude Code stores its OAuth login in
`~/.claude.json` (plus `~/.claude.json.lock` / `~/.claude.lock`). Granting `~`
would expose `.ssh`, `.aws`, `.env`; granting just the files keeps the blast
radius to the login file. The agent rewrites that file to refresh tokens, so it
needs **read+write** (`write_files`):

```toml
[agents.claude.sandbox]
write_files = ["~/.claude.json", "~/.claude.json.lock", "~/.claude.lock"]
```

Without this, a sandboxed Claude agent starts **logged out**: `~/.claude` (the
directory) is granted, but the login lives in the sibling file `~/.claude.json`,
blocked by the inherited `deny_credentials` group — which the `write_files` grant
overrides.

`read_files` is the read-only equivalent (nono `filesystem.read_file`), for
single files an agent only reads (e.g. a shared `~/.gitconfig`). Like
`write_dirs`, `write_files` avoids nono's write-only `filesystem.write_file`.

Unlike `read_dirs`/`write_dirs` (dropped if the directory doesn't exist), file
grants are **not** existence-checked: a `write_files` entry for a not-yet-created
file is kept so the agent can create it at runtime — required for lockfiles like
`~/.claude.json.lock`, which only appear while a write is in flight.

## Config-only enforcement

Sandboxing is **config-only** — no CLI flags enable or disable it, so a sandboxed
agent can't spawn a child that escapes; Landlock/Seatbelt restrictions are
inherited by all descendants. Disable it only via `[sandbox] enabled = false`
(globally or per-agent); see [Fail closed](#fail-closed).

## Fail closed

An explicitly disabled sandbox is allowed and visible. Once the merged sandbox is
enabled, inability to enforce it refuses creation or resume — Graith never
silently downgrades an enabled policy:

| Condition | Result |
|-----------|--------|
| `enabled = false` | **Runs without Graith's sandbox**, with a startup warning and `gr doctor` diagnostic. |
| `backend` unset | **Hard error** — choose `safehouse` or `nono`. |
| Backend binary not on `$PATH` | **Hard error** (with an install hint). |
| nono version below the required minimum | **Hard error** (profile schema may not match). |
| Linux kernel too old for Landlock (`NotEnforced`) | **Hard error** — a warning here would be a fail-open regression. |
| Landlock present but no network-filtering ABI (`PartiallyEnforced`), **no** network policy set | **Runs** — filesystem confinement holds. `gr doctor` notes the degraded state. |
| Landlock present but no network-filtering ABI (`PartiallyEnforced`), **network policy set** | **Hard error** — filtering egress needs Landlock ABI v4 (kernel 6.7+); graith refuses rather than pretend to block. |
| Network policy set with `backend = "safehouse"` | **Hard error** — safehouse has no network primitive; use `nono`. |
| safehouse selected on non-macOS | **Hard error.** |
