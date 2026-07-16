GOLANGCI_LINT_VERSION := v2.12.2

.PHONY: build test lint lint-only lint-darwin fmt clean notifier demo demo-clean

build:
	go build -v -ldflags="-s -w" -o gr ./cmd/graith

# Build the macOS notification helper .app bundle (issue #1094). macOS only —
# the build script skips itself on non-Darwin hosts, so this is safe to run on
# Linux (it just prints a skip message and does nothing). Output lands in
# macos/build/GraithNotifier.app.
notifier:
	sh macos/notifier/build.sh

test:
	go test -v -race ./...

lint:
	docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run --fix

lint-only:
	docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run

# Lint with GOOS=darwin so Darwin-only files (e.g. *_darwin.go) are compiled and
# checked. CI lints on Linux, which never sees these files (issue #784).
lint-darwin:
	docker run --rm -e GOOS=darwin -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run

fmt:
	docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint fmt

clean:
	rm -f gr
	rm -rf macos/build

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
