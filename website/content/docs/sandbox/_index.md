---
weight: 1100
title: "Sandbox"
description: "Run agents in an isolated sandbox."
icon: "shield"
toc: true
draft: false
---

graith can wrap agent processes in an enforceable OS sandbox that restricts
file access, environment, processes, signals, and — depending on backend — the network. The sandbox is enabled by default but may be disabled when you deliberately rely on agent-native controls, an external sandbox, or a VM. There are
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

The bundled agent definitions disable their native permission prompts so they
can run unattended. Clear `non_interactive_args` to use an agent's own approval
TUI; Graith does not proxy or track those prompts. When enabled, Graith's OS
sandbox confines the process regardless of what the agent believes it may do.
The kernel enforces that boundary. This also
isolates sessions from each other — without a sandbox, one agent can read
graith's `state.json` and impersonate another session.

## Choosing a backend

The `backend` field is required. The built-in default is `nono`; choose
`safehouse` explicitly on macOS if preferred. If the selected backend is absent,
unsupported, or cannot enforce the requested policy, creation and resume fail
closed with an actionable error.

- `backend = "safehouse"` on macOS if you already use safehouse.
- `backend = "nono"` on Linux (the only cross-platform option) or on macOS.

```toml
[sandbox]
enabled = true
backend = "nono"          # or "safehouse" (macOS only)
read_dirs  = ["~/Code"]
write_dirs = []
```

`enabled = false` or a per-agent `disabled = true` starts the session without
Graith's sandbox. Graith prints a one-time startup warning, logs the launch, and
reports it in `gr doctor`; it cannot verify an external sandbox or VM. An empty,
unsupported, or unavailable backend still fails closed whenever the merged
sandbox is enabled.

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

The default configuration already enables `nono`. Install the selected backend
before creating or resuming sandboxed sessions.

## Command policy is subtractive

The optional `[command_policy]` layer can synchronously deny shell commands
before execution. It works whether Graith's sandbox is enabled or disabled. An
allow only continues normal agent execution; it never grants a capability or
bypasses an enabled Graith sandbox, agent-native policy, or external isolation.
With no command policy configured, Graith adds no shell-command restriction.
