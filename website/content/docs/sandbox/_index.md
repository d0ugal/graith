---
weight: 1100
title: "Sandbox"
description: "Run agents in an isolated sandbox."
icon: "shield"
toc: true
draft: false
---

graith can wrap agent processes in a deny-by-default OS sandbox that restricts
file access, environment, and — depending on backend — the network. There are
two backends:

| Backend | Platforms | Primitive |
|---------|-----------|-----------|
| `safehouse` | macOS only | `sandbox-exec` / Seatbelt (via [safehouse](https://github.com/eugene1g/agent-safehouse)) |
| `nono` | **Linux + macOS** | [nono](https://github.com/nolabs-ai/nono): Landlock LSM + seccomp on Linux, Seatbelt on macOS |

This page covers why to sandbox, choosing a backend, and setup. See the
sub-pages for [how it works]({{< relref "how-it-works.md" >}}),
[configuration]({{< relref "configuration.md" >}}), and
[diagnostics]({{< relref "debugging.md" >}}).

## Why sandbox

AI coding agents often request broad permissions (e.g.
`--dangerously-skip-permissions` for Claude,
`--dangerously-bypass-approvals-and-sandbox` for Codex). Sandboxing lets you
grant those agent-level permissions while confining the process at the OS level.
The agent thinks it has full access; the kernel enforces boundaries. This also
isolates sessions from each other — without a sandbox, one agent can read
graith's `state.json` and impersonate another session.

## Choosing a backend

The `backend` field is **required** when the sandbox is enabled — there is no
default. If you enable the sandbox without choosing a backend, session creation
fails closed with an actionable error. Pick:

- `backend = "safehouse"` on macOS if you already use safehouse.
- `backend = "nono"` on Linux (the only cross-platform option) or on macOS.

```toml
[sandbox]
enabled = true
backend = "nono"          # or "safehouse" (macOS only)
read_dirs  = ["~/Code"]
write_dirs = []
```

> **Migration (pre-1.0 breaking change).** Earlier versions defaulted to
> safehouse implicitly. `backend` is now required when `sandbox.enabled = true`.
> **To keep your current behaviour, add `backend = "safehouse"` to your
> `[sandbox]` block.** `gr doctor` flags a missing backend.

## Setup

### safehouse (macOS)

```bash
brew install eugene1g/safehouse/agent-safehouse
gr doctor            # checks safehouse is on $PATH
```

### nono (Linux / macOS)

```bash
# Homebrew (macOS or Linuxbrew)
brew install nono
# or download the pinned release and verify its provenance before installing:
#   https://github.com/nolabs-ai/nono/releases
#   gh attestation verify <tarball> --repo nolabs-ai/nono

gr doctor            # checks the nono binary, its version, and Landlock support
```

nono needs Linux kernel **5.13+** for Landlock filesystem enforcement (its
practical floor is **5.14+**, which it uses for the seccomp supervisor-notify
layer); network filtering, when graith grows it, needs 6.7+. On macOS, nono uses
Seatbelt, which is always present. graith requires a minimum nono version and
refuses to run below it (see `gr doctor`).

Then enable in config with a backend, as above.
