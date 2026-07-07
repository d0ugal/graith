#!/usr/bin/env bash
# Preset the RPM signing passphrase into gpg-agent for EVERY keygrip of the
# signing key — the primary key AND any subkeys — not just the first one.
#
# rpm --addsign offers no passphrase channel of its own: the .rpmmacros
# %_gpg_sign_cmd runs gpg with --pinentry-mode loopback and no passphrase
# source, so it relies entirely on gpg-agent already holding the passphrase
# for whichever key gpg selects. When the signing key is structured as a
# primary key plus a dedicated signing subkey (a very common GPG layout),
# rpm drives gpg to use the *subkey*, whose keygrip differs from the
# primary's. Presetting only the first `grp:` line therefore breaks signing
# in batch mode — the agent can't supply the passphrase for the subkey and
# the sign hangs/fails. Preset every keygrip instead. (issue #767)
#
# Reads the output of
#   gpg --with-keygrip --list-secret-keys --with-colons "$KEY_ID"
# on stdin, and for each keygrip invokes the gpg-preset-passphrase helper.
#
# Usage:
#   gpg --with-keygrip --list-secret-keys --with-colons "$GPG_KEY_ID" \
#     | GPG_PASSPHRASE="$GPG_PASSPHRASE" \
#       scripts/rpm-preset-keygrips.sh /usr/lib/gnupg/gpg-preset-passphrase
set -euo pipefail

PRESET="${1:?path to the gpg-preset-passphrase helper is required}"
: "${GPG_PASSPHRASE:?GPG_PASSPHRASE must be set in the environment}"

# Field 10 of every `grp:` record is a keygrip (primary key + each subkey).
keygrips="$(awk -F: '/^grp:/ { print $10 }')"

if [ -z "$keygrips" ]; then
  echo "rpm-preset-keygrips: no keygrips found in the key listing" >&2
  exit 1
fi

while IFS= read -r keygrip; do
  [ -n "$keygrip" ] || continue
  # Feed the passphrase on stdin rather than as an argv `--passphrase` so it
  # doesn't appear in the process table (`ps`) to co-tenants of the runner.
  printf '%s' "$GPG_PASSPHRASE" | "$PRESET" --preset "$keygrip"
done <<EOF
$keygrips
EOF
