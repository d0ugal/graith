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
make build    # produces ./gr from native libghostty inputs
```

Or directly:

```bash
scripts/libghostty-native.sh build-local
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
CGO_ENABLED=1 go test -v -race -tags='integration libghostty' ./internal/integration/...
```

The `integration` build tag keeps them out of `go test ./...`; generic CI compiles these tests without native inputs, while the native Linux amd64 lane executes the full package with the pinned helper. The harness (`internal/integration/integration_test.go`) makes a temp git repo, starts a `SessionManager` with `config.Paths` in temp dirs, and connects over a real Unix socket.

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
make shellcheck # lint every tracked shell script (all optional warning/error checks)
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

## Native libghostty dependency updates

`libghostty-native.lock.json` is the canonical record for the complete native
dependency unit. Do not separately edit the generated inventory in
`libghostty-native.spdx.json` or the marked dependency table in
`THIRD_PARTY_NOTICES.libghostty.md`. Regenerate and verify the unit with:

```bash
scripts/libghostty-native.sh generate-dependency-unit
scripts/libghostty-native.sh verify-dependency-unit
```

Generation may produce a complete but deliberately red review branch when a
license or embedded-notice hash changes. Inspect the exact changed evidence,
confirm the conclusions and declared-license choice in the lock, then record
that explicit review and regenerate:

```bash
scripts/libghostty-native.sh accept-license-reviews
scripts/libghostty-native.sh generate-dependency-unit
scripts/libghostty-native.sh verify-dependency-unit
```

The review fingerprint binds each conclusion to its exact license and notice
hashes. Do not run the acceptance command as a mechanical update step.

Renovate detects go-libghostty, Ghostty, Zig, uucode, Highway, simdutf, and the
SPDX Java validator from exact fields in the lock. It groups them as
`libghostty-native`, disables automerge, and disables the ordinary Go manager
for go-libghostty so a wrapper-only module PR cannot merge. The hosted Renovate
service cannot run repository-defined post-upgrade commands, so the repository's
regeneration workflow projects every proposed lock update. A generated commit explicitly dispatches all
required workflows at its new branch SHA because a normal `GITHUB_TOKEN` push
does not create a second pull-request run. Validate the config and its
deliberately stale update fixture locally with:

```bash
scripts/verify-renovate-libghostty.sh
```

go-libghostty is commit-pinned from its canonical Tangled repository. Its
`CMakeLists.txt` exact `GIT_TAG` is the default tested Ghostty revision. Ghostty
is also commit-pinned because its C API is not released independently. A newer
Ghostty commit can be proposed through Renovate's dependency dashboard, but it
needs explicit approval and the complete native compatibility suite.

Zig, uucode, Highway, and simdutf are discovered from their upstream release
feeds, but compatibility comes from the selected Ghostty source tree. The
generator reads Ghostty's root and package `build.zig.zon` declarations and the
vendored simdutf header, then derives the exact compiled versions, commits, Zig
content hashes, source archives, and license hashes. A transitive-only “latest”
proposal fails until a selected Ghostty commit actually consumes it; reviewers
use those dashboard entries to discover upstream changes rather than overriding
Ghostty's tested graph.

The SPDX validator is update tooling rather than compiled native content, so it
may open automatically in the same non-automerge group. It remains
checksum-pinned, GitHub's release-asset digest is independently checked, and its
official validation result is still required.

Relevant native changes also run the required Linux source matrix. The amd64
lane executes the wrapper, PTY, daemon lifecycle, race, and fixed-budget fuzz
checks on a GitHub-hosted Linux runner; the arm64 lane proves the exact pinned
source build and archive shape. Both lanes exercise the fail-closed archive
policy directly:

```bash
scripts/libghostty-native.sh test-source-archive-policy
```

The policy accepts only the audited 11-member Ghostty archive closure and
publishes a deterministic, self-contained regular archive. Injected path,
stat, hash, format, temporary-directory, copy, Zig archiver, verifier, and
final-move failures must leave no archive, pkg-config file, snapshot, or private
temporary behind. Untagged or missing-input builds fail closed; they do not
select a terminal fallback.

The `graith-dev` and stable workflows turn that exact source unit into
release-shaped native Linux amd64 and arm64 artifacts. Platform jobs package and
validate final executables, and actual-architecture jobs execute the uploaded
bytes. Stable additionally compares the executable in tar/deb/rpm/apk and joins
those eight Linux outputs with the native macOS arm64 archive. One aggregator
accepts only same-revision manifests and creates the complete checksum set.
Darwin amd64 is absent from both release configurations and selectors; its
tagged native selection remains an explicit fail-closed test. Provenance is
attested and reverified before publication.
Pull requests exercise the unsigned build/aggregation topology without changing
a release or downstream repository. A real tag requires configured macOS
signing/notarization. The publisher prepares and validates every configured
downstream update while the GitHub release remains a draft, then exposes the
complete release before pushing package metadata that refers to its public URLs.
Retries accept the already-public exact asset set and converge an interrupted
downstream push. Dev and stable configuration must not add separately named
rollback archives.

Before generation can succeed, the checksum-reviewed Apple xcframework for the
selected Ghostty commit must already be published at the exact URL derived by
the lock tool. Its release notes must bind the archive to the full Ghostty commit
and checksum, and the downloaded bytes must match GitHub's release-asset digest.
This verifies the separately built and reviewed artifact; the dependency update
does not rebuild or publish it. Review any changed source or license hashes and
confirm the recorded license conclusions still apply; generation never weakens
or guesses those conclusions. Every generated PR must pass `verify-metadata`
and the existing exact-pin wrapper, compatibility, race/fuzz, packaging, SPDX,
linkage, privacy, and supported-platform native checks at its final head SHA.

## CI pipeline

CI (`ci.yml`) runs on every push to `main` and every PR:

| Job | What it does |
|-----|-------------|
| Build | untagged compile gate on Ubuntu and macOS; native build runs in the native workflow |
| Test | `go test -race` on Ubuntu and macOS |
| Integration | compile-only generic tests; full runtime package executes in native Linux lane |
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

Setup and teardown require matching ownership proofs on the demo config, data, and runtime paths. They refuse changed or pre-existing state rather than adopting or deleting it; a deliberately edited demo config must be checked and removed manually. On Linux, `XDG_RUNTIME_DIR` must be set so the harness and CLI agree on the runtime target. macOS also requires it when `XDG_DATA_HOME` is customized. Removing only an owned runtime directory is safe: the next setup reconstructs it from the durable config and data proofs.

Recording must run **locally and unsandboxed** (VHS needs a real TTY, the daemon binds a unix socket, sessions create git worktrees), so it's not a CI step.

## Project layout

Packages live under `internal/`; no public Go API.

The [package dependency graph]({{< relref "/docs/contributing/package-dependencies.md" >}})
is generated from the current source tree and committed for review. Run
`make package-graph` after changing packages or their import relationships;
CI rejects stale graph data.

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
