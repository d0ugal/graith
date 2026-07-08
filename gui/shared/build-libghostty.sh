#!/usr/bin/env bash
#
# build-libghostty.sh — build a SHA-pinned libghostty-vt.xcframework
# (macos + ios + ios-simulator) for the universal graith app (#628, Task 13).
#
# This replaces the unpinned, macOS-only gui/shared/Libraries/libghostty-vt.a with a
# reproducible, version-pinned artifact. It clones Ghostty at a pinned commit
# and uses Ghostty's OWN build system, which already knows how to emit a
# universal libghostty-vt xcframework on Apple platforms (verified against the
# pinned tree: build.zig gates the xcframework on `emit-lib-vt && emit-xcframework`
# and calls GhosttyLibVt.initStaticAppleUniversal + GhosttyLibVt.xcframework).
#
# ─────────────────────────────────────────────────────────────────────────────
# PINNED VERSION
# ─────────────────────────────────────────────────────────────────────────────
# Ghostty commit (repo default branch tip at the time this was written):
GHOSTTY_SHA="91f66da24527fa02d92b5fd0b41cd020f553a64c"
GHOSTTY_REPO="https://github.com/ghostty-org/ghostty.git"
# Ghostty pins its Zig toolchain in build.zig.zon: `minimum_zig_version`.
# At the pinned SHA this is:
REQUIRED_ZIG="0.15.2"
#
# ─────────────────────────────────────────────────────────────────────────────
# REQUIREMENTS (see gui/NEEDS-MAC-VALIDATION.md for the environment gaps that
# currently block this from completing in CI/headless):
#   - Zig == $REQUIRED_ZIG  (NOT newer: Ghostty's build.zig at this SHA uses the
#     0.15.x std.Io.Dir.readFileAlloc 3-arg signature and rejects >= 0.16).
#   - FULL Xcode (not just Command Line Tools): `xcodebuild -create-xcframework`
#     and the iOS/iOS-simulator SDKs are Xcode-only. `xcode-select -p` must point
#     at Xcode.app.
#   - A macOS SDK that the pinned Zig can link against. (Zig 0.15.2 fails to
#     resolve libSystem for the macOS 26 SDK — see NEEDS-MAC-VALIDATION.md.)
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")" && pwd)"             # gui/shared/ (this script lives here)
WORK="${WORK:-$(mktemp -d)}"
OUT="$REPO_DIR/Libraries/libghostty-vt.xcframework"

echo "==> Checking toolchain"
command -v zig >/dev/null || { echo "error: zig not found"; exit 1; }
ZIG_VER="$(zig version)"
if [[ "$ZIG_VER" != "$REQUIRED_ZIG" ]]; then
    echo "error: zig $ZIG_VER found, but Ghostty $GHOSTTY_SHA requires $REQUIRED_ZIG" >&2
    echo "       Install it: https://ziglang.org/download/$REQUIRED_ZIG/" >&2
    exit 1
fi
if ! xcodebuild -version >/dev/null 2>&1; then
    echo "error: xcodebuild requires full Xcode (Command Line Tools is not enough)." >&2
    echo "       sudo xcode-select -s /Applications/Xcode.app/Contents/Developer" >&2
    exit 1
fi

echo "==> Cloning Ghostty @ $GHOSTTY_SHA"
if [[ ! -d "$WORK/ghostty/.git" ]]; then
    git clone "$GHOSTTY_REPO" "$WORK/ghostty"
fi
git -C "$WORK/ghostty" fetch --depth 1 origin "$GHOSTTY_SHA" 2>/dev/null || true
git -C "$WORK/ghostty" checkout -q "$GHOSTTY_SHA"

echo "==> Building universal libghostty-vt.xcframework (macos + ios + ios-sim)"
# Ghostty emits the xcframework itself when both flags are set on a Darwin host.
# This produces the three slices (arm64-macos, arm64-ios, arm64-ios-simulator)
# and assembles them with `xcodebuild -create-xcframework`.
(
    cd "$WORK/ghostty"
    zig build \
        -Demit-lib-vt=true \
        -Demit-xcframework=true \
        -Doptimize=ReleaseFast
)

echo "==> Installing xcframework -> $OUT"
FRAMEWORK="$(find "$WORK/ghostty/zig-out" -maxdepth 3 -name 'libghostty-vt.xcframework' -type d | head -1)"
if [[ -z "$FRAMEWORK" ]]; then
    echo "error: no libghostty-vt.xcframework produced under zig-out" >&2
    exit 1
fi
rm -rf "$OUT"
cp -R "$FRAMEWORK" "$OUT"

echo "==> Copying pinned C headers -> gui/shared/Sources/CGhosttyVT/include"
# The xcframework bundles headers per-slice, but the CGhosttyVT module map
# references gui/shared/Sources/CGhosttyVT/include; keep them in sync with the pin.
HEADERS="$(find "$WORK/ghostty/zig-out" -type d -name ghostty -path '*/include/*' | head -1)"
if [[ -n "$HEADERS" ]]; then
    rsync -a --delete "$(dirname "$HEADERS")/" "$REPO_DIR/Sources/CGhosttyVT/include/"
fi

echo "==> Done. Pinned to Ghostty $GHOSTTY_SHA (Zig $REQUIRED_ZIG)."
echo "    Next: switch Package.swift's CGhosttyVT to a binaryTarget on"
echo "    Libraries/libghostty-vt.xcframework (see NEEDS-MAC-VALIDATION.md)."
