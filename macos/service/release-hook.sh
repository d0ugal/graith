#!/bin/bash
# GoReleaser Darwin post-build hook: sign/notarize Graith.app, then replace the
# archive's standalone gr with the byte-identical signed embedded payload.
set -euo pipefail

[ "$#" -eq 5 ] || { echo "usage: release-hook.sh ARTIFACT TARGET VERSION COMMIT IS_SNAPSHOT" >&2; exit 2; }
artifact="$1"
target="$2"
version="$3"
commit="$4"
snapshot="$5"

case "$target" in
	darwin_arm64*) arch=arm64 ;;
	darwin_amd64*) arch=amd64 ;;
	*) echo "unsupported daemon service release target: $target" >&2; exit 1 ;;
esac

build_number="${GITHUB_RUN_NUMBER:-1}"
output="macos/build/service-release-$arch"
here="$(cd "$(dirname "$0")" && pwd)"

if [ "$snapshot" = true ]; then
	sh "$here/build.sh" \
		--arch "$arch" \
		--version "$version" \
		--commit "$commit" \
		--build-number "$build_number" \
		--payload "$artifact" \
		--output "$output" \
		--development
else
	[ "$snapshot" = false ] || { echo "invalid snapshot marker: $snapshot" >&2; exit 1; }
	: "${GRAITH_MACOS_SIGNING_IDENTITY:?missing GRAITH_MACOS_SIGNING_IDENTITY}"
	: "${GRAITH_NOTARY_PROFILE:?missing GRAITH_NOTARY_PROFILE}"
	: "${GRAITH_SIGNING_TEAM_ID:?missing GRAITH_SIGNING_TEAM_ID}"
	: "${GRAITH_SIGNING_REQUIREMENT:?missing GRAITH_SIGNING_REQUIREMENT}"
	sh "$here/build.sh" \
		--arch "$arch" \
		--version "$version" \
		--commit "$commit" \
		--build-number "$build_number" \
		--payload "$artifact" \
		--output "$output" \
		--identity "$GRAITH_MACOS_SIGNING_IDENTITY" \
		--notary-profile "$GRAITH_NOTARY_PROFILE" \
		--expected-team "$GRAITH_SIGNING_TEAM_ID" \
		--expected-requirement "$GRAITH_SIGNING_REQUIREMENT"
fi

cp "$output/gr" "$artifact"
chmod 755 "$artifact"
cmp "$artifact" "$output/Graith.app/Contents/MacOS/gr"
