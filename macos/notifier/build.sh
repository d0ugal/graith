#!/usr/bin/env bash
# Build the graith-notifier macOS .app bundle (issue #1094).
#
# Assembles:
#   <out>/GraithNotifier.app/
#     Contents/Info.plist
#     Contents/MacOS/graith-notifier   (compiled from main.swift)
#
# The bundle carries CFBundleIdentifier = com.graith.notifier so `gr notify`
# notifications appear under "Graith" in System Settings > Notifications instead
# of "Script Editor" (osascript). The daemon discovers the bundle at runtime
# and falls back to osascript if it isn't installed — see
# internal/daemon/pushnotify.go.
#
# This is macOS-only: it needs `swiftc`. The Makefile `notifier` target skips it
# on non-Darwin hosts, so Linux builds/CI never invoke this.
set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
	echo "build.sh: skipping — the notifier app is macOS only" >&2
	exit 0
fi

if ! command -v swiftc >/dev/null 2>&1; then
	echo "build.sh: swiftc not found (install the Xcode command line tools)" >&2
	exit 1
fi

here="$(cd "$(dirname "$0")" && pwd)"
out_dir="${1:-$here/../build}"
app="$out_dir/GraithNotifier.app"
macos_dir="$app/Contents/MacOS"

rm -rf "$app"
mkdir -p "$macos_dir"

cp "$here/Info.plist" "$app/Contents/Info.plist"

swiftc -O \
	-framework Foundation \
	-framework UserNotifications \
	-o "$macos_dir/graith-notifier" \
	"$here/main.swift"

# Ad-hoc code signature. UNUserNotificationCenter refuses to deliver from an
# unsigned bundle; ad-hoc signing (identity "-") is enough for a locally built
# helper. A distributed build would sign with a real Developer ID.
codesign --force --sign - "$app" >/dev/null 2>&1 || \
	echo "build.sh: codesign failed (notifications may be blocked until signed)" >&2

echo "built $app"
