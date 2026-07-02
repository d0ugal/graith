---
title: "Design Doc: nono Sandbox Backend (cross-platform)"
authors: Dougal Matthews
created: 2026-07-02
status: Draft
reviewers: (none yet)
informed: (TBD)
---

# nono Sandbox Backend (cross-platform)

## Background

graith can wrap each agent process in an OS-level sandbox so an agent can only
touch the files (and, in future, the network) it is meant to. Today this is
implemented by a single backend, `safehouse`, in `internal/sandbox/sandbox.go`.
`sandbox.Wrap` turns graith's merged sandbox policy into a `safehouse wrap`
command line:

- `--workdir <worktree>` — the session's git worktree (read-write)
- `--add-dirs-ro <a:b:c>` — colon-joined read-only directories (`read_dirs`)
- `--add-dirs <a:b:c>` — colon-joined read-write directories (`write_dirs`)
- `--enable <ssh,clipboard,...>` — feature gates (`features`)
- `--env-pass <KEY,KEY>` — **environment-variable allowlist** (`WrapOpts.EnvKeys`)
- `-- <agent> <args...>` — the sandboxed command

`safehouse` is a thin wrapper over macOS `sandbox-exec`/Seatbelt.
`sandbox.Available()` and `sandbox.AvailableCommand()` both short-circuit to
`false` on any non-`darwin` GOOS, so **the sandbox only ever engages on macOS**.
`WrapOpts` also carries a `SafehouseCommand` field (the backend binary name);
the backend abstraction below renames/generalises this — see Proposal §1.

`validatePaths` in `sandbox.go` rejects any path containing a colon, because
safehouse colon-joins its dir lists. That validator is safehouse-specific.

The daemon owns policy resolution. The relevant config types live in
`internal/config/config.go`:

- `SandboxConfig{ Enabled, Disabled *bool, Command, Features, ReadDirs, WriteDirs }`
  — the `[sandbox]` block and each `[agents.*.sandbox]` block.
- `SandboxConfig.Merge(agent)` — OR the enabled flags, dedup and concatenate
  `Features`/`ReadDirs`/`WriteDirs`, let the agent override `Command`, and let
  an agent `disabled = true` force the whole thing off.
- `OrchestratorSandboxConfig{ ReadDirs, WriteDirs }` — a deliberately narrow
  third layer (the orchestrator can only *add directories*, never features or
  the command; see `2026-06-02-orchestrator-sandbox-config.md`).
- `OrchestratorSandboxMerged(agentName)` — global ⊕ agent ⊕ orchestrator dirs.

At session create/resume/fork the daemon calls `resolveSandbox` →
`resolveSandboxFromConfig`, which merges the layers, returns `false` when the
sandbox is disabled, and **fails closed** — returns an error — when the sandbox
is enabled but the backend command is not available. It then builds a
`sandbox.WrapOpts` in `sandboxOptsFromConfig` (expanding `~` **and globs** via
`expandPaths`, adding graith's own hook dir / config dir / `gr` binary dir /
runtime dir as read paths) and calls `sandbox.Wrap`. Paths are `~`-expanded and
glob-expanded *before* they reach `Wrap`, so a backend receives concrete
absolute paths. The resolved policy is persisted on `SessionState`
(`Sandboxed`, `SandboxConfig`) so resume re-derives it and `gr doctor` can
report drift. `gr doctor` (`internal/cli/doctor.go`, `checkEnvironment` +
`checkSandboxPaths`) reports whether the sandbox is enabled, whether we are on
macOS, whether `safehouse` is on `PATH`, and whether the configured dirs exist.

[nono](https://github.com/nolabs-ai/nono) (Apache-2.0, by **nolabs.ai** — built
by the team that brought you Sigstore) is a cross-platform, daemon-less agent
sandbox. Its Linux backend uses **Landlock LSM** as the hard filesystem-
enforcement floor, with **seccomp** as a *supervisor-notify* layer above it
(capability elevation, network fallback when the Landlock ABI predates v4,
AF_UNIX mediation, trust prompts) — not a co-equal base FS mechanism. Its macOS
backend is **Seatbelt/`sandbox_init`** — the same primitive safehouse uses.
nono is **pre-1.0** (the project states APIs are stabilising but may still
change); this doc pins facts to nono **v0.66.0** and the `nono-go` binding read
at commit `875bc26` unless noted.

## Problem

The sandbox is macOS-only. On Linux (and Windows) `sandbox.Available()` returns
`false`, so:

- If a user sets `sandbox.enabled = true` on Linux, `resolveSandboxFromConfig`
  fails closed and **session creation errors out** — there is no way to get a
  working sandbox at all.
- If they leave it disabled (the only option that works), agents run
  unconfined: they can read `~/.ssh`, cloud credentials, the whole home
  directory, and — because graith's own `state.json` is readable — one agent
  can read another session's auth token and impersonate it (this is exactly the
  risk `gr doctor` warns about when the sandbox is off with >1 running session).

graith wants least-privilege isolation on every platform it runs on, not just
macOS. safehouse can't provide that because it has no Linux backend. nono can:
one backend covers macOS, Linux, and Windows (WSL2, with caveats — see §C8).
This is the Phase-1 target of the pluggable-sandbox epic (#631), tracked as
#632.

## Threat model

Every "fail-closed" and "least-privilege" claim below traces back to this
model. It is deliberately conservative: the sandboxed agent is untrusted.

**Adversary.** A coding agent that has been prompt-injected or otherwise
subverted (by a hostile file in the repo, a poisoned dependency, or a malicious
instruction), running as *the same OS user* as the daemon. It can run arbitrary
code and spawn children, but only within whatever the OS sandbox permits. We do
**not** model a remote attacker or a different-uid attacker.

**Protected assets.**

- Host credentials outside the worktree: `~/.ssh`, cloud/API tokens, keychains,
  browser data, shell history/config (which can leak API keys).
- **Cross-session isolation:** graith's own `state.json` holds each session's
  `GRAITH_TOKEN`. An unconfined agent can read it and impersonate a sibling
  session (auth bypass). Confining the FS to the worktree + declared dirs closes
  this.
- The integrity of files outside the declared write set.

**Trust boundary.** The kernel (Landlock on Linux, Seatbelt on macOS) is the
enforcement boundary. Once nono applies the ruleset in the child before exec,
the agent and every descendant it spawns inherit the restriction irreversibly —
so an agent cannot escape by spawning an "unsandboxed" child. The daemon itself
stays outside the sandbox (it must, to manage other sessions); it never runs
untrusted code.

**Explicit non-defenses (out of scope).**

- **Kernel 0-days / Landlock or Seatbelt bypass.** If the kernel enforcement is
  buggy, the sandbox is bypassable. We rely on the OS primitive; we do not add a
  second containment layer (that's a VM/container job — nono's own docs say so).
- **A malicious `nono` binary on `PATH`.** graith execs whatever `nono` is
  found. A tampered binary defeats the sandbox. Mitigation is operational
  (trust your `PATH`, pin a version, verify the install) — see §B4.
- **Egress / exfiltration.** v1 emits no network policy (matches safehouse
  today): a compromised agent with network access can exfiltrate anything it can
  read. Network confinement and credential-leak-via-network are deferred to
  Phase 2/3 (§6, §C7, ties to #596).
- **In-process side channels** between same-uid processes (nono's security model
  explicitly does not defend same-uid process-to-process attacks beyond the FS
  boundary).

## Goals

- Add a second `SandboxBackend`, `nono`, that runs the agent under nono and
  works on **Linux (Landlock + seccomp) and macOS (Seatbelt)**.
- Compile graith's already-merged sandbox policy into a nono policy: worktree
  read-write, plus `read_dirs`/`write_dirs`, an **environment allowlist that
  matches safehouse's `--env-pass` semantics** (no env-leak regression), a
  best-effort mapping of graith `features` onto nono capabilities, and a network
  allowlist if/when the config grows one.
- Make the backend **selectable via config** (a new `backend` key), keep
  existing safehouse behaviour byte-for-byte unchanged.
- Preserve and *sharpen* the **fail-closed** contract: if the selected backend
  cannot enforce on this host (binary missing, kernel too old, wrong version)
  when the sandbox is enabled, session creation fails — it never silently
  downgrades to unconfined or partially-confined-without-saying-so (§B2).
- Extend `gr doctor` to detect the `nono` binary, assert a minimum nono version,
  and report the Landlock enforcement state on Linux.

### Non-Goals

- **Not removing or replacing safehouse.** It stays the macOS-native option.
  This is additive.
- **Not shipping or vendoring nono itself.** Like safehouse today, nono is an
  external tool the user installs (`brew install nono` /
  `curl -fsSL https://nono.sh/install.sh | sh`). graith detects it.
- **Not wiring up credential injection in v1.** nono's `--credential`/proxy
  injection overlaps with graith #596 and needs a joined-up design (§C7, Open
  Questions). v1 maps filesystem + env allowlist + feature gates only.
- **Not adopting nono's registry / profile-authoring workflow as a user-facing
  surface.** graith generates an ephemeral profile from its own config; users
  configure the sandbox through graith's TOML.
- **Not adding a network policy field to graith's config in this doc.** The
  proposal describes where a future `network` allowlist plugs in; adding the
  surface is deferred.
- **No changes to the protocol schema** beyond one new persisted `Backend`
  field on `SandboxConfig`, the doctor output, and one new config key.
- **Cross-backend feature parity is a stated non-goal for v1** where nono's
  primitive genuinely differs (see `process-control`, §C5) — divergences are
  documented rather than papered over.

## Proposal

### 1. Extract a `SandboxBackend` interface

Today `internal/sandbox` is a package of free functions hard-wired to
safehouse. Introduce a small interface so the daemon can select a backend:

```go
// Backend turns a resolved graith sandbox policy into a wrapped command.
type Backend interface {
    // Name is the config value that selects this backend ("safehouse", "nono").
    Name() string
    // Available reports whether this backend can *enforce* on this host right
    // now. It distinguishes "cannot enforce at all" (→ hard error upstream)
    // from degraded-but-enforcing; see WrapResult / §B2.
    Availability() Availability
    // Wrap turns (command, args) + policy into the actual command to exec,
    // plus any ephemeral artifacts (e.g. a generated profile file) the caller
    // must clean up.
    Wrap(command string, args []string, opts WrapOpts) (WrapResult, error)
}

type Availability struct {
    CanEnforce bool     // false ⇒ resolveSandbox must fail closed
    Degraded   bool     // enforcing, but some requested controls unavailable
    Detail     string   // human-readable ("Landlock ABI v2, no net filtering")
}

type WrapResult struct {
    Command string
    Args    []string
    Cleanup func() error // remove generated profile temp file, if any
}
```

`WrapOpts` already carries everything a backend needs (`WorktreeDir`,
`ReadDirs`, `WriteDirs`, `Features`, `EnvKeys`); its backend-binary field
(currently `SafehouseCommand`) is **renamed to `BackendCommand`** since it now
means "the binary for whichever backend is selected". The existing safehouse
logic moves behind a `safehouseBackend` implementing this interface, producing
byte-identical output (its `Availability` is `{CanEnforce: darwin && onPATH}`).

A `Backends` registry maps name → backend so `resolveSandboxFromConfig` can look
up the configured backend and apply the fail-closed rules in §B2.

### 2. Backend selection via config

Add one field to `SandboxConfig`:

```go
type SandboxConfig struct {
    Enabled   bool
    Disabled  *bool
    Backend   string   // "" | "safehouse" | "nono"  (see §C6 for default)
    Command   string   // path/name override for the backend binary
    Features  []string
    ReadDirs  []string
    WriteDirs []string
}
```

- `Merge` treats `Backend` like `Command`: an agent may override it, otherwise
  it inherits from global. The orchestrator layer stays narrow (dirs only).
- `Command` keeps meaning "the binary name/path for the selected backend";
  empty ⇒ each backend uses its own default (`safehouse` / `nono`).
- The persisted `SessionState.SandboxConfig` gains `Backend` so resume
  re-derives the same backend and doctor can report backend drift.

Default resolution (`Backend == ""`) is a **decision that needs sign-off** — see
§C6. This doc recommends a **platform-aware default** (nono on Linux, safehouse
on macOS) so the "least-privilege on every platform" goal holds out of the box,
but flags it because it changes behaviour for a Linux user who enables the
sandbox without naming a backend.

Config example:

```toml
[sandbox]
enabled = true
backend = "nono"          # Landlock+seccomp on Linux, Seatbelt on macOS
read_dirs = ["~/Code/shared"]
write_dirs = ["~/.cache/agent"]
features = ["ssh"]
```

### 3. Shell out to `nono` vs link `nono-go` (recommendation: shell out)

**Option A — shell out to the `nono` binary** (`nono run --profile <file> -- <agent>`):

- Matches how safehouse works today (`Wrap` returns a command to exec). Minimal
  new machinery; no cgo; graith stays pure-Go and cross-compiles cleanly.
- nono is installed like safehouse (external tool on `PATH`), so detection /
  fail-closed / `gr doctor` are symmetric.
- nono applies the sandbox in the child before exec; Landlock/Seatbelt
  restrictions are inherited by all descendants — preserving graith's
  "config-only, no escape hatch" property.

**Option A cons (must be acknowledged, §C2):**

- An extra process in the tree (nono supervisor → agent). For `nono run` this is
  intentional (the supervisor is needed for future proxy/network/credential
  modes; `nono wrap` removes itself but can't do those — so we choose `run`).
- **No compile-time API contract.** A `nono` CLI change (renamed flag, changed
  profile schema, different exit code) breaks graith *silently at runtime*, not
  at build time. Mitigations: pin a **minimum nono version** and assert it in
  `gr doctor` and at wrap time (§B4); parse nono's structured/JSON output where
  available rather than scraping human text; treat any non-zero nono exit before
  the agent starts as a fail-closed session error.
- Error handling is stderr/exit-code based, not typed Go errors.

**Option B — link `nono-go`** (`github.com/nolabs-ai/nono-go`):

- cgo package with bundled static libs for darwin/linux × amd64/arm64 (Go 1.24+,
  needs a C toolchain). API: `caps := nono.New(); caps.AllowPath(path, mode);
  caps.SetNetworkMode(mode); nono.Apply(caps)`. `Apply` is **irreversible,
  applies to the current process and all children, and frees the capability set
  on success** (a subsequent mutation is a no-op).
- Because `Apply` sandboxes *the calling process*, correct use means applying in
  a child between fork and exec — re-implementing what the `nono` binary already
  does — and cgo breaks the clean cross-compile / GoReleaser matrix.

**Recommendation: Option A (shell out) for v1.** Simpler, pure-Go, mirrors
safehouse, kernel-inherited enforcement. Its runtime-coupling risk is bounded by
the version pin + doctor assertion. `nono-go` becomes attractive only if we
later want in-process policy *introspection* (`QueryContext` answers "would this
path be allowed?" without applying) for a `gr why`-style command — not needed to
run agents.

### 4. Compiling graith policy → a nono profile

**Decision (revised from the original draft): v1 generates a per-session nono
profile file, not ad-hoc `run` flags.** The reason is decisive and is a genuine
finding of this design pass:

> nono has **no `run` flag to restrict the environment**. By default a
> sandboxed process **inherits *all* environment variables** from the parent.
> Restricting env is a *profile-only* field (`environment.allow_vars`, which
> clears the env and passes through only the allowlist). safehouse today gives
> agents an env **allowlist** via `--env-pass`/`WrapOpts.EnvKeys`. If the nono
> backend used ad-hoc flags it could not honour `EnvKeys`, so every host env var
> (including any API keys in the daemon's environment) would leak into the
> agent — a **credential-leak regression versus safehouse**. Env filtering must
> therefore be in a generated profile, in v1, not deferred.

The same is true for the ssh AF_UNIX socket grant under a restricted network
mode and for the `security.*` modes behind `process-control`/`clipboard`. So the
profile file is the right substrate for all of v1's mappings, and it also lets
graith inherit nono's audited baseline (see below).

**Profile shape** (JSONC; format per nono `profile-authoring` docs), written to
a per-session temp file and passed as `nono run --profile <path> -- <agent>`,
removed via `WrapResult.Cleanup`:

```jsonc
{
  "extends": "default",                 // inherits nono's audited deny/allow groups
  "meta": { "name": "graith-<session-id>" },
  "workdir": { "access": "readwrite" }, // the session worktree
  "filesystem": {
    "allow": ["<worktree>", "<write_dirs...>"],   // read+write recursive
    "read":  ["<read_dirs...>", "<graith hook/config/bin/runtime dirs...>"]
  },
  "environment": {
    "allow_vars": ["<EnvKeys...>"]      // mirrors safehouse --env-pass exactly
  }
  // "security"/"network" added by later phases / feature mapping (§5, §6)
}
```

**Why `extends: "default"` matters.** nono's `default` profile is always merged
in and ships audited deny groups — confirmed present in v0.66.0:
`deny_credentials` (SSH keys, cloud creds, GPG, container/package tokens),
`deny_keychains_macos`/`deny_keychains_linux`, `deny_browser_data_macos`/
`deny_browser_data_linux`, `deny_macos_private`, `deny_shell_history`,
`deny_shell_configs`, plus base `system_read_*`/`system_write_*`. graith
therefore does **not** enumerate `/usr/lib`, `/etc`, etc., and gets
credential/history denial for free. This baseline is a **trust dependency** of
graith's least-privilege story: if nono changes these groups, graith's guarantees
shift. Mitigation: pin a minimum nono version (§B4) and add an *enforcement*
test (§C1) that proves e.g. `~/.ssh/id_rsa` is actually denied end-to-end —
argv-shape tests do not prove the sandbox holds.

The generation code consumes the same merged `WrapOpts` the safehouse backend
uses, so the two backends share the policy input and differ only in emitter.

### 4b. argv & path handling rules

Unlike safehouse (colon-joined lists, colon-rejecting `validatePaths`), the nono
backend passes **concrete absolute paths inside a JSON profile file**, so:

- **No shell is involved** — graith uses `exec.Command` with an argv slice; the
  only nono argv entries are `run`, `--profile`, `<tempfile>`, `--`,
  `<agent>`, `<agent-args...>`. Paths live in the JSON file, not on argv, so a
  path beginning with `-` cannot be misread as a nono flag (flag-injection is
  structurally impossible for the policy paths). The agent's *own* args are
  already passed positionally after `--` exactly as today.
- **Colons are allowed** in paths (no colon-joining), so safehouse's
  colon-rejecting validator is *not* applied by the nono backend. This is a
  behaviour difference to note: a path with a colon that safehouse rejects is
  accepted by nono. A `--`-prefixed path and a colon-containing path both get an
  adversarial fixture (§C1 / testing).
- Because paths are `~`- and glob-expanded by `expandPaths` **before** `Wrap`
  (see Background), the backend receives literal directories; see §B6 for what
  the backend does with glob results and missing dirs.

### 5. Mapping graith `features` → nono capabilities

graith's README documents semantics for **`ssh`** (grants `SSH_AUTH_SOCK`
access) and **`process-control`** (allows signal sending). It lists `clipboard`
only as an *example* feature string in a config snippet — the README defines
**no** semantics for it, and there is no code mapping. So `clipboard` is an
undefined token today; nono has no clipboard capability either. Mapping (per
nono `landlock`, `profiles-groups`, `seatbelt` docs):

| graith feature | nono mapping | Phase | Confidence |
|---|---|---|---|
| `ssh` | profile `filesystem.allow_unix_socket: ["$SSH_AUTH_SOCK"]` (resolved at wrap time) — plain fs grants no longer imply socket access under restricted net — optionally `filesystem.read: ["~/.ssh"]` if key-file access is wanted (§C4) | see §C3 | Medium — socket path is dynamic; needs testing both platforms |
| `process-control` | rely on nono's default `security.signal_mode` (`allow_same_sandbox`); optionally set `isolated` (§C5) | 1 | Medium — near no-op under nono (§C5) |
| `clipboard` | **No nono equivalent, and no defined graith semantics.** | — | **GAP** |

**Decision for v1:**

- **`ssh`:** implement via the profile's AF_UNIX socket grant. Because that grant
  requires profile mode (not ad-hoc flags) and its exact necessity under
  restricted net was the deciding factor, ssh belongs in the *profile-based*
  Phase 1 — see §C3 for why this resolves the "ad-hoc vs profile" tension.
- **`process-control`:** satisfied by nono's default signal mode in v1; §C5
  records the cross-backend divergence and the option to tighten the default.
- **`clipboard` and any other unmapped feature:** emit a **warning** (and a
  `gr doctor` note) and do not silently drop it. Since `clipboard` has no defined
  meaning in graith *and* no nono capability, v1 treats it as unsupported and
  warns; giving it meaning is a product decision, not something to invent here.

### 6. Network

graith has no network policy field today; safehouse takes none. nono is
**network-allowed by default** and offers `--block-net`,
`--network-profile <minimal|developer|claude-code|...>`, and `--allow-domain`
(L7 proxy allowlist; proxy mode needs `nono run`, another reason we use `run`).
When graith grows a network config the mapping is direct (`block=true` →
`network.block` / `--block-net`; `allow_domains` → `network.allow_domain`).

**v1 emits no network policy — matching safehouse — so v1 provides *no egress
protection*** (see threat model). A compromised agent with network access can
exfiltrate anything it can read. Egress confinement and credential-leak-via-
network are Phase 2/3, tied to #596.

### 7. `gr doctor` detection

Make the sandbox section backend-aware in `checkEnvironment`:

- Resolve the configured backend (default per §C6).
- **safehouse:** unchanged (macOS + binary-on-PATH).
- **nono:**
  - `nono` on `PATH` (`exec.LookPath`), else fail closed with an install hint.
  - **Assert a minimum nono version** (parse `nono --version`); warn/fail if
    below the pin (§B4). The pin is the contract that guards against the
    silent-runtime-break risk of shelling out (§C2).
  - **Linux enforcement state:** rather than hard-gating on a raw kernel number,
    report nono's own enforcement classification. nono reports
    `FullyEnforced` / `PartiallyEnforced` / `NotEnforced` (via its sandbox
    status; obtainable from `nono setup`/`nono why`). Landlock filesystem
    control needs kernel **5.13+**; nono's *practical* floor is **5.14+** (it
    uses `SECCOMP_ADDFD_FLAG_SEND` for the supervisor-notify layer). Network
    filtering needs Landlock ABI v4 (kernel 6.7+) and only matters once graith
    emits network policy. doctor reports the state and the kernel; `NotEnforced`
    with the sandbox enabled is a **fail** (matches §B2).
  - **macOS:** Seatbelt always present; report binary available.
  - Reuse `checkSandboxPaths` for dir existence (backend-agnostic).

### 8. Fail-closed & unchanged semantics

- Empty/absent `backend` on macOS ⇒ safehouse ⇒ identical behaviour to today.
  (Linux default: see §C6.)
- The fail-closed contract is defined precisely in §B2 below.
- The sandbox stays **config-only, no CLI flags** — Landlock/Seatbelt
  inheritance means spawned children can't escape.

## Fail-closed against nono's degraded modes (B2)

The single most important safety rule: **sandbox enabled + host cannot enforce =
session-creation error.** A *warning that still runs* would be a fail-**open**
regression versus safehouse and is not acceptable. `resolveSandboxFromConfig`
maps each condition as follows:

| Condition | Behaviour |
|---|---|
| `nono` binary absent | **Hard error** (session create fails), install hint. |
| `nono` version below pin | **Hard error**; the profile schema / flags may not match, so we refuse rather than risk a mis-shaped policy. |
| Landlock `NotEnforced` (kernel < 5.13/5.14 or Landlock disabled) | **Hard error.** This is the key rule: a mere warning here = fail-open. Same posture as safehouse on non-macOS. |
| Landlock `PartiallyEnforced` — FS enforced, but ABI < v4 (no TCP filtering) | v1 emits no network policy, so **run** (FS confinement holds). doctor notes the degraded state. **If/when a network policy is set** and the ABI can't enforce it, that becomes a **hard error** (don't pretend to block egress). |
| macOS, Seatbelt unavailable (shouldn't happen) | **Hard error.** |
| Feature has no faithful nono mapping (e.g. `clipboard`) | **Warn and run** — the *feature* is dropped, but the core FS/env sandbox still holds; this is a capability gap, not an enforcement failure. |

`resolveSandboxFromConfig` returns `(false, nil)` when disabled and
`(false, err)` on any **Hard error** row; the error names the backend and the
reason. This preserves the existing `(sandboxed bool, err error)` contract.

## B6. Config-mapping edge cases

Rules (each gets a fixture in §C1/testing):

- **Globs.** `expandPaths` expands `*?[` before `Wrap`, so the backend never
  sees a glob — each expanded match becomes its own `filesystem.read`/`allow`
  entry. A glob that matches nothing yields no entries (same as today).
- **Files vs dirs.** graith's config models *directories* (`read_dirs`/
  `write_dirs`). nono's profile has both recursive dir grants (`filesystem.allow`
  /`read`/`write`) and single-file grants (`allow_file`/`read_file`/`write_file`).
  v1 maps graith dirs to the **directory** grants. If an expanded glob yields a
  file path (not a dir), the backend uses the file-grant form for that entry so
  nono doesn't reject a file under a dir-only key. **Open detail:** confirm nono
  errors vs tolerates a file path under `filesystem.read`; if it tolerates, we
  can keep it simple. Flagged, not invented.
- **`~` expansion.** Already done before `Wrap`; the backend asserts absolute
  paths and errors on a non-absolute entry (defensive).
- **Symlinks.** Landlock evaluates the *resolved* path; a symlinked read_dir is
  enforced at its target. Document that a read_dir which is a symlink to a
  denied location resolves to the target (no surprise widening beyond the
  target).
- **Overlap / precedence.** If the same path appears as both read and write, the
  broader grant wins (write ⊇ read); the worktree is always read-write. nono
  dedups; graith also dedups in `Merge`.
- **Missing dirs.** Today graith **silently drops** missing sandbox dirs
  (`expandPaths` skips them; doctor only *warns*). nono may **error** on a
  `--profile` that names a non-existent path, which would turn a missing dir into
  a **dead session**. **Decision:** the nono backend filters out non-existent
  paths before writing the profile (matching graith's current lenient
  behaviour), and doctor keeps warning about them. This preserves today's UX and
  avoids a fail-closed-by-accident on a stale config entry. (If we later want
  strictness, that's a separate opt-in.)

## Other Notes

### Open questions

**(a) Must decide before Phase 1 lands:**

- **Default backend on Linux (§C6).** Static `safehouse` default (a Linux user
  who enables the sandbox without `backend=nono` still gets a fail-closed error)
  vs a **platform-aware default** (nono on Linux). Recommendation: platform-aware
  + a config-load/doctor hint on Linux. Needs sign-off because it changes
  default behaviour.
- **ssh socket scope (§C4).** Socket-only vs socket + `~/.ssh` read. `features`
  is a flat list, so "also read `~/.ssh`" needs either a new token
  (e.g. `ssh-keys`) or a config field. Recommendation: `ssh` = agent-socket only
  (most agents use the agent, not raw keys); add `ssh-keys` later if needed.
  Needs a call on the token surface.
- **Old-kernel behaviour (§B2).** Confirm the hard-error posture for
  `NotEnforced` (recommended) — this is the fail-open guard and should not be a
  warning.
- **`process-control` semantics (§C5).** Accept it as a no-op under nono v1, or
  set `signal_mode: isolated` as graith's default so it actually gates. Product
  call on cross-backend parity.

**(b) Genuinely deferrable:**

- **Generated profile vs registry.** v1 generates from graith config; the
  registry is for human-shared profiles. A future escape hatch could let a user
  name a base profile graith passes as `extends`. Not v1.
- **Credential injection & #596 (§C7).** nono ships `--credential` proxy
  injection (keys never enter the sandbox; the proxy sets `ANTHROPIC_BASE_URL`
  etc.) plus keyring/1Password/Apple-Passwords/file/env injection. This overlaps
  heavily with #596 and the two must be co-designed so they don't fight over the
  same base-URL env vars. Deferred to a joint follow-up.
- **`nono-go` FFI for introspection.** Adopt only if we later want a `gr why`-
  style in-process query (`QueryContext`).
- **Windows/WSL2 (§C8).** Downgraded to *future / untested*: graith's daemon on
  Windows is out of scope, `Available()` returns false there, and WSL2 is a
  distinct path namespace with degraded proxy semantics (nono blocks proxy mode
  on WSL2 by default because seccomp-notify returns `EBUSY`). We do **not** claim
  Windows coverage; the same backend *would* apply if graith ran under WSL2, with
  path translation to be specified then.

### Recommended phased rollout

1. **Phase 1 — backend abstraction + nono FS/env sandbox (profile-based).**
   Extract `Backend`, move safehouse behind it (no behaviour change, rename
   `SafehouseCommand`→`BackendCommand`), add the `nono` backend that **generates
   a per-session profile** (worktree RW + read/write dirs + `environment.allow_vars`
   from `EnvKeys` + the `ssh` AF_UNIX socket grant), shells out to `nono run
   --profile`, and applies the §B2 fail-closed table. `process-control` = default
   signal mode; warn on `clipboard`/unmapped features. Extend `gr doctor`
   (binary + version pin + Landlock enforcement state). Unit-test profile
   generation + backend selection + argv/edge-case fixtures; **add ≥1 enforcement
   test** (§C1).
2. **Phase 2 — richer features + network.** `security.*` tightening (optional
   `signal_mode: isolated`), give `clipboard` meaning if a product decision lands
   and nono grows a capability, add the graith `network` config field →
   `network.block`/`allow_domain`, and the ABI-v4 fail-closed rule for network.
3. **Phase 3 — credential injection (with #596).** Co-design graith credential
   handling with nono `--credential` proxy injection. Consider `nono-go`
   `QueryContext` for a `gr why`-style command.

### Testing

Per AGENTS.md (tests beside the code; `-race`; Scots-word fixtures):

- **Profile generation** (`internal/sandbox`): given `WrapOpts` with worktree
  `bothy`, `read_dirs` `[glen]`, `write_dirs` `[croft]`, `EnvKeys`
  `[PATH, HOME]`, assert the emitted JSON has `workdir.access=readwrite`,
  `filesystem.allow` ⊇ `{bothy, croft}`, `filesystem.read` ⊇ `{glen}`,
  `environment.allow_vars == [PATH, HOME]`, and `extends=default`. Assert `ssh`
  adds the `$SSH_AUTH_SOCK` socket grant and `clipboard` produces a warning and
  no silent drop (a `thrawn`/`fash` case).
- **argv & adversarial paths** (§4b/B5/B6): a `--`-prefixed path (`--wynd`) and a
  colon-containing path (`glen:ben`) both land intact in the profile file, and
  the exec argv is exactly `nono run --profile <f> -- <agent> <args>`; a
  non-existent dir (`dreich`) is filtered out, not passed; a glob (`glen/*`)
  expands to per-entry grants; a file (not dir) uses the file-grant form.
- **Backend selection** (`internal/config` + `internal/daemon`): `Merge` picks
  the agent's `backend` over global; empty resolves per §C6; unknown backend
  errors; the §B2 table — `resolveSandboxFromConfig` **hard-errors** when
  `Availability.CanEnforce` is false (a `dreich`/`scunner` case), **runs** when
  degraded-but-FS-enforcing, and returns `(false, nil)` when disabled.
- **Availability stubs:** inject a fake backend (or a lookPath/version seam) so
  CI doesn't need `nono`/`safehouse` installed — mirror how safehouse tests
  avoid needing macOS.
- **Enforcement test (§C1) — proves the sandbox holds, not just the argv shape.**
  A build-tagged integration test, gated to Linux CI with kernel ≥ 5.13 and
  `nono` installed (skip otherwise with a clear message), runs a tiny agent
  under the nono backend and asserts: a path *outside* the grant (e.g. a temp
  `hame/.ssh/id_rsa`) is **not** readable, and a path inside the worktree/write
  set **is** writable. Note the CI-runner requirement (kernel + nono binary) in
  the workflow.
- **safehouse regression:** existing `sandbox_test.go` (e.g.
  `TestWrapWithFeatures`, colon validation) passes unchanged, proving the
  interface extraction didn't alter safehouse output.
- Keep `go build ./...`, `go vet ./...`, `go test ./... -race` green.

### References

Internal:

- `internal/sandbox/sandbox.go` — `Wrap`, `WrapOpts` (incl. `EnvKeys`,
  `SafehouseCommand`→`BackendCommand`), `Available`, `validatePaths`.
- `internal/config/config.go` — `SandboxConfig`, `Merge`,
  `OrchestratorSandboxConfig`, `OrchestratorSandboxMerged`.
- `internal/daemon/daemon.go` — `resolveSandbox`,
  `resolveSandboxFromConfig`, `sandboxOptsFromConfig`, `expandPaths`.
- `internal/daemon/state.go` — `SessionState.Sandboxed` / `SandboxConfig`.
- `internal/cli/doctor.go` — `checkEnvironment`, `checkSandboxPaths`.
- `docs/design/2026-06-02-orchestrator-sandbox-config.md` — narrow orchestrator
  layer.
- README `Configuration` — `features` semantics (`ssh` → `SSH_AUTH_SOCK`,
  `process-control` → signals; `clipboard` is only an example string, no defined
  semantics).

Issues: graith #632 (this backend), #631 (epic), #596 (credentials, overlaps
nono credential injection).

External / authoritative (nono, Apache-2.0, by nolabs.ai; facts pinned to
v0.66.0 / `nono-go` @ `875bc26`):

- nono repo: <https://github.com/nolabs-ai/nono>
- nono-go bindings (cgo, bundled libs, irreversible `Apply`):
  <https://github.com/nolabs-ai/nono-go>
- Install: <https://nono.sh> / <https://nono.sh/docs/cli/getting_started/installation>
- Profile authoring (JSONC, `extends`, `nono profile init`):
  <https://nono.sh/docs/cli/features/profile-authoring>
- CLI reference (`nono run`, `--allow`/`--read`/`--write`,
  `--allow-unix-socket`, network flags, `--credential`):
  <https://nono.sh/docs/cli/usage/flags>
- Environment variable filtering (`environment.allow_vars`; inherit-all default):
  <https://nono.sh/docs/cli/features/environment>
- Linux Landlock internals (ABI table, 5.13+ FS / 6.7+ net;
  FullyEnforced/PartiallyEnforced/NotEnforced): <https://nono.sh/docs/cli/internals/landlock>
- Supervisor (seccomp notify, kernel 5.14+ `SECCOMP_ADDFD_FLAG_SEND`):
  <https://nono.sh/docs/cli/features/supervisor>
- macOS Seatbelt internals: <https://nono.sh/docs/cli/internals/seatbelt>
- Security model (irreversible, kernel-enforced, child-inherited; non-defenses):
  <https://nono.sh/docs/cli/internals/security-model>
- Profiles & groups (built-in deny/allow group names): <https://nono.sh/docs/cli/features/profiles-groups>
- Networking (network profiles, L7 allowlist, WSL2 caveats): <https://nono.sh/docs/cli/features/networking>
- WSL2 feature matrix / degraded proxy: <https://nono.sh/docs/cli/internals/wsl2>
- Credential injection (`--credential`, proxy + env): <https://nono.sh/docs/cli/features/credential-injection>
