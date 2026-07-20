#!/bin/bash
# Assemble an architecture-specific Graith.app and byte-identical standalone gr.
set -euo pipefail

usage() {
	echo "usage: build.sh --arch arm64|amd64 --version VERSION --commit SHA --payload GR --output DIR [--build-number N] [--identity ID --notary-profile PROFILE --expected-team TEAM --expected-requirement REQUIREMENT | --development]" >&2
	exit 2
}

arch=""
version=""
commit=""
payload=""
output=""
identity=""
notary_profile=""
build_number="1"
expected_team=""
expected_requirement=""
development=false

while [ "$#" -gt 0 ]; do
	case "$1" in
		--arch) [ "$#" -ge 2 ] || usage; arch="$2"; shift 2 ;;
		--version) [ "$#" -ge 2 ] || usage; version="$2"; shift 2 ;;
		--commit) [ "$#" -ge 2 ] || usage; commit="$2"; shift 2 ;;
		--payload) [ "$#" -ge 2 ] || usage; payload="$2"; shift 2 ;;
		--output) [ "$#" -ge 2 ] || usage; output="$2"; shift 2 ;;
		--identity) [ "$#" -ge 2 ] || usage; identity="$2"; shift 2 ;;
		--notary-profile) [ "$#" -ge 2 ] || usage; notary_profile="$2"; shift 2 ;;
		--build-number) [ "$#" -ge 2 ] || usage; build_number="$2"; shift 2 ;;
		--expected-team) [ "$#" -ge 2 ] || usage; expected_team="$2"; shift 2 ;;
		--expected-requirement) [ "$#" -ge 2 ] || usage; expected_requirement="$2"; shift 2 ;;
		--development) development=true; shift ;;
		*) usage ;;
	esac
done

[ "$(uname -s)" = Darwin ] || { echo "Graith.app must be built on macOS" >&2; exit 1; }
[ -n "$arch" ] && [ -n "$version" ] && [ -n "$commit" ] && [ -n "$payload" ] && [ -n "$output" ] || usage
[ -x "$payload" ] || { echo "payload is not executable: $payload" >&2; exit 1; }
[[ "$build_number" =~ ^[0-9]+(\.[0-9]+){0,2}$ ]] || { echo "build number must contain one to three numeric components" >&2; exit 1; }
case "$arch" in
	arm64) swift_arch=arm64 ;;
	amd64|x86_64) arch=amd64; swift_arch=x86_64 ;;
	*) usage ;;
esac

if [ "$development" = true ]; then
	[ -z "$notary_profile" ] || { echo "development build cannot use notarization" >&2; exit 1; }
	[ -n "$identity" ] || identity="-"
else
	[ -n "$identity" ] || { echo "stable build requires --identity" >&2; exit 1; }
	[ "$identity" != "-" ] || { echo "stable build refuses ad-hoc signing" >&2; exit 1; }
	[ -n "$notary_profile" ] || { echo "stable build requires --notary-profile" >&2; exit 1; }
	[ -n "$expected_team" ] || { echo "stable build requires --expected-team" >&2; exit 1; }
	[ -n "$expected_requirement" ] || { echo "stable build requires --expected-requirement" >&2; exit 1; }
fi

payload_arches="$(lipo -archs "$payload")"
case " $payload_arches " in
	*" $swift_arch "*) ;;
	*) echo "payload architecture $payload_arches does not include $swift_arch" >&2; exit 1 ;;
esac

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
app="$stage/Graith.app"
contents="$app/Contents"
macos_dir="$contents/MacOS"
agents_dir="$contents/Library/LaunchAgents"
resources_dir="$contents/Resources"
generated_swift="$stage/Services.generated.swift"
mkdir -p "$macos_dir" "$agents_dir" "$resources_dir"

cp "$here/Info.plist" "$contents/Info.plist"
cp "$payload" "$macos_dir/gr"
chmod 755 "$macos_dir/gr"

(cd "$repo" && go run ./internal/daemonservice/cmd/generate \
	--launch-agents "$agents_dir" --swift "$generated_swift")

swiftc -O -target "${swift_arch}-apple-macosx13.0" \
	-framework Foundation -framework ServiceManagement \
	-o "$macos_dir/graith-service-controller" \
	"$here/controller.swift" "$generated_swift"

# Reuse the product mark rather than introducing a second app identity asset.
icon_source="$repo/website/assets/images/logos/mark.svg"
iconset="$stage/Graith.iconset"
mkdir -p "$iconset"
# Rasterize the vector source at the largest required size first; producing its
# intrinsic 48px size and upscaling it makes the Login Items icon visibly soft.
sips -s format png -z 1024 1024 "$icon_source" --out "$stage/icon.png" >/dev/null
for size in 16 32 128 256 512; do
	sips -z "$size" "$size" "$stage/icon.png" --out "$iconset/icon_${size}x${size}.png" >/dev/null
	double=$((size * 2))
	sips -z "$double" "$double" "$stage/icon.png" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$iconset" -o "$resources_dir/Graith.icns"

sign_args="--force --options runtime"
if [ "$identity" = "-" ]; then
	sign_args="$sign_args --timestamp=none"
else
	sign_args="$sign_args --timestamp"
fi
# shellcheck disable=SC2086
codesign $sign_args --sign "$identity" "$macos_dir/gr"
# shellcheck disable=SC2086
codesign $sign_args --sign "$identity" "$macos_dir/graith-service-controller"

payload_hash="$(shasum -a 256 "$macos_dir/gr" | awk '{print $1}')"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $version" "$contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $build_number" "$contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :GraithCommitSHA $commit" "$contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :GraithPayloadSHA256 $payload_hash" "$contents/Info.plist"

plutil -lint "$contents/Info.plist" >/dev/null
for plist in "$agents_dir"/*.plist; do
	plutil -lint "$plist" >/dev/null
done

# Nested executables are signed first; the outer app seals them without --deep.
# shellcheck disable=SC2086
codesign $sign_args --sign "$identity" "$app"
codesign --verify --deep --strict --verbose=2 "$app"

if [ "$development" = false ]; then
	signing_details="$(codesign -d --verbose=4 -r- "$app" 2>&1)"
	actual_team="$(printf '%s\n' "$signing_details" | sed -n 's/^TeamIdentifier=//p' | head -1)"
	actual_requirement="$(printf '%s\n' "$signing_details" | sed -n 's/^designated => //p' | head -1)"
	[ "$actual_team" = "$expected_team" ] || { echo "signed app team $actual_team does not match expected team" >&2; exit 1; }
	[ "$actual_requirement" = "$expected_requirement" ] || { echo "signed app designated requirement does not match the release expectation" >&2; exit 1; }
fi

if [ "$development" = false ]; then
	archive="$stage/Graith.zip"
	/usr/bin/ditto -c -k --keepParent "$app" "$archive"
	xcrun notarytool submit "$archive" --keychain-profile "$notary_profile" --wait
	xcrun stapler staple "$app"
	xcrun stapler validate "$app"
	spctl --assess --type execute --verbose=2 "$app"
fi

rm -rf "$output"
mkdir -p "$output"
/usr/bin/ditto --rsrc --extattr --noqtn "$app" "$output/Graith.app"
cp "$app/Contents/MacOS/gr" "$output/gr"
chmod 755 "$output/gr"
cmp "$output/gr" "$output/Graith.app/Contents/MacOS/gr"
codesign --verify --deep --strict "$output/Graith.app"
if [ "$development" = false ]; then
	xcrun stapler validate "$output/Graith.app"
	spctl --assess --type execute --verbose=2 "$output/Graith.app"
fi

echo "built $output/Graith.app and byte-identical $output/gr"
