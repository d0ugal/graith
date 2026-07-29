#!/usr/bin/env bash
# Validate Renovate and prove custom managers extract the expected dependency
# surfaces. Native pins use deliberately stale fixtures to exercise grouping.
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
mkdir -p "$fixture/.github/workflows"
cp "$REPO_DIR/.github/ci-tool-versions.env" "$fixture/.github/ci-tool-versions.env"
cp "$REPO_DIR/.github/workflows/sandbox.yml" "$fixture/.github/workflows/sandbox.yml"
git -C "$fixture" init -q
git -C "$fixture" config user.name "Renovate fixture"
git -C "$fixture" config user.email "renovate-fixture@example.invalid"
git -C "$fixture" add renovate.json5 libghostty-native.lock.json .github/ci-tool-versions.env .github/workflows/sandbox.yml
git -C "$fixture" commit -qm "test: add dreich dependency fixture"

is_transient_tangled_tls_failure() {
    jq -se '
        # Level 40 is warning; unrelated warnings must not suppress a retry.
        [.[] | select(.level >= 50)] as $errors |
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

ci_expected='["gohugoio/hugo","golang.org/x/vuln/cmd/govulncheck","grafana/k6"]'
ci_actual="$(jq -sc '
    [
        .[] |
        select(.msg == "packageFiles with updates") |
        .config.regex[]? |
        select(.packageFile == ".github/ci-tool-versions.env") |
        .deps[]? |
        .depName
    ] | unique | sort
    ' "$log")"
if [[ "$ci_actual" != "$ci_expected" ]]; then
    echo "error: Renovate CI tool dependencies = $ci_actual; want $ci_expected" >&2
    exit 1
fi

if ! jq -se '
    [
        .[] |
        select(.msg == "packageFiles with updates") |
        .config.regex[]? |
        select(.packageFile == ".github/ci-tool-versions.env") |
        .deps[]?
    ] as $deps |
    any($deps[];
        .depName == "grafana/k6" and
        .datasource == "docker" and
        (.currentDigest | test("^sha256:[a-f0-9]{64}$")) and
        all(.updates[]?;
            .branchName != "renovate/pin-dependencies" and
            (.newValue | test("^[0-9]+[.][0-9]+[.][0-9]+-with-browser$")) and
            (.newDigest | test("^sha256:[a-f0-9]{64}$")))) and
    any($deps[];
        .depName == "golang.org/x/vuln/cmd/govulncheck" and
        .packageName == "golang.org/x/vuln" and
        .datasource == "go") and
    any($deps[];
        .depName == "gohugoio/hugo" and
        .datasource == "github-releases")
    ' "$log" >/dev/null; then
    echo "error: CI tool managers did not retain expected datasource or integrity metadata" >&2
    exit 1
fi

sandbox_expected='["eugene1g/agent-safehouse","nolabs-ai/nono"]'
sandbox_actual="$(jq -sc '
    [
        .[] |
        select(.msg == "packageFiles with updates") |
        .config.regex[]? |
        select(.packageFile == ".github/workflows/sandbox.yml") |
        .deps[]? |
        .depName
    ] | unique | sort
    ' "$log")"
if [[ "$sandbox_actual" != "$sandbox_expected" ]]; then
    echo "error: Renovate sandbox workflow dependencies = $sandbox_actual; want $sandbox_expected" >&2
    exit 1
fi

if ! jq -se '
    [
        .[] |
        select(.msg == "packageFiles with updates") |
        .config.regex[]? |
        select(.packageFile == ".github/workflows/sandbox.yml") |
        .deps[]?
    ] as $deps |
    any($deps[];
        .depName == "eugene1g/agent-safehouse" and
        .depType == "ci-safehouse" and
        .datasource == "github-releases") and
    any($deps[];
        .depName == "nolabs-ai/nono" and
        .datasource == "github-releases")
    ' "$log" >/dev/null; then
    echo "error: sandbox workflow managers did not retain expected datasources" >&2
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
    any($deps[];
        .depName == "Zig" and
        .datasource == "forgejo-tags" and
        .packageName == "ziglang/zig" and
        (.updates | length) > 0 and
        (((.registryUrls // []) | index("https://codeberg.org/") != null) or
         .registryUrl == "https://codeberg.org")) and
    all($deps[];
        all(.updates[]; .branchName | test("^renovate/(major-)?libghostty-native$")))
    ' "$log" >/dev/null; then
    echo "error: one or more native fixture updates escaped the libghostty group, Zig left Codeberg, or Zig stopped resolving updates" >&2
    exit 1
fi

if ! jq -se '
    any(.[] | select(.msg == "packageFiles with updates") |
        .config.regex[]?.deps[]?;
        .depType == "libghostty-native" and
        .depName != "Ghostty" and .depName != "Highway" and
        (.updates | length) > 0)
    ' "$log" >/dev/null; then
    echo "error: unrelated native dependency updates disappeared" >&2
    exit 1
fi

if ! jq -se '
    first(.[] | select(.msg == "Repository config") | .config) as $config |
    any($config.packageRules[];
        .matchDepTypes == ["ci-safehouse"] and
        .automerge == false and
        .dependencyDashboardApproval == true and
        ((.prBodyNotes // []) | length) > 0) and
    any($config.packageRules[];
        .matchDepTypes == ["libghostty-native"] and
        .groupSlug == "libghostty-native" and
        .automerge == false and
        .postUpgradeTasks == null) and
    any($config.packageRules[];
        .matchDepTypes == ["libghostty-native"] and
        .matchDepNames == ["go-libghostty", "Ghostty", "Zig", "uucode", "Highway", "simdutf"] and
        .dependencyDashboardApproval == true) and
    any($config.packageRules[];
        .matchDepTypes == ["libghostty-native"] and
        .enabled == false and
        (.matchJsonata // [] | length) > 0) and
    any($config.packageRules[];
        .matchManagers == ["gomod"] and
        .matchPackageNames == ["go.mitchellh.com/libghostty"] and
        .enabled == false and
        .automerge == false)
    ' "$log" >/dev/null; then
    echo "error: Safehouse review gate, native grouping, or go-libghostty automerge protection is missing" >&2
    exit 1
fi

if jq -se '
    any(.[] | select(.msg == "packageFiles with updates") |
        .config.regex[]?.deps[]?;
        .depType == "libghostty-native" and
        (.updates | length) > 0 and
        ((.depName == "Ghostty" and .currentDigest == "d4ac93a0395d321b043ee0116dc8a1a384f0fb83") or
         (.depName == "Highway" and .currentValue == "1.2.0")))
    ' "$log" >/dev/null; then
    echo "error: deferred unsupported Ghostty/Highway proposal is still offered" >&2
    exit 1
fi

echo "Renovate recognized CI and sandbox tool pins, suppressed the unsupported Ghostty/Highway proposal, and retained unrelated native dependency updates."
