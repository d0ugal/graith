---
title: "Design Doc: Richer Per-Session Metadata (Listening Ports & Linked PR)"
authors: Dougal Matthews
created: 2026-06-25
status: Draft
reviewers: (none yet)
informed: (TBD)
---

# Richer Per-Session Metadata (Listening Ports & Linked PR)

> **⚠ Superseded by
> [`2026-06-25-pr-ci-awareness-design.md`](2026-06-25-pr-ci-awareness-design.md).**
> After review, listening ports were judged low-value for graith's workflow
> (and process-tree `lsof` misses Docker/Compose-published ports — the common
> Grafana dev case). The PR work was refocused from passive display into an
> active **PR & CI awareness + agent-notification loop**. Ports are demoted to a
> Future note there. This doc is retained for the port-detection mechanism
> study and the cmux PR-fetch findings, which the successor reuses.

> **Note on code references.** `file:line` citations are anchored to symbol
> names and were written against this design branch; absolute line numbers
> drift against `main`, so trust the symbol name, not the number. Mechanism
> claims about cmux were verified against a local clone at
> `~/Code/manaflow-ai/cmux` (see References).

## Background

graith runs each agent in its own PTY session inside an isolated git worktree.
A session is described by `SessionState` (`internal/daemon/state.go`), and the
daemon already derives and surfaces *live* per-session metadata beyond the
static fields. The model to follow is **git status**: `detectAgentStatuses`
(`internal/daemon/daemon.go`) runs on a 500 ms ticker
(`RunDetectionLoop`), and for each running session it computes
`GitDirty`/`GitUnpushed` by shelling out via the `internal/git` package
(`HasUncommittedChanges`, `UnpushedCommitCount`), stores them as **runtime-only**
fields on `SessionState` (tagged `json:"-"`, so they never hit `state.json`),
and `toSessionInfo` (`internal/daemon/handler.go`) copies them into the wire
type `protocol.SessionInfo` (`internal/protocol/messages.go`). From there:

- `gr list` renders them through `formatBranch`/`formatGitStatus`
  (`internal/cli/list.go`); `gr list --json` marshals the whole
  `SessionListMsg`.
- The overlay sidebar renders them through `displayGit` and the preview panel
  `line1`/`line2` builders (`internal/client/overlay.go`).
- The future macOS GUI polls `gr list --json` every 2 s and renders a session
  sidebar (`docs/design/.../gui` POC; `SessionStore`/`SessionSidebar`).

Crucially, the daemon already tracks each session's agent PID and start time —
`SessionState.PID` / `SessionState.PIDStartTime` — so the process whose
descendants we need to inspect for listening ports is already known.

cmux, a comparable product, surfaces two pieces of per-workspace metadata that
graith does not, both of which materially help when an agent is running a dev
server or has opened a PR:

1. **Listening ports** opened by the agent/process inside the workspace.
2. **Linked GitHub PR** number + state (open / merged / closed) for the
   workspace branch.

This design adds both to graith, reusing the git-status pattern end-to-end.

**Reference:** cmux port detection lives in `Sources/PortScanner.swift`
(local, macOS) and `Packages/macOS/CmuxRemoteSession/.../RemoteSessionCoordinator+PortScan.swift`
(remote, over SSH). cmux PR detection lives in
`Packages/macOS/CmuxGit/.../PullRequestProbeService.swift` (fetch) and
`Packages/macOS/CmuxSidebarGit/.../PullRequestPollService.swift` (poll/cache).

## Problem

When an agent runs a dev server or opens a PR, the user has no in-graith
visibility:

- **Ports are invisible.** An agent that starts `npm run dev` on `:5173` or a
  backend on `:8080` gives no signal in `gr list` or the overlay. The user must
  attach and read scrollback to learn what's listening and where — and when
  several sessions each run a server, there's no at-a-glance map of which
  session owns which port. This is the single most-requested cmux feature graith
  lacks.
- **PR status is invisible.** A session's branch may have an open PR (or a
  merged/closed one), but graith shows only the branch name and ahead-count.
  The user can't tell from the overlay whether the branch is already in review,
  already merged (worktree is now stale), or still local-only — they leave
  graith and run `gh pr view` per session.
- **The GUI sidebar is thin.** The macOS GUI POC mirrors `gr list` fields. To
  match cmux's workspace tabs it needs richer metadata flowing through the same
  protocol, not a GUI-only side channel.

The constraint that shapes the design: this metadata is **derived and
best-effort**. Port scanning shells out to OS tools that differ by platform and
may be blocked by the sandbox; PR status hits a rate-limited external API that
may be unauthenticated or point at a non-GitHub remote. Neither may ever block
or slow `gr list`, and both must degrade silently to "no data" rather than error.

## Goals

1. Surface **listening TCP ports** opened anywhere in a running session's agent
   process tree, in `gr list`/`--json`, the overlay sidebar, and the GUI
   sidebar — reusing the git-status derive-and-publish pattern.
2. Surface the **linked GitHub PR** (number + state: open/draft/merged/closed,
   plus URL) for a session's branch, in the same three surfaces.
3. **Never block or slow `gr list`.** Both features are computed off the request
   path on the existing detection cadence; a request reads only cached values.
4. **Bounded external cost.** PR lookups are cached with explicit TTLs and a
   conservative poll interval, justified below, so a fleet of sessions cannot
   hammer the GitHub API or trip rate limits.
5. **Graceful, silent degradation.** No `lsof`/`gh`, sandbox-blocked scans,
   unauthenticated `gh`, non-GitHub remotes, or API failures all degrade to
   "no metadata shown" — never an error, never a stall.
6. **Cross-platform port detection** (macOS and Linux), with the sandbox
   implications of walking the session process tree spelled out.

### Non-Goals

- **Port forwarding / proxying.** cmux's remote-session SSH relay and
  port-forward UX are out of scope; graith sessions are local to the daemon
  host. We *detect and display* ports, we do not tunnel them.
- **Remote (SSH) sessions.** graith has no remote-host session model, so the
  cmux `ss`/`netstat`-over-SSH path is not ported. The Linux path here is for a
  Linux *daemon host*, not a remote target.
- **UDP / non-listening sockets.** Only listening TCP sockets, matching cmux.
- **Non-GitHub forge support** (GitLab MRs, Gitea, Bitbucket) in v1 — detected
  and skipped gracefully; a pluggable provider is a Future note.
- **CI check status, mergeable flag, review state, PR title.** cmux itself does
  *not* surface these (it fetches only number/state/url). v1 matches that
  minimal set; richer fields are a Future note.
- **Persisting derived metadata to `state.json`.** Ports and PR status are
  ephemeral runtime values (`json:"-"`), exactly like `GitDirty`.
- **A new daemon→client push/subscription channel.** v1 rides the existing
  poll-and-`toSessionInfo` path; a push channel is a separate, larger change
  (noted in the GUI doc as future work).

## Proposals

### Proposal 0: Do Nothing

Keep showing only branch + git dirty/ahead. Users continue to attach to a
session and scrape scrollback to find which port a dev server bound to, and
shell out to `gh pr view` per session to learn review state. As the fleet grows
and the GUI sidebar lands, the gap with cmux's workspace tabs widens: graith has
the PID and branch in hand but exposes neither the ports nor the PR the user
most wants to see. No external cost, no new failure modes — but the most-asked
metadata stays invisible.

### Proposal 1: Derive in the Detection Loop, Publish via `SessionInfo` (Recommended)

Add two new derived-metadata producers that run on the daemon's existing
detection cadence, store results as runtime-only fields on `SessionState`, and
publish them through `toSessionInfo` → `SessionInfo` → all three surfaces —
exactly mirroring how `GitDirty`/`GitUnpushed` already flow. Port scanning runs
inline with the 500 ms git/status sweep (it is local and cheap); PR status runs
on its own slow, cached loop (it is external and rate-limited).

**Architecture diagram:**

```mermaid
graph TD
  subgraph "Daemon — detection loop (500ms, RunDetectionLoop)"
    A["detectAgentStatuses: per running session"] --> B["git status (existing)"]
    A --> C["scanListeningPorts(pid, pidStartTime)<br/>walk process tree → lsof / /proc<br/>coalesced, cached ~2s per session"]
  end
  subgraph "Daemon — PR loop (separate, slow, cached)"
    P["RunPullRequestLoop (new)"] --> Q["per session branch:<br/>resolve owner/repo from remote<br/>gh api repos/:o/:r/pulls?head=...<br/>cache 15s / poll 30-60s / merged-sweep 15m"]
  end
  B --> S["SessionState (runtime-only json:\"-\"):<br/>GitDirty/GitUnpushed +<br/>ListeningPorts []int +<br/>PullRequest *PRStatus"]
  C --> S
  Q --> S
  S --> T["toSessionInfo → protocol.SessionInfo"]
  T --> U["gr list / --json (cli/list.go)"]
  T --> V["overlay sidebar + preview (client/overlay.go)"]
  T --> W["macOS GUI sidebar (polls gr list --json @2s)"]
```

#### 1. State and protocol surface

Add runtime-only fields to `SessionState` (`internal/daemon/state.go`),
mirroring `GitDirty`/`GitUnpushed` — **tagged `json:"-"`** so they never persist
and need no state-version bump or migration:

```go
// SessionState (runtime-only, never persisted)
ListeningPorts []int      `json:"-"`
PullRequest    *PRStatus  `json:"-"`
PRCheckedAt    time.Time  `json:"-"` // last successful PR fetch (drives TTL)
```

`PRStatus` is a small value type (new, in `state.go` or a `pr.go`):

```go
type PRStatus struct {
    Number int    // PR number
    State  string // "open" | "draft" | "merged" | "closed"
    URL    string // html_url
    Stale  bool   // last refresh failed; show last-known dimmed
}
```

Add the wire-visible fields to `protocol.SessionInfo`
(`internal/protocol/messages.go`), `omitempty` so old clients and
no-data sessions stay clean:

```go
ListeningPorts []int       `json:"listening_ports,omitempty"`
PullRequest    *PRInfo     `json:"pull_request,omitempty"`
```

`PRInfo` mirrors `PRStatus` (number/state/url/stale). `toSessionInfo`
(`internal/daemon/handler.go`) copies `s.ListeningPorts` and maps
`s.PullRequest` → `info.PullRequest`, right next to the existing
`Dirty: s.GitDirty` / `UnpushedCount: s.GitUnpushed` lines. No new control
message type is needed — the data rides the existing `list`/`status`
responses, so `gr list --json` and the overlay get it for free.

#### 2. Port detection — where and how

Port detection runs **inside `detectAgentStatuses`**, in the same per-session
loop that already computes git status, gated on `Status == StatusRunning` and a
known `PID`. cmux's mechanism (verified) is a two-command pipeline —
`ps` to expand the process tree, then `lsof` filtered to listening TCP — kicked
by terminal activity with a 200 ms coalesce and an agent-rescan every 2 s
(`PortScanner.swift`: `agentRescanInterval = 2`, `expandAgentProcessTree`). We
adopt the same shape, in a new `internal/portscan` package with a single
entry point:

```go
// internal/portscan/portscan.go
func ListeningPorts(pid int, pidStartTime int64) ([]int, error)
```

- **Process-tree walk.** Start from `SessionState.PID` (the agent) and collect
  all descendant PIDs, so a dev server spawned by the agent (e.g.
  `agent → node → vite`) is caught — cmux walks the full tree, and a one-PID
  scan would miss almost every real dev server. We pass `PIDStartTime` so the
  walk can defend against PID reuse (graith already records it for exactly this
  reason; `internal/daemon` has a PID-reuse fix on record).
- **macOS:** shell out to `lsof`, mirroring cmux's invocation:
  `lsof -nP -a -p <pid-csv> -iTCP -sTCP:LISTEN -Fpn` (numeric, no DNS, filtered
  to listening TCP, machine-readable `-F` output). Tree expansion via
  `ps -ax -o pid=,ppid=` (build the child map in Go).
- **Linux:** prefer parsing `/proc/<pid>/net/tcp{,6}` joined with
  `/proc/<pid>/fd/*` socket inodes — no external binary, no sandbox-exec cost,
  and robust in containers. Fall back to `ss -ltnH` (then `lsof`) if `/proc` is
  unavailable. (cmux's Linux logic lives only in its SSH path — `ss → lsof →
  netstat` — but it confirms the right tool order for a Linux host.)
- **Filtering** (match cmux): drop ports `< 1024` and `> 65535`; de-duplicate;
  sort ascending. Exclude graith's own daemon socket port if ever TCP (it is a
  Unix socket today, so this is a no-op, but documented).
- **Cost control.** The 500 ms git sweep is fine for git, but a full `lsof`
  every 500 ms per session is wasteful. Cache per session with a **~2 s
  min-interval** (cmux's `agentRescanInterval`): `detectAgentStatuses` calls
  `ListeningPorts` only if `time.Since(lastPortScan[id]) > 2s`, else reuses the
  cached slice. This keeps the scan off the hot path and bounds `lsof` spawns to
  ~once/2 s/session.

Store the result under `sm.mu` next to the git-status assignment:
`s.ListeningPorts = ports`. On scan error, **retain the last-known slice** (or
clear after N consecutive failures) rather than flapping to empty.

**Cross-platform + sandbox implications (called out per review).** The daemon
runs **unsandboxed** — it is the sandbox *author*, wrapping agent processes with
`safehouse wrap` (`internal/sandbox`), not itself wrapped. So the port scan,
which the daemon performs, is **not** subject to the agent's sandbox profile:
the daemon can `lsof`/read `/proc` for the agent's descendants regardless of
what the agent itself may do. Two real concerns remain:

- **The descendants are sandboxed**, but their *listening sockets* are still
  visible to an unsandboxed `lsof`/`/proc` reader, because the kernel owns the
  socket table — sandboxing restricts what the child may do, not what the parent
  daemon may observe. Confirmed conceptually; flagged for an integration test on
  a sandboxed session.
- **`lsof` may be slow or absent.** On macOS a busy `lsof` can take 100s of ms;
  the 2 s coalesce + running it off the request path contains this. If `lsof`
  (macOS) or `/proc`+`ss`+`lsof` (Linux) are all missing, `ListeningPorts`
  returns `(nil, err)` and the feature shows nothing — Goal 5.

#### 3. PR status — where, how, and bounded cost

PR status is **not** computed in the 500 ms loop — it is external and
rate-limited. Add a dedicated, slow producer modeled on the existing
`RunGitPullLoop` (`internal/daemon/gitpull.go`), e.g.
`RunPullRequestLoop(ctx)`, launched alongside the other daemon loops.

**Fetch mechanism.** cmux calls the GitHub REST API directly via `URLSession`
and authenticates with a token from, in order, `GH_TOKEN`/`GITHUB_TOKEN`, then
`gh auth token` (which reads the user's keychain), then unauthenticated
(`PullRequestProbeService+Fetch.swift`: `authHeaderValue`, `performRequest`). It
deliberately does **not** shell out to `gh pr` and fetches only number/state/url.

For graith, **shell out to `gh`** rather than reimplement an HTTP client +
auth chain — it matches the codebase's "shell to the tool" idiom (the `git`
package), inherits the user's existing `gh` auth (keychain/`GH_TOKEN`) with zero
new config, and `gh` already resolves the owner/repo from the remote. Concretely,
per session branch:

```
gh pr list --repo <owner/repo> --head <branch> --state all \
   --json number,state,isDraft,url --limit 1
```

run via `exec.CommandContext` with a short timeout (~5 s, matching cmux's
`probeTimeout`). `<owner/repo>` is resolved once per repo from
`git remote get-url origin` (parse GitHub host; **skip non-GitHub remotes**).
`state` maps to open/closed/merged; `isDraft` refines open→draft.

**Known limitation — fork PRs.** `gh pr list --head <branch>` matches a head
branch *within the resolved repo*, so a PR opened from a **fork** (head in a
different owner's repo) may not resolve and will show no badge. This is
acceptable for v1 — graith's own self-development workflow pushes branches to
the same repo — and is called out here so it's a known gap, not a surprise. A
future refinement could qualify the head as `<owner>:<branch>` once the fork
owner is known (cmux does this via `head=owner:branch` in its raw API call).

**Bounded cost — TTLs and intervals (justified, per review).** A naive
"fetch per session per tick" would issue hundreds of API calls/minute across a
fleet and trip GitHub's rate limit (5000/h authenticated, **60/h
unauthenticated**). We adopt cmux's three-layer caching, tuned for graith's
poll-driven (not event-driven) model:

- **Per-(repo,branch) cache, 15 s TTL** — cmux's `repoCacheLifetime = 15`. A
  refresh that finds a cache entry < 15 s old is a no-op. This collapses the
  common case of many sessions on the same repo.
- **Open-PR poll interval: 30–60 s** with ±10 % jitter (cmux uses 10 s for the
  *focused* tab, 60 s background; graith has no "focused session" on the daemon
  side, so a flat 30–60 s is the safe default). Jitter prevents a thundering
  herd when the daemon starts and all sessions are due at once.
- **Terminal-state sweep: 15 min** — once a PR is `merged`/`closed`, re-check
  only every 15 min (cmux `terminalStateSweepInterval = 15*60`); these rarely
  change.
- **Batch cap.** Refresh at most a few sessions per loop pass (cmux caps at 3)
  so a large fleet spreads its calls over time rather than bursting.

With these, a 20-session fleet on 5 repos with open PRs issues on the order of a
few calls/minute — comfortably inside even the unauthenticated budget, and
trivial when authenticated.

**Graceful degradation (per review).** Every failure path leaves
`s.PullRequest` unchanged (or marks `Stale: true` after repeated failures, shown
dimmed — cmux's `isStale` after 3 transient failures) and **never** errors `gr
list`:

- `gh` not installed / not on `PATH` → loop disables itself (logs once).
- `gh` unauthenticated → `gh` returns an auth error; we skip and leave no PR
  badge. (We do *not* fall back to unauthenticated raw HTTP — simpler, and the
  user clearly hasn't set up `gh`.)
- Non-GitHub remote (GitLab, internal host) → owner/repo resolution returns
  "not GitHub", session is permanently skipped (cheap negative-cache).
- No remote / detached / no branch → skipped.
- API/network error or timeout → keep last-known, mark stale, retry next poll.

Store the result under `sm.mu`: `s.PullRequest = &PRStatus{...}`;
`s.PRCheckedAt = time.Now()`.

#### 4. Rendering — `gr list`, overlay, GUI

All three surfaces read the new `SessionInfo` fields; no new transport.

- **`gr list` (`internal/cli/list.go`).** Add a `PORTS` column (compact
  `:5173,:8080`, via a `formatPorts` helper beside `formatGitStatus`) and fold
  PR into the status/branch area, e.g. `PR #123 open`, `#123 draft`,
  `#123 merged`, via a `formatPR` helper. `gr list --json` needs **no code** —
  the marshaled `SessionListMsg` carries the new fields automatically. Keep the
  default table lean (PR as a short token; ports only when non-empty) to avoid
  blowing up width; the JSON carries the full detail.
- **Overlay (`internal/client/overlay.go`).** In the preview-panel builder add a
  `line` after the existing branch/base/agent `line1`: `ports: :5173 :8080` and
  `PR #123 (open)`, styled with the existing `dim`/`warnStyle` lipgloss styles
  (merged → a distinct color). In the compact list row (where `displayGit`
  already appends dirty/ahead) optionally append a port count or PR glyph for
  at-a-glance scanning. cmux renders the PR as a status glyph + `PR #42` +
  state label and ports as `:%d` chips (`SidebarPortDisplayText`,
  `PullRequestStatusIcon`) — the overlay mirrors that compactly.
- **GUI sidebar.** The macOS GUI already polls `gr list --json` every 2 s
  (`SessionStore`) and renders `SessionSidebar`. Because the new fields ride the
  same JSON, the GUI gets them with no protocol change — it renders port chips
  and a PR badge per session row exactly like cmux's workspace tabs. This is the
  payoff of routing through `SessionInfo` rather than a CLI-only formatter.

#### Pros

- **Reuses the proven derive-and-publish pattern** (git status) end-to-end:
  runtime-only state fields, `toSessionInfo`, one wire type, three surfaces.
- **Zero protocol churn** — `omitempty` additions to `SessionInfo`; old clients
  ignore them, `--json` and GUI get them free.
- **No state migration** — fields are `json:"-"`, like `GitDirty`.
- **Bounded, justified external cost** with explicit TTLs; can't hammer GitHub.
- **Honest degradation** — every missing-tool / unauth / non-GitHub / sandbox
  path falls back to "no data," never blocking `gr list`.
- **Cross-platform** with the right tool per OS (`lsof` on macOS, `/proc`/`ss`
  on Linux), and the daemon's unsandboxed position makes the scan feasible.
- **Directly closes the cmux gap** the GUI sidebar most wants.

#### Cons

- **Per-OS port code** (`lsof` vs `/proc`) needs its own tests and a fallback
  matrix; `lsof` can be slow (mitigated by the 2 s coalesce, off-path).
- **`gh` dependency** for PR status; users without `gh` get no PR badge (this is
  acceptable and matches cmux's reliance on `gh auth token`).
- **Best-effort accuracy** — a port that opens and the snapshot that catches it
  are up to ~2 s apart; a just-opened/closed PR up to one poll interval stale.
- **Process-tree walk cost** scales with descendant count; bounded by the
  coalesce interval.

### Proposal 2: Parse ports from PTY scrollback (Rejected)

Instead of scanning the OS, scrape the agent's scrollback
(`internal/pty/scrollback.go`) for "listening on :5173"-style lines, the way
status detection scrapes output. Rejected: hopelessly format-fragile (every
framework prints differently; many print nothing), can't see ports opened by a
quiet child process, and reports stale ports long after the server dies. The OS
socket table is the ground truth; scrollback is a guess. (Noted as a *last*
fallback only if no port tool exists at all — and even then, low value.)

### Proposal 3: Reimplement the GitHub HTTP client + auth in Go (Rejected for v1)

Mirror cmux exactly: a Go `net/http` client hitting `api.github.com` with a
`GH_TOKEN`/`gh auth token`/unauthenticated fallback chain
(`PullRequestProbeService+Fetch.swift`). Rejected for v1: it reimplements auth,
pagination, and `head=owner:branch` resolution that `gh` already does, adds an
HTTP dependency, and buys little over shelling to `gh` on a 30–60 s loop. Revisit
only if shelling to `gh` proves too slow at fleet scale or we need fields `gh`
doesn't expose cheaply.

### Future: richer PR fields, push updates, and non-GitHub forges

- **More PR fields** — CI check rollup, mergeable, review state, title. cmux
  itself fetches none of these; add behind a config flag if demanded (a single
  `gh pr view --json statusCheckRollup,...` per open PR, on the slow loop).
- **Push instead of poll** — replace the GUI's 2 s `gr list` poll and the CLI
  pull model with a daemon→client notification channel so port/PR changes push
  live. This is a broader protocol change already flagged as future work in the
  GUI design.
- **Pluggable forge provider** — a `PRProvider` interface so GitLab MRs / Gitea
  slot in beside GitHub, selected by remote host.

## Other Notes

### References

- `internal/daemon/daemon.go` — `RunDetectionLoop`, `detectAgentStatuses`
  (500 ms git/status sweep; the host for port scanning), `RunGitPullLoop` model
  for the new slow PR loop.
- `internal/daemon/state.go` — `SessionState` (`GitDirty`/`GitUnpushed`
  runtime-only fields as the template; `PID`/`PIDStartTime` for the tree walk);
  no version bump needed (fields are `json:"-"`).
- `internal/daemon/handler.go` — `toSessionInfo` (copy new fields here, beside
  `Dirty`/`UnpushedCount`).
- `internal/protocol/messages.go` — `SessionInfo` (add `ListeningPorts`,
  `PullRequest`); `SessionListMsg` carries them to `--json` automatically.
- `internal/cli/list.go` — `printFlat`/`printTree`, `formatGitStatus`,
  `formatBranch` (add `formatPorts`/`formatPR` beside them).
- `internal/client/overlay.go` — preview `line1`/`line2` builders, `displayGit`
  (add port/PR rendering).
- `internal/git/git.go` — `Run`/`RunContext` shell-out idiom to copy for the
  `gh` and port-tool calls.
- `internal/sandbox/sandbox.go` — confirms the daemon is the sandbox *author*,
  not sandboxed itself, so the port scan can observe sandboxed descendants.
- **cmux — ports:** `Sources/PortScanner.swift` (`ps`+`lsof`,
  `expandAgentProcessTree`, `agentRescanInterval = 2`, 200 ms coalesce, port
  range filter); `RemoteSessionCoordinator+PortScan.swift` (Linux tool order
  `ss → lsof → netstat`); `SidebarPortDisplayText.swift` (`:%d` chips).
- **cmux — PR:** `PullRequestProbeService.swift` /
  `PullRequestProbeService+Fetch.swift` (`performRequest`, `authHeaderValue`,
  `repoCacheLifetime = 15`, `head=owner:branch`); `PullRequestPollService.swift`
  (`backgroundPollInterval = 60`, `selectedPollInterval = 10`,
  `terminalStateSweepInterval = 900`, ±10 % jitter, batch limit 3, `isStale`
  after 3 transient failures); `ContentView.swift` `PullRequestDisplay` /
  `PullRequestStatusIcon`.

### Alternatives considered

- **A new daemon→client subscription for metadata** instead of poll: cleaner
  for live updates but a much larger protocol change; deferred (Future).
- **Scanning every session including stopped ones:** pointless — a stopped agent
  has no process and no listening ports; gate on `StatusRunning` like the git
  sweep does.
- **Caching PR status in `state.json`:** rejected — it's ephemeral and would go
  stale across daemon restarts; keep it runtime-only like `GitDirty`.
- **Unauthenticated raw GitHub HTTP fallback** when `gh` is unauthenticated:
  rejected — 60/h budget is too small for a fleet and it adds an HTTP client we
  otherwise don't need; degrade to "no badge" instead.

### Implementation Notes

| File | Change |
|------|--------|
| `internal/portscan/portscan.go` | New: `ListeningPorts(pid, pidStartTime)`; process-tree walk; macOS `lsof -nP -a -p <pids> -iTCP -sTCP:LISTEN -Fpn` + `ps` tree; Linux `/proc/<pid>/net/tcp{,6}`+fd inodes, `ss`/`lsof` fallback; port-range filter, dedupe, sort |
| `internal/daemon/daemon.go` | In `detectAgentStatuses`: per running session, call `portscan.ListeningPorts` gated by a ~2 s per-session min-interval cache; assign `s.ListeningPorts` under `sm.mu` next to git status |
| `internal/daemon/gitpull.go` (or new `prstatus.go`) | New `RunPullRequestLoop`: per-(repo,branch) 15 s cache, 30–60 s open-PR poll w/ ±10 % jitter, 15 min merged/closed sweep, batch cap; resolve owner/repo from remote, skip non-GitHub; shell `gh pr list --repo .. --head .. --state all --json number,state,isDraft,url --limit 1` w/ 5 s timeout; set `s.PullRequest`/`s.PRCheckedAt`; degrade silently |
| `internal/daemon/state.go` | Add runtime-only `ListeningPorts []int`, `PullRequest *PRStatus`, `PRCheckedAt time.Time` (all `json:"-"`); add `PRStatus{Number,State,URL,Stale}` |
| `internal/daemon/handler.go` | `toSessionInfo`: copy `s.ListeningPorts`; map `s.PullRequest`→`info.PullRequest` |
| `internal/protocol/messages.go` | `SessionInfo`: add `ListeningPorts []int` + `PullRequest *PRInfo` (`omitempty`); add `PRInfo{Number,State,URL,Stale}` |
| `internal/cli/list.go` | `formatPorts`/`formatPR` helpers; `PORTS` column + PR token in `printFlat`/`printTree` |
| `internal/client/overlay.go` | Preview panel: ports + PR line; compact row glyph via `displayGit` neighbor |

**Cost/degradation model (human + JSON):** port scan off the request path,
≤ once/2 s/session; PR loop bounded by 15 s cache + 30–60 s poll + 15 min
terminal sweep + batch cap; every missing-tool / unauth / non-GitHub / sandbox /
API-error path leaves last-known or empty and never blocks `gr list`.

**Tests:**

| File | Change |
|------|--------|
| `internal/portscan/portscan_test.go` | Bind a listener in-test on an ephemeral port, assert `ListeningPorts` for the test PID + a child PID includes it (process-tree walk); filter drops `< 1024`; dedupe/sort; missing-tool → `(nil, err)` not panic. Fixtures: session `braw`, child server `bothy`, edge case `dreich` |
| `internal/daemon/daemon_test.go` | `detectAgentStatuses` populates `ListeningPorts` for a running session (`bide`) and leaves a stopped one (`thrawn`) empty; 2 s min-interval cache honored; sandboxed session (`canny`) ports still observed by the unsandboxed daemon |
| `internal/daemon/prstatus_test.go` | Owner/repo parse from `git@github.com:croft/loch.git` & `https://`; non-GitHub remote (`glen`) skipped; `gh` JSON `{number,state,isDraft}`→`PRStatus` (open/draft/merged/closed); failure keeps last-known + `Stale`; 15 s cache suppresses re-fetch; merged → 15 min sweep |
| `internal/cli/list_test.go` | `formatPorts`/`formatPR` rendering (open/draft/merged), empty → no column noise; `--json` carries `listening_ports`/`pull_request` |
| `internal/integration/integration_test.go` | End-to-end: a session (`bonnie`) running a server shows its port in `gr list --json`; a session with no `gh`/non-GitHub remote (`scunner`) shows no PR and `gr list` still succeeds (never blocks) |

Per repo convention, test fixture strings use old Scots words (e.g. `braw`,
`bothy`, `bide`, `thrawn`, `canny`, `croft`, `loch`, `glen`, `bonnie`,
`scunner`, `dreich`).
