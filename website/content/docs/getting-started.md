---
weight: 100
title: "Getting Started"
description: "Create your first session and learn the basics."
icon: "rocket_launch"
toc: true
draft: false
---

## First session

By default, agents run inside an OS sandbox. The default backend is `nono`, so
install it before starting a session:

```bash
brew install nono # macOS or Linuxbrew
gr doctor
```

On macOS you can instead install safehouse and set
`[sandbox] backend = "safehouse"` — see [sandbox setup]({{< relref
"sandbox/_index.md#setup" >}}). Graith won't start or resume an agent if the
backend can't enforce.

The daemon starts automatically on your first command. Create a session:

```bash
gr new fix-auth-bug
```

Pulls from origin, creates a worktree on a new branch, starts a Claude agent there, and attaches your terminal. The agent gets an isolated copy of the repo; your main checkout is untouched.

## Detach and reattach

Press `ctrl+b d` to detach. The agent keeps running. Reattach later:

```bash
gr attach fix-auth-bug
```

Or `gr attach` with no arguments opens the picker.

## Multiple sessions

Create more sessions for parallel work:

```bash
gr new refactor-api
gr new add-tests --agent codex
gr new explore-codebase --background    # don't attach yet
```

Switch with `ctrl+b n` (next) / `ctrl+b p` (previous), or `ctrl+b w` for the picker overlay.

## Session picker

The picker shows all sessions grouped by repo:

- Session name and status (running, stopped, errored)
- Agent type
- Current tool being used (from hook reports)
- Branch name and git status (dirty, unpushed commits)
- Status summary (set by the agent or you)

Navigate with arrow keys or `j`/`k`. Press `enter` to attach.

Cycle views with `h`/`l` or left/right arrows:

| View | Shows |
|------|-------|
| All | Every session, grouped by repo |
| Needs Attention | Errored sessions, agent runtime errors, idle sessions (running but ready), or stopped sessions with uncommitted/unpushed changes. Sorted oldest-first by time in current state |
| Active | Running sessions only, newest first |
| Starred | Starred sessions only |

## Sending prompts

Start a session with an initial prompt:

```bash
gr new fix-tests --prompt "the auth tests are flaky, find out why"
gr new migration --prompt-file ./migration-plan.md
```

Or type into a running session from outside:

```bash
gr type fix-tests "please also check the integration tests"
```

## Choosing a model

Pass `--model` to expand `{model}` in the agent's configured args:

```bash
gr new quick-fix --model sonnet
gr new deep-analysis --model opus
```

The string is passed straight to the agent command, so it only works if the agent's args include `{model}`. Some agents (like `cursor`) validate it via `validate_model`.

## Lifecycle operations

```bash
gr stop fix-auth-bug        # stop agent, keep worktree
gr restart fix-auth-bug     # restart agent in same worktree
gr migrate fix-auth-bug --agent codex  # swap agent in place (e.g. on a provider outage)
gr delete fix-auth-bug      # kill agent, remove worktree and branch
gr update fix-auth-bug --name auth-fix
gr update important-session --starred       # protect from accidental deletion
gr update important-session --starred=false # remove deletion protection
```

## Forking

Fork a session to branch off from a conversation:

```bash
gr fork fix-auth-bug auth-approach-2
```

This copies the source worktree into a new one and starts a fresh agent process. With `fork_args` configured, the new agent inherits the source's conversation history. The source session is unaffected.

## Monitoring

```bash
gr list                    # all sessions with status
gr list --tree             # show parent-child hierarchy
gr list --starred          # starred sessions only
gr logs fix-auth-bug       # show recent output
gr logs fix-auth-bug -f    # follow output live
gr info                    # info for current session (auto-detected from cwd)
```

## Batch operations

Stop or delete multiple sessions at once:

```bash
gr stop --repo myproject                    # all sessions for a repo
gr stop --repo myproject --stopped          # only stopped/errored ones
gr delete --repo myproject --stale 7d       # sessions not attached for 7 days
gr delete --repo myproject --stale 7d -f    # skip confirmation
gr stop --children                          # stop all child sessions (from inside a session)
gr delete --children                        # delete all child sessions
```
