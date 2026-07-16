# demo.tape — VHS script for the graith demo GIF.
#
# Render with:  make demo            (builds gr, sets up an isolated demo
#                                      environment, records, then tears down)
# Or manually:  demo/setup.sh && vhs demo/demo.tape && demo/teardown.sh
#
# VHS docs: https://github.com/charmbracelet/vhs
#
# The flow leads with *attaching* to a session — the whole point is that it's
# the real Claude Code / Codex you already know, running under graith. Then it
# shows the fleet view, spinning up an agent, messaging, and the picker overlay.
#
# Prerequisites (make demo handles these):
#   - the `demo` profile is set up (demo/setup.sh) with a mix of running/stopped
#     real agent sessions, so this only ever types read-friendly commands.
#   - vhs runs from the repo root, so `gr new` / `gr attach` default to this
#     repo, and `gr` resolves to the freshly-built binary (PATH is set by make).

Output demo/graith.gif

# Fail fast if gr isn't on PATH rather than recording an error.
Require gr

# ---- Look & feel -----------------------------------------------------------
# Catppuccin Mocha to match graith's design language (dark, monospace).
Set Shell "bash"
Set Theme "Catppuccin Mocha"
Set FontSize 18
Set Width 1200
Set Height 760
Set Padding 24
Set Margin 24
Set MarginFill "#1e1e2e"
Set BorderRadius 12
Set WindowBar Colorful
Set TypingSpeed 55ms

# Talk to the isolated demo profile (never the user's real sessions).
Env GRAITH_PROFILE demo

# Start from a clean prompt.
Hide
Type "clear" Enter
Show
Sleep 500ms

# ============================================================================
# 1. It's the Claude Code you already know — attach and talk to it.
# ============================================================================
Type "# attach to a session — it's the real Claude Code, right where you left it"
Enter
Sleep 1s
Type "gr attach tracing-api"
Enter
Sleep 4s
# Type a real prompt straight to the agent.
Type "in two sentences, what does this project do?"
Sleep 800ms
Enter
# Let the agent actually answer.
Sleep 10s
# Detach with the tmux-style prefix — the agent keeps running in the background.
Ctrl+B
Sleep 500ms
Type "d"
Sleep 2s

# ============================================================================
# 2. The whole fleet at a glance.
# ============================================================================
Type "# every agent runs in its own git worktree, and outlives your terminal"
Enter
Sleep 1s
Type "gr list"
Enter
Sleep 4s
Type "gr ls --wide"
Enter
Sleep 4s

# ============================================================================
# 3. Spin up another agent in one line.
# ============================================================================
Type "gr new refactor-cli --agent codex --background"
Enter
Sleep 3s
Type "gr list"
Enter
Sleep 3.5s

# ============================================================================
# 4. Agents — and you — message each other. (A non-mutating status ping: the
# target is a real agent, so keep it a question, never an instruction to act.)
# ============================================================================
Type `gr msg send tracing-api "how's the ingest endpoint coming along?"`
Enter
Sleep 3s

# ============================================================================
# 5. Codex works the same way — attach and drive it too.
# ============================================================================
Type "# same for codex — attach and put it to work"
Enter
Sleep 1s
Type "gr attach trace-ui"
Enter
Sleep 4s
Type "what are the main packages in this repo?"
Sleep 800ms
Enter
Sleep 9s
Ctrl+B
Sleep 500ms
Type "d"
Sleep 2s

# ============================================================================
# 6. The session picker overlay — live previews of every agent.
# ============================================================================
Type "# or drive everything from the picker, with a live preview of each agent"
Enter
Sleep 1s
Type "gr attach"
Enter
Sleep 3s
Down@700ms 2
Sleep 2s
# Cycle view modes: All -> Needs Attention -> Active -> back.
Type "l"
Sleep 2.5s
Type "l"
Sleep 2.5s
Type "h"
Sleep 1.5s
# Close the overlay.
Type "q"
Sleep 1.5s

# End on the wide table — a meaningful frame to loop on.
Type "gr ls --wide"
Enter
Sleep 4s
