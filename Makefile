GOLANGCI_LINT_VERSION := v2.12.2
GOLANGCI_LINT_DIGEST := sha256:5cceeef04e53efe1470638d4b4b4f5ceefd574955ab3941b2d9a68a8c9ad5240
GOLANGCI_LINT_IMAGE := golangci/golangci-lint:$(GOLANGCI_LINT_VERSION)@$(GOLANGCI_LINT_DIGEST)
GOLANGCI_LINT_RUN_ARGS ?=
GOLANGCI_LINT_CACHE_ARGS := \
	-v graith-golangci-go-mod:/go/pkg/mod \
	-v graith-golangci-go-build:/root/.cache/go-build \
	-v graith-golangci-cache:/root/.cache/golangci-lint \
	-e GOMODCACHE=/go/pkg/mod \
	-e GOCACHE=/root/.cache/go-build \
	-e GOLANGCI_LINT_CACHE=/root/.cache/golangci-lint
GOLANGCI_LINT_DOCKER_BASE := docker run --rm $(GOLANGCI_LINT_CACHE_ARGS) -v $(CURDIR):/app -w /app
GOLANGCI_LINT_DOCKER := $(GOLANGCI_LINT_DOCKER_BASE) $(GOLANGCI_LINT_IMAGE)

.PHONY: build test architecture-check lint lint-only lint-darwin lint-profile lint-cache-clean shellcheck fmt clean notifier service-app package-graph package-graph-check docs docs-serve demo demo-clean demo-test

build:
	GRAITH_LIBGHOSTTY_LDFLAGS="-s -w" scripts/libghostty-native.sh build-local

# Build the macOS notification helper .app bundle (issue #1094). macOS only —
# the build script skips itself on non-Darwin hosts, so this is safe to run on
# Linux (it just prints a skip message and does nothing). Output lands in
# macos/build/GraithNotifier.app.
notifier:
	sh macos/notifier/build.sh

# Local ad-hoc Graith.app for lifecycle/manual verification. Production release
# packaging invokes the same script with Developer ID + notarization inputs.
service-app:
	@arch=$$(go env GOARCH); \
		[ "$$arch" = arm64 ] || { echo "Graith.app supports only Apple Silicon" >&2; exit 1; }; \
		commit=$$(git rev-parse --short HEAD); \
		mkdir -p macos/build; \
		GRAITH_LIBGHOSTTY_OUTPUT="macos/build/service-payload-$$arch" \
		GRAITH_LIBGHOSTTY_LDFLAGS="-s -w \
			-X github.com/d0ugal/graith/internal/version.Version=0.0.0 \
			-X github.com/d0ugal/graith/internal/version.CommitSHA=$$commit \
			-X github.com/d0ugal/graith/internal/daemonservice.ManagedBuild=true \
			-X github.com/d0ugal/graith/internal/daemonservice.DevelopmentBuild=true" \
			scripts/libghostty-native.sh build-local; \
		sh macos/service/build.sh --development --arch $$arch \
			--version 0.0.0 --commit $$commit \
			--payload macos/build/service-payload-$$arch \
			--output macos/build/service-$$arch

test:
	go test -v -race ./...

architecture-check:
	GOFLAGS=-mod=readonly GOWORK=off go run ./cmd/architecturecheck

lint:
	$(GOLANGCI_LINT_DOCKER) golangci-lint run --fix $(GOLANGCI_LINT_RUN_ARGS)

lint-only:
	$(GOLANGCI_LINT_DOCKER) golangci-lint run $(GOLANGCI_LINT_RUN_ARGS)

# Lint with GOOS=darwin so Darwin-only files (e.g. *_darwin.go) are compiled and
# checked. CI lints on Linux, which never sees these files (issue #784).
lint-darwin:
	$(GOLANGCI_LINT_DOCKER_BASE) -e GOOS=darwin $(GOLANGCI_LINT_IMAGE) golangci-lint run $(GOLANGCI_LINT_RUN_ARGS)

lint-profile:
	$(GOLANGCI_LINT_DOCKER) golangci-lint run -v $(GOLANGCI_LINT_RUN_ARGS)

lint-cache-clean:
	-docker volume rm graith-golangci-go-mod graith-golangci-go-build graith-golangci-cache

# Lint every tracked shell script, including ShellCheck's opt-in checks. Keep
# warnings and errors as the enforced baseline so correctness findings fail CI
# without imposing ShellCheck's optional formatting preferences. The
# NUL-delimited file list keeps paths safe and works with GNU and BSD xargs.
shellcheck:
	command -v shellcheck >/dev/null
	git ls-files -z -- '*.sh' | xargs -0 shellcheck --enable=all --severity=warning

fmt:
	$(GOLANGCI_LINT_DOCKER) golangci-lint fmt

clean:
	rm -f gr
	rm -rf macos/build

package-graph:
	cd website && GOFLAGS=-mod=readonly GOWORK=off go run ./cmd/packagegraph -repo ..

package-graph-check:
	cd website && GOFLAGS=-mod=readonly GOWORK=off go run ./cmd/packagegraph -repo .. -check

# Documentation builds consume the committed package graph without rewriting it.
docs:
	cd website && hugo --gc --minify

docs-serve:
	cd website && hugo server

# Record the demo GIF (demo/graith.gif) with VHS. Runs unsandboxed on your own
# machine: it stands up an isolated `demo` profile with a mix of running/stopped
# real agent sessions (demo/setup.sh), records the tape, then tears it down.
# Requires VHS: `brew install vhs` (or `go install github.com/charmbracelet/vhs@latest`).
# Putting the repo root first on PATH makes the tape use the freshly-built ./gr.
# If VHS fails mid-run, clean up with `make demo-clean`.
demo: build
	@command -v vhs >/dev/null 2>&1 || { \
		echo "vhs not found. Install with: brew install vhs"; \
		echo "                    or: go install github.com/charmbracelet/vhs@latest"; \
		exit 1; }
	./demo/setup.sh
	PATH="$(CURDIR):$$PATH" GRAITH_PROFILE=demo vhs demo/demo.tape
	./demo/teardown.sh

# Tear down the isolated demo environment (safe to run any time).
demo-clean:
	./demo/teardown.sh

# Exercise demo-profile ownership with isolated HOME/XDG paths. This includes a
# real-CLI regression for runtime-directory recreation without launching agents.
demo-test:
	./demo/test.sh
