#!/usr/bin/env bash
# Validate Renovate and prove every native pin is upgraded into the explicit,
# non-automerge libghostty dependency unit using deliberately stale fixtures.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
readonly REPO_DIR
readonly RENOVATE_BIN="${RENOVATE_BIN:-renovate}"
readonly RENOVATE_CONFIG_VALIDATOR_BIN="${RENOVATE_CONFIG_VALIDATOR_BIN:-renovate-config-validator}"
readonly RENOVATE_LOOKUP_ATTEMPTS=3

for required in "$RENOVATE_BIN" "$RENOVATE_CONFIG_VALIDATOR_BIN" git jq; do
    if ! command -v "$required" >/dev/null 2>&1; then
        echo "error: $required is required" >&2
        exit 1
    fi
done

"$RENOVATE_CONFIG_VALIDATOR_BIN" --strict --no-global "$REPO_DIR/renovate.json5"

fixture="$(mktemp -d)"
log="$(mktemp)"
cleanup() {
    rm -rf "$fixture"
    rm -f "$log"
}
trap cleanup EXIT

cp "$REPO_DIR/renovate.json5" "$fixture/renovate.json5"
cp "$REPO_DIR/internal/libghosttydeps/testdata/renovate/libghostty-native.lock.json" \
    "$fixture/libghostty-native.lock.json"
git -C "$fixture" init -q
git -C "$fixture" config user.name "Renovate fixture"
git -C "$fixture" config user.email "renovate-fixture@example.invalid"
git -C "$fixture" add renovate.json5 libghostty-native.lock.json
git -C "$fixture" commit -qm "test: add dreich dependency fixture"

is_transient_tangled_tls_failure() {
    jq -se '
        [.[] | select(.level >= 40)] as $errors |
        ($errors | length) > 0 and
        all($errors[];
            .msg == "lookupUpdates error" and
            ((.err.message // .err // "") | tostring |
                contains("fatal: unable to access '\''https://tangled.org/mitchellh.com/go-libghostty/'\''")) and
            ((.err.message // .err // "") | tostring |
                contains("gnutls_handshake() failed: The TLS connection was non-properly terminated.")))
        ' "$log" >/dev/null
}

run_renovate_lookup() {
    local attempt=1

    while true; do
        : >"$log"
        if (
            cd "$fixture"
            LOG_FORMAT=json LOG_LEVEL=debug \
                "$RENOVATE_BIN" --platform=local --dry-run=lookup --require-config=required \
                >"$log"
        ); then
            return 0
        fi

        if ((attempt >= RENOVATE_LOOKUP_ATTEMPTS)) || ! is_transient_tangled_tls_failure; then
            return 1
        fi

        attempt=$((attempt + 1))
        echo "warning: transient tangled.org TLS failure; retrying Renovate lookup (attempt $attempt of $RENOVATE_LOOKUP_ATTEMPTS)" >&2
        sleep "$((attempt - 1))"
    done
}

if ! run_renovate_lookup; then
    echo "error: Renovate lookup dry run failed" >&2
    jq -r 'select(.level >= 40) | [.msg, (.err.message // .err // "")] | @tsv' "$log" >&2 || true
    exit 1
fi

expected='["Ghostty","Highway","SPDX tools-java","Zig","go-libghostty","simdutf","uucode"]'
actual="$(jq -sc '
    [
        .[] |
        select(.msg == "packageFiles with updates") |
        .config.regex[]?.deps[]? |
        select(.depType == "libghostty-native") |
        .depName
    ] | unique | sort
    ' "$log")"
if [[ "$actual" != "$expected" ]]; then
    echo "error: Renovate native dependencies = $actual; want $expected" >&2
    exit 1
fi

if ! jq -se '
    [
        .[] |
        select(.msg == "packageFiles with updates") |
        .config.regex[]?.deps[]? |
        select(.depType == "libghostty-native")
    ] as $deps |
    ($deps | length) == 7 and
    all($deps[];
        (.updates | length) > 0 and
        all(.updates[]; .branchName | test("^renovate/(major-)?libghostty-native$")))
    ' "$log" >/dev/null; then
    echo "error: one or more native fixtures were not updated in the libghostty group" >&2
    exit 1
fi

if ! jq -se '
    first(.[] | select(.msg == "Repository config") | .config) as $config |
    any($config.packageRules[];
        .matchDepTypes == ["libghostty-native"] and
        .groupSlug == "libghostty-native" and
        .automerge == false and
        .postUpgradeTasks == null) and
    any($config.packageRules[];
        .matchDepTypes == ["libghostty-native"] and
        .matchDepNames == ["Ghostty", "Zig", "uucode", "Highway", "simdutf"] and
        .dependencyDashboardApproval == true) and
    any($config.packageRules[];
        .matchManagers == ["gomod"] and
        .matchPackageNames == ["go.mitchellh.com/libghostty"] and
        .enabled == false and
        .automerge == false)
    ' "$log" >/dev/null; then
    echo "error: native grouping or go-libghostty automerge protection is missing" >&2
    exit 1
fi

echo "Renovate detected and grouped all seven native dependency fixtures."
