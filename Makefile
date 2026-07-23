GOLANGCI_LINT_VERSION := v2.12.2

.PHONY: build test architecture-check lint lint-only lint-darwin shellcheck fmt clean notifier service-app package-graph package-graph-check docs docs-serve demo demo-clean demo-test

build:
	go build -v -ldflags="-s -w" -o gr ./cmd/graith

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
		go build -v -trimpath -ldflags="-s -w \
			-X github.com/d0ugal/graith/internal/version.Version=0.0.0 \
			-X github.com/d0ugal/graith/internal/version.CommitSHA=$$commit \
			-X github.com/d0ugal/graith/internal/daemonservice.ManagedBuild=true \
			-X github.com/d0ugal/graith/internal/daemonservice.DevelopmentBuild=true" \
			-o macos/build/service-payload-$$arch ./cmd/graith; \
		sh macos/service/build.sh --development --arch $$arch \
			--version 0.0.0 --commit $$commit \
			--payload macos/build/service-payload-$$arch \
			--output macos/build/service-$$arch

test:
	go test -v -race ./...

architecture-check:
	GOFLAGS=-mod=readonly GOWORK=off go run ./cmd/architecturecheck

lint:
	docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run --fix

lint-only:
	docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run

# Lint with GOOS=darwin so Darwin-only files (e.g. *_darwin.go) are compiled and
# checked. CI lints on Linux, which never sees these files (issue #784).
lint-darwin:
	docker run --rm -e GOOS=darwin -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run

# Lint every tracked shell script, including ShellCheck's opt-in checks. Keep
# warnings and errors as the enforced baseline so correctness findings fail CI
# without imposing ShellCheck's optional formatting preferences. The
# NUL-delimited file list keeps paths safe and works with GNU and BSD xargs.
shellcheck:
	command -v shellcheck >/dev/null
	git ls-files -z -- '*.sh' | xargs -0 shellcheck --enable=all --severity=warning

fmt:
	docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint fmt

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
