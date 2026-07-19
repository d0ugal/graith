---
weight: 1110
title: "How it works"
description: "How graith builds and enforces the sandbox policy."
icon: "engineering"
toc: true
draft: false
---

When `sandbox.enabled = true`, the daemon resolves the merged sandbox policy
(global + per-agent), expands `~` and globs to absolute paths, and wraps the
agent command with the selected backend before spawning it under the PTY.

**safehouse** runs `safehouse wrap ... -- <agent>` (macOS `sandbox-exec`):
denies file access by default, allows the worktree (read-write) plus
`read_dirs`/`write_dirs` (with the single-file grants `read_files`/`write_files`
folded into the same read-only / read-write path lists), strips the environment
to an allowlist, and gates `features`.

**nono** generates a per-session JSON profile and runs
`nono run --profile <file> --workdir <dir> -- <agent>` (`--workdir` pins nono's
read-write workdir to the session's worktree/scratch dir rather than resolving
it from the process cwd — important for `--mirror`, where the cwd is the
read-only source). The profile:

- `extends: "default"` — inherits nono's audited deny groups (`deny_credentials`,
  `deny_shell_history`, `deny_shell_configs`, browser/keychain denies) and base
  system-read paths, so credentials and shell history are denied out of the box.
- worktree → `filesystem.allow` (read+write) + `workdir: readwrite`
- `write_dirs` → `filesystem.allow` (read+write). graith deliberately does **not**
  use nono's `filesystem.write`, which is *write-only* (no read-back or delete)
  and would break agents that read files they just wrote.
- `read_dirs` → `filesystem.read` (read-only)
- `write_files` → `filesystem.allow_file` (read+write, single file) and
  `read_files` → `filesystem.read_file` (read-only, single file). These grant
  individual files for paths that can't be a directory grant without
  over-sharing — most importantly single files directly in `$HOME` such as an
  agent's `~/.claude.json` login file. An explicit file grant also punches
  through the inherited `deny_credentials` group, exactly what a login/token file
  needs. See [File grants](#file-grants) below.
- the env allowlist → `environment.allow_vars`, including `PATH`, `HOME`, and
  graith's injected `GRAITH_*` vars plus your configured keys. This block is
  **always emitted** for the nono backend, even when the allowlist is empty:
  nono scrubs the environment down to exactly the allowlist, so any variable not
  listed (including host secrets) is stripped and can't leak into the agent
  (mirroring safehouse's env allowlist). Env is scrubbed by default — it fails
  closed. Omitting the block would make nono inherit the daemon's entire
  environment, so graith never omits it.
- the **agent binary's directory** → `filesystem.read`. nono doesn't auto-grant
  the launched command's location (only system paths like `/usr/bin`), so graith
  resolves the agent command via `$PATH` and grants read on its directory.
- feature `ssh` → `filesystem.unix_socket` for `$SSH_AUTH_SOCK` (agent socket)
- feature `ssh-keys` → `filesystem.read` **and** `filesystem.bypass_protection`
  for `~/.ssh` (read-only raw key-file access; opt-in on top of `ssh`, which
  stays agent-socket-only). `~/.ssh` is in nono's required `deny_credentials`
  group, so the read grant alone is a no-op — `bypass_protection` relaxes that
  deny while the read grant provides the (read-only) access.

The profile is written under graith's runtime dir (readable inside the sandbox)
and lives for the session's lifetime, resume included.

### The `/tmp` default-writable caveat

nono's built-in `system_write_linux` group makes `/tmp`, `$TMPDIR`,
`/dev/null`, and `/proc/self/fd` writable by default, and nono **can't** carve a
read-only exception out of a writable prefix: on Linux, Landlock has no
deny-under-an-allowed-parent (a deny overlapping the inherited `/tmp` allow is a
hard validation error); on macOS, a Seatbelt `deny` removes read as well as
write, making the path unreadable. Either way, a read-only grant under `/tmp` or
`$TMPDIR` can't be enforced.

Rather than emit a profile that breaks that promise, graith **rejects** a
read-only `read_dirs`/`read_files` grant under those prefixes with a clear config
error and fails closed (session creation aborts). Paths meant to be writable (the
worktree, `write_dirs`) are exempt — a read grant within a region you made
writable on purpose is accepted. graith's own data dir defaults to
`~/.local/share/graith` (not `/tmp`), so this only bites custom configs that
point read-only policy paths at `/tmp` or `$TMPDIR`: move them elsewhere, or
grant them as writable paths.

### File grants

`read_dirs`/`write_dirs` grant whole directories (recursive). `read_files` and
`write_files` grant **individual files** — use them for paths that can't be a
directory grant without over-sharing, most importantly single files directly in
`$HOME`, where granting the parent would expose every dotfile secret.

The motivating case is agent login state. Claude Code stores its OAuth login in
`~/.claude.json` (plus `~/.claude.json.lock` / `~/.claude.lock`). Granting `~`
would expose `.ssh`, `.aws`, `.env` files, and so on; granting just the files
keeps the blast radius to the login file. Since the agent rewrites that file to
refresh tokens, it needs **read+write** (`write_files`), not read-only:

```toml
[agents.claude.sandbox]
write_files = ["~/.claude.json", "~/.claude.json.lock", "~/.claude.lock"]
```

Without this, a sandboxed Claude agent starts **logged out** — `~/.claude`
(the directory) is granted, but the login lives in the sibling file
`~/.claude.json`, and nono's inherited `deny_credentials` group blocks it. An
explicit `write_files` grant both allows the file and overrides that deny.

`read_files` is the read-only equivalent (nono `filesystem.read_file`), for
single files an agent only reads (e.g. a shared `~/.gitconfig`). As with
`write_dirs`, `write_files` maps to nono's read+write `filesystem.allow_file`,
never its write-only `filesystem.write_file`.

Unlike `read_dirs`/`write_dirs` (dropped if the directory doesn't exist), file
grants are **not** existence-checked: a `write_files` entry for a file that
doesn't exist yet is kept, so the agent can create it at runtime. This is
required for lockfiles like `~/.claude.json.lock` — they only appear while a
write is in flight, so their grants must survive a session start when the file
is absent.

## Config-only enforcement

Sandboxing is **config-only** — there are no CLI flags to enable or disable it.
This stops a sandboxed agent from spawning a child process that escapes the
sandbox; Landlock/Seatbelt restrictions are inherited by all descendants. Set
`[sandbox] enabled = false` (globally or via the per-agent merge) only when you
deliberately rely on agent-native controls, an external sandbox, or a VM. Graith
prints one warning per successful unsandboxed start and reports the configuration
in `gr doctor`; it can't verify external isolation.

## Fail closed

An explicitly disabled sandbox is allowed and visible. Once the merged sandbox
is enabled, inability to enforce it refuses session creation or resume — Graith
never silently downgrades an enabled policy. The rules:

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
