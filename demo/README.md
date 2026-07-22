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
your default config (not triggers or repos); set
`GRAITH_DEMO_SRC_CONFIG` to copy from a different file. It reports the sandbox
posture it detected and rejects a copied config that leaves agents unsandboxed.
If no sandbox config is found, it
uses the mandatory platform sandbox (`safehouse` on macOS, `nono` on Linux) and
the built-in non-interactive agent definitions. Safety guardrails: the fixed
`demo` profile's config, data, and runtime directories carry matching per-run
ownership markers. Setup and teardown refuse before contacting `gr` if any
present target is unmarked or mismatched, so an existing `demo` profile or
daemon cannot be adopted or purged. The runtime directory is ephemeral: if it
alone disappears after a reboot, matching durable config/data markers authorize
the harness to reconstruct it before any daemon or session operation.

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

## Testing

Run the ownership and teardown regressions with:

```bash
make demo-test
```

The tests isolate `HOME`, `XDG_CONFIG_HOME`, and `XDG_RUNTIME_DIR` under a
temporary directory. They also exercise runtime-directory recreation with the
real `gr` binary while replacing demo agent launches with inert test commands.

On Linux, `XDG_RUNTIME_DIR` must be set. Graith otherwise falls back to a
separate `/run/user/<uid>` location that the harness cannot safely associate
with its fixed demo paths, so setup and teardown fail closed instead of
contacting an untracked daemon. macOS safely folds its unset-XDG runtime
fallback into the owned demo data directory only with the default data home;
when `XDG_DATA_HOME` is customized, set `XDG_RUNTIME_DIR` explicitly too.

The generated config is covered by an ownership checksum. Editing it after
setup—including changing `data_dir`—makes both setup and teardown refuse; clean
up such a deliberately modified profile manually after checking its paths.

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
