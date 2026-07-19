#!/bin/sh
# Disposable SMAppService spike for design issue #1472. This assembles an
# ad-hoc-signed Graith.app containing the current gr daemon plus a tiny service
# registration controller. It does not install production resources.
set -eu

if [ "$(uname -s)" != "Darwin" ]; then
	echo "build.sh: macOS is required" >&2
	exit 1
fi

spike_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$spike_dir/../../../.." && pwd)
output_root=${1:-$(mktemp -d "${TMPDIR:-/tmp}/graith-identity-spike.XXXXXX")}
app="$output_root/Graith.app"

if [ -e "$app" ]; then
	echo "build.sh: refusing to overwrite existing $app" >&2
	exit 1
fi

mkdir -p "$app/Contents/MacOS" "$app/Contents/Library/LaunchAgents"
cp "$spike_dir/Info.plist" "$app/Contents/Info.plist"
cp "$spike_dir/net.graith.design-spike.daemon.plist" \
	"$app/Contents/Library/LaunchAgents/net.graith.design-spike.daemon.plist"
cp "$spike_dir/net.graith.design-spike.daemon.profile.00.plist" \
	"$app/Contents/Library/LaunchAgents/net.graith.design-spike.daemon.profile.00.plist"
cp "$spike_dir/net.graith.design-spike.daemon.profile.01.plist" \
	"$app/Contents/Library/LaunchAgents/net.graith.design-spike.daemon.profile.01.plist"

xcrun swiftc -O -target "$(uname -m)-apple-macosx13.0" \
	-framework Foundation -framework ServiceManagement \
	-o "$app/Contents/MacOS/graith-identity-spike-control" \
	"$spike_dir/control.swift"

(cd "$repo_dir" && go build -o "$app/Contents/MacOS/gr" ./cmd/graith)

codesign --force --deep --sign - "$app"
codesign --verify --deep --strict "$app"

echo "$app"
