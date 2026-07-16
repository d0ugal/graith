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

The entry point is `cmd/graith/main.go`. The binary is named `gr` but the Go module path is `cmd/graith`.

## Tests

### Unit tests

```bash
go test ./...              # all unit tests
go test -race ./...        # with race detector (CI runs this)
go test ./internal/daemon/ # single package
```

Unit tests live next to the code using the plain `<file>_test.go` convention.
Use `t.TempDir()` for fixtures -- never hardcode paths.

### Integration tests

Integration tests spawn a real daemon and exercise the full client-daemon-PTY pipeline:

```bash
go test -v -race -tags=integration ./internal/integration/...
```

These are gated behind the `integration` build tag, so `go test ./...` skips them. CI runs them separately on both Ubuntu and macOS.

The integration test harness (`internal/integration/integration_test.go`) creates a temporary git repo, starts a `SessionManager` with a `config.Paths` pointing at temp directories, and connects over a real Unix socket.

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

CI measures Go and Swift coverage on every PR and posts a summary comment
(overall % plus a Go delta vs the base branch) via the `Coverage` workflow —
no third-party service. Generate the Go report locally:

```bash
go test -coverprofile=coverage.txt ./...
go tool cover -func=coverage.txt   # overall % (total: line)
go tool cover -html=coverage.txt   # browsable HTML report
```

Swift coverage for the shared package:

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

Locally, at minimum:

```bash
gofmt -w path/to/modified.go  # format modified Go files
go vet ./...    # static analysis
```

The `.golangci.yml` enables: `govet`, `staticcheck`, `ineffassign`, `unused`, `gocritic`, `misspell`, plus `gofmt` formatting. CI will fail on violations.

## Commit messages

Commits must follow [Conventional Commits](https://www.conventionalcommits.org/). CI enforces this with [commitsar](https://github.com/aevea/commitsar).

```
feat: add idle timeout configuration
fix: handle missing worktree on resume
chore: update golangci-lint to v2.12
docs: add contributing guide
test: add fuzz test for frame decoder
```

## CI pipeline

CI (`ci.yml`) runs on every push to `main` and every pull request:

| Job | What it does |
|-----|-------------|
| Build | `go build` on Ubuntu and macOS |
| Test | `go test -race` on Ubuntu and macOS |
| Integration | `go test -tags=integration` on Ubuntu and macOS |
| Lint | `golangci-lint run` on Ubuntu |
| Vulnerability Check | `govulncheck ./...` on Ubuntu |
| Conventional Commits | Validates PR commit messages |

A separate `coverage.yml` workflow runs on pull requests only and posts the
coverage summary comment described above (informational, not a required check).

## Profiles (`GRAITH_PROFILE`)

Profiles let you run multiple independent graith instances on the same machine. Set the `GRAITH_PROFILE` environment variable to isolate config, data, state, and the daemon socket.

```bash
GRAITH_PROFILE=dev gr daemon start
GRAITH_PROFILE=dev gr new my-session --repo ~/Code/project
GRAITH_PROFILE=dev gr list
```

Each profile gets its own:
- Config file: `~/.config/graith-<profile>/config.toml`
- Data directory: `~/.local/share/graith-<profile>/`
- Runtime directory and socket: `$XDG_RUNTIME_DIR/graith-<profile>/graith.sock`
- State, logs, messages database, and tmp directory

Profile names must be lowercase alphanumeric with hyphens (no leading hyphen), at most 32 characters. `"default"` is reserved. `gr list` displays the active profile name when one is set.

When no profile is set, graith uses the base app name `graith` for all paths.

**Use cases:**
- Running a development build alongside a stable release
- Testing config changes without affecting your main sessions
- CI environments that need isolated daemon instances

The daemon propagates `GRAITH_PROFILE` to child sessions via environment variables, so sessions created under a profile stay within that profile.

## Project layout

All packages are under `internal/` -- there is no public Go API.

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

After rebuilding, pick up the new binary in the running daemon:

```bash
make build
gr daemon restart    # preserves live sessions
```

The client binary in your shell also needs a fresh build. The daemon binary is what `gr daemon restart` replaces via exec.

When testing protocol or handler changes, integration tests are the most reliable way to validate -- they exercise the full wire protocol path:

```bash
go test -v -race -tags=integration ./internal/integration/...
```

## Errors

Return `fmt.Errorf(...)` from library code. Do not use `log.Fatal` in packages under `internal/` -- only `main.go` should exit the process. The daemon logs via `slog` in JSON format to `~/.local/share/graith/daemon.log`.
