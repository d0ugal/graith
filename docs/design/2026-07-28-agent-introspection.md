---
title: "Design Doc: Agent Introspection"
authors: Codex
created: 2026-07-28
status: Implemented
reviewers: review-tribunal
informed: Graith users
issue: https://github.com/d0ugal/graith/issues/1815
---

# Agent Introspection

Graith needs a config-driven way to list configured agents and run small provider
information probes, such as Cursor model discovery, from the daemon environment
that can actually see the provider CLI.

## Background

The `gr` CLI is stateless and talks to the long-lived daemon over the framed
control protocol. The daemon owns effective configuration, agent launch
environment, optional Graith sandbox wrapping, and the paths a provider CLI may
need. Skills such as review-tribunal need to discover available providers and,
for Cursor, query `agent --list-models` from that daemon/provider context.

Agent launches are already config-driven through `[agents.<name>]`; Graith should
not add provider-specific hard-coded command lines for one model catalog.

## Problem

Before this change, Graith could create sessions with configured agents but had
no direct way to show which agents were configured or to run configured provider
info commands. A skill had to guess available providers or try to run provider
CLIs from the orchestrator shell, which may not have Cursor's `agent` binary,
PATH, sandbox grants, or provider-specific environment.

## Goals

- List configured agents with enough metadata for agents and scripts to choose a
  provider.
- Run provider-neutral info keys mapped to provider-native argv fragments.
- Execute probes through daemon-owned provider context and optional sandboxing.
- Return structured output for automation and a readable form for humans.
- Keep the design extensible for new info keys without adding top-level provider
  commands.
- Fail clearly for missing config, unknown agents or keys, command failures, and
  timeouts.

### Non-Goals

- Add a top-level `gr models` command.
- Define a common model schema across providers.
- Run normal session launch args, prompt injection, hooks, or included-repo flags
  for info probes.
- Surface provider info probes in native app UI.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | The feature is for humans, shell scripts, and agent skills that call `gr`. |
| iOS | Excluded | Native apps can use the existing agent catalog for picker data, but running provider CLIs is a local daemon/scripting workflow. |
| macOS | Excluded | The macOS app should not expose provider subprocess probes; it can keep using catalog data for session creation. |

## Proposals

### Proposal 0: Do Nothing

Graith could leave model/provider discovery to skills and local shell snippets.
This keeps the daemon unchanged but breaks the Cursor use case because the
orchestrator shell is not guaranteed to have the provider binary or sandbox
paths. It also makes each skill rediscover the same config and error handling.

### Proposal 1: Configured Agent Info Commands (Recommended)

Add `gr agent list` and `gr agent info <agent> [key]`. The daemon returns a
catalog of configured agents, including configured info keys, and accepts an
`agent_info` control message to run one or all configured keys. Each key maps to
an argv fragment under `[agents.<name>.info]`; Graith combines that with the
agent's configured `command` and runs it in a scratch worktree with the agent's
configured environment and optional Graith sandbox.

The daemon treats each probe as lifecycle work so upgrades and shutdown wait for
in-flight provider subprocesses. Probe commands run in their own process
group/session, and timeout cleanup kills that group so child processes cannot
outlive the probe or hold captured output pipes open. Command failures and
timeouts are reported per result with stdout, stderr, exit code, and an error
string, so a multi-key request can still return successful sibling probes.

The trade-off is that Graith captures provider text without understanding its
schema. That is deliberate for v1: Cursor model lines and `--version` output are
already provider-defined, and JSON consumers can parse the specific key they
asked for.

### Proposal 2: Provider-Specific Commands

Graith could add commands such as `gr cursor models` and `gr codex version`.
That gives each provider a tailored UX, but it hard-codes provider behavior into
Graith, does not help custom agents, and creates a new command every time a
provider exposes another useful metadata query.

## Consensus

Review agreed with the config-driven shape and requested three safeguards:
validate info keys and non-empty argv at config load, avoid aborting all keys
when one provider command fails, and register probe subprocesses with the
daemon lifecycle barrier. The implemented design includes those changes.

## Other Notes

### References

- Issue: https://github.com/d0ugal/graith/issues/1815
- CLI: `internal/cli/agent.go`
- Daemon: `internal/daemon/agent_info.go`
- Protocol: `internal/protocol/messages.go`
- Config defaults: `internal/config/default_config.toml`

### Implementation Notes

Info probes inherit only a small daemon-side environment allowlist plus the
agent's explicit `env` map. Graith adds its own `GRAITH_*` session markers for
the scratch worktree but does not return environment values in the response.
Captured stdout and stderr are capped at 1 MiB per stream; JSON responses expose
`stdout_truncated` and `stderr_truncated` when a provider exceeds that cap.

### Testing

Coverage includes config parsing and validation, command construction, CLI
aliases and output modes, daemon handler responses, provider command failures,
timeouts, inherited-pipe process-group cleanup, output truncation, environment
filtering, sorted all-key execution, and lifecycle drain behavior while a
provider probe is blocked.
