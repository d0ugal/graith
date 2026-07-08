# gui/ios — build / test / simulator helpers for the graith iOS universal-app
# modules (#628, Tasks 18-20).
#
# Toolchain: full Xcode is required for iOS builds/tests/simulators; point at it
# via DEVELOPER_DIR (defaults to /Applications/Xcode.app). SDK version/triple are
# derived dynamically so they don't rot.
#
# Sandbox note: the `build*` and `smoke` targets use `swift ... --disable-sandbox`
# and run fine inside a graith session. The `test*`, `sim*`, `schemes`, and
# `doctor` targets shell out to xcodebuild/simctl/open, which a graith OS sandbox
# blocks (nested sandbox-exec is denied) — run those from a normal terminal, or a
# graith session with [sandbox] enabled = false.

export DEVELOPER_DIR ?= /Applications/Xcode.app/Contents/Developer

DEVICE      ?= iPhone 17 Pro
SCHEME      ?= GraithMobile-Package
SDK_PATH    := $(shell xcrun --sdk iphonesimulator --show-sdk-path 2>/dev/null)
SDK_VERSION := $(shell xcrun --sdk iphonesimulator --show-sdk-version 2>/dev/null)
IOS_TRIPLE  := arm64-apple-ios$(SDK_VERSION)-simulator
DEST        := platform=iOS Simulator,name=$(DEVICE)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@echo "graith iOS (gui/ios) targets:"
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Config: DEVICE=\"$(DEVICE)\"  SCHEME=$(SCHEME)  iOS-SDK=$(SDK_VERSION)"
	@echo "Override e.g.: make test DEVICE=\"iPhone Air\""

## ---- build / compile (safe inside the graith sandbox) ----

.PHONY: build
build: ## Build all modules for the host (SDK-neutral; verifies non-UIKit code)
	swift build --disable-sandbox

.PHONY: build-ios
build-ios: ## Cross-compile every module for the iOS Simulator (xcframework slice-aware)
	@D="$$(mktemp $${TMPDIR:-/tmp}/iosdest.XXXXXX)"; \
	printf '{"version":1,"sdk":"%s","toolchain-bin-dir":"%s","target":"%s","extra-cc-flags":["-isysroot","%s","-target","%s"],"extra-swiftc-flags":["-sdk","%s","-target","%s"],"extra-cpp-flags":["-isysroot","%s"]}' \
	  "$(SDK_PATH)" "$$(dirname $$(xcrun -f swiftc))" "$(IOS_TRIPLE)" "$(SDK_PATH)" "$(IOS_TRIPLE)" "$(SDK_PATH)" "$(IOS_TRIPLE)" "$(SDK_PATH)" > "$$D"; \
	swift build --disable-sandbox --destination "$$D"

.PHONY: smoke
smoke: ## Run the SDK-neutral logic smoke checks (GraithMobileSmoke)
	swift run --disable-sandbox GraithMobileSmoke

.PHONY: app
app: ## Build + bundle the iOS app into a launchable .app (sandbox-safe)
	bash build-ios-app.sh

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf .build
	-xcodebuild clean -scheme $(SCHEME) >/dev/null 2>&1 || true

## ---- XCTest on a simulator (full Xcode; run OUTSIDE the graith sandbox) ----

.PHONY: test
test: ## Run all XCTest suites on the simulator
	xcodebuild test -scheme $(SCHEME) -destination '$(DEST)'

.PHONY: test-unit
test-unit: ## Run only GraithMobileKitTests
	xcodebuild test -scheme $(SCHEME) -destination '$(DEST)' -only-testing:GraithMobileKitTests

.PHONY: test-uikit
test-uikit: ## Run only GraithTerminalUIKitTests
	xcodebuild test -scheme $(SCHEME) -destination '$(DEST)' -only-testing:GraithTerminalUIKitTests

## ---- simulator control (full Xcode; run OUTSIDE the graith sandbox) ----

.PHONY: sim
sim: ## Boot $(DEVICE) and open the interactive Simulator window
	-xcrun simctl boot "$(DEVICE)" 2>/dev/null || true
	open -a Simulator
	-xcrun simctl bootstatus "$(DEVICE)" || true
	@echo "Simulator '$(DEVICE)' is ready."

.PHONY: run
run: ## Build, install, and LAUNCH the app on $(DEVICE) (run OUTSIDE the sandbox)
	-xcrun simctl boot "$(DEVICE)" 2>/dev/null || true
	open -a Simulator
	xcrun simctl bootstatus "$(DEVICE)"
	xcrun simctl install booted "$$(bash build-ios-app.sh | tail -1)"
	xcrun simctl launch booted net.graith.mobile
	@echo "Launched graith (net.graith.mobile) on '$(DEVICE)'."

.PHONY: sim-shutdown
sim-shutdown: ## Shut down $(DEVICE)
	-xcrun simctl shutdown "$(DEVICE)" || true

.PHONY: sim-list
sim-list: ## List available simulators
	xcrun simctl list devices available

## ---- introspection ----

.PHONY: schemes
schemes: ## List the package's xcodebuild schemes
	xcodebuild -list

.PHONY: doctor
doctor: ## Show toolchain / SDK / device info
	@echo "DEVELOPER_DIR = $(DEVELOPER_DIR)"
	@xcodebuild -version 2>/dev/null | head -1 || echo "xcodebuild: unavailable"
	@echo "iOS Simulator SDK = $(SDK_VERSION)"
	@echo "SDK path          = $(SDK_PATH)"
	@echo "target triple     = $(IOS_TRIPLE)"
	@echo "default device    = $(DEVICE)"
