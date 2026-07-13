---
title: "Design Doc: Pluggable Approvals Backends + Built-in localmost-compatible Engine"
authors: Dougal Matthews
created: 2026-07-03
status: Draft
reviewers: internal technical review (rev1 incorporated)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/729
---

# Pluggable Approvals Backends + Built-in localmost-compatible Engine

## Background

When an agent wants to run a tool (a Bash command, a file edit, etc.), graith
can intercept the request and decide whether to allow it. Today the flow is:

1. The daemon installs a `PreToolUse` hook into each agent's config that runs
   `gr approve-request` (`internal/daemon/hooks.go`). For Claude this is a
   `PreToolUse`/`Bash` hook; Codex maps its `permission-request` event to the
   same command.
2. `gr approve-request` (`internal/cli/approve_request.go`) reads the agent's
   hook payload from stdin — `{tool_name, tool_input}` — **truncates
   `tool_input` to 500 chars**, and sends a
   `protocol.ApprovalRequestMsg{RequestID, SessionID, ToolName, ToolInput}` to
   the daemon, then blocks for a decision.
3. The daemon's `SessionManager.SubmitApproval`
   (`internal/daemon/approvals.go`) decides and replies with
   `ApprovalDecisionMsg{Decision, Reason}`.
4. `gr approve-request` translates that decision into the agent's own hook
   vocabulary via `internal/hookoutput` and prints it. Every error path
   **fails open** (`hookoutput.AllowAll`).

`SubmitApproval` currently has exactly one automation branch:

```go
if approvalsCfg.Mode == "localmost" {
    if decision, ok := sm.tryLocalmost(ctx, req, approvalsCfg.Command); ok {
        return decision
    }
}
// ... otherwise queue the request for the human and block.
```

`tryLocalmost` shells out to a command (defaulting to a binary literally named
`localmost`) and speaks **graith's own** JSON contract:

- **stdin:** `{"tool_name","tool_input","session_id","session_name"}`
- **stdout:** `{"decision":"allow"|"block"|"deny"|"defer","reason":"..."}`

This is **not** the protocol of [federicotdn/localmost][lm]. The coupling to
the name "localmost" is cosmetic: the mode is really "shell out to an arbitrary
command over graith's contract." Meanwhile, users who actually want localmost's
behaviour can't get it without writing a shim that adapts localmost's real
Claude Code hook protocol to graith's contract.

### The decision vocabulary

graith uses four internal decision values; only some are producible from each
layer today:

| Value    | Meaning                                   | Produced by                          |
|----------|-------------------------------------------|--------------------------------------|
| `allow`  | let the tool run                          | overlay, external command, timeouts-not |
| `block`  | refuse (Claude `deny`, Codex/Cursor `deny`) | overlay, timeout                  |
| `deny`   | refuse (synonym of block at the agent edge) | external command                    |
| `defer`  | "I have no opinion — ask the human"       | external command (`""`/`defer`)      |

`internal/hookoutput` maps these to each agent's schema (Claude
`hookSpecificOutput.permissionDecision`: `allow→allow`, `deny/block→deny`,
`defer→ask`; Codex/Cursor `block→deny`). The
interactive overlay (`internal/client/approval_overlay.go`) only ever emits
`allow` and `block`. `defer` exists purely to let an automated decider hand a
request back to the human queue.

### What localmost actually is

[localmost][lm] is a **Haskell, GPL-3.0** "flexible and deterministic Claude
Code `PreToolUse` handler." It is registered as `localmost check` on the
`Bash` matcher. It:

- Parses the candidate command **and** every configured rule with
  **ShellCheck's** parser, then works on the ASTs.
- Splits the input command into **subcommands** (`echo | foo; bar` → `echo`,
  `foo`, `bar`), understanding `|`, `;`, `&&`, `||`, `if`, `for`, redirects.
- For each subcommand: if any `deny` rule matches → denied; else if any `allow`
  rule matches → allowed. Combining: **any** subcommand denied → `deny`; **all**
  allowed → `allow`; otherwise → `ask`.
- Rules live in a `config.json` with `allow` / `deny` arrays. Rule text is
  bash-like with meta-expressions and quantifiers:

  | Meta | Meaning | | Quant | Meaning |
  |------|---------|-|-------|---------|
  | `@arg` | any single argument | | `?` | 0 or 1 |
  | `@path` | argument that is a valid path | | `+` | 1 or more |
  | `@int` | integer argument | | `*` | 0 or more |
  | `@env` | `FOO=bar` assignment (literal value only) | | | |
  | `@sub` | trailing subcommand that must itself be `allow` (no quantifier) | | | |
  | `@@` | literal `@` | | | |
  | `@{a,b,c}` | choice | | | |
  | `@(a b c)` | ordered group | | | |

  `@*` is shorthand for `@arg*`. Matching is ordered (regex-like): `foo -a -b`
  does not match `foo -b -a`.

- Each rule may also set:
  - **`unless`**: list of expressions that must **not** appear anywhere in the
    subcommand for the rule to match.
  - **`redirect`**: `true` | `false` | `"safe"` (default `"safe"` — only
    non-destructive redirects like `> /dev/null` are allowed).
  - **`pipe`**: `true` | `false` | `"in"` | `"out"` (default `true`) —
    constrains where in a pipeline the subcommand may appear.
- Top-level options: **`allowSafeXargs`** (default `true`; special-cases
  `echo ARGS | xargs PROG` and `PROG1 | xargs PROG2`) and
  **`askNoninteractive`** (default `true`; when `false`, `ask` becomes `deny`
  in accept-edits mode). Missing/invalid config → defaults to `ask`.

localmost's three policies map cleanly onto graith's vocabulary:
`allow → allow`, `deny → block`, `ask → defer` (hand to the human queue).

## Goals

1. Make the approvals decision layer **pluggable**, mirroring the sandbox
   backend pattern (`internal/sandbox`), so a decision can come from any
   configured backend.
2. Ship these backends:
   - **`prompt`** (default) — no automation; queue for the human. Equivalent to
     today's default behaviour.
   - **`command`** (alias `external`) — delegate to an arbitrary command over
     graith's own JSON contract (today's `tryLocalmost`, renamed and
     documented). Any tool can plug in here.
   - **`localmost`** — invoke the **real** localmost binary using **its** native
     Claude Code `PreToolUse` protocol, faithfully.
   - **`builtin`** — a **clean-room, behaviourally-compatible reimplementation
     of localmost's rule engine inside graith** (validated by a conformance
     corpus, not bit-identical parsing — see
     [Fidelity and conformance](#fidelity-and-conformance)), so users get
     localmost behaviour with no external dependency.
3. Preserve backward compatibility: existing `[approvals] mode = "localmost"`
   (with or without `command`) keeps working, with a deprecation path.
4. Document the JSON contract (for `command`) and the `config.json` rule format
   (for `builtin` / `localmost`) in code comments, config samples, and docs.

## Non-goals

- A rule-editing UI or interactive rule authoring. `config.json` is edited by
  hand (or generated by localmost's own `init`).
- Windows support for the `builtin` engine's shell parsing beyond what the
  chosen parser already provides (localmost itself is not Windows-supported).
- Changing the human overlay UX, the hook installation, or the wire protocol
  beyond the additive changes noted below.
- Porting localmost's Haskell source. The `builtin` engine is a clean-room
  reimplementation from localmost's public documentation and observable
  behaviour (see [Licensing](#licensing)).

## Proposal

### 1. An `ApprovalBackend` interface (mirroring `internal/sandbox`)

The sandbox package selects a stateless backend from a config string via a
single `backendByName` switch and a small interface (`Name`, `Availability`,
`Wrap`). We mirror that *shape*; the method set necessarily differs — approvals
has no `Wrap` analog, and its availability check takes the approvals config
rather than a command line — so "mirror", not "identical".

```go
// internal/approvals/backend.go
package approvals

// Request is everything a backend needs to decide. It is derived from
// protocol.ApprovalRequestMsg plus session metadata resolved by the daemon.
type Request struct {
    RequestID   string
    SessionID   string
    SessionName string
    Agent       string
    ToolName    string // "Bash", "Write", "Edit", ...
    ToolInput   string // full, UNtruncated tool input JSON (see §5)
}

// Decision is what a backend returns. Decision is one of "allow", "block", or
// "defer" ONLY — a backend that would emit "deny" must normalise it to "block"
// before returning (see dispatch below). "defer" means "no opinion — queue for
// the human."
type Decision struct {
    Decision string // "allow" | "block" | "defer"
    Reason   string
}

// Backend makes (or declines to make) an automated approval decision.
type Backend interface {
    Name() string

    // Availability reports whether this backend can enforce with the given
    // config (verb matches sandbox.Backend). An unavailable backend fails
    // *closed* at session create/resume so misconfiguration is loud, not
    // silently ignored. There is no "degraded" analog to the sandbox
    // Availability.Degraded flag — a decision is binary, never partial.
    Availability(cfg Config) Availability

    // Decide returns a decision. A "defer" decision (or an error) means
    // "no opinion — queue for the human"; errors fail-safe to the human,
    // never fail-open to allow.
    Decide(ctx context.Context, req Request) (Decision, error)
}
```

Dispatch is a switch keyed off the backend name, exactly like
`sandbox.backendByName`:

```go
func backendByName(name string) (Backend, error) {
    switch name {
    case "", "prompt":
        return promptBackend{}, nil        // no automation; always defers
    case "command", "external":
        return commandBackend{}, nil
    case "localmost":
        return localmostBackend{}, nil     // real localmost binary, native protocol
    case "builtin":
        return builtinBackend{}, nil       // graith's own engine
    default:
        return nil, fmt.Errorf("unknown approvals backend %q", name)
    }
}
```

`SubmitApproval`'s current inline branch is replaced by backend dispatch:

```go
be, _ := backendByName(cfg.Backend())          // validated earlier at config load / session create
decision, err := be.Decide(ctx, req)
d := decision.Decision
if d == "deny" {                                // normalise the agent-edge synonym
    d = "block"
}
if err == nil && (d == "allow" || d == "block") {
    return protocol.ApprovalDecisionMsg{Decision: d, Reason: decision.Reason}
}
// defer / error / unknown  ->  fall through to the existing human-queue path.
```

**`deny`→`block` normalisation is load-bearing.** Today's `tryLocalmost`
returns `resp.Decision` verbatim (`daemon/approvals.go` does *not* map
`deny`→`block`), so a `command` backend that returned `"deny"` would currently
match neither `allow` nor `block` and fall through to the human — silently
*not* blocking. Each backend normalises `deny`→`block` before returning (the
`Decision.Decision` field is documented as `allow|block|defer` only), and the
dispatch repeats it defensively. The back-compat test matrix must cover a
backend returning `deny`.

The human-queue path (register `pendingApproval`, set `AgentStatus="approval"`,
broadcast, block on channel/timeout/ctx) is unchanged and becomes the universal
fallback. `promptBackend.Decide` returns `defer` unconditionally, so the
default (`backend = ""`) routes straight to it with no special-casing.

### 2. Config schema

`backend` is the **sole** decision selector. There is deliberately **no**
`prompt`/`notify` value on `mode`: despite the current README, `mode` today is
only ever compared to the literal `"localmost"` (`daemon/approvals.go`) — there
is no `prompt` or `notify` behaviour in the code. The notify/auto-open
behaviour is driven entirely by the separate `auto_pop` bool
(`config.go` → `attach.go` → `passthrough.go`). Rather than invent a second
knob for the same behaviour, we keep `auto_pop` as-is and do **not** add a
`mode` axis. `mode` survives only as a **deprecated** back-compat input (§3).

```toml
[approvals]
backend  = "builtin"     # "" (none) | command | external | localmost | builtin
timeout  = "10m"
auto_pop = false         # unchanged: auto-open the overlay when a request is queued

# backend = "command" / "external" (uncomment for those backends — with
# backend = "builtin" a set command is a hard error):
#   command = "my-approver" # required; receives graith's JSON on stdin, prints {decision,reason}

# backend = "localmost" (real binary):
#   command = "localmost"        # optional path override (default "localmost")

# backend = "builtin" (graith's own engine): point at an external file...
[approvals.builtin]
config = "~/.config/graith/approvals.json"   # localmost-format config.json
                                             # (default: this path, then $XDG)
```

...**or**, instead of an external file, define the rules inline (#737). The two
forms are mutually exclusive — setting both is a hard error. Pick one of the
snippets below; they are alternatives, not to be combined in one config:

```toml
# Inline, flat form: bare rule strings plus the top-level flags.
[approvals.builtin]
allow = ["@arg @*"]
deny  = ["shutdown @*", "reboot @*", "mkfs @*"]
allowSafeXargs = true
askNoninteractive = true
```

```toml
# Inline, table form: for per-rule keys (unless/redirect/pipe) use arrays of
# tables. Note a single key (allow) must use either bare strings OR tables, not
# both, so this replaces the `allow = [...]` line above rather than adding to it.
[[approvals.builtin.allow]]
rule   = "find @*"
unless = ["-exec", "-delete"]

[[approvals.builtin.deny]]
rule = "rm @arg*"
```

Unknown keys under `[approvals.builtin]` (or in a rule table) are rejected at
config-load, so a misspelled `deny`/`unless` fails loudly rather than silently
dropping a rule.

`config.Approvals` gains `Backend string` and a nested `Builtin ApprovalsBuiltin`
carrying either the external `Config` path or the inline
`Allow`/`Deny`/`AllowSafeXargs`/`AskNoninteractive` rules; the existing `Mode`
field is kept only for back-compat. A `Backend()` accessor resolves the
**effective** backend and encapsulates back-compat (§3).

### 3. Backward compatibility

In the wild, `[approvals] mode` is only ever unset or `"localmost"` (the only
value the code acts on). We preserve every existing config via
`Approvals.Backend()`:

1. If `backend` is set → use it (after validation). If **both** `backend` and a
   non-empty legacy `mode` are set, that is a **hard error** (refuse to guess
   intent), unless they trivially agree. This hard error is *exclusively* for
   the both-set conflict — a pure back-compat config with only `mode` set (no
   `backend`) is always a warning (step 2), never an error.
2. Else if legacy `mode` ∈ {`command`, `external`, `localmost`} → map to a
   backend and emit a **one-time** deprecation warning (once per daemon
   lifetime, not per request — resolution is memoised, since `sm.cfg.Approvals`
   is read on the approval hot path). Crucially, `mode = "localmost"` maps to
   the **`command`** backend (graith's own contract — its historical
   behaviour), **not** the new native-protocol `localmost` backend.
3. Else → the `prompt` backend (no automation; queue for the human).

**The `localmost` overload is a footgun, so the warning text is part of the
spec** and is tested. It must name both intents, e.g.:

```
[approvals] mode="localmost" is deprecated. It currently maps to
backend="command" (graith's JSON contract — unchanged behaviour); set
backend="command" to silence this. To instead run the real localmost binary
over its native protocol, set backend="localmost".
```

(We considered renaming the native backend `localmost-native` to remove the
overload entirely; rejected to keep the config value intuitive, on the strength
of the explicit warning + the both-set hard error. Those two mitigations are a
**package**: if the overloaded `localmost` is kept, neither is independently
droppable — the overload is only survivable with both. Revisit if it still
confuses — see Open questions.)

Validation happens at config load and again at session create (fail-closed,
mirroring the sandbox `resolveSandboxFromConfig` check): an unknown backend,
`command`/`external` with no `command`, `localmost` with no binary on `PATH`,
or `builtin` with an unreadable/invalid config is a hard error, not a silent
fall-through.

### 4. Backends in detail

**`prompt` (`promptBackend`)** — the default (`backend = ""`). `Decide` returns
`defer` unconditionally; `Availability` is always enforce=true. Notify/auto-open
is orthogonal and stays on `auto_pop`.

**`command` / `external` (`commandBackend`)** — today's `tryLocalmost`, renamed
`tryExternalCommand`, documented, and moved behind the interface. Contract
(unchanged, now written down):

- stdin: one JSON object
  `{"tool_name","tool_input","session_id","session_name"}`
- stdout: one JSON object
  `{"decision":"allow"|"block"|"deny"|"defer","reason":"..."}`
- `defer`/empty/non-zero-exit/bad-JSON/timeout → defer to human. `deny`
  normalises to `block` **inside the backend** (see §1 — this normalisation is
  currently absent in `tryLocalmost` and must be added). 5s subprocess timeout,
  context-cancellable (existing behaviour; port
  `TestSubmitApprovalLocalmostCancelledByContext` and the timeout/ctx tests when
  `tryLocalmost` becomes `commandBackend.Decide` — they guard the
  fail-safe-to-human path).

`Availability` requires a non-empty `command`.

**`localmost` (`localmostBackend`)** — invokes the real binary using its native
Claude Code protocol. Because `gr approve-request` *is* the `PreToolUse` hook,
the most faithful approach is to **forward the original hook payload**:
`gr approve-request` already receives the exact Claude `PreToolUse` JSON on
stdin. We add that raw payload to the request (§5) and the backend pipes it to
`localmost check`, then parses localmost's hook output
(`hookSpecificOutput.permissionDecision` ∈ `allow`/`deny`/`ask`, with
`permissionDecisionReason`) and maps `allow→allow`, `deny→block`, `ask→defer`.
For non-Claude agents whose payload shape differs, the backend reconstructs a
minimal `PreToolUse` envelope from `ToolName`/`ToolInput`, or falls back to
`echo <command> | localmost check --mode text`. `Availability` requires the
`localmost` binary on `PATH` (fail-closed).

**`builtin` (`builtinBackend`)** — see §6. `Availability` requires either a
readable, valid external `config.json` or a valid inline `[approvals.builtin]`
ruleset (fail-closed on parse error). Note this is itself a
**documented divergence** from localmost, which treats an invalid config as
`ask` at runtime; we prefer a loud config error at session create.

### 5. Full (untruncated) tool input

`gr approve-request` truncates `tool_input` to 500 chars **before** sending it
to the daemon (`approve_request.go`). That is fine for the overlay's display,
but the `builtin` and `localmost` backends must evaluate the **whole** command —
a 600-char `rm ...` truncated at 500 could parse to something benign. Changes:

- `ApprovalRequestMsg` carries the **full** `ToolInput` (and, additively, the
  raw hook payload `HookPayload` for the `localmost` backend). Truncation moves
  to the **display** layer: `ApprovalInfo.ToolInput` (shown in the overlay) is
  truncated when the daemon builds it, not on the wire.
- **The 100ms stdin read timeout in `gr approve-request` is the real sharp
  edge.** Today it's harmless because only 500 display chars matter, but once a
  backend must see the whole command, a large `tool_input` (a big `Write`
  payload, a long heredoc) that doesn't finish reading in 100ms drops to empty
  stdin → empty `ToolName`/`ToolInput` → the backend can't evaluate → defers.
  That silently defeats automation and reads as "localmost randomly asks the
  human." Fix: read stdin to EOF *before* connecting, and bound only the daemon
  round-trip, not the stdin read.
- **Broadcast must use the truncated copy.** `ApprovalNotificationMsg` is pushed
  to every attached client (`broadcastApprovalNotification`). It must carry the
  truncated display `ToolInput`, never the full one — otherwise every attached
  overlay receives full file contents on every approval.
- This is an additive protocol change (new optional field, larger existing
  field); old/new client/daemon interoperate since JSON ignores unknown fields
  and the daemon already tolerates a short `ToolInput`.

### 6. The built-in engine (`internal/approvals/localmost`)

This is the substantial piece and is phased (see [Milestones](#milestones)).
Components:

1. **Shell parser.** localmost uses ShellCheck (Haskell). In Go we use
   [`mvdan.cc/sh/v3/syntax`][mvdansh] (BSD-3-Clause), a mature, well-tested bash
   parser, to produce an AST and split a command into subcommands, detecting
   pipelines, `;`/`&&`/`||`, `if`/`for`, redirects, and `xargs` shapes. **This
   is the primary compatibility risk:** mvdan/sh's AST is not byte-for-byte
   ShellCheck's, so edge-case tokenisation may diverge. Mitigation: a
   conformance corpus (§Testing) run against real localmost, and documenting
   known divergences rather than claiming bit-identical parsing.
2. **Rule compiler.** Parse the rule DSL (bash-like text with the
   meta-expression + quantifier grammar above) into a matcher. Rules tokenise
   with the same shell parser, then tokens are reinterpreted: `@arg`, `@path`,
   `@int`, `@env`, `@sub`, `@@`, `@{...}`, `@(...)`, plus `?`/`+`/`*`. Compiles
   to a small ordered NFA over argument tokens (regex-like, greedy, ordered —
   matching localmost's stated semantics).
3. **Matcher.** Runs a subcommand's argument list against a compiled rule,
   honouring `unless` (none of the listed expressions appear anywhere),
   `redirect` (`true`/`false`/`"safe"` — classify redirects as safe vs
   destructive), `pipe` (`true`/`false`/`"in"`/`"out"` — position in pipeline),
   and `@sub` (recurse: the trailing subcommand must itself resolve to
   `allow`).
4. **Policy combiner.** deny-if-any-subcommand-denied, allow-if-all-allowed,
   else ask. Plus `allowSafeXargs` (the two `xargs` special cases) and
   `askNoninteractive`. Note the last is an **approximation, not an
   equivalence**: localmost's flag concerns Claude's accept-edits mode, whereas
   graith's nearest signal is "no client attached." And graith **already**
   yields `block` for an unattended session via the timeout path — so mapping
   `askNoninteractive` to "no attached client" is a no-op *except* that it fires
   **immediately** (fast-deny) instead of after `timeout` (wait-then-deny).
   That fast-deny is the only real behaviour it would buy. As shipped, the
   built-in engine **parses** `askNoninteractive` but does **not** wire the
   fast-deny: a pure backend can't observe client attachment, so `ask` defers
   and an unattended session still blocks via the timeout (just not
   immediately). Wiring fast-deny is a follow-up needing daemon-side
   client-count support (documented divergence).
5. **Config loader + validator.** Read localmost's `config.json` verbatim
   (`allow`, `deny`, `allowSafeXargs`, `askNoninteractive`). Expose
   `gr approvals validate [--config path]` mirroring `localmost config
   validate`, and `gr approvals check` (stdin command → decision) mirroring
   `localmost check`, so users can test rules and we can diff against real
   localmost in CI.

Non-Bash tools: the localmost-family backends only reason about shell commands.
For `ToolName != "Bash"` they return `defer` (the human decides), matching
localmost's `Bash`-matcher scope. `command`/`external` backends still see all
tools.

### Fidelity and conformance

We claim **behavioural compatibility on a documented corpus, with known
divergences listed** — not bit-identical parsing (mvdan/sh ≠ ShellCheck makes
that unachievable, and Open-question 3 rightly prefers behavioural). The phrase
"100% compatible" is therefore avoided.

- **Conformance is enforced in CI, not skipped.** A dedicated CI job installs a
  **pinned** localmost version and runs the `(config, command) → policy`
  corpus, and the **golden outputs are checked into the repo** so divergence is
  caught even where the binary is absent. Skip-by-default would let the claim
  rot silently.
- **Documented divergences (running list, as implemented in the built-in
  engine):**
  - `mvdan.cc/sh` parsing is not bit-identical to ShellCheck's, so rare
    tokenisation edge cases may differ.
  - `builtin` fails **closed** on an invalid config, whereas localmost defaults
    to `ask` at runtime.
  - `askNoninteractive` is parsed but its **fast-deny is not wired**: a pure
    backend cannot observe client attachment, so `ask` always defers and an
    unattended session still blocks via the approval timeout (just not
    immediately). Wiring fast-deny needs daemon-side client-count support.
  - `allowSafeXargs` is **partial**: the `echo ARGS | xargs PROG` and
    `PROG1 | xargs PROG2` shapes are handled, but xargs's own options are not
    stripped from the program, and xargs can only *allow* (never *deny*).
  - `@env` requires a **literal** assignment value: `@env` matches
    `FOO=bar` but not `FOO=$(...)` / `FOO=$var` (it falls through to `ask`),
    consistent with `@arg`/`@path`/`@int`. This is a security hardening over a
    naive `@env` (see #782) — a non-literal value could otherwise smuggle an
    unchecked command past the deny rules.
  - Command and process substitutions (`$(...)`, backticks, `<(...)`, `>(...)`)
    are **decomposed**: their inner commands are harvested as standalone
    subcommands and evaluated against deny/allow rules, wherever the
    substitution appears (assignment value, argument, redirect target,
    here-string/heredoc body, `for` list, `case` subject, …). A substitution
    running a denied command makes the whole command deny; one running an
    un-allowed command makes it ask. This closes the #782 bypass class (a
    dangerous command hidden in a substitution that the shell executes but no
    rule ever saw) and is convergence with localmost/ShellCheck, not a
    divergence.
  - `@path` uses localmost's **character-level path check**: a literal argument
    that is non-empty and NUL-free. It does **not** require a path-shaped prefix
    and does **not** validate filesystem limits (component/path length) or
    existence — so `@path` also matches bare relative names (`foo`), dot-files,
    option-looking tokens (`-x`), and absolute paths. It is therefore only
    *marginally* narrower than `@arg` (it additionally rejects the empty-string
    argument `""`), and operators should **not** rely on `@path` as an option
    filter or path sanitizer, nor to narrow an allow rule meaningfully beyond
    `@arg`. This is **convergence** with localmost, not a divergence: tightening
    to a "path-shaped" heuristic (leading `/`, `./`, `~`, …) would reject paths
    localmost accepts (e.g. `mkdir foo`) and is deliberately avoided. See #732.
  - Any further parser edge cases the corpus surfaces are added here rather than
    silently absorbed.

### Fail-open vs fail-closed (unchanged philosophy)

- **Hook edge** (`gr approve-request`): fails **open** (allow) if it can't reach
  the daemon — unchanged; a broken control plane must not wedge the agent.
- **Backend errors / `defer`**: fail **safe to the human** (queue), never
  auto-allow and never auto-deny. If no human is attached, the existing timeout
  path returns `block`.
- **Config/availability errors**: fail **closed** at session create (loud
  error), mirroring the sandbox backend resolution.

## Licensing

localmost is **GPL-3.0**; graith is not GPL. The `builtin` engine will be a
**clean-room** implementation. The `config.json` format and rule DSL are
interface specifications (not copyrightable expression), so reproducing them for
interoperability is fine — but the behaviours enumerated in this doc are
detailed, so clean-room only holds with discipline:

- **Provenance.** Every behaviour must derive from localmost's public
  README/`examples.md`, not the Haskell source. Non-obvious behaviours
  (`redirect` defaulting to `"safe"`, the two `allowSafeXargs` shapes,
  `askNoninteractive`) get a recorded citation to the public doc they came from.
  A behaviour learnable *only* by reading source is treated as a **documented
  divergence**, not copied.
- **Process.** Prefer two-person clean-room: one reads the public docs (never
  the source) and writes a plain-English spec; a second implements from that
  spec only. At minimum, a single implementer works from README/examples and
  never opens the GPL source.
- **Corpus is validation, not specification.** Diffing against the GPL binary's
  outputs is fine (outputs aren't copyrightable expression), but corpus-fitting
  must not become reverse-engineering the source.

The `localmost` (external-binary) backend interoperates with the GPL tool at
arm's length via its documented CLI, which raises no linking concern.

## Milestones

1. **Backend scaffolding.** Add `internal/approvals` package, `Backend`
   interface, `backendByName`, `promptBackend`, `commandBackend` (= renamed
   `tryExternalCommand`, now with `deny`→`block` normalisation). Wire
   `SubmitApproval` through dispatch. Add `backend` config + `Backend()`
   back-compat resolution + validation + the deprecation warning. Full,
   untruncated `ToolInput` on the wire (with the stdin-read fix, §5). Docs +
   config samples. **No user-visible behaviour change; additive wire change**
   (larger `ToolInput` + new `HookPayload` field, truncation moves to display).
   Closes the original narrow decoupling ask.
2. **`localmost` external backend.** Forward the hook payload / reconstruct
   `PreToolUse`, invoke `localmost check`, parse native output, fail-closed
   availability check. `gr doctor` reports localmost presence.
3. **`builtin` engine — core.** Shell parser integration, rule compiler +
   matcher for `@arg`/`@path`/`@int`/`@env`/`@@`/`@{}`/`@()` + quantifiers +
   `unless`, subcommand splitting, policy combiner. `gr approvals
   check`/`validate`. Conformance corpus vs real localmost.
4. **`builtin` engine — full fidelity.** `redirect`/`pipe` semantics, `@sub`
   recursion, `allowSafeXargs`, `askNoninteractive`. Expand conformance corpus;
   document known divergences.

Milestone 1 is independently shippable and satisfies issue #729's minimum;
2–4 deliver the pluggable + built-in vision.

## Testing

- **Unit** (mirroring `approvals_test.go`): `promptBackend` always defers;
  `commandBackend` allow/block/**deny→block**/defer/error/timeout/ctx-cancel
  (port the existing tests, renamed); `Backend()` back-compat matrix
  (`mode=localmost` → command backend; `backend=localmost` → native; both set →
  error; unknown → error); deprecation-warning text + fires-once; fail-closed
  availability.
- **Rule engine**: table-driven tests over the meta-expression grammar,
  quantifiers, ordering, `unless`, `redirect`, `pipe`, `@sub`, `xargs`, plus
  the README/`examples.md` sample configs.
- **Conformance corpus**: a fixture set of `(config, command) → policy` triples
  with **golden outputs checked into the repo**, plus a dedicated CI job that
  installs a **pinned** `localmost` and re-derives them, so parser divergences
  are caught even when the binary is absent (see
  [Fidelity and conformance](#fidelity-and-conformance)). Divergences are
  documented, not silently accepted.
- **Config**: `Backend()` resolution and validation errors.
- Fixtures use Scots words per the project convention.

## Open questions

1. **`mode` vs `backend` naming.** This doc makes `backend` the sole selector
   and keeps notify behaviour on the existing `auto_pop` bool, because `mode`
   has no real prompt/notify semantics today (§2); `mode` remains only as a
   deprecated back-compat input. Alternative considered: define a real
   `mode` × `auto_pop` truth table. Recommendation: `backend`-only + keep
   `auto_pop` (less surface, no two-knobs-one-behaviour ambiguity). Also open:
   whether to rename the native backend `localmost-native` to kill the overload
   (§3). **Decision needed.**
2. **Shell parser dependency.** Take on `mvdan.cc/sh` (new dependency, not
   ShellCheck-identical) vs. a bespoke minimal parser. Recommendation:
   `mvdan.cc/sh` — reimplementing a bash parser is out of proportion to the
   task and riskier. **Decision needed.**
3. **Fidelity bar.** Documented behavioural compatibility with a conformance
   suite, not bit-identical parsing (impossible without ShellCheck). The
   "100% compatible" phrasing is dropped (see
   [Fidelity and conformance](#fidelity-and-conformance)). Recommendation:
   confirm the behavioural bar.
4. **Where the `builtin` engine runs.** Daemon (proposed — has config, matches
   today, single policy source) vs. in-process in `gr approve-request` (avoids
   sending full input over the wire, but splits policy across processes).
   Recommendation: daemon. Deliberate consequence: the engine then parses
   attacker-influenced command strings inside the privileged daemon; mvdan/sh
   is a pure parser (no execution) so the risk is low, but it's a conscious
   choice.

[lm]: https://github.com/federicotdn/localmost
[mvdansh]: https://github.com/mvdan/sh
