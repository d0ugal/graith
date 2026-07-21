#!/bin/sh
# Verify that a controller in a second same-identifier app copy can query and
# unregister the default service registered by the first copy.
set -eu

if [ "$(uname -s)" != "Darwin" ]; then
	echo "cross-copy.sh: macOS is required" >&2
	exit 1
fi

spike_dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
first_root=$(mktemp -d "${TMPDIR:-/tmp}/graith-identity-spike-v1.XXXXXX")
second_root=$(mktemp -d "${TMPDIR:-/tmp}/graith-identity-spike-v2.XXXXXX")
first_app=$("$spike_dir/build.sh" "$first_root")
second_app="$second_root/Graith.app"
cp -R "$first_app" "$second_app"

plutil -replace CFBundleShortVersionString -string 2.0 \
	"$second_app/Contents/Info.plist"
plutil -replace CFBundleVersion -string 2 "$second_app/Contents/Info.plist"
codesign --force --sign - "$second_app"
codesign --verify --deep --strict "$second_app"

first_control="$first_app/Contents/MacOS/graith-identity-spike-control"
second_control="$second_app/Contents/MacOS/graith-identity-spike-control"
initial_status=$("$first_control" default status)
case "$initial_status" in
	not-registered | not-found) ;;
	*)
		echo "cross-copy.sh: refusing to alter existing status $initial_status" >&2
		exit 1
		;;
esac

trap '"$first_control" default unregister >/dev/null 2>&1 || true' EXIT
echo "first-before=$initial_status"
echo "first-register=$("$first_control" default register)"
echo "second-before=$("$second_control" default status)"
echo "second-unregister=$("$second_control" default unregister)"
echo "first-after=$("$first_control" default status)"
echo "second-after=$("$second_control" default status)"

label="gui/$(id -u)/net.graith.design-spike.daemon"
attempt=0
while launchctl print "$label" >/dev/null 2>&1; do
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 5 ]; then
		echo "cross-copy.sh: $label remained loaded" >&2
		exit 1
	fi
	sleep 1
done
echo "launchctl=absent"
echo "temporary apps retained at $first_root and $second_root"
