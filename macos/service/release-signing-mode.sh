#!/bin/bash
# Report whether the complete macOS release-signing credential set is present.
# No credentials means the dev release keeps its legacy direct-spawn packaging;
# a partial set is an operator error and must not silently downgrade the build.
set -euo pipefail

secret_names=(
	MACOS_SIGNING_CERTIFICATE
	MACOS_SIGNING_CERTIFICATE_PASSWORD
	MACOS_SIGNING_IDENTITY
	MACOS_SIGNING_TEAM_ID
	MACOS_SIGNING_REQUIREMENT
	APPLE_NOTARY_PRIVATE_KEY
	APPLE_NOTARY_KEY_ID
	APPLE_NOTARY_ISSUER_ID
)

present=0
missing=""
for name in "${secret_names[@]}"; do
	if [ -n "${!name:-}" ]; then
		present=$((present + 1))
	else
		missing="${missing:+$missing }$name"
	fi
done

if [ "$present" -eq 0 ]; then
	echo disabled
	exit 0
fi

if [ "$present" -ne "${#secret_names[@]}" ]; then
	echo "partial macOS release-signing credentials; missing: $missing" >&2
	exit 1
fi

echo enabled
