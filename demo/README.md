# graith demo recording

This directory holds everything needed to (re-)record the graith demo GIF with
[VHS](https://github.com/charmbracelet/vhs), Charmbracelet's declarative
terminal recorder.

| File | Purpose |
|------|---------|
| `demo.tape` | The VHS script — keystrokes, waits, and output settings. |
| `setup.sh` | Stands up an **isolated** `demo` graith profile with real agent sessions. |
| `teardown.sh` | Removes the demo profile, its sessions, worktrees, and daemon. |
| `graith.gif` | The rendered output — produced by `make demo`, then committed and embedded in the repo README. |

## How it works

The demo runs against a dedicated `GRAITH_PROFILE=demo` instance, fully isolated
from your real sessions, config, and data
(`~/.config/graith-demo/`, `~/.graith-demo/`). It uses your **real local
agents** (`claude`/`codex`) under **your real sandbox configuration** — both
copied straight out of your default graith config
(`~/.config/graith/config.toml`) — so the recording is the genuine article, not
a mock-up. Sessions start idle; the tape then attaches and types real prompts so
you see an actual agent respond.

`setup.sh` creates a spread of sessions — some running, one stopped, two agent
types, one starred, each with a status summary — so the recording looks like a
real working fleet. It copies only the `[agents.*]` and `[sandbox]` tables from
your default config (not triggers, repos, or MCP servers); set
`GRAITH_DEMO_SRC_CONFIG` to copy from a different file. It prints the sandbox
posture it detected and **warns loudly** if the copied config leaves the agents
unsandboxed; if no agent/sandbox config is found at all it falls back to
unsandboxed `claude`/`codex`. Safety guardrails: the fixed `demo` profile's
config, data, and runtime directories carry matching per-run ownership markers.
The harness refuses setup and teardown if any target is pre-existing, unmarked,
or mismatched, and it never contacts an unowned `demo` daemon.

## Re-recording

Install VHS once:

```bash
brew install vhs
# or:
go install github.com/charmbracelet/vhs@latest
```

Then, from the repo root:

```bash
make demo
```

That builds `gr`, runs `setup.sh`, records `demo.tape` → `demo/graith.gif`, and
runs `teardown.sh`. If VHS fails partway through, clean up with:

```bash
make demo-clean
```

### Manual control

To iterate on the tape without the full build/teardown cycle:

```bash
./demo/setup.sh                 # set up the isolated demo environment
vhs demo/demo.tape              # record (uses GRAITH_PROFILE=demo)
./demo/teardown.sh              # clean up
```

`vhs -p demo/demo.tape` shows a live preview while iterating.

## Notes & gotchas

- **Run it locally, unsandboxed.** Recording needs a real TTY (VHS spawns its
  own terminal), the daemon binds a unix socket, and sessions create git
  worktrees — all of which a restrictive agent sandbox blocks. This is the
  intended "middle ground": recorded by a human on their machine, not in CI.
  (The *agents* in the demo are still safehoused as normal — it's the recording
  process itself, i.e. your shell running `make demo`, that must be unsandboxed.)
- **It talks to the agents.** The tape types real prompts into `claude`/`codex`,
  so recording spends a small amount of API tokens and needs your normal agent
  auth. The prompts are read-only questions, not mutating tasks.
- **The demo repo** defaults to this checkout. Override with `DEMO_REPO=/path`
  before `setup.sh` if you want the sessions to sit on a different repo.
- **Editing the script:** VHS is declarative — tweak `Sleep` durations to pace
  the demo, `Set FontSize`/`Width`/`Height` for dimensions, and the `Type`/key
  lines for the command sequence. See the comments in `demo.tape`.
- **Other formats:** add another `Output` line to `demo.tape` (e.g.
  `Output demo/graith.mp4` or `.webm`) to emit video alongside the GIF.

## Embedding in the README

Once `demo/graith.gif` exists (after `make demo`) and is committed, embed it near
the top of the repo `README.md`:

```markdown
<p align="center">
  <img src="demo/graith.gif" alt="graith demo — a fleet of AI agents in parallel" width="900">
</p>
```
