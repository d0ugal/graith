---
weight: 100
title: "Getting Started"
description: "Create your first session and learn the basics."
icon: "rocket_launch"
toc: true
draft: false
---

## First session

Running agents inside graith's OS sandbox is strongly recommended, but it is
**off by default** — graith does not assume a backend is installed. To enable
it, install a backend and turn the sandbox on:

```bash
brew install nono # macOS or Linuxbrew
gr doctor
```

Then set `[sandbox] enabled = true` and pick a `backend` (`nono`, or on macOS
`safehouse`). See the [sandbox setup]({{< relref "sandbox/_index.md#setup" >}})
for details. Once enabled, graith won't start or resume an agent if the backend
can't enforce. Until you enable it, agents run under their own native controls
only.

The daemon starts on your first command. Create a session:

```bash
gr new fix-auth-bug
```

Fetches origin, creates a worktree on a new branch, starts a Claude agent, and attaches. The agent works in an isolated repo copy — your checkout is untouched.

## Detach and reattach

Press `ctrl+b d` to detach. The agent keeps running. Reattach later:

```bash
gr attach fix-auth-bug
```

Or `gr attach` with no arguments opens the picker.

## Multiple sessions

```bash
gr new refactor-api
gr new add-tests --agent codex
gr new explore-codebase --background    # don't attach yet
```

Switch with `ctrl+b n` (next) / `ctrl+b p` (previous), or `ctrl+b w` for the picker overlay.

## Session picker

The picker shows all sessions as one parent/child tree by default. Repository
names stay visible on each row, including when a parent and child belong to
different repositories:

- Session name and status (running, stopped, errored)
- Agent type
- Current tool being used (from hook reports)
- Branch name and git status (dirty, unpushed commits)
- Status summary (set by the agent or you)

Navigate with arrow keys or `j`/`k`. Press `enter` to attach.

Cycle views with `h`/`l` or left/right arrows:

| View | Shows |
|------|-------|
| All | Every session in one global parent/child tree, with repository names on each row |
| Repo | Every session grouped by repository, with a separate tree inside each group |
| Starred | Starred sessions in a parent/child tree |
| Labels | Sessions grouped by label across repositories, with a tree inside each label |
| Scenarios | Every session grouped by scenario, with a tree inside each group and unassigned sessions kept together |
| Deleted | Recently deleted sessions available to restore |

## Sending prompts

Start with an initial prompt:

```bash
gr new fix-tests --prompt "the auth tests are flaky, find out why"
gr new migration --prompt-file ./migration-plan.md
```

Or type into a running session:

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

Branch off from a conversation:

```bash
gr fork fix-auth-bug auth-approach-2
```

Copies the source worktree and starts a fresh agent. With `fork_args` configured, the new agent inherits the source's conversation history. The source session is unaffected.

## Monitoring

```bash
gr list                    # all sessions in parent-child hierarchy
gr list --flat             # use flat repo/name ordering
gr list --starred          # starred sessions only
gr logs fix-auth-bug       # show recent output
gr logs fix-auth-bug -f    # follow output live
gr info                    # info for current session (auto-detected from cwd)
```

## Batch operations

```bash
gr stop --repo myproject                    # all sessions for a repo
gr stop --repo myproject --stopped          # only stopped/errored ones
gr delete --repo myproject --stale 7d       # sessions not attached for 7 days
gr delete --repo myproject --stale 7d -f    # skip confirmation
gr stop --children                          # stop all child sessions (from inside a session)
gr delete --children                        # delete all child sessions
```
