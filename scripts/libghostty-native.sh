#!/usr/bin/env bash
# Build and validate the native libghostty-vt daemon backend without committing
# generated libraries. The Apple path reuses the exact xcframework consumed by
# the GUI. The source-build path checks out the same Ghostty SHA for Linux and
# cross-build verification.
set -euo pipefail

readonly GHOSTTY_SHA="91f66da24527fa02d92b5fd0b41cd020f553a64c"
readonly GHOSTTY_REPO="https://github.com/ghostty-org/ghostty.git"
readonly GHOSTTY_ARTIFACT_URL="https://github.com/d0ugal/graith/releases/download/libghostty-vt-91f66da/libghostty-vt.xcframework.zip"
readonly GHOSTTY_ARTIFACT_SHA256="25c1620e63311cc687637c8e3bdfae1fe3e070892966c07d0d91065ccda541c0"
readonly REQUIRED_ZIG="0.15.2"
readonly GO_LIBGHOSTTY_SHA="e9e1010f80b1ced0b7efcdb300f4838513c0816e"
readonly GO_LIBGHOSTTY_VERSION="v0.0.0-20260527181217-e9e1010f80b1"
readonly UUCODE_VERSION="0.2.0"
readonly UUCODE_HASH="uucode-0.2.0-ZZjBPqZVVABQepOqZHR7vV_NcaN-wats0IB6o-Exj6m9"
readonly HIGHWAY_VERSION="1.2.0"
readonly HIGHWAY_SHA="66486a10623fa0d72fe91260f96c892e41aceb06"
readonly SIMDUTF_VERSION="5.2.8"

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
NATIVE_WORK="${GRAITH_LIBGHOSTTY_WORK:-}"
OWN_WORK=0
if [[ -z "$NATIVE_WORK" ]]; then
    NATIVE_WORK="$(mktemp -d)"
    OWN_WORK=1
fi
KEEP_WORK="${GRAITH_LIBGHOSTTY_KEEP_WORK:-0}"
mkdir -p "$NATIVE_WORK"

cleanup() {
    if [[ "$OWN_WORK" == "1" && "$KEEP_WORK" != "1" && -d "$NATIVE_WORK" ]]; then
        rm -rf "$NATIVE_WORK"
    fi
}
trap cleanup EXIT

sha256_check() {
    local expected="$1"
    local path="$2"
    local actual

    if [[ "$(uname -s)" == "Darwin" ]]; then
        actual="$(shasum -a 256 "$path" | awk '{print $1}')"
    else
        actual="$(sha256sum "$path" | awk '{print $1}')"
    fi

    [[ "$actual" == "$expected" ]]
}

write_pkg_config() {
    local library="$1"
    local directory="$NATIVE_WORK/pkgconfig"
    mkdir -p "$directory"
    cat > "$directory/libghostty-vt-static.pc" <<EOF
Name: libghostty-vt-static
Description: pinned static libghostty-vt for Graith
Version: $GHOSTTY_SHA
Cflags: -I$REPO_DIR/gui/shared/Sources/CGhosttyVT/include -DGHOSTTY_STATIC
Libs: $library
EOF
    printf '%s\n' "$directory"
}

apple_library() {
    if [[ "$(uname -s)" != "Darwin" ]]; then
        echo "error: the pinned GUI artifact contains Apple slices only; use source-build on Linux" >&2
        return 1
    fi

    local archive="$NATIVE_WORK/libghostty-vt.xcframework.zip"
    local framework="$NATIVE_WORK/libghostty-vt.xcframework"
    local library="$framework/macos-arm64_x86_64/libghostty-vt.a"
    if [[ -f "$library" ]]; then
        printf '%s\n' "$library"
        return
    fi

    curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
        "$GHOSTTY_ARTIFACT_URL" --output "$archive"
    if ! sha256_check "$GHOSTTY_ARTIFACT_SHA256" "$archive"; then
        echo "error: libghostty-vt artifact checksum mismatch" >&2
        return 1
    fi
    unzip -q "$archive" -d "$NATIVE_WORK"

    if [[ ! -f "$library" ]]; then
        echo "error: pinned artifact does not contain the universal macOS library" >&2
        return 1
    fi
    printf '%s\n' "$library"
}

verify_metadata() {
    local source="${1:-}"
    local actual_version

    cd "$REPO_DIR"
    if ! command -v jq >/dev/null 2>&1; then
        echo "error: jq is required to verify the native SPDX inventory" >&2
        return 1
    fi

    actual_version="$(go list -mod=readonly -m -f '{{.Version}}' go.mitchellh.com/libghostty)"
    if [[ "$actual_version" != "$GO_LIBGHOSTTY_VERSION" ]]; then
        echo "error: go-libghostty version is $actual_version; want $GO_LIBGHOSTTY_VERSION" >&2
        return 1
    fi

    jq -e \
        --arg ghostty "$GHOSTTY_SHA" \
        --arg go_libghostty "$GO_LIBGHOSTTY_SHA" \
        --arg uucode "$UUCODE_VERSION" \
        --arg highway "$HIGHWAY_VERSION+$HIGHWAY_SHA" \
        --arg simdutf "$SIMDUTF_VERSION" '
        .spdxVersion == "SPDX-2.3" and
        any(.packages[]; .SPDXID == "SPDXRef-Package-Ghostty" and
            any(.externalRefs[]; .referenceLocator == $ghostty)) and
        any(.packages[]; .SPDXID == "SPDXRef-Package-GoLibghostty" and
            any(.externalRefs[]; .referenceLocator == $go_libghostty)) and
        any(.packages[]; .SPDXID == "SPDXRef-Package-Uucode" and .versionInfo == $uucode) and
        any(.packages[]; .SPDXID == "SPDXRef-Package-Highway" and
            (.versionInfo | startswith($highway[0:12]))) and
        any(.packages[]; .SPDXID == "SPDXRef-Package-Simdutf" and .versionInfo == $simdutf)
        ' libghostty-native.spdx.json >/dev/null

    for required in "$GHOSTTY_SHA" "$GO_LIBGHOSTTY_SHA" "$UUCODE_HASH" "$HIGHWAY_SHA" "$SIMDUTF_VERSION"; do
        if ! grep -Fq "$required" THIRD_PARTY_NOTICES.libghostty.md; then
            echo "error: native notice inventory is missing $required" >&2
            return 1
        fi
    done

    if [[ -z "$source" ]]; then
        return
    fi
    if [[ "$(git -C "$source" rev-parse HEAD)" != "$GHOSTTY_SHA" ]]; then
        echo "error: native metadata source is not the required Ghostty commit" >&2
        return 1
    fi
    grep -Fq ".version = \"$UUCODE_VERSION\"" "$source/build.zig.zon" ||
        grep -Fq "$UUCODE_HASH" "$source/build.zig.zon"
    grep -Fq ".version = \"$HIGHWAY_VERSION\"" "$source/pkg/highway/build.zig.zon"
    grep -Fq "$HIGHWAY_SHA" "$source/pkg/highway/build.zig.zon"
    grep -Fq ".version = \"$SIMDUTF_VERSION\"" "$source/pkg/simdutf/build.zig.zon"
}

run_go() {
    local mode="$1"
    local library
    library="$(apple_library)"
    PKG_CONFIG_PATH="$(write_pkg_config "$library")${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
    export PKG_CONFIG_PATH

    cd "$REPO_DIR"
    case "$mode" in
        test)
            verify_metadata
            go test -count=1 go.mitchellh.com/libghostty
            go test -count=1 -tags=libghostty ./internal/pty
            go test -count=1 -tags=libghostty ./internal/daemon \
                -run 'TestLibghostty|TestProbeUpgrade|TestUpgradeHelperHandoff'
            ;;
        race)
            verify_metadata
            go test -race -count=1 -tags=libghostty ./internal/pty \
                -run 'TestTerminalBackendCompatibilityCorpus|TestGhostty'
            go test -race -count=1 -tags=libghostty ./internal/daemon \
                -run 'TestLibghostty|TestProbeUpgrade|TestUpgradeHelperHandoff'
            ;;
        fuzz)
            verify_metadata
            local fuzztime="${GRAITH_LIBGHOSTTY_FUZZTIME:-10s}"
            local parallel="${GRAITH_LIBGHOSTTY_FUZZ_PARALLEL:-4}"
            go test -tags=libghostty ./internal/pty -run '^$' -parallel="$parallel" \
                -fuzz '^FuzzGhosttySnapshotDecoder$' -fuzztime="$fuzztime"
            go test -tags=libghostty ./internal/pty -run '^$' -parallel="$parallel" \
                -fuzz '^FuzzGhosttyRequestDecoder$' -fuzztime="$fuzztime"
            go test -tags=libghostty ./internal/pty -run '^$' -parallel="$parallel" \
                -fuzz '^FuzzGhosttyHelperWrite$' -fuzztime="$fuzztime"
            ;;
        bench)
            go test -run '^$' -tags=libghostty ./internal/pty \
                -bench '^BenchmarkTerminalBackends$' -benchmem -benchtime=3x -count=5
            ;;
        memory)
            local charm_test="$NATIVE_WORK/pty-charm.test"
            local ghostty_test="$NATIVE_WORK/pty-libghostty.test"
            go test -c -o "$charm_test" ./internal/pty
            go test -c -tags=libghostty \
                -o "$ghostty_test" ./internal/pty

            for backend in charm libghostty-helper; do
                local test_binary="$charm_test"
                if [[ "$backend" == "libghostty-helper" ]]; then
                    test_binary="$ghostty_test"
                fi
                if [[ "$(uname -s)" == "Darwin" ]]; then
                    /usr/bin/time -l env GRAITH_TERMINAL_MEMORY_BACKEND="$backend" \
                        "$test_binary" -test.run '^TestTerminalBackendPeakMemoryWorkload$' -test.v
                else
                    /usr/bin/time -v env GRAITH_TERMINAL_MEMORY_BACKEND="$backend" \
                        "$test_binary" -test.run '^TestTerminalBackendPeakMemoryWorkload$' -test.v
                fi
            done
            ;;
    esac
}

source_build() {
    local target="${1:-}"
    local output="${2:-}"
    if [[ -z "$target" || -z "$output" ]]; then
        echo "usage: $0 source-build <zig-target> <output-library>" >&2
        return 2
    fi
    if ! command -v zig >/dev/null 2>&1; then
        echo "error: zig $REQUIRED_ZIG is required" >&2
        return 1
    fi
    if [[ "$(zig version)" != "$REQUIRED_ZIG" ]]; then
        echo "error: zig $REQUIRED_ZIG is required; found $(zig version)" >&2
        return 1
    fi

    local source="$NATIVE_WORK/ghostty"
    git init -q "$source"
    if ! git -C "$source" remote get-url origin >/dev/null 2>&1; then
        git -C "$source" remote add origin "$GHOSTTY_REPO"
    elif [[ "$(git -C "$source" remote get-url origin)" != "$GHOSTTY_REPO" ]]; then
        echo "error: existing Ghostty worktree has an unexpected origin" >&2
        return 1
    fi
    git -C "$source" fetch --depth 1 origin "$GHOSTTY_SHA"
    git -C "$source" checkout -q --detach FETCH_HEAD
    if [[ "$(git -C "$source" rev-parse HEAD)" != "$GHOSTTY_SHA" ]]; then
        echo "error: Ghostty checkout did not resolve to the required SHA" >&2
        return 1
    fi
    verify_metadata "$source"

    (
        cd "$source"
        zig build \
            --global-cache-dir "$NATIVE_WORK/zig-global" \
            --cache-dir "$NATIVE_WORK/zig-local" \
            -Demit-lib-vt=true \
            -Demit-xcframework=false \
            -Doptimize=ReleaseFast \
            -Dtarget="$target"
    )

    local library="$source/zig-out/lib/libghostty-vt.a"
    if [[ ! -f "$library" ]]; then
        echo "error: Ghostty build did not produce $library" >&2
        return 1
    fi
    mkdir -p "$(dirname "$output")"
    cp "$library" "$output"
    write_pkg_config "$output" >/dev/null
}

source_test() {
    local target="${1:-}"
    local source="$NATIVE_WORK/ghostty"
    if [[ -z "$target" ]]; then
        echo "usage: $0 source-test <zig-target>" >&2
        return 2
    fi
    verify_metadata "$source"
    # Ghostty enables the slow runtime safety asserted by test-lib-vt only in
    # Debug builds; release artifacts remain ReleaseFast in source_build above.
    (
        cd "$source"
        zig build test-lib-vt \
            --global-cache-dir "$NATIVE_WORK/zig-global" \
            --cache-dir "$NATIVE_WORK/zig-local" \
            -Demit-lib-vt=true \
            -Demit-xcframework=false \
            -Doptimize=Debug \
            -Dtarget="$target"
    )
}

usage() {
    cat <<EOF
usage: $0 test|race|fuzz|bench|memory|all
       $0 source-build <zig-target> <output-library>
       $0 source-test <zig-target>
       $0 verify-metadata [ghostty-source]

test/bench/memory use the checksum-pinned universal Apple artifact.
source-build checks out Ghostty $GHOSTTY_SHA and requires Zig $REQUIRED_ZIG.
EOF
}

case "${1:-}" in
    test|race|fuzz|bench|memory)
        run_go "$1"
        ;;
    all)
        run_go test
        run_go race
        run_go fuzz
        run_go bench
        run_go memory
        ;;
    source-build)
        source_build "${2:-}" "${3:-}"
        ;;
    source-test)
        source_test "${2:-}"
        ;;
    verify-metadata)
        verify_metadata "${2:-}"
        ;;
    *)
        usage >&2
        exit 2
        ;;
esac
