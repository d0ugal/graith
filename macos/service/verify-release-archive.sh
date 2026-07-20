#!/bin/bash
# GoReleaser archive-signing hook. The signing pipe runs after archives and
# checksums are assembled but before any publisher, giving OSS GoReleaser a
# fail-closed verification gate over the exact Darwin tarball it will upload.
set -euo pipefail

[ "$#" -eq 3 ] || { echo "usage: verify-release-archive.sh ARCHIVE VERIFICATION_FILE IS_SNAPSHOT" >&2; exit 2; }
archive="$1"
verification="$2"
snapshot="$3"
signed_snapshot="${GRAITH_SIGNED_SNAPSHOT:-false}"

[ -f "$archive" ] || { echo "Darwin release archive is missing: $archive" >&2; exit 1; }
case "$snapshot" in true|false) ;; *) echo "invalid snapshot marker: $snapshot" >&2; exit 1 ;; esac
case "$signed_snapshot" in true|false) ;; *) echo "invalid signed snapshot marker: $signed_snapshot" >&2; exit 1 ;; esac

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

listing="$(tar -tzf "$archive")"
[ "$(printf '%s\n' "$listing" | grep -c 'Graith.app/Contents/Info.plist$')" -eq 1 ] || { echo "$archive has an ambiguous Graith.app" >&2; exit 1; }
tar -xzf "$archive" -C "$scratch"

info_files="$(find "$scratch" -type f -path '*/Graith.app/Contents/Info.plist' -print)"
[ "$(printf '%s\n' "$info_files" | grep -c .)" -eq 1 ] || { echo "$archive has an ambiguous Graith.app" >&2; exit 1; }
info="$info_files"
app="$(dirname "$(dirname "$info")")"

standalone_files="$(find "$scratch" -type f \( -name gr -o -name gr-dev \) ! -path '*/Graith.app/*' -print)"
[ "$(printf '%s\n' "$standalone_files" | grep -c .)" -eq 1 ] || { echo "$archive has an ambiguous standalone Graith CLI" >&2; exit 1; }
standalone="$standalone_files"

[ "$(find "$app/Contents/Library/LaunchAgents" -type f -name '*.plist' | wc -l | tr -d ' ')" -eq 65 ] || { echo "$archive does not contain the complete signed service slot set" >&2; exit 1; }
[ "$(/usr/libexec/PlistBuddy -c 'Print :LSUIElement' "$info")" = true ] || { echo "$archive service app is not headless" >&2; exit 1; }
cmp "$standalone" "$app/Contents/MacOS/gr"
codesign --verify --deep --strict --verbose=2 "$app"

verification_kind="development-structure"
if [ "$snapshot" = false ] || [ "$signed_snapshot" = true ]; then
	: "${GRAITH_SIGNING_TEAM_ID:?missing GRAITH_SIGNING_TEAM_ID}"
	: "${GRAITH_SIGNING_REQUIREMENT:?missing GRAITH_SIGNING_REQUIREMENT}"

	xcrun stapler validate "$app"
	spctl --assess --type execute --verbose=2 "$app"

	signing_details="$(codesign -d --verbose=4 -r- "$app" 2>&1)"
	actual_team="$(printf '%s\n' "$signing_details" | sed -n 's/^TeamIdentifier=//p' | head -1)"
	actual_requirement="$(printf '%s\n' "$signing_details" | sed -n 's/^designated => //p' | head -1)"
	[ "$actual_team" = "$GRAITH_SIGNING_TEAM_ID" ] || { echo "$archive service app has the wrong signing team" >&2; exit 1; }
	[ "$actual_requirement" = "$GRAITH_SIGNING_REQUIREMENT" ] || { echo "$archive service app has the wrong designated requirement" >&2; exit 1; }
	verification_kind="signed-notarized-service"
fi

mkdir -p "$(dirname "$verification")"
archive_hash="$(shasum -a 256 "$archive" | awk '{print $1}')"
printf '%s  %s  %s\n' "$archive_hash" "$(basename "$archive")" "$verification_kind" > "$verification"
