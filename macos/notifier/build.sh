#!/bin/sh
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
# This is macOS-only: it needs `swiftc`. It is written for POSIX sh (no
# bashisms, no `set -o pipefail`) so `make notifier` — which invokes it via
# `sh` — is a genuine no-op on Linux/dash rather than erroring before the
# Darwin guard is reached (issue #1094 review).
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
out_dir="${1:-$here/../build}"
app="$out_dir/GraithNotifier.app"
macos_dir="$app/Contents/MacOS"

if [ "$(uname -s)" != "Darwin" ]; then
	# Remove any stale bundle left by a prior macOS build so a Linux `--snapshot`
	# in a shared checkout can't package an old app (issue #1101 review); then
	# skip — swiftc/codesign are macOS only.
	rm -rf "$app"
	echo "build.sh: skipping — the notifier app is macOS only" >&2
	exit 0
fi

if ! command -v swiftc >/dev/null 2>&1; then
	echo "build.sh: swiftc not found (install the Xcode command line tools)" >&2
	exit 1
fi

rm -rf "$app"
mkdir -p "$macos_dir"

cp "$here/Info.plist" "$app/Contents/Info.plist"

# Build a universal (arm64 + x86_64) binary so one bundle works on both Apple
# Silicon and Intel Macs. The release compiles the helper once on a single
# runner arch but ships it into both the darwin_arm64 and darwin_amd64 archives,
# so a host-arch-only build would leave the notifier broken on the other arch
# (issue #1101 review). Compile each slice (swiftc cross-compiles via the macOS
# SDK), then combine with lipo. The deployment target is pinned to match
# LSMinimumSystemVersion in Info.plist — without an explicit -target, swiftc
# stamps the build host's OS version, so a bundle built on a newer macOS would
# refuse to launch on older ones despite the plist claiming compatibility.
bin="$macos_dir/graith-notifier"
slice_dir="$(mktemp -d)"
arm_slice="$slice_dir/arm64"
x86_slice="$slice_dir/x86_64"
trap 'rm -rf "$slice_dir"' EXIT

swiftc -O \
	-target arm64-apple-macosx11.0 \
	-framework Foundation \
	-framework UserNotifications \
	-o "$arm_slice" \
	"$here/main.swift"

swiftc -O \
	-target x86_64-apple-macosx11.0 \
	-framework Foundation \
	-framework UserNotifications \
	-o "$x86_slice" \
	"$here/main.swift"

lipo -create -output "$bin" "$arm_slice" "$x86_slice"

# Code signature. UNUserNotificationCenter refuses to deliver from an unsigned
# bundle, so signing is required — a failure here is fatal, not a warning, so a
# broken bundle can't be reported as a successful build. Ad-hoc signing
# (identity "-") suffices for a locally built helper; a distributed build would
# sign with a real Developer ID.
if ! codesign --force --sign - "$app"; then
	echo "build.sh: codesign failed" >&2
	exit 1
fi

if ! codesign --verify --deep --strict "$app"; then
	echo "build.sh: codesign verification failed" >&2
	exit 1
fi

echo "built $app"
