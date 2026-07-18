#!/usr/bin/env bash
# Reproduce the libghostty-vt daemon-backend spike without committing native
# artifacts. The Apple test path reuses the exact xcframework consumed by the
# GUI. The source-build path checks out the same Ghostty SHA for Linux/cross
# build verification.
set -euo pipefail

readonly GHOSTTY_SHA="91f66da24527fa02d92b5fd0b41cd020f553a64c"
readonly GHOSTTY_REPO="https://github.com/ghostty-org/ghostty.git"
readonly GHOSTTY_ARTIFACT_URL="https://github.com/d0ugal/graith/releases/download/libghostty-vt-91f66da/libghostty-vt.xcframework.zip"
readonly GHOSTTY_ARTIFACT_SHA256="25c1620e63311cc687637c8e3bdfae1fe3e070892966c07d0d91065ccda541c0"
readonly REQUIRED_ZIG="0.15.2"

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SPIKE_WORK="${GRAITH_LIBGHOSTTY_WORK:-}"
OWN_WORK=0
if [[ -z "$SPIKE_WORK" ]]; then
    SPIKE_WORK="$(mktemp -d)"
    OWN_WORK=1
fi
KEEP_WORK="${GRAITH_LIBGHOSTTY_KEEP_WORK:-0}"
mkdir -p "$SPIKE_WORK"

cleanup() {
    if [[ "$OWN_WORK" == "1" && "$KEEP_WORK" != "1" && -d "$SPIKE_WORK" ]]; then
        rm -rf "$SPIKE_WORK"
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

apple_library() {
    if [[ "$(uname -s)" != "Darwin" ]]; then
        echo "error: the pinned GUI artifact contains Apple slices only; use source-build on Linux" >&2
        return 1
    fi

    local archive="$SPIKE_WORK/libghostty-vt.xcframework.zip"
    local framework="$SPIKE_WORK/libghostty-vt.xcframework"
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
    unzip -q "$archive" -d "$SPIKE_WORK"

    if [[ ! -f "$library" ]]; then
        echo "error: pinned artifact does not contain the universal macOS library" >&2
        return 1
    fi
    printf '%s\n' "$library"
}

run_go() {
    local mode="$1"
    local library
    library="$(apple_library)"

    cd "$REPO_DIR"
    case "$mode" in
        test)
            go test -count=1 -tags=libghostty -ldflags="-extldflags $library" ./internal/pty \
                -run 'TestTerminalSpikeCompatibilityCorpus'
            ;;
        bench)
            go test -run '^$' -tags=libghostty -ldflags="-extldflags $library" ./internal/pty \
                -bench '^BenchmarkTerminalSpike$' -benchmem -benchtime=3x -count=5
            ;;
        memory)
            local charm_test="$SPIKE_WORK/pty-charm.test"
            local ghostty_test="$SPIKE_WORK/pty-libghostty.test"
            go test -c -o "$charm_test" ./internal/pty
            go test -c -tags=libghostty -ldflags="-extldflags $library" \
                -o "$ghostty_test" ./internal/pty

            for backend in charm libghostty; do
                local test_binary="$charm_test"
                if [[ "$backend" == "libghostty" ]]; then
                    test_binary="$ghostty_test"
                fi
                if [[ "$(uname -s)" == "Darwin" ]]; then
                    /usr/bin/time -l env GRAITH_TERMINAL_MEMORY_BACKEND="$backend" \
                        "$test_binary" -test.run '^TestTerminalSpikePeakMemoryWorkload$' -test.v
                else
                    /usr/bin/time -v env GRAITH_TERMINAL_MEMORY_BACKEND="$backend" \
                        "$test_binary" -test.run '^TestTerminalSpikePeakMemoryWorkload$' -test.v
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

    local source="$SPIKE_WORK/ghostty"
    git init -q "$source"
    git -C "$source" remote add origin "$GHOSTTY_REPO"
    git -C "$source" fetch --depth 1 origin "$GHOSTTY_SHA"
    git -C "$source" checkout -q --detach FETCH_HEAD
    if [[ "$(git -C "$source" rev-parse HEAD)" != "$GHOSTTY_SHA" ]]; then
        echo "error: Ghostty checkout did not resolve to the required SHA" >&2
        return 1
    fi

    (
        cd "$source"
        zig build \
            --global-cache-dir "$SPIKE_WORK/zig-global" \
            --cache-dir "$SPIKE_WORK/zig-local" \
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
}

usage() {
    cat <<EOF
usage: $0 test|bench|memory|all
       $0 source-build <zig-target> <output-library>

test/bench/memory use the checksum-pinned universal Apple artifact.
source-build checks out Ghostty $GHOSTTY_SHA and requires Zig $REQUIRED_ZIG.
EOF
}

case "${1:-}" in
    test|bench|memory)
        run_go "$1"
        ;;
    all)
        run_go test
        run_go bench
        run_go memory
        ;;
    source-build)
        source_build "${2:-}" "${3:-}"
        ;;
    *)
        usage >&2
        exit 2
        ;;
esac
