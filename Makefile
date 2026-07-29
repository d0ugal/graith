GOLANGCI_LINT_VERSION := v2.12.2
GOLANGCI_LINT_DIGEST := sha256:5cceeef04e53efe1470638d4b4b4f5ceefd574955ab3941b2d9a68a8c9ad5240
GOLANGCI_LINT_IMAGE := golangci/golangci-lint:$(GOLANGCI_LINT_VERSION)@$(GOLANGCI_LINT_DIGEST)
GOLANGCI_LINT_RUN_ARGS ?=
GOLANGCI_LINT_REGISTRY_AUTH_FILE ?= $(CURDIR)/scripts/public-registry-auth.json
GOLANGCI_LINT_CACHE_ARGS := \
	-v graith-golangci-go-mod:/go/pkg/mod \
	-v graith-golangci-go-build:/root/.cache/go-build \
	-v graith-golangci-cache:/root/.cache/golangci-lint \
	-e GOMODCACHE=/go/pkg/mod \
	-e GOCACHE=/root/.cache/go-build \
	-e GOLANGCI_LINT_CACHE=/root/.cache/golangci-lint
# The linter image is public. Podman honors REGISTRY_AUTH_FILE; use an empty
# authfile so unrelated host credential helpers cannot break public pulls.
GOLANGCI_LINT_DOCKER_BASE := REGISTRY_AUTH_FILE=$(GOLANGCI_LINT_REGISTRY_AUTH_FILE) docker run --rm $(GOLANGCI_LINT_CACHE_ARGS) -v $(CURDIR):/app -w /app
GOLANGCI_LINT_DOCKER := $(GOLANGCI_LINT_DOCKER_BASE) $(GOLANGCI_LINT_IMAGE)
GOLANGCI_LINT_LIBGHOSTTY_GOARCH ?=
GOLANGCI_LINT_LIBGHOSTTY_PACKAGES := ./internal/pty ./internal/daemon ./cmd/graith

.PHONY: build test architecture-check lint lint-only lint-darwin lint-libghostty lint-profile lint-cache-clean shellcheck fmt clean notifier service-app package-graph package-graph-check docs docs-serve demo demo-clean demo-test

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

# Lint with GOOS=darwin so non-cgo Darwin-only files (e.g. *_darwin.go) are
# compiled and checked. CI lints on Linux, which never sees these files
# (issue #784); darwin+cgo surfaces are covered by native macOS build/test lanes.
lint-darwin:
	$(GOLANGCI_LINT_DOCKER_BASE) -e GOOS=darwin -e CGO_ENABLED=0 $(GOLANGCI_LINT_IMAGE) golangci-lint run $(GOLANGCI_LINT_RUN_ARGS)

# Lint the supported Linux libghostty+cgo surface. The pinned artifact setup
# runs outside the golangci-lint container so the host-side lock parsing,
# download, and bind-mounted artifact preparation happen before analysis uses
# the single .golangci.yml config.
lint-libghostty:
	@goarch="$(GOLANGCI_LINT_LIBGHOSTTY_GOARCH)"; \
	if [ -z "$$goarch" ]; then \
		image_arch="$$(docker run --rm $(GOLANGCI_LINT_IMAGE) uname -m)"; \
		case "$$image_arch" in \
			x86_64) goarch=amd64 ;; \
			aarch64|arm64) goarch=arm64 ;; \
			*) echo "unsupported golangci-lint container architecture: $$image_arch" >&2; exit 1 ;; \
		esac; \
	fi; \
	case "$$goarch" in \
		amd64|arm64) ;; \
		*) echo "unsupported libghostty lint GOARCH: $$goarch" >&2; exit 1 ;; \
	esac; \
	work="$(CURDIR)/.lint-libghostty-linux-$$goarch"; \
	GRAITH_LIBGHOSTTY_WORK="$$work" scripts/libghostty-native.sh prepare-linux-artifact "$$goarch" >/dev/null; \
	$(GOLANGCI_LINT_DOCKER_BASE) \
		-e GOOS=linux \
		-e GOARCH="$$goarch" \
		-e CGO_ENABLED=1 \
		-e PKG_CONFIG_PATH="/app/.lint-libghostty-linux-$$goarch/pkgconfig" \
		$(GOLANGCI_LINT_IMAGE) golangci-lint run --build-tags=integration,libghostty $(GOLANGCI_LINT_RUN_ARGS) $(GOLANGCI_LINT_LIBGHOSTTY_PACKAGES)

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
	rm -rf macos/build .lint-libghostty-linux-*

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
