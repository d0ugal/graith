# gui/macos — the graith macOS app (GraithGUI, AppKit/SwiftUI + Metal). All
# cross-platform logic lives in ../shared (GraithProtocol, GraithTerminalCore,
# CGhosttyVT); this package is macOS-specific glue + the app entry point.
#
# Sandbox note: `build`/`release`/`clean` run fine inside a graith session.
# `run` launches a windowed AppKit app, so it needs a display — run it from a
# normal terminal on the Mac.

export DEVELOPER_DIR ?= /Applications/Xcode.app/Contents/Developer

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@echo "graith macOS app (gui/macos) targets:"
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the macOS app (safe inside the graith sandbox)
	swift build --disable-sandbox

.PHONY: release
release: ## Release build of the macOS app
	swift build -c release --disable-sandbox

.PHONY: run
run: ## Launch GraithGUI (needs a display — run on the Mac)
	swift run --disable-sandbox GraithGUI

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf .build

.PHONY: doctor
doctor: ## Show toolchain info
	@echo "DEVELOPER_DIR = $(DEVELOPER_DIR)"
	@swift --version 2>/dev/null | head -1
	@xcodebuild -version 2>/dev/null | head -1 || echo "xcodebuild: unavailable (CLT only)"
