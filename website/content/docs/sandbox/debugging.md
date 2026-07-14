---
weight: 1130
title: "Diagnostics & limitations"
description: "gr doctor checks, gr sandbox why, and backend limitations."
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

## Debugging denials with `gr sandbox why`

When an agent hits a confusing "permission denied", `gr sandbox why` explains
whether a given access *would* be allowed or denied under your configured
policy — without launching an agent or changing anything. It builds the exact
nono profile graith would generate from your config and asks nono's policy
oracle (`nono why`), then renders the decision.

```bash
# Would the agent be able to read your SSH key? (no — deny_credentials)
gr sandbox why --path ~/.ssh/id_rsa --op read

# Is a write into a read-only read_dir allowed? (no — read grant only)
gr sandbox why --path ~/Code/shared --op write

# Would an outbound connection to github.com:443 be allowed?
gr sandbox why --host github.com --port 443

# Resolve the *merged* policy for a specific agent (global + per-agent)
gr sandbox why --agent codex --path /etc/hosts --op read
```

`--op` is one of `read`, `write`, or `readwrite`. Add `--json` for a
machine-readable decision (`allowed`, `status`, `reason`, `details`, `source`,
`suggested_flag`). The command **only targets the `nono` backend** — it is the
only backend with a policy oracle; on a `safehouse` config it returns an error.

The answer reflects graith's generated profile (including the
`environment.allow_vars` allowlist), so it is a faithful preview of what a real
session would enforce. Note that a read-only `read_dirs`/`read_files` grant
at or under `/tmp` or `$TMPDIR` is rejected up front (see the
[default-writable caveat]({{< relref "how-it-works.md#the-tmp-default-writable-caveat" >}})), so `gr sandbox
why` returns that config error rather than a decision — another reason to keep
read-only sandbox paths outside `/tmp`.

## Limitations

- `safehouse`: macOS only; requires safehouse installed separately; cannot
  filter network egress.
- `nono`: requires the nono binary installed separately; Linux needs kernel
  5.13+ (Landlock) for filesystem enforcement and **6.7+ (Landlock ABI v4)** for
  network filtering. Credential injection is not wired up yet.
- `process-control` is a no-op under nono unless `signal_mode = "isolated"` is
  set (see [feature caveats]({{< relref "configuration.md#feature-gate-caveats" >}})).
