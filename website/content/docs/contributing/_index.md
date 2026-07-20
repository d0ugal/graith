---
weight: 1700
title: "Contributing"
description: "Contribute to graith."
icon: "handshake"
toc: true
draft: false
---

## Build

```bash
make build    # produces ./gr
```

Or directly:

```bash
go build -v -ldflags="-s -w" -o gr ./cmd/graith
```

Entry point `cmd/graith/main.go`; the binary's named `gr` but the module path is `cmd/graith`.

## Tests

### Unit tests

```bash
go test ./...              # all unit tests
go test -race ./...        # with race detector (CI runs this)
go test ./internal/daemon/ # single package
```

Unit tests live next to the code as `<file>_test.go`. Use `t.TempDir()` for fixtures -- never hardcode paths.

### Integration tests

These spawn a real daemon, exercising the full client-daemon-PTY pipeline:

```bash
go test -v -race -tags=integration ./internal/integration/...
```

The `integration` build tag keeps them out of `go test ./...`; CI runs them separately on Ubuntu and macOS. The harness (`internal/integration/integration_test.go`) makes a temp git repo, starts a `SessionManager` with `config.Paths` in temp dirs, and connects over a real Unix socket.

### Fuzz tests

Protocol and detector packages have fuzz tests:

```bash
go test -fuzz=FuzzDecodeControl ./internal/protocol/
go test -fuzz=FuzzReadFrame ./internal/protocol/
go test -fuzz=FuzzDecodePayload ./internal/protocol/
go test -fuzz=FuzzDetect ./internal/detector/
go test -fuzz=FuzzStripANSI ./internal/detector/
```

### Coverage

The `Coverage` workflow measures Go and Swift coverage per PR, posting a comment (overall % plus a Go delta vs base) — no third-party service. Go report locally:

```bash
go test -coverprofile=coverage.txt ./...
go tool cover -func=coverage.txt   # overall % (total: line)
go tool cover -html=coverage.txt   # browsable HTML report
```

Swift, shared package:

```bash
swift test --package-path gui/shared --enable-code-coverage
jq '.data[0].totals.lines.percent' "$(swift test --package-path gui/shared --show-codecov-path)"
```

## Lint

CI runs `golangci-lint` via Docker:

```bash
make lint       # lint and autofix
make lint-only  # lint without fixing
make fmt        # format only
```

Locally:

```bash
gofmt -w path/to/modified.go  # format modified Go files
go vet ./...    # static analysis
```

`.golangci.yml` enables `govet`, `staticcheck`, `ineffassign`, `unused`, `gocritic`, `misspell`, and `gofmt`. CI fails on violations.

## Commit messages

Commits must follow [Conventional Commits](https://www.conventionalcommits.org/), enforced by [commitsar](https://github.com/aevea/commitsar).

```
feat: add idle timeout configuration
fix: handle missing worktree on resume
chore: update golangci-lint to v2.12
docs: add contributing guide
test: add fuzz test for frame decoder
```

## CI pipeline

CI (`ci.yml`) runs on every push to `main` and every PR:

| Job | What it does |
|-----|-------------|
| Build | `go build` on Ubuntu and macOS |
| Test | `go test -race` on Ubuntu and macOS |
| Integration | `go test -tags=integration` on Ubuntu and macOS |
| Lint | `golangci-lint run` on Ubuntu |
| Vulnerability Check | `govulncheck ./...` on Ubuntu |
| Conventional Commits | Validates PR commit messages |

The separate `coverage.yml` workflow (PRs only) posts that summary comment — informational, not required.

## Profiles (`GRAITH_PROFILE`)

Set `GRAITH_PROFILE` to run multiple independent graith instances on one machine. Each gets its own:

```bash
GRAITH_PROFILE=dev gr daemon start
GRAITH_PROFILE=dev gr new my-session --repo ~/Code/project
GRAITH_PROFILE=dev gr list
```

- Config file: `~/.config/graith-<profile>/config.toml`
- Data directory: `~/.local/share/graith-<profile>/`
- Runtime directory and socket: `$XDG_RUNTIME_DIR/graith-<profile>/graith.sock`
- State, logs, messages database, and tmp directory

Names must be lowercase alphanumeric with hyphens (no leading hyphen), at most 32 characters; `"default"` is reserved. `gr list` shows the active profile when set; with none, graith uses the base name `graith` for all paths. The daemon propagates it to child sessions.

**Use cases:** a dev build alongside a stable release; config changes isolated from your main sessions; CI needing isolated daemons.

## Demo recording

The demo GIF (`demo/graith.gif`, embedded in the README) is recorded with [VHS](https://github.com/charmbracelet/vhs), a dev-only dependency. Install once:

```bash
brew install vhs
# or:
go install github.com/charmbracelet/vhs@latest
```

From the repo root:

```bash
make demo         # build gr, set up an isolated demo env, record, tear down
make demo-clean   # tear down the demo env if a run is interrupted
```

`make demo` uses a dedicated, isolated `GRAITH_PROFILE=demo` instance with your real local agents (`claude`/`codex`) and sandbox config, copied from your default config. The tape types real prompts, so it spends some API budget. Setup lives in `demo/`; see [`demo/README.md`](https://github.com/d0ugal/graith/blob/main/demo/README.md) for the tape format.

Recording must run **locally and unsandboxed** (VHS needs a real TTY, the daemon binds a unix socket, sessions create git worktrees), so it's not a CI step.

## Project layout

Packages live under `internal/`; no public Go API.

The [package dependency graph]({{< relref "/docs/contributing/package-dependencies.md" >}})
is generated from the current source tree during every documentation build.

```
cmd/graith/              Entry point (main.go)
internal/
  agent/                 Agent environment detection
  cli/                   Cobra command definitions (one file per command)
  client/                Client: connection, passthrough, overlay, shell, status bar
  config/                TOML config loading, defaults, XDG paths, profiles
  daemon/                Daemon: session manager, handler, state, server, messaging
  detector/              Agent type detection from running processes
  git/                   Git operations (fetch, worktree, branch)
  hookoutput/            Agent-specific hook response formatting
  integration/           Integration tests (build tag: integration)
  mcp/                   MCP server implementation
  output/                Structured output helpers (text/JSON)
  protocol/              Wire protocol: framing, control messages, encoding
  pty/                   PTY session management, scrollback buffer
  sandbox/               Safehouse sandbox wrapping
  store/                 Flat-file git-backed document store
  version/               Build-time version injection
```

## Development workflow

After rebuilding, refresh the running daemon:

```bash
make build
gr daemon restart    # preserves live sessions
```

Rebuild your shell's client binary too. For protocol/handler changes, integration tests are the best check:

```bash
go test -v -race -tags=integration ./internal/integration/...
```

## Errors

Return `fmt.Errorf(...)` from library code. Don't use `log.Fatal` under `internal/` -- only `main.go` exits. The daemon logs JSON via `slog` to `~/.local/share/graith/daemon.log`.
