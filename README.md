# graith

**Run a fleet of AI coding agents in parallel — each in its own git worktree, each in a session that outlives your terminal.**

<p align="center">
  <img src="demo/graith.gif" alt="graith demo — attach to a running agent, view the fleet, and drive them from the session picker" width="900">
</p>

graith is a terminal multiplexer built for AI coding agents (Claude, Codex, OpenCode, Cursor, Agy). Spin up an agent per task, let them work isolated and unattended, and jump between them with a tmux-style prefix key. A long-lived daemon owns the sessions, so closing your terminal — or losing your SSH connection — doesn't stop the work.

**graith** (Scots) — *noun:* equipment, tools, gear for a specific trade. *verb:* to make ready, prepare, equip. Your agents, graithed and ready to work.

📖 **[Documentation](https://d0ugal.github.io/graith/)** — full guide, CLI reference, configuration, and architecture.

## Why

Running several agents at once shouldn't mean juggling terminal tabs and stepping on your own branches. graith gives you:

- **Isolation** — every agent gets its own git worktree and branch, so parallel work never collides
- **Persistence** — a daemon owns the PTYs; sessions survive terminal closures, daemon restarts, and SSH drops
- **Switching** — hop between agents instantly with a tmux-style prefix key
- **Visibility** — see every session at a glance, group the picker by repository or scenario, and use `gr ls --tokens` for per-session token usage
- **Coordination** — agents message each other over pub/sub, and you drive them remotely with `type`, `logs`, and the attached-session picker

It owns the PTY, manages the worktrees, and otherwise gets out of your way.

## Install

With Homebrew:

```bash
brew install d0ugal/tap/graith
```

For apt, dnf, prebuilt binaries, Go, and source installs, see the [installation guide](https://d0ugal.github.io/graith/docs/installation/). Then follow the [getting started guide](https://d0ugal.github.io/graith/docs/getting-started/) to create your first session.

Session metadata changes share one update command and can be combined:

```bash
gr update important-session --starred --name release-watch
gr update release-watch --starred=false
```
