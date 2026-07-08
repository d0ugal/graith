# gui/shared — the cross-platform core for the graith universal app (#628):
# GraithProtocol, GraithTerminalCore, and the CGhosttyVT shim. Consumed by the
# macOS app (../macos) and the iOS app (../ios).
#
# Sandbox note: `build`/`test`/`clean` all run fine inside a graith session with
# full Xcode (the in-memory ClientIntegrationTests use no real sockets). On a
# CLT-only machine, use `test-clt` (swift-testing lives in the CLT frameworks).

export DEVELOPER_DIR ?= /Applications/Xcode.app/Contents/Developer

# CLT-only fallback: swift-testing's `Testing` module isn't on the default
# search path without full Xcode; these point at the CLT-bundled copy.
CLT := /Library/Developer/CommandLineTools
FW  := $(CLT)/Library/Developer/Frameworks
RT  := $(CLT)/Library/Developer/usr/lib

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@echo "graith shared core (gui/shared) targets:"
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the shared libraries (safe inside the graith sandbox)
	swift build --disable-sandbox

.PHONY: test
test: ## Run the unit tests (full Xcode; sandbox-safe)
	swift test --disable-sandbox

.PHONY: test-clt
test-clt: ## Run the unit tests on a CLT-only machine (no full Xcode)
	DEVELOPER_DIR=$(CLT) swift test --disable-sandbox \
		-Xswiftc -F -Xswiftc $(FW) \
		-Xlinker -F -Xlinker $(FW) \
		-Xlinker -rpath -Xlinker $(FW) \
		-Xlinker -rpath -Xlinker $(RT)

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf .build

.PHONY: libghostty
libghostty: ## Build libghostty-vt into Libraries/ (needs Zig; see ../NEEDS-MAC-VALIDATION.md)
	bash build-libghostty.sh
