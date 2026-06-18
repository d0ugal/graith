# Getting Started

## First session

The daemon starts automatically on your first command. Create a session:

```bash
gr new fix-auth-bug
```

This fetches the latest from origin, creates a git worktree on a new branch, starts a Claude agent in that worktree, and attaches your terminal. The agent sees an isolated copy of the repo and can work without affecting your main checkout.

## Detach and reattach

Press `ctrl+b d` to detach. The agent keeps running. Reattach later:

```bash
gr attach fix-auth-bug
```

Or run `gr attach` with no arguments to open the session picker.

## Multiple sessions

Create more sessions for parallel work:

```bash
gr new refactor-api
gr new add-tests --agent codex
gr new explore-codebase --background    # don't attach yet
```

Switch between them with `ctrl+b n` (next) and `ctrl+b p` (previous), or press `ctrl+b w` to open the session picker overlay.

## Session picker

The session picker (overlay) shows all sessions grouped by repo. It displays:

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
| Needs Attention | Sessions waiting for approval, errored, idle (running but ready), or stopped with uncommitted/unpushed changes. Sorted oldest-first by time in current state |
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

The model string is passed through to the agent command. It only works if the agent's config includes `{model}` in its args (agents like `cursor` support `--list-models` validation via `validate_model` in config).

## Lifecycle operations

```bash
gr stop fix-auth-bug        # stop agent, keep worktree
gr restart fix-auth-bug     # restart agent in same worktree
gr delete fix-auth-bug      # kill agent, remove worktree and branch
gr rename fix-auth-bug auth-fix
gr star important-session   # protect from accidental deletion
gr unstar important-session
```

## Forking

Fork a session to branch off from a conversation:

```bash
gr fork fix-auth-bug auth-approach-2
```

This creates a new worktree (copied from the source) and a new agent process. If the agent has `fork_args` configured, the new agent inherits the source agent's conversation history. The source session is unaffected.

## Monitoring

```bash
gr list                    # all sessions with status
gr list --tree             # show parent-child hierarchy
gr list --starred          # starred sessions only
gr logs fix-auth-bug       # show recent output
gr logs fix-auth-bug -f    # follow output live
gr dashboard               # live TUI with inline controls
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
