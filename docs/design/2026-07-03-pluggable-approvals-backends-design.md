---
title: "Design Doc: Pluggable Approvals Backends + Built-in localmost-compatible Engine"
authors: Dougal Matthews
created: 2026-07-03
status: Draft
reviewers: (none yet)
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
| `block`  | refuse (Claude `block`, Codex/Cursor `deny`) | overlay, timeout                  |
| `deny`   | refuse (synonym of block at the agent edge) | external command                    |
| `defer`  | "I have no opinion — ask the human"       | external command (`""`/`defer`)      |

`internal/hookoutput` maps these to each agent's schema (Claude
`allow→approve`, `deny/block→block`; Codex/Cursor `block→deny`). The
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
  | `@env` | `FOO=bar` assignment | | | |
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
   - **`builtin`** — a **clean-room, 100%-compatible reimplementation of
     localmost's rule engine inside graith**, so users get localmost behaviour
     with zero external dependencies.
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
`Wrap`). We mirror that exactly.

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

// Outcome is what a backend returns.
type Decision struct {
    Decision string // "allow" | "block" | "defer"
    Reason   string
}

// Backend makes (or declines to make) an automated approval decision.
type Backend interface {
    Name() string

    // Available reports whether this backend can enforce with the given
    // config. Like sandbox.Availability, an unavailable backend fails
    // *closed* at session create/resume so misconfiguration is loud, not
    // silently ignored.
    Available(cfg Config) Availability

    // Decide returns a decision. A "defer" decision (or handled=false) means
    // "no opinion — queue for the human." Errors also defer (fail-safe to the
    // human), never fail-open to allow.
    Decide(ctx context.Context, req Request) (Decision, error)
}
```

Dispatch is a switch keyed off the backend name, exactly like
`sandbox.backendByName`:

```go
func backendByName(name string) (Backend, error) {
    switch name {
    case "", "prompt", "notify":
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
if err == nil && (decision.Decision == "allow" || decision.Decision == "block") {
    return protocol.ApprovalDecisionMsg{Decision: decision.Decision, Reason: decision.Reason}
}
// defer / error / unknown  ->  fall through to the existing human-queue path.
```

The human-queue path (register `pendingApproval`, set `AgentStatus="approval"`,
broadcast, block on channel/timeout/ctx) is unchanged and becomes the universal
fallback. `promptBackend.Decide` simply returns `defer`, so `mode = "prompt"`
routes straight to it with no special-casing.

### 2. Config schema

Introduce an explicit `backend` selector and per-backend sub-config, keeping
`mode` for the human-facing behaviour axis it already documents (prompt vs
notify).

```toml
[approvals]
mode     = "prompt"      # prompt | notify — behaviour when the backend defers / says ask
backend  = "builtin"     # "" | prompt | command | external | localmost | builtin
timeout  = "10m"
auto_pop = false

# backend = "command" / "external":
command  = "my-approver" # required; receives graith's JSON on stdin, prints {decision,reason}

# backend = "localmost" (real binary):
#   command = "localmost"        # optional path override (default "localmost")

# backend = "builtin" (graith's own engine):
[approvals.builtin]
config = "~/.config/graith/approvals.json"   # localmost-format config.json
                                             # (default: this path, then $XDG)
```

`config.Approvals` gains `Backend string` and a nested
`Builtin ApprovalsBuiltin{ Config string }`. A `Backend()` accessor resolves
the **effective** backend and encapsulates back-compat (§3).

### 3. Backward compatibility

`mode` currently doubles as the automation selector (`"localmost"` triggers
`tryLocalmost`; anything else means "prompt"). We preserve every existing
config:

`Approvals.Backend()` resolution order:

1. If `backend` is set and non-empty → use it (after validation).
2. Else if `mode` ∈ {`command`, `external`, `localmost`} → treat `mode` as the
   backend (so old configs keep working) and log a one-time deprecation warning
   suggesting `backend = "..."`. `mode = "localmost"` with an empty `command`
   keeps its historical default (the `localmost` binary **via graith's
   contract** — i.e. the `command` backend, not the new native-protocol
   `localmost` backend) to avoid changing behaviour for existing users.
3. Else → `prompt` (no automation).

Validation happens at config load and again at session create (fail-closed,
mirroring the sandbox `resolveSandboxFromConfig` check): an unknown backend, or
`command`/`external` with no `command`, or `builtin`/`localmost` whose
dependency is unavailable, is a hard error rather than a silent fall-through.

> Note the deliberate ambiguity resolution: the string `localmost` means two
> different things depending on where it appears. As a legacy **`mode`** value
> it maps to the `command` backend (graith's contract, historical behaviour).
> As the new **`backend`** value it means the real localmost binary over its
> native protocol. The deprecation warning tells legacy users exactly which
> `backend` value reproduces their current behaviour (`command`) versus opts
> into the real protocol (`localmost`).

### 4. Backends in detail

**`prompt` / `notify` (`promptBackend`)** — `Decide` returns `defer`
unconditionally. `notify` differs from `prompt` only in the existing
`auto_pop` / notification behaviour, which is unchanged. `Available` always
true.

**`command` / `external` (`commandBackend`)** — today's `tryLocalmost`, renamed
`tryExternalCommand`, documented, and moved behind the interface. Contract
(unchanged, now written down):

- stdin: one JSON object
  `{"tool_name","tool_input","session_id","session_name"}`
- stdout: one JSON object
  `{"decision":"allow"|"block"|"deny"|"defer","reason":"..."}`
- `defer`/empty/non-zero-exit/bad-JSON/timeout → defer to human. `deny`
  normalises to `block`. 5s subprocess timeout, context-cancellable (existing
  behaviour, covered by `TestSubmitApprovalLocalmostCancelledByContext`).

`Available` requires a non-empty `command`.

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
`echo <command> | localmost check --mode text`. `Available` requires the
`localmost` binary on `PATH` (fail-closed).

**`builtin` (`builtinBackend`)** — see §6. `Available` requires a readable,
valid `config.json` (fail-closed on parse error; matches localmost's "invalid →
ask" only at *runtime*, but we prefer a loud config error at session create).

### 5. Full (untruncated) tool input

`gr approve-request` truncates `tool_input` to 500 chars **before** sending it
to the daemon (`approve_request.go:68`). That is fine for the overlay's
display, but the `builtin` and `localmost` backends must evaluate the **whole**
command — a 600-char `rm ...` truncated at 500 could parse to something
benign. Changes:

- `ApprovalRequestMsg` carries the **full** `ToolInput` (and, additively, the
  raw hook payload `HookPayload` for the `localmost` backend). Truncation moves
  to the **display** layer: `ApprovalInfo.ToolInput` (shown in the overlay) is
  truncated when the daemon builds it, not on the wire.
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
   `askNoninteractive` (map `ask→deny` when non-interactive — graith knows
   whether a human is attached, so this maps to "no attached client → treat
   `defer` as `block`", consistent with today's timeout→block).
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
**clean-room** implementation written from localmost's public README/docs and
from black-box conformance tests, **without reading or copying** the Haskell
source. The `config.json` format and rule DSL are interface specifications
(not copyrightable expression), so reproducing them for interoperability is
fine. This constraint is called out here so implementers do not "just port"
the Haskell. The `localmost` (external-binary) backend interoperates with the
GPL tool at arm's length via its documented CLI, which raises no linking
concern.

## Milestones

1. **Backend scaffolding + no behaviour change.** Add `internal/approvals`
   package, `Backend` interface, `backendByName`, `promptBackend`,
   `commandBackend` (= renamed `tryExternalCommand`). Wire `SubmitApproval`
   through dispatch. Add `backend` config + `Backend()` back-compat resolution
   + validation. Full/untruncated `ToolInput` on the wire. Docs + config
   samples. **Ships the decoupling from the name "localmost" with zero
   behaviour change** and closes the original narrow ask.
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
  `commandBackend` allow/block/deny/defer/error/timeout/ctx-cancel (port the
  existing tests, renamed); `Backend()` back-compat matrix
  (`mode=localmost` → command backend; `backend=localmost` → native; unknown →
  error); fail-closed availability.
- **Rule engine**: table-driven tests over the meta-expression grammar,
  quantifiers, ordering, `unless`, `redirect`, `pipe`, `@sub`, `xargs`, plus
  the README/`examples.md` sample configs.
- **Conformance corpus**: a fixture set of `(config, command) → policy` triples
  checked against the real `localmost` binary in CI when available (skipped
  otherwise), to catch parser divergences. Divergences are documented, not
  silently accepted.
- **Config**: `Backend()` resolution and validation errors.
- Fixtures use Scots words per the project convention.

## Open questions

1. **`mode` vs `backend` naming.** This doc keeps `mode` (prompt/notify) and
   adds `backend`. Alternative: fold everything into `mode` and drop the
   distinction. Recommendation: keep both — `backend` is the "who decides" axis
   and matches the sandbox mental model; `mode` stays the "what to do when
   deferred" axis. **Decision needed.**
2. **Shell parser dependency.** Take on `mvdan.cc/sh` (new dependency, not
   ShellCheck-identical) vs. a bespoke minimal parser. Recommendation:
   `mvdan.cc/sh` — reimplementing a bash parser is out of proportion to the
   task and riskier. **Decision needed.**
3. **Fidelity bar for "100% compatible".** Do we commit to bit-identical
   parsing (effectively impossible without ShellCheck) or to
   documented-behavioural-compatibility with a conformance suite?
   Recommendation: the latter.
4. **Where the `builtin` engine runs.** Daemon (proposed — has config, matches
   today) vs. in-process in `gr approve-request` (avoids sending full input
   over the wire, but splits policy across processes). Recommendation: daemon.

[lm]: https://github.com/federicotdn/localmost
[mvdansh]: https://github.com/mvdan/sh
