# gui/ — top-level orchestrator for the graith universal app (#628).
#
# Three sibling SwiftPM packages, each with its own Makefile:
#   shared/  cross-platform core: GraithProtocol, GraithTerminalCore, CGhosttyVT
#   macos/   macOS app (GraithGUI) — depends on ../shared
#   ios/     iOS app modules       — see NEEDS-IOS-VALIDATION.md for the pending
#            unification of its GraithClientAPI onto ../shared's GraithProtocol
#
# This Makefile aggregates the common flows and delegates everything else to the
# per-package Makefiles via `<pkg>-<target>` (e.g. `make macos-run`).

export DEVELOPER_DIR ?= /Applications/Xcode.app/Contents/Developer

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@echo "graith universal app (gui/) — packages: shared, macos, ios"
	@echo ""
	@echo "Aggregate targets (incl. 'xcodeproj' to generate the Xcode project):"
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Per-package (delegates to the subdir Makefile):"
	@echo "  shared-<t>   e.g. shared-build, shared-test, shared-libghostty"
	@echo "  macos-<t>    e.g. macos-build, macos-run, macos-release"
	@echo "  ios-<t>      e.g. ios-build-ios, ios-test, ios-sim, ios-smoke"
	@echo ""
	@echo "See 'make -C shared help', 'make -C macos help', 'make -C ios help'."

.PHONY: build
build: ## Build all three packages (safe inside the graith sandbox)
	$(MAKE) -C shared build
	$(MAKE) -C macos build
	$(MAKE) -C ios build

.PHONY: xcodeproj
xcodeproj: ## Generate graith.xcodeproj (both iOS + macOS apps) via XcodeGen
	@command -v xcodegen >/dev/null 2>&1 || { \
		echo "xcodegen not found. Install it: brew install xcodegen"; exit 1; }
	xcodegen generate --spec project.yml
	@echo "Generated graith.xcodeproj — open it and pick the 'graith (iOS)' or 'GraithGUI (macOS)' scheme."

.PHONY: test
test: ## Test shared + ios (needs full Xcode; run OUTSIDE the graith sandbox)
	$(MAKE) -C shared test
	$(MAKE) -C ios test

.PHONY: clean
clean: ## Clean all three packages
	$(MAKE) -C shared clean
	$(MAKE) -C macos clean
	$(MAKE) -C ios clean

.PHONY: doctor
doctor: ## Show toolchain info (macos + ios)
	$(MAKE) -C macos doctor
	@echo ""
	$(MAKE) -C ios doctor

# Delegation: `make <pkg>-<target>` -> `make -C <pkg> <target>`.
shared-%:
	$(MAKE) -C shared $*
macos-%:
	$(MAKE) -C macos $*
ios-%:
	$(MAKE) -C ios $*
