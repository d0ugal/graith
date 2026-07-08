#!/usr/bin/env bash
#
# build-ios-app.sh — build the graith iOS app (GraithMobileApp) for the iOS
# Simulator and assemble it into a launchable .app bundle (#628).
#
# There is no .xcodeproj (and no xcodegen/tuist on this machine), so we build the
# SwiftPM executable product for the iphonesimulator SDK and hand-assemble a
# minimal .app around it. Simulator apps don't need code signing, so `xcrun
# simctl install` accepts the unsigned bundle. Verified: the produced binary is
# a real IOSSIMULATOR mach-o (LC_BUILD_VERSION platform IOSSIMULATOR).
#
# The `swift build` step is sandbox-safe; `make run` adds the simctl
# install/launch, which must run OUTSIDE a graith session sandbox.
#
# Echoes the assembled .app path on the last line.
set -euo pipefail

cd "$(dirname "$0")"

PRODUCT="GraithMobileApp"
SDK="$(xcrun --sdk iphonesimulator --show-sdk-path)"
VER="$(xcrun --sdk iphonesimulator --show-sdk-version)"
TRIPLE="arm64-apple-ios${VER}-simulator"

BUILD_FLAGS=(--disable-sandbox --sdk "$SDK" -Xswiftc -target -Xswiftc "$TRIPLE" --product "$PRODUCT")

echo "==> Building $PRODUCT for $TRIPLE" >&2
swift build "${BUILD_FLAGS[@]}" >&2

BIN_DIR="$(swift build "${BUILD_FLAGS[@]}" --show-bin-path)"
BIN="$BIN_DIR/$PRODUCT"
if [[ ! -x "$BIN" ]]; then
    echo "error: built product not found at $BIN" >&2
    exit 1
fi

APP="$BIN_DIR/graith.app"
echo "==> Assembling $APP" >&2
rm -rf "$APP"
mkdir -p "$APP"
cp "$BIN" "$APP/$PRODUCT"
cp Resources/GraithMobile-Info.plist "$APP/Info.plist"

# Ad-hoc sign (no entitlements). The simulator launches ad-hoc/unsigned bundles;
# adding the keychain-access-groups entitlement here without a real signing
# identity/provisioning makes SpringBoard refuse the launch. Consequence: the
# app can't reach the Keychain, so DeviceIdentity falls back to an in-memory
# store (see GraithMobileApp.makeIdentity). For a Keychain-backed, distributable
# build, sign with a real identity + Resources/GraithMobile.entitlements:
#   codesign --force --sign "<identity>" --entitlements Resources/GraithMobile.entitlements "$APP"
echo "==> Ad-hoc signing $APP" >&2
codesign --force --sign - --timestamp=none "$APP" >&2

echo "==> Done: $APP" >&2
echo "$APP"
