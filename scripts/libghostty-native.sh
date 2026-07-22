#!/usr/bin/env bash
# Build and validate the native libghostty-vt daemon backend without committing
# generated libraries. The Apple path reuses the exact xcframework consumed by
# the GUI. The source-build path checks out the same Ghostty SHA for Linux and
# cross-build verification.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DEPENDENCY_LOCK="$REPO_DIR/libghostty-native.lock.json"
if ! command -v jq >/dev/null 2>&1; then
    echo "error: jq is required to load the native dependency lock" >&2
    exit 1
fi

GHOSTTY_SHA="$(jq -er '.ghostty.commit' "$DEPENDENCY_LOCK")"
GHOSTTY_REPO="$(jq -er '.ghostty.repository' "$DEPENDENCY_LOCK")"
GHOSTTY_ARTIFACT_URL="$(jq -er '.ghostty.appleArtifact.url' "$DEPENDENCY_LOCK")"
GHOSTTY_ARTIFACT_SHA256="$(jq -er '.ghostty.appleArtifact.sha256' "$DEPENDENCY_LOCK")"
REQUIRED_ZIG="$(jq -er '.zig.version' "$DEPENDENCY_LOCK")"
GO_LIBGHOSTTY_SHA="$(jq -er '.goLibghostty.commit' "$DEPENDENCY_LOCK")"
GO_LIBGHOSTTY_VERSION="$(jq -er '.goLibghostty.version' "$DEPENDENCY_LOCK")"
GO_LIBGHOSTTY_SUM="$(jq -er '.goLibghostty.moduleSum' "$DEPENDENCY_LOCK")"
UUCODE_VERSION="$(jq -er '.uucode.version' "$DEPENDENCY_LOCK")"
UUCODE_HASH="$(jq -er '.uucode.zigHash' "$DEPENDENCY_LOCK")"
HIGHWAY_VERSION="$(jq -er '.highway.version' "$DEPENDENCY_LOCK")"
HIGHWAY_SHA="$(jq -er '.highway.commit' "$DEPENDENCY_LOCK")"
SIMDUTF_VERSION="$(jq -er '.simdutf.version' "$DEPENDENCY_LOCK")"
SIMDUTF_MANIFEST_VERSION="$(jq -er '.simdutf.manifestVersion' "$DEPENDENCY_LOCK")"
SIMDUTF_UPSTREAM_SHA="$(jq -er '.simdutf.commit' "$DEPENDENCY_LOCK")"
SIMDUTF_CPP_SHA256="$(jq -er '.simdutf.cppSHA256' "$DEPENDENCY_LOCK")"
SIMDUTF_HEADER_SHA256="$(jq -er '.simdutf.headerSHA256' "$DEPENDENCY_LOCK")"
ZIG_SOURCE_SHA256="$(jq -er '.zig.sourceSHA256' "$DEPENDENCY_LOCK")"
SPDX_TOOLS_VERSION="$(jq -er '.spdxTools.version' "$DEPENDENCY_LOCK")"
SPDX_TOOLS_URL="$(jq -er '.spdxTools.url' "$DEPENDENCY_LOCK")"
SPDX_TOOLS_SHA256="$(jq -er '.spdxTools.sha256' "$DEPENDENCY_LOCK")"
readonly GHOSTTY_SHA GHOSTTY_REPO GHOSTTY_ARTIFACT_URL GHOSTTY_ARTIFACT_SHA256
readonly REQUIRED_ZIG GO_LIBGHOSTTY_SHA GO_LIBGHOSTTY_VERSION GO_LIBGHOSTTY_SUM
readonly UUCODE_VERSION UUCODE_HASH HIGHWAY_VERSION HIGHWAY_SHA
readonly SIMDUTF_VERSION SIMDUTF_MANIFEST_VERSION SIMDUTF_UPSTREAM_SHA
readonly SIMDUTF_CPP_SHA256 SIMDUTF_HEADER_SHA256 ZIG_SOURCE_SHA256
readonly SPDX_TOOLS_VERSION SPDX_TOOLS_URL SPDX_TOOLS_SHA256
readonly SPDX_NAMESPACE="https://github.com/d0ugal/graith/sbom/libghostty-native/$GHOSTTY_SHA/$GO_LIBGHOSTTY_SHA"
readonly BENCH_SAMPLES="5"
readonly BENCHTIME="1s"
readonly MEASUREMENT_GOMAXPROCS="10"
readonly MEMORY_SAMPLES="5"

NATIVE_WORK="${GRAITH_LIBGHOSTTY_WORK:-}"
OWN_WORK=0
if [[ -z "$NATIVE_WORK" ]]; then
    if ! NATIVE_WORK="$(mktemp -d)" || [[ -z "$NATIVE_WORK" ]]; then
        echo "error: could not create the native work directory" >&2
        exit 1
    fi
    OWN_WORK=1
fi
KEEP_WORK="${GRAITH_LIBGHOSTTY_KEEP_WORK:-0}"
if ! mkdir -p "$NATIVE_WORK" || [[ ! -d "$NATIVE_WORK" || -L "$NATIVE_WORK" ]]; then
    echo "error: native work path is not a regular directory" >&2
    exit 1
fi
if ! NATIVE_WORK="$(cd "$NATIVE_WORK" && pwd -P)" ||
    [[ -z "$NATIVE_WORK" || "$NATIVE_WORK" == "/" ]]; then
    echo "error: could not canonicalize a safe native work directory" >&2
    exit 1
fi

cleanup() {
    if [[ "$OWN_WORK" == "1" && "$KEEP_WORK" != "1" && -d "$NATIVE_WORK" ]]; then
        rm -rf -- "$NATIVE_WORK"
    fi
}
trap cleanup EXIT

die() {
    echo "error: $*" >&2
    return 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

path_has_symlink_component() {
    local path="$1"

    while [[ "$path" != "/" && "$path" != "." ]]; do
        [[ -L "$path" ]] && return 0
        case "$path" in
            */*) path="${path%/*}"; [[ -n "$path" ]] || path="/" ;;
            *) path="." ;;
        esac
    done
    return 1
}

private_directory_is_safe() {
    local directory="${1:-}"
    local expected_parent="${2:-}"
    local expected_prefix="${3:-}"
    local actual_parent actual_name actual_uid actual_mode current_uid

    [[ -n "$directory" && -n "$expected_parent" && -n "$expected_prefix" &&
        -d "$directory" && ! -L "$directory" ]] || return 1
    if ! actual_parent="$(dirname -- "$directory")" || [[ -z "$actual_parent" ]]; then
        return 1
    fi
    if ! actual_parent="$(realpath "$actual_parent")" || [[ -z "$actual_parent" ]]; then
        return 1
    fi
    if ! expected_parent="$(realpath "$expected_parent")" ||
        [[ -z "$expected_parent" || "$actual_parent" != "$expected_parent" ]]; then
        return 1
    fi
    if ! actual_name="$(basename -- "$directory")" ||
        [[ -z "$actual_name" || "$actual_name" != "$expected_prefix"* ]]; then
        return 1
    fi
    if ! current_uid="$(id -u)" || [[ -z "$current_uid" ]]; then
        return 1
    fi
    local host
    if ! host="$(uname -s)" || [[ -z "$host" ]]; then
        return 1
    fi
    if [[ "$host" == "Darwin" ]]; then
        if ! actual_uid="$(stat -f '%u' "$directory")" ||
            ! actual_mode="$(stat -f '%Lp' "$directory")"; then
            return 1
        fi
    else
        if ! actual_uid="$(stat -c '%u' "$directory")" ||
            ! actual_mode="$(stat -c '%a' "$directory")"; then
            return 1
        fi
    fi
    [[ "$actual_uid" == "$current_uid" && "$actual_mode" == "700" ]]
}

cleanup_candidate_staging() {
    local staging="${1:-}"

    if [[ -n "$staging" && -d "$staging" ]]; then
        rm -rf "$staging"
    fi
}

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

sha256_value() {
    local path="$1"
    local host actual

    if ! host="$(uname -s)" || [[ -z "$host" ]]; then
        die "could not determine the hash host"
        return 1
    fi
    if [[ "$host" == "Darwin" ]]; then
        if ! actual="$(shasum -a 256 "$path" | awk '{print $1}')"; then
            die "could not hash $path"
            return 1
        fi
    else
        if ! actual="$(sha256sum "$path" | awk '{print $1}')"; then
            die "could not hash $path"
            return 1
        fi
    fi
    [[ "$actual" =~ ^[0-9a-f]{64}$ ]] || {
        die "hash command returned an invalid SHA-256 for $path"
        return 1
    }
    printf '%s\n' "$actual"
}

verify_dependency_unit() {
    if ! cd "$REPO_DIR"; then
        die "could not enter the repository for dependency verification"
        return 1
    fi
    go run ./internal/libghosttydeps/cmd verify "$REPO_DIR"
}

verify_generated_dependency_unit() {
    cd "$REPO_DIR"
    go run ./internal/libghosttydeps/cmd verify-generated "$REPO_DIR"
}

generate_dependency_unit() {
    cd "$REPO_DIR"
    go run ./internal/libghosttydeps/cmd generate "$REPO_DIR"
}

accept_license_reviews() {
    cd "$REPO_DIR"
    go run ./internal/libghosttydeps/cmd accept-license-reviews "$REPO_DIR"
}

write_pkg_config() (
    local library="${1:-}"
    [[ -f "$library" && ! -L "$library" ]] || {
        die "pkg-config generation requires a regular non-symlink library"
        return 1
    }
    local directory="$NATIVE_WORK/pkgconfig"
    if ! mkdir -p "$directory" || [[ ! -d "$directory" || -L "$directory" ]]; then
        die "could not create a regular native pkg-config directory"
        return 1
    fi
    if path_has_symlink_component "$directory"; then
        die "native pkg-config path traverses a symlink"
        return 1
    fi
    if ! directory="$(realpath "$directory")" || [[ -z "$directory" ]]; then
        die "could not canonicalize the native pkg-config directory"
        return 1
    fi

    local output="$directory/libghostty-vt-static.pc"
    [[ ( ! -e "$output" && ! -L "$output" ) ||
        ( -f "$output" && ! -L "$output" ) ]] || {
        die "native pkg-config output is not absent or a regular file"
        return 1
    }
    local temporary="" published=0 succeeded=0
    # shellcheck disable=SC2317,SC2329
    cleanup_pkg_config() {
        set +e
        if [[ -n "$temporary" && "$temporary" == "$directory"/.libghostty-pkgconfig.* ]]; then
            rm -f -- "$temporary"
        fi
        if [[ "$succeeded" != "1" && "$published" == "1" &&
            ( -f "$output" || -L "$output" ) ]]; then
            rm -f -- "$output"
        fi
    }
    trap cleanup_pkg_config EXIT

    if ! temporary="$(mktemp "$directory/.libghostty-pkgconfig.XXXXXX")" ||
        [[ -z "$temporary" || ! -f "$temporary" || -L "$temporary" ||
            "$temporary" != "$directory"/.libghostty-pkgconfig.* ]]; then
        die "could not create a private pkg-config temporary"
        return 1
    fi
    if ! chmod 600 "$temporary"; then
        die "could not restrict the pkg-config temporary"
        return 1
    fi
    if ! cat >"$temporary" <<EOF
Name: libghostty-vt-static
Description: pinned static libghostty-vt for Graith
Version: $GHOSTTY_SHA
Cflags: -I$REPO_DIR/gui/shared/Sources/CGhosttyVT/include -DGHOSTTY_STATIC
Libs: $library
EOF
    then
        die "could not write native pkg-config metadata"
        return 1
    fi
    local temporary_sha output_sha
    if ! temporary_sha="$(sha256_value "$temporary")" || [[ -z "$temporary_sha" ]]; then
        return 1
    fi
    if [[ -f "$output" && ! -L "$output" ]]; then
        if ! output_sha="$(sha256_value "$output")" || [[ -z "$output_sha" ]]; then
            return 1
        fi
        [[ "$output_sha" == "$temporary_sha" ]] || {
            die "existing pkg-config metadata does not match the requested library"
            return 1
        }
        if ! rm -f -- "$temporary"; then
            die "could not remove the redundant pkg-config temporary"
            return 1
        fi
        temporary=""
        succeeded=1
        printf '%s\n' "$directory"
        return 0
    fi
    if ! mv -n "$temporary" "$output"; then
        if [[ ! -e "$temporary" && -f "$output" && ! -L "$output" ]]; then
            published=1
        fi
        die "could not publish native pkg-config metadata"
        return 1
    fi
    published=1
    [[ ! -e "$temporary" && ! -L "$temporary" && -f "$output" && ! -L "$output" ]] || {
        die "pkg-config publication did not produce the exact output"
        return 1
    }
    if ! output_sha="$(sha256_value "$output")" || [[ -z "$output_sha" ]]; then
        return 1
    fi
    [[ "$output_sha" == "$temporary_sha" ]] || {
        die "published pkg-config metadata differs from the verified temporary"
        return 1
    }
    succeeded=1
    printf '%s\n' "$directory"
)

apple_library() {
    if [[ "$(uname -s)" != "Darwin" ]]; then
        echo "error: the pinned GUI artifact contains Apple slices only; use source-build on Linux" >&2
        return 1
    fi
    if [[ "$(uname -m)" != "arm64" ]]; then
        echo "error: the native libghostty daemon backend supports macOS arm64 only" >&2
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
        echo "error: pinned artifact does not contain the supported macOS arm64 slice" >&2
        return 1
    fi
    printf '%s\n' "$library"
}

verify_metadata() {
    local source="${1:-}"
    local document="${2:-$REPO_DIR/libghostty-native.spdx.json}"
    local notices="${3:-$REPO_DIR/THIRD_PARTY_NOTICES.libghostty.md}"
    local actual_sum
    local actual_version

    verify_dependency_unit
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
    actual_sum="$(go mod download -json go.mitchellh.com/libghostty | jq -r .Sum)"
    if [[ "$actual_sum" != "$GO_LIBGHOSTTY_SUM" ]]; then
        echo "error: go-libghostty module sum is $actual_sum; want $GO_LIBGHOSTTY_SUM" >&2
        return 1
    fi

    if ! jq -e \
        --arg ghostty "$GHOSTTY_SHA" \
        --arg go_libghostty "$GO_LIBGHOSTTY_VERSION" \
        --arg uucode "$UUCODE_VERSION" \
        --arg highway "$HIGHWAY_VERSION+$HIGHWAY_SHA" \
        --arg simdutf "$SIMDUTF_VERSION+$SIMDUTF_UPSTREAM_SHA" \
        --arg simdutf_cpp "$SIMDUTF_CPP_SHA256" \
        --arg simdutf_h "$SIMDUTF_HEADER_SHA256" \
        --arg zig "$REQUIRED_ZIG" \
        --arg zig_sha "$ZIG_SOURCE_SHA256" '
        def package($id): first(.packages[] | select(.SPDXID == $id));
        def has_sha($package; $sha): any($package.checksums[]?;
            .algorithm == "SHA256" and .checksumValue == $sha);
        def relates($from; $type; $to): any(.relationships[];
            .spdxElementId == $from and .relationshipType == $type and .relatedSpdxElement == $to);
        .spdxVersion == "SPDX-2.3" and
        (.packages | length) == 6 and
        (package("SPDXRef-Package-Ghostty").versionInfo | contains($ghostty)) and
        (package("SPDXRef-Package-Ghostty").sourceInfo |
            contains("-Demit-lib-vt=true -Demit-xcframework=true -Doptimize=ReleaseFast")) and
        (package("SPDXRef-Package-Ghostty").sourceInfo | contains("simd=") | not) and
        package("SPDXRef-Package-GoLibghostty").versionInfo == $go_libghostty and
        package("SPDXRef-Package-Uucode").versionInfo == $uucode and
        package("SPDXRef-Package-Highway").versionInfo == $highway and
        package("SPDXRef-Package-Simdutf").versionInfo == $simdutf and
        (package("SPDXRef-Package-Simdutf").sourceInfo | contains($simdutf_cpp)) and
        (package("SPDXRef-Package-Simdutf").sourceInfo | contains($simdutf_h)) and
        package("SPDXRef-Package-ZigRuntime").versionInfo == $zig and
        has_sha(package("SPDXRef-Package-ZigRuntime"); $zig_sha) and
        relates("SPDXRef-Package-Ghostty"; "STATIC_LINK"; "SPDXRef-Package-Simdutf") and
        relates("SPDXRef-Package-Ghostty"; "STATIC_LINK"; "SPDXRef-Package-ZigRuntime")
        ' "$document" >/dev/null; then
        echo "error: native SPDX inventory does not match the pinned dependency unit" >&2
        return 1
    fi

    for required in \
        "$GHOSTTY_SHA" "$GHOSTTY_ARTIFACT_URL" "$GHOSTTY_ARTIFACT_SHA256" \
        "$GO_LIBGHOSTTY_SHA" "$GO_LIBGHOSTTY_SUM" "$UUCODE_HASH" \
        "$HIGHWAY_SHA" "$SIMDUTF_VERSION" "$SIMDUTF_UPSTREAM_SHA" \
        "$SIMDUTF_CPP_SHA256" "$SIMDUTF_HEADER_SHA256" \
        "$REQUIRED_ZIG" "$ZIG_SOURCE_SHA256"; do
        if ! grep -Fq "$required" "$notices"; then
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
    grep -Fq ".version = \"$SIMDUTF_MANIFEST_VERSION\"" "$source/pkg/simdutf/build.zig.zon"
    grep -Fq "#define SIMDUTF_VERSION \"$SIMDUTF_VERSION\"" \
        "$source/pkg/simdutf/vendor/simdutf.h"
    sha256_check "$SIMDUTF_CPP_SHA256" "$source/pkg/simdutf/vendor/simdutf.cpp"
    sha256_check "$SIMDUTF_HEADER_SHA256" "$source/pkg/simdutf/vendor/simdutf.h"
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
                -run 'TestLibghostty|TestProbeUpgrade|TestUpgradeHelperHandoff|TestDiagnostics|TestLogTerminalBackendSelectionFields'
            go test -count=1 -tags=libghostty,libghostty_compare ./internal/pty \
                -run '^TestTerminalBackendCompatibilityCorpus$'
            ;;
        race)
            verify_metadata
            go test -race -count=1 -tags=libghostty ./internal/pty \
                -run 'TestGhostty'
            go test -race -count=1 -tags=libghostty,libghostty_compare ./internal/pty \
                -run '^TestTerminalBackendCompatibilityCorpus$'
            go test -race -count=1 -tags=libghostty ./internal/daemon \
                -run 'TestLibghostty|TestProbeUpgrade|TestUpgradeHelperHandoff|TestDiagnostics|TestLogTerminalBackendSelectionFields'
            ;;
        fuzz)
            verify_metadata
            local fuzztime="${GRAITH_LIBGHOSTTY_FUZZTIME:-}"
            # Fixed budgets avoid the wall-clock deadline path affected by
            # golang/go#75804. Snapshot CI reached 494,054 executions; local
            # request/helper calibration reached 789,507-829,186 and 341-747.
            local snapshot_fuzztime="${fuzztime:-500000x}"
            local request_fuzztime="${fuzztime:-830000x}"
            local helper_fuzztime="${fuzztime:-750x}"
            local parallel="${GRAITH_LIBGHOSTTY_FUZZ_PARALLEL:-4}"
            go test -tags=libghostty ./internal/pty -run '^$' -parallel="$parallel" \
                -fuzz '^FuzzGhosttySnapshotDecoder$' -fuzztime="$snapshot_fuzztime"
            go test -tags=libghostty ./internal/pty -run '^$' -parallel="$parallel" \
                -fuzz '^FuzzGhosttyRequestDecoder$' -fuzztime="$request_fuzztime"
            go test -tags=libghostty ./internal/pty -run '^$' -parallel="$parallel" \
                -fuzz '^FuzzGhosttyHelperWrite$' -fuzztime="$helper_fuzztime"
            ;;
        bench)
            verify_metadata
            GOMAXPROCS="$MEASUREMENT_GOMAXPROCS" \
                go test -run '^$' -tags=libghostty,libghostty_compare ./internal/pty \
                -bench '^BenchmarkTerminalBackends$' -benchmem \
                -benchtime="$BENCHTIME" -count="$BENCH_SAMPLES"
            ;;
        memory)
            verify_metadata
            local charm_test="$NATIVE_WORK/pty-charm.test"
            local ghostty_test="$NATIVE_WORK/pty-libghostty.test"
            local rss_probe="$NATIVE_WORK/pty-current-rss"
            go test -c -o "$charm_test" ./internal/pty
            go test -c -tags=libghostty,libghostty_compare \
                -o "$ghostty_test" ./internal/pty
            go build -o "$rss_probe" ./internal/pty/testdata/currentrss

            local workloads=(
                reconstruct_4MiB_1term
                scroll_12000_1term
                scroll_24000_1term
                scroll_12000_3term
                scroll_24000_3term
            )
            for backend in charm libghostty-helper; do
                local test_binary="$charm_test"
                if [[ "$backend" == "libghostty-helper" ]]; then
                    test_binary="$ghostty_test"
                fi
                local workload
                for workload in "${workloads[@]}"; do
                    local sample
                    for ((sample = 1; sample <= MEMORY_SAMPLES; sample++)); do
                        printf 'backend=%s workload=%s sample=%d/%d\n' \
                            "$backend" "$workload" "$sample" "$MEMORY_SAMPLES"
                        /usr/bin/time -l env GOMAXPROCS="$MEASUREMENT_GOMAXPROCS" \
                            GRAITH_TERMINAL_MEMORY_BACKEND="$backend" \
                            GRAITH_TERMINAL_MEMORY_WORKLOAD="$workload" \
                            GRAITH_TERMINAL_RSS_PROBE="$rss_probe" \
                            "$test_binary" -test.run '^TestTerminalBackendPeakMemoryWorkload$' -test.v
                    done
                done
            done
            ;;
    esac
}

run_daemon_validation() {
    local cycles="${1:-12}"
    local test_pattern="${2:-^TestLibghosttyDaemon}"
    local workload_timeout="${3:-3m}"
    local go_timeout="${4:-5m}"
    local long_soak="${5:-0}"
    local host library pkgconfig_directory
    local binary="$NATIVE_WORK/gr-libghostty-daemon-race"
    local daemon_gocache="${GRAITH_LIBGHOSTTY_GOCACHE:-$NATIVE_WORK/go-cache}"

    if ! host="$(uname -s)" || [[ -z "$host" ]]; then
        die "could not determine the daemon validation host"
        return 1
    fi
    if [[ "$host" == "Darwin" ]]; then
        if ! library="$(apple_library)" || [[ -z "$library" ]]; then
            return 1
        fi
        if ! pkgconfig_directory="$(write_pkg_config "$library")" ||
            [[ -z "$pkgconfig_directory" ]]; then
            return 1
        fi
        PKG_CONFIG_PATH="$pkgconfig_directory${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
        export PKG_CONFIG_PATH
    elif [[ "$host" == "Linux" ]]; then
        if [[ -z "${PKG_CONFIG_PATH:-}" ]] ||
            ! pkg-config --exists libghostty-vt-static; then
            die "Linux daemon validation requires the published libghostty pkg-config path"
            return 1
        fi
    else
        die "native daemon validation is unsupported on $host"
        return 1
    fi
    if ! mkdir -p "$daemon_gocache" ||
        [[ ! -d "$daemon_gocache" || -L "$daemon_gocache" ]]; then
        die "could not create a regular daemon validation cache"
        return 1
    fi

    if ! verify_metadata; then
        return 1
    fi
    if ! cd "$REPO_DIR"; then
        die "could not enter the repository for daemon validation"
        return 1
    fi
    if ! GOCACHE="$daemon_gocache" CGO_ENABLED=1 \
        go build -race -trimpath -tags='libghostty' \
        -o "$binary" ./cmd/graith; then
        return 1
    fi
    if ! GRAITH_LIBGHOSTTY_DAEMON_BINARY="$binary" \
        GRAITH_LIBGHOSTTY_SOAK_CYCLES="$cycles" \
        GRAITH_LIBGHOSTTY_SOAK_TIMEOUT="$workload_timeout" \
        GRAITH_LIBGHOSTTY_LONG_SOAK="$long_soak" \
        GOCACHE="$daemon_gocache" \
        CGO_ENABLED=1 go test -v -race -count=1 -tags='integration libghostty' \
            -timeout="$go_timeout" -run "$test_pattern" ./internal/integration; then
        return 1
    fi
}

require_exact_zig() {
    local actual

    if ! require_command zig; then
        return 1
    fi
    if ! actual="$(zig version)" || [[ -z "$actual" ]]; then
        die "could not determine the Zig version"
        return 1
    fi
    [[ "$actual" == "$REQUIRED_ZIG" ]] || {
        die "Zig $REQUIRED_ZIG is required; found $actual"
        return 1
    }
}

archive_magic_hex() {
    local archive="${1:-}"
    local magic

    [[ -f "$archive" && ! -L "$archive" ]] || {
        die "archive magic requires a regular non-symlink file"
        return 1
    }
    if ! magic="$(od -An -tx1 -N8 "$archive" | tr -d '[:space:]')" ||
        [[ -z "$magic" || ! "$magic" =~ ^[0-9a-f]{16}$ ]]; then
        die "could not read archive magic"
        return 1
    fi
    printf '%s\n' "$magic"
}

expected_static_archive_members() {
    printf '%s\n' \
        abort.o base64.o codepoint_width.o compiler_rt.o index_of.o \
        libghostty-vt-static_zcu.o libhighway_zcu.o per_target.o \
        simdutf.o targets.o vt.o | sort
}

file_identity() {
    local path="${1:-}"
    local host identity

    [[ -f "$path" && ! -L "$path" ]] || {
        die "file identity requires a regular non-symlink file"
        return 1
    }
    if ! host="$(uname -s)" || [[ -z "$host" ]]; then
        die "could not determine the stat host"
        return 1
    fi
    if [[ "$host" == "Darwin" ]]; then
        if ! identity="$(stat -f '%d:%i' "$path")"; then
            die "could not stat $path"
            return 1
        fi
    else
        if ! identity="$(stat -c '%d:%i' "$path")"; then
            die "could not stat $path"
            return 1
        fi
    fi
    [[ "$identity" =~ ^[0-9]+:[0-9]+$ ]] || {
        die "stat returned an invalid file identity for $path"
        return 1
    }
    printf '%s\n' "$identity"
}

canonical_allowed_roots() {
    local root canonical_root

    [[ "$#" -gt 0 ]] || {
        die "at least one archive root is required"
        return 1
    }
    for root in "$@"; do
        [[ -d "$root" && ! -L "$root" ]] || {
            die "archive root is not a regular directory: $root"
            return 1
        }
        if path_has_symlink_component "$root"; then
            die "archive root traverses a symlink: $root"
            return 1
        fi
        if ! canonical_root="$(realpath "$root")" || [[ -z "$canonical_root" ]]; then
            die "could not canonicalize archive root: $root"
            return 1
        fi
        printf '%s\n' "$canonical_root" || return 1
    done
}

resolve_zig_build_archive() {
    local published="${1:-}"
    [[ "$#" -ge 2 && -n "$published" ]] || {
        die "Zig build archive resolution requires a published path and allowed roots"
        return 1
    }
    shift

    local published_parent published_name
    if ! published_parent="$(dirname -- "$published")" || [[ -z "$published_parent" ]]; then
        die "could not determine the Zig build archive parent"
        return 1
    fi
    if ! published_name="$(basename -- "$published")" ||
        [[ -z "$published_name" || "$published_name" == "." ||
            "$published_name" == ".." || "$published_name" =~ [[:cntrl:]] ]]; then
        die "could not determine a safe Zig build archive name"
        return 1
    fi
    [[ -d "$published_parent" && ! -L "$published_parent" ]] || {
        die "Zig build archive parent is not a regular directory"
        return 1
    }
    if path_has_symlink_component "$published_parent"; then
        die "Zig build archive parent traverses a symlink"
        return 1
    fi
    if ! published_parent="$(realpath "$published_parent")" ||
        [[ -z "$published_parent" ]]; then
        die "could not canonicalize the Zig build archive parent"
        return 1
    fi
    published="$published_parent/$published_name"

    local candidate="$published" link_target
    if [[ -L "$published" ]]; then
        if ! link_target="$(readlink "$published")" ||
            [[ -z "$link_target" || "$link_target" =~ [[:cntrl:]] ]]; then
            die "Zig build archive has an unsafe final symlink"
            return 1
        fi
        case "$link_target" in
            /*) candidate="$link_target" ;;
            *) candidate="$published_parent/$link_target" ;;
        esac
        [[ ! -L "$candidate" ]] || {
            die "Zig build archive final symlink points through another symlink"
            return 1
        }
    fi
    [[ -f "$candidate" && ! -L "$candidate" ]] || {
        die "Zig build archive does not resolve one hop to a regular file"
        return 1
    }

    local candidate_parent resolved
    if ! candidate_parent="$(dirname -- "$candidate")" || [[ -z "$candidate_parent" ]]; then
        die "could not determine the Zig build archive target parent"
        return 1
    fi
    if path_has_symlink_component "$candidate_parent"; then
        die "Zig build archive target parent traverses a symlink"
        return 1
    fi
    if ! resolved="$(realpath "$candidate")" || [[ -z "$resolved" ]]; then
        die "could not canonicalize the Zig build archive target"
        return 1
    fi
    [[ -f "$resolved" && ! -L "$resolved" ]] || {
        die "Zig build archive canonical target is not a regular file"
        return 1
    }

    local roots root allowed=0
    if ! roots="$(canonical_allowed_roots "$@")" || [[ -z "$roots" ]]; then
        return 1
    fi
    while IFS= read -r root; do
        case "$resolved" in
            "$root"/*) allowed=1; break ;;
        esac
    done <<<"$roots"
    [[ "$allowed" == "1" ]] || {
        die "Zig build archive target is outside the script-owned roots"
        return 1
    }
    printf '%s\n' "$resolved" || {
        die "could not report the canonical Zig build archive"
        return 1
    }
}

snapshot_zig_build_archive() (
    local published="${1:-}"
    local snapshot="${2:-}"
    shift 2 || true
    [[ -n "$published" && -n "$snapshot" && "$#" -gt 0 ]] || {
        die "Zig build archive snapshot requires input, output, and allowed roots"
        return 1
    }

    local snapshot_parent snapshot_name
    if ! snapshot_parent="$(dirname -- "$snapshot")" || [[ -z "$snapshot_parent" ]]; then
        die "could not determine the Zig archive snapshot parent"
        return 1
    fi
    if ! snapshot_name="$(basename -- "$snapshot")" ||
        [[ -z "$snapshot_name" || "$snapshot_name" == "." ||
            "$snapshot_name" == ".." || "$snapshot_name" =~ [[:cntrl:]] ]]; then
        die "could not determine a safe Zig archive snapshot name"
        return 1
    fi
    [[ -d "$snapshot_parent" && ! -L "$snapshot_parent" ]] || {
        die "Zig archive snapshot requires a regular parent directory"
        return 1
    }
    if path_has_symlink_component "$snapshot_parent"; then
        die "Zig archive snapshot parent traverses a symlink"
        return 1
    fi
    if ! snapshot_parent="$(realpath "$snapshot_parent")" ||
        [[ -z "$snapshot_parent" ]]; then
        die "could not canonicalize the Zig archive snapshot parent"
        return 1
    fi
    snapshot="$snapshot_parent/$snapshot_name"
    [[ ! -e "$snapshot" && ! -L "$snapshot" ]] || {
        die "Zig archive snapshot output already exists"
        return 1
    }

    local temporary="" published_output=0 succeeded=0
    # shellcheck disable=SC2317,SC2329
    cleanup_zig_archive_snapshot() {
        set +e
        if [[ -n "$temporary" && "$temporary" == "$snapshot_parent"/.zig-archive-snapshot.* ]]; then
            rm -f -- "$temporary"
        fi
        if [[ "$succeeded" != "1" && "$published_output" == "1" &&
            ( -f "$snapshot" || -L "$snapshot" ) ]]; then
            rm -f -- "$snapshot"
        fi
    }
    trap cleanup_zig_archive_snapshot EXIT

    if ! temporary="$(mktemp "$snapshot_parent/.zig-archive-snapshot.XXXXXX")" ||
        [[ -z "$temporary" || ! -f "$temporary" || -L "$temporary" ||
            "$temporary" != "$snapshot_parent"/.zig-archive-snapshot.* ]]; then
        die "could not create a private Zig archive snapshot"
        return 1
    fi
    if ! chmod 600 "$temporary"; then
        die "could not restrict the Zig archive snapshot"
        return 1
    fi

    local pre_target post_target pre_identity post_identity pre_sha post_sha temporary_sha magic
    if ! pre_target="$(resolve_zig_build_archive "$published" "$@")" ||
        [[ -z "$pre_target" ]]; then
        return 1
    fi
    if ! pre_identity="$(file_identity "$pre_target")" || [[ -z "$pre_identity" ]]; then
        return 1
    fi
    if ! pre_sha="$(sha256_value "$pre_target")" || [[ -z "$pre_sha" ]]; then
        return 1
    fi
    if ! cp "$pre_target" "$temporary"; then
        die "could not copy the canonical Zig build archive"
        return 1
    fi
    [[ -f "$temporary" && ! -L "$temporary" ]] || {
        die "Zig archive snapshot is not a regular file"
        return 1
    }
    if ! post_target="$(resolve_zig_build_archive "$published" "$@")" ||
        [[ -z "$post_target" ]]; then
        return 1
    fi
    if ! post_identity="$(file_identity "$post_target")" || [[ -z "$post_identity" ]]; then
        return 1
    fi
    if ! post_sha="$(sha256_value "$post_target")" || [[ -z "$post_sha" ]]; then
        return 1
    fi
    if ! temporary_sha="$(sha256_value "$temporary")" || [[ -z "$temporary_sha" ]]; then
        return 1
    fi
    local temporary_identity
    if ! temporary_identity="$(file_identity "$temporary")" || [[ -z "$temporary_identity" ]]; then
        return 1
    fi
    [[ "$pre_target" == "$post_target" && "$pre_identity" == "$post_identity" &&
        "$pre_sha" == "$post_sha" && "$post_sha" == "$temporary_sha" ]] || {
        die "Zig build archive target changed while taking its snapshot"
        return 1
    }
    if ! magic="$(archive_magic_hex "$temporary")" || [[ -z "$magic" ]]; then
        return 1
    fi
    [[ "$magic" == "213c7468696e3e0a" || "$magic" == "213c617263683e0a" ]] || {
        die "Zig source build did not produce a supported static archive"
        return 1
    }

    if ! mv -n "$temporary" "$snapshot"; then
        if [[ ! -e "$temporary" && -f "$snapshot" && ! -L "$snapshot" ]]; then
            published_output=1
        fi
        die "could not publish the Zig archive snapshot"
        return 1
    fi
    published_output=1
    [[ ! -e "$temporary" && ! -L "$temporary" &&
        -f "$snapshot" && ! -L "$snapshot" ]] || {
        die "Zig archive snapshot publication did not produce the exact output"
        return 1
    }
    local published_identity published_sha
    if ! published_identity="$(file_identity "$snapshot")" ||
        [[ -z "$published_identity" || "$published_identity" != "$temporary_identity" ]]; then
        die "published Zig archive snapshot has an unexpected identity"
        return 1
    fi
    if ! published_sha="$(sha256_value "$snapshot")" ||
        [[ -z "$published_sha" || "$published_sha" != "$temporary_sha" ]]; then
        die "published Zig archive snapshot differs from the verified temporary"
        return 1
    fi
    succeeded=1
)

resolve_thin_archive_member() {
    local input="${1:-}"
    local member="${2:-}"
    [[ "$#" -ge 3 ]] || {
        die "thin member resolution requires archive, member, and allowed roots"
        return 1
    }
    shift 2
    [[ -f "$input" && ! -L "$input" && -n "$member" ]] || {
        die "thin member resolution requires a regular archive and member"
        return 1
    }
    local checked="/${member#/}/"
    case "$checked" in
        *//*|*/../*|*/./*)
            die "thin archive member is noncanonical: $member"
            return 1
            ;;
    esac
    [[ ! "$member" =~ [[:cntrl:]] ]] || {
        die "thin archive member contains control characters"
        return 1
    }

    local candidate input_parent
    case "$member" in
        /*) candidate="$member" ;;
        *)
            if ! input_parent="$(dirname -- "$input")" || [[ -z "$input_parent" ]]; then
                die "could not determine the thin archive parent"
                return 1
            fi
            candidate="$input_parent/$member"
            ;;
    esac
    [[ -f "$candidate" && ! -L "$candidate" ]] || {
        die "thin archive member is not a regular non-symlink file: $member"
        return 1
    }
    if path_has_symlink_component "$candidate"; then
        die "thin archive member traverses a symlink: $member"
        return 1
    fi

    local resolved roots root allowed=0
    if ! resolved="$(realpath "$candidate")" || [[ -z "$resolved" ]]; then
        die "could not canonicalize thin archive member: $member"
        return 1
    fi
    if ! roots="$(canonical_allowed_roots "$@")" || [[ -z "$roots" ]]; then
        return 1
    fi
    while IFS= read -r root; do
        case "$resolved" in
            "$root"/*) allowed=1; break ;;
        esac
    done <<<"$roots"
    [[ "$allowed" == "1" ]] || {
        die "thin archive member is outside the script-owned roots: $member"
        return 1
    }
    [[ -s "$resolved" ]] || {
        die "thin archive member is empty: $member"
        return 1
    }

    local name format identity
    if ! name="$(basename -- "$member")" ||
        [[ -z "$name" || ! "$name" =~ ^[A-Za-z0-9._+-]+$ ]]; then
        die "thin archive member has an unsafe normalized name: $member"
        return 1
    fi
    if ! format="$(file -b "$resolved")" || [[ -z "$format" ]]; then
        die "could not inspect thin archive member format: $member"
        return 1
    fi
    [[ "$format" == *ELF*relocatable* ]] || {
        die "thin archive member is not a supported ELF object: $member ($format)"
        return 1
    }
    if ! identity="$(file_identity "$resolved")" || [[ -z "$identity" ]]; then
        return 1
    fi
    printf '%s\t%s\t%s\n' "$name" "$resolved" "$identity" || {
        die "could not report thin archive member evidence: $member"
        return 1
    }
}

verify_static_archive() {
    local library="${1:-}"
    local magic listing actual_members expected_members symbols member format archive_strings

    [[ -f "$library" && ! -L "$library" ]] || {
        die "usage: $0 verify-static-archive <library>"
        return 1
    }
    if ! magic="$(archive_magic_hex "$library")" || [[ -z "$magic" ]]; then
        return 1
    fi
    [[ "$magic" == "213c617263683e0a" ]] || {
        die "static archive verification requires a self-contained regular archive"
        return 1
    }
    if ! listing="$(zig ar t "$library")" || [[ -z "$listing" ]]; then
        die "exact Zig archiver could not list the static archive"
        return 1
    fi
    if ! actual_members="$(printf '%s\n' "$listing" | sed '/^__\.SYMDEF/d' | sort)" ||
        [[ -z "$actual_members" ]]; then
        die "could not normalize static archive members"
        return 1
    fi
    if ! expected_members="$(expected_static_archive_members)" ||
        [[ -z "$expected_members" || "$actual_members" != "$expected_members" ]]; then
        die "static archive contents do not match the audited dependency closure"
        return 1
    fi
    while IFS= read -r member; do
        if ! format="$(zig ar p "$library" "$member" | file -b -)" ||
            [[ -z "$format" || "$format" != *ELF*relocatable* ]]; then
            die "static archive member is not a supported ELF object: $member"
            return 1
        fi
    done <<<"$actual_members"
    if ! symbols="$(nm -g "$library" 2>/dev/null)" || [[ -z "$symbols" ]]; then
        die "could not inspect static archive symbols"
        return 1
    fi
    if ! grep -Eq '[[:space:]][Tt][[:space:]]ghostty_terminal_new$' <<<"$symbols"; then
        die "static archive does not define ghostty_terminal_new"
        return 1
    fi
    if ! grep -Eq '[[:space:]][Tt][[:space:]]_ZN7simdutf' <<<"$symbols"; then
        die "static archive does not contain simdutf"
        return 1
    fi
    if ! grep -Eq '[[:space:]][Tt][[:space:]]_ZN3hwy' <<<"$symbols"; then
        die "static archive does not contain Highway"
        return 1
    fi
    if ! grep -Eq '[[:space:]][TtW][[:space:]]__ubsan_handle_[[:alnum:]_]+$' \
        <<<"$symbols"; then
        die "static archive does not contain the audited Zig UBSan runtime"
        return 1
    fi
    if ! archive_strings="$(strings "$library")" || [[ -z "$archive_strings" ]]; then
        die "could not inspect static archive strings"
        return 1
    fi
    if ! grep -Fq "zig $REQUIRED_ZIG" <<<"$archive_strings"; then
        die "static archive does not identify the required Zig toolchain"
        return 1
    fi
}

materialize_static_archive() (
    local input="${1:-}"
    local output="${2:-}"
    [[ "$#" -ge 3 ]] || {
        die "thin archive materialization requires input, output, and allowed roots"
        return 1
    }
    shift 2
    [[ -f "$input" && ! -L "$input" && -n "$output" ]] || {
        die "thin archive materialization requires a regular input and output"
        return 1
    }
    if ! require_exact_zig; then return 1; fi

    local input_magic
    if ! input_magic="$(archive_magic_hex "$input")" || [[ -z "$input_magic" ]]; then
        return 1
    fi
    [[ "$input_magic" == "213c7468696e3e0a" ||
        "$input_magic" == "213c617263683e0a" ]] || {
        die "archive materialization requires a supported static archive"
        return 1
    }
    if path_has_symlink_component "$input"; then
        die "thin archive input traverses a symlink"
        return 1
    fi

    local output_parent output_name
    if ! output_parent="$(dirname -- "$output")" || [[ -z "$output_parent" ]]; then
        die "could not determine the regular archive output parent"
        return 1
    fi
    if ! output_name="$(basename -- "$output")" ||
        [[ -z "$output_name" || "$output_name" == "." || "$output_name" == ".." ||
            "$output_name" =~ [[:cntrl:]] ]]; then
        die "could not determine a safe regular archive output name"
        return 1
    fi
    [[ -d "$output_parent" && ! -L "$output_parent" ]] || {
        die "regular archive output requires an existing regular parent"
        return 1
    }
    if path_has_symlink_component "$output_parent"; then
        die "regular archive output parent traverses a symlink"
        return 1
    fi
    if ! output_parent="$(realpath "$output_parent")" || [[ -z "$output_parent" ]]; then
        die "could not canonicalize the regular archive output parent"
        return 1
    fi
    output="$output_parent/$output_name"
    [[ ! -e "$output" && ! -L "$output" ]] || {
        die "regular archive output already exists: $output"
        return 1
    }

    local roots
    if ! roots="$(canonical_allowed_roots "$@")" || [[ -z "$roots" ]]; then
        return 1
    fi
    local -a allowed_roots=()
    while IFS= read -r output_name; do allowed_roots+=("$output_name"); done <<<"$roots"

    local staging="" output_temp_directory="" published=0 succeeded=0
    # shellcheck disable=SC2317,SC2329
    cleanup_archive_materialization() {
        set +e
        if [[ -n "$staging" && "$staging" == "$NATIVE_WORK"/regular-archive.* ]]; then
            rm -rf -- "$staging"
        fi
        if [[ -n "$output_temp_directory" &&
            "$output_temp_directory" == "$output_parent"/.libghostty-archive.* ]]; then
            rm -rf -- "$output_temp_directory"
        fi
        if [[ "$succeeded" != "1" && "$published" == "1" &&
            ( -f "$output" || -L "$output" ) ]]; then
            rm -f -- "$output"
        fi
    }
    trap cleanup_archive_materialization EXIT

    if ! staging="$(mktemp -d "$NATIVE_WORK/regular-archive.XXXXXX")" ||
        [[ -z "$staging" ]]; then
        die "could not create a private archive staging directory"
        return 1
    fi
    if ! chmod 700 "$staging" ||
        ! private_directory_is_safe "$staging" "$NATIVE_WORK" "regular-archive."; then
        die "archive staging directory failed private ownership/path validation"
        return 1
    fi

    local listing expected_members actual_members member record name resolved identity
    if ! listing="$(zig ar t "$input")" || [[ -z "$listing" ]]; then
        die "exact Zig archiver could not list the thin archive"
        return 1
    fi
    if ! expected_members="$(expected_static_archive_members)" || [[ -z "$expected_members" ]]; then
        return 1
    fi
    local normalized_names="" normalized_name
    while IFS= read -r member; do
        if ! normalized_name="$(basename -- "$member")" || [[ -z "$normalized_name" ]]; then
            die "could not normalize thin archive member name"
            return 1
        fi
        normalized_names+="${normalized_names:+$'\n'}$normalized_name"
    done <<<"$listing"
    if ! actual_members="$(printf '%s\n' "$normalized_names" | sort)" ||
        [[ -z "$actual_members" ]]; then
        die "could not normalize thin archive member names"
        return 1
    fi
    [[ "$actual_members" == "$expected_members" ]] || {
        die "thin archive does not match the exact audited 11-member closure"
        return 1
    }

    local -a names=() paths=() identities=() hashes=() objects=()
    local pre_sha post_sha staged_sha checked_name checked_path checked_identity format
    while IFS= read -r member; do
        if [[ "$input_magic" == "213c7468696e3e0a" ]]; then
            if ! record="$(resolve_thin_archive_member "$input" "$member" \
                "${allowed_roots[@]}")" || [[ -z "$record" ]]; then
                return 1
            fi
            if ! IFS=$'\t' read -r name resolved identity <<<"$record" ||
                [[ -z "$name" || -z "$resolved" || -z "$identity" ]]; then
                die "thin archive member evidence is incomplete"
                return 1
            fi
        else
            if ! name="$(basename -- "$member")" ||
                [[ -z "$name" || ! "$name" =~ ^[A-Za-z0-9._+-]+$ ||
                    "$member" =~ [[:cntrl:]] ]]; then
                die "regular build archive contains an unsafe member name"
                return 1
            fi
            resolved=""
            identity=""
        fi
        case " ${names[*]} " in *" $name "*)
            die "thin archive members collide after path normalization: $name"
            return 1 ;;
        esac
        if [[ "$input_magic" == "213c7468696e3e0a" ]]; then
            if ! pre_sha="$(sha256_value "$resolved")" || [[ -z "$pre_sha" ]]; then
                return 1
            fi
            if ! cp "$resolved" "$staging/$name" ||
                [[ ! -f "$staging/$name" || -L "$staging/$name" ]]; then
                die "could not stage thin archive member: $name"
                return 1
            fi
            if ! record="$(resolve_thin_archive_member "$input" "$member" \
                "${allowed_roots[@]}")" || [[ -z "$record" ]]; then
                return 1
            fi
            if ! IFS=$'\t' read -r checked_name checked_path checked_identity <<<"$record" ||
                [[ "$checked_name" != "$name" || "$checked_path" != "$resolved" ||
                    "$checked_identity" != "$identity" ]]; then
                die "thin archive member identity changed while staging: $name"
                return 1
            fi
            if ! post_sha="$(sha256_value "$resolved")" ||
                ! staged_sha="$(sha256_value "$staging/$name")" ||
                [[ -z "$post_sha" || -z "$staged_sha" || "$pre_sha" != "$post_sha" ||
                    "$post_sha" != "$staged_sha" ]]; then
                die "thin archive member changed while staging: $name"
                return 1
            fi
        else
            if ! zig ar p "$input" "$member" >"$staging/$name" ||
                [[ ! -s "$staging/$name" || -L "$staging/$name" ]]; then
                die "could not extract regular build archive member: $name"
                return 1
            fi
            if ! format="$(file -b "$staging/$name")" ||
                [[ -z "$format" || "$format" != *ELF*relocatable* ]]; then
                die "regular build archive member is not a supported ELF object: $name"
                return 1
            fi
            if ! pre_sha="$(sha256_value "$staging/$name")" || [[ -z "$pre_sha" ]]; then
                return 1
            fi
        fi
        names+=("$name")
        paths+=("$resolved")
        identities+=("$identity")
        hashes+=("$pre_sha")
    done <<<"$listing"

    while IFS= read -r name; do objects+=("$staging/$name"); done <<<"$expected_members"
    if ! output_temp_directory="$(mktemp -d \
        "$output_parent/.libghostty-archive.XXXXXX")" ||
        [[ -z "$output_temp_directory" ]]; then
        die "could not create a private regular archive output directory"
        return 1
    fi
    if ! chmod 700 "$output_temp_directory" ||
        ! private_directory_is_safe "$output_temp_directory" "$output_parent" \
            ".libghostty-archive."; then
        die "regular archive output directory failed private ownership/path validation"
        return 1
    fi

    local temporary="$output_temp_directory/libghostty-vt.a"
    if ! zig ar rcsD "$temporary" "${objects[@]}"; then
        die "exact Zig archiver could not create the regular archive"
        return 1
    fi
    [[ -f "$temporary" && ! -L "$temporary" ]] || {
        die "exact Zig archiver did not create a regular archive"
        return 1
    }
    local regular_magic
    if ! regular_magic="$(archive_magic_hex "$temporary")" ||
        [[ -z "$regular_magic" || "$regular_magic" != "213c617263683e0a" ]]; then
        die "materialized archive is not a self-contained regular archive"
        return 1
    fi
    if ! actual_members="$(zig ar t "$temporary")" ||
        [[ -z "$actual_members" || "$actual_members" != "$expected_members" ]]; then
        die "regular archive member names differ from the audited thin input"
        return 1
    fi

    local index extracted actual_sha current_identity current_sha
    for index in "${!names[@]}"; do
        name="${names[$index]}"
        if [[ "$input_magic" == "213c7468696e3e0a" ]]; then
            if ! current_identity="$(file_identity "${paths[$index]}")" ||
                ! current_sha="$(sha256_value "${paths[$index]}")" ||
                ! staged_sha="$(sha256_value "$staging/$name")" ||
                [[ -z "$current_identity" || -z "$current_sha" || -z "$staged_sha" ||
                    "$current_identity" != "${identities[$index]}" ||
                    "$current_sha" != "${hashes[$index]}" ||
                    "$staged_sha" != "${hashes[$index]}" ]]; then
                die "thin archive source or staged bytes changed before publication: $name"
                return 1
            fi
        elif ! staged_sha="$(sha256_value "$staging/$name")" ||
            [[ -z "$staged_sha" || "$staged_sha" != "${hashes[$index]}" ]]; then
            die "staged regular build archive member changed before publication: $name"
            return 1
        fi
        extracted="$staging/extracted-$name"
        if ! zig ar p "$temporary" "$name" >"$extracted" ||
            [[ ! -f "$extracted" || -L "$extracted" ]]; then
            die "exact Zig archiver could not extract $name"
            return 1
        fi
        if ! actual_sha="$(sha256_value "$extracted")" ||
            [[ -z "$actual_sha" || "$actual_sha" != "${hashes[$index]}" ]]; then
            die "regular archive changed the bytes for $name"
            return 1
        fi
    done

    if ! "$REPO_DIR/scripts/libghostty-native.sh" verify-static-archive "$temporary"; then
        die "materialized regular archive failed final verification"
        return 1
    fi
    local temporary_identity temporary_sha published_identity published_sha
    if ! temporary_identity="$(file_identity "$temporary")" ||
        ! temporary_sha="$(sha256_value "$temporary")" ||
        [[ -z "$temporary_identity" || -z "$temporary_sha" ]]; then
        return 1
    fi
    if ! mv -n "$temporary" "$output"; then
        if [[ ! -e "$temporary" && -f "$output" && ! -L "$output" ]]; then published=1; fi
        die "could not atomically publish the verified regular archive"
        return 1
    fi
    published=1
    [[ ! -e "$temporary" && ! -L "$temporary" && -f "$output" && ! -L "$output" ]] || {
        die "regular archive publication did not create the exact output file"
        return 1
    }
    if ! published_identity="$(file_identity "$output")" ||
        ! published_sha="$(sha256_value "$output")" ||
        [[ -z "$published_identity" || -z "$published_sha" ||
            "$published_identity" != "$temporary_identity" ||
            "$published_sha" != "$temporary_sha" ]]; then
        die "published regular archive differs from the verified temporary"
        return 1
    fi
    succeeded=1
)

finalize_source_build_archive() (
    local built_library="${1:-}"
    local output="${2:-}"
    local source="${3:-}"
    local local_cache="${4:-}"
    local global_cache="${5:-}"
    [[ -n "$built_library" && -n "$output" && -d "$source" &&
        -d "$local_cache" && -d "$global_cache" ]] || {
        die "source archive finalization requires build output, destination, and roots"
        return 1
    }
    [[ -f "$built_library" || -L "$built_library" ]] || {
        die "Ghostty build did not produce $built_library"
        return 1
    }

    local output_parent output_name
    if ! output_parent="$(dirname -- "$output")" || [[ -z "$output_parent" ]]; then
        die "could not determine the source archive destination parent"
        return 1
    fi
    if ! output_name="$(basename -- "$output")" ||
        [[ -z "$output_name" || "$output_name" == "." || "$output_name" == ".." ||
            "$output_name" =~ [[:cntrl:]] ]]; then
        die "could not determine a safe source archive destination name"
        return 1
    fi
    [[ -d "$output_parent" && ! -L "$output_parent" ]] || {
        die "source archive destination requires an existing regular parent"
        return 1
    }
    if path_has_symlink_component "$output_parent"; then
        die "source archive destination parent traverses a symlink"
        return 1
    fi
    if ! output_parent="$(realpath "$output_parent")" || [[ -z "$output_parent" ]]; then
        die "could not canonicalize the source archive destination parent"
        return 1
    fi
    output="$output_parent/$output_name"
    [[ ! -e "$output" && ! -L "$output" ]] || {
        die "source archive destination already exists"
        return 1
    }

    local snapshot_directory="" output_created=0 pkgconfig_created=0 succeeded=0
    local pkgconfig="$NATIVE_WORK/pkgconfig/libghostty-vt-static.pc"
    [[ ! -e "$pkgconfig" && ! -L "$pkgconfig" ]] || {
        die "source finalization requires absent pkg-config metadata"
        return 1
    }
    # shellcheck disable=SC2317,SC2329
    cleanup_source_archive_finalization() {
        set +e
        if [[ -n "$snapshot_directory" &&
            "$snapshot_directory" == "$NATIVE_WORK"/source-archive.* ]]; then
            rm -rf -- "$snapshot_directory"
        fi
        if [[ "$succeeded" != "1" ]]; then
            if [[ "$output_created" == "1" && ( -f "$output" || -L "$output" ) ]]; then
                rm -f -- "$output"
            fi
            if [[ "$pkgconfig_created" == "1" && ( -f "$pkgconfig" || -L "$pkgconfig" ) ]]; then
                rm -f -- "$pkgconfig"
            fi
        fi
    }
    trap cleanup_source_archive_finalization EXIT

    if ! snapshot_directory="$(mktemp -d "$NATIVE_WORK/source-archive.XXXXXX")" ||
        [[ -z "$snapshot_directory" ]]; then
        die "could not create a private source archive snapshot directory"
        return 1
    fi
    if ! chmod 700 "$snapshot_directory" ||
        ! private_directory_is_safe "$snapshot_directory" "$NATIVE_WORK" \
            "source-archive."; then
        die "source finalizer directory failed private ownership/path validation"
        return 1
    fi
    local snapshot="$snapshot_directory/libghostty-vt.a"
    if ! snapshot_zig_build_archive "$built_library" "$snapshot" \
        "$source" "$local_cache" "$global_cache"; then
        return 1
    fi
    if ! materialize_static_archive "$snapshot" "$output" \
        "$source" "$local_cache" "$global_cache"; then
        if [[ -f "$output" || -L "$output" ]]; then output_created=1; fi
        return 1
    fi
    output_created=1
    if ! "$REPO_DIR/scripts/libghostty-native.sh" verify-static-archive "$output"; then
        return 1
    fi
    if ! write_pkg_config "$output" >/dev/null; then
        return 1
    fi
    pkgconfig_created=1

    local final_sha
    if ! final_sha="$(sha256_value "$output")" || [[ -z "$final_sha" ]]; then
        die "could not re-hash the published source archive"
        return 1
    fi
    [[ -f "$pkgconfig" && ! -L "$pkgconfig" ]] || {
        die "source finalization did not publish pkg-config metadata"
        return 1
    }
    succeeded=1
)

assert_source_archive_failure_clean() {
    local output="${1:-}"
    local root="${2:-}"
    local residue

    [[ -n "$output" && -d "$root" ]] || return 1
    if [[ -e "$output" || -L "$output" ]]; then
        die "failed source archive operation published output"
        return 1
    fi
    if ! residue="$(find "$NATIVE_WORK" \
        \( -name 'regular-archive.*' -o -name 'source-archive.*' -o \
        -name '.zig-archive-snapshot.*' -o -name '.libghostty-pkgconfig.*' \) \
        -print -quit)"; then
        die "could not inspect native-work cleanup residue"
        return 1
    fi
    if [[ -n "$residue" ]]; then
        die "failed source archive operation left native-work residue"
        return 1
    fi
    if ! residue="$(find "$root" -name '.libghostty-archive.*' -print -quit)"; then
        die "could not inspect publication cleanup residue"
        return 1
    fi
    if [[ -n "$residue" ]]; then
        die "failed source archive operation left publication residue"
        return 1
    fi
}

# The single-quoted shim bodies are intentionally expanded by the generated
# scripts, not while this test constructs them.
# shellcheck disable=SC2016
test_source_archive_policy() {
    local host
    if ! host="$(uname -s)" || [[ -z "$host" ]]; then
        die "could not determine the source archive policy test host"
        return 1
    fi
    [[ "$host" == "Linux" ]] || return 0
    if ! require_exact_zig || ! require_command cc; then return 1; fi

    local root="$NATIVE_WORK/source-archive-policy-test"
    if ! mkdir -p "$root" || [[ ! -d "$root" || -L "$root" ]]; then
        die "could not create source archive policy test directory"
        return 1
    fi
    if [[ -n "$(find "$root" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
        die "source archive policy test directory is not empty"
        return 1
    fi
    local source_root="$root/source" local_cache="$root/zig-local"
    local global_cache="$root/zig-global" output_root="$root/output"
    if ! mkdir -p "$source_root" "$local_cache" "$global_cache" "$output_root"; then
        return 1
    fi

    local members name source identifier
    if ! members="$(expected_static_archive_members)" || [[ -z "$members" ]]; then
        return 1
    fi
    local -a objects=()
    while IFS= read -r name; do
        source="$source_root/${name%.o}.c"
        case "$name" in
            libghostty-vt-static_zcu.o)
                printf '%s\n' \
                    'void *ghostty_terminal_new(void) { return 0; }' \
                    'const char graith_zig_version[] = "zig 0.15.2";' >"$source"
                ;;
            simdutf.o)
                printf '%s\n' \
                    'void simdutf_braw(void) __asm__("_ZN7simdutf4brawEv");' \
                    'void simdutf_braw(void) {}' >"$source"
                ;;
            libhighway_zcu.o)
                printf '%s\n' \
                    'void highway_braw(void) __asm__("_ZN3hwy4brawEv");' \
                    'void highway_braw(void) {}' >"$source"
                ;;
            compiler_rt.o)
                printf '%s\n' 'void __ubsan_handle_braw(void) {}' >"$source"
                ;;
            *)
                identifier="${name//[^A-Za-z0-9]/_}"
                printf 'void braw_%s(void) {}\n' "$identifier" >"$source"
                ;;
        esac
        if ! cc -c -o "$local_cache/$name" "$source"; then return 1; fi
        objects+=("$local_cache/$name")
    done <<<"$members"

    local thin="$local_cache/libghostty-vt-thin.a"
    if ! zig ar rcsT "$thin" "${objects[@]}"; then return 1; fi
    local published_dir="$source_root/zig-out/lib"
    if ! mkdir -p "$published_dir" ||
        ! ln -s ../../../zig-local/libghostty-vt-thin.a \
            "$published_dir/libghostty-vt.a"; then
        return 1
    fi
    local published="$published_dir/libghostty-vt.a"

    local snapshot="$output_root/snapshot.a" regular="$output_root/regular.a"
    if ! snapshot_zig_build_archive "$published" "$snapshot" \
        "$source_root" "$local_cache" "$global_cache"; then
        return 1
    fi
    if ! materialize_static_archive "$snapshot" "$regular" \
        "$source_root" "$local_cache" "$global_cache"; then
        return 1
    fi
    if ! verify_static_archive "$regular"; then return 1; fi
    local regular_sha
    if ! regular_sha="$(sha256_value "$regular")" || [[ -z "$regular_sha" ]]; then
        return 1
    fi
    local second="$output_root/regular-second.a"
    if ! materialize_static_archive "$snapshot" "$second" \
        "$source_root" "$local_cache" "$global_cache" ||
        [[ "$(sha256_value "$second")" != "$regular_sha" ]]; then
        die "regular archive materialization is not deterministic"
        return 1
    fi

    local shim_dir="$root/shims" original_path="$PATH" system_command
    if ! mkdir -p "$shim_dir"; then return 1; fi
    make_failure_shim() {
        local command_name="$1"
        if ! system_command="$(PATH="$original_path" command -v "$command_name")" ||
            [[ -z "$system_command" ]]; then
            return 1
        fi
        printf '%s\n' \
            '#!/usr/bin/env bash' \
            'exit 1' >"$shim_dir/$command_name"
        chmod 700 "$shim_dir/$command_name"
        printf '%s\n' "$system_command"
    }
    make_delegating_shim() {
        local command_name="$1" body="$2"
        if ! system_command="$(PATH="$original_path" command -v "$command_name")" ||
            [[ -z "$system_command" ]]; then
            return 1
        fi
        printf '%s\n' \
            '#!/usr/bin/env bash' \
            'set -euo pipefail' \
            "$body" \
            'exec "$GRAITH_TEST_SYSTEM_COMMAND" "$@"' >"$shim_dir/$command_name"
        chmod 700 "$shim_dir/$command_name"
        printf '%s\n' "$system_command"
    }
    clear_shims() {
        find "$shim_dir" -mindepth 1 -maxdepth 1 -type f -delete
    }

    local failure output command_name
    for command_name in dirname basename realpath stat sha256sum od mktemp cp; do
        clear_shims
        if ! system_command="$(make_failure_shim "$command_name")"; then return 1; fi
        output="$output_root/snapshot-$command_name.a"
        if GRAITH_TEST_SYSTEM_COMMAND="$system_command" PATH="$shim_dir:$original_path" \
            snapshot_zig_build_archive "$published" "$output" \
            "$source_root" "$local_cache" "$global_cache" >/dev/null 2>&1; then
            die "snapshot accepted injected $command_name failure"
            return 1
        fi
        assert_source_archive_failure_clean "$output" "$root" || return 1
    done

    clear_shims
    system_command="$(make_delegating_shim stat \
        'last="${!#}"; if [[ "$last" == "$GRAITH_TEST_FAIL_PATH" ]]; then exit 1; fi')"
    output="$output_root/snapshot-post-publish.a"
    if GRAITH_TEST_FAIL_PATH="$output" GRAITH_TEST_SYSTEM_COMMAND="$system_command" \
        PATH="$shim_dir:$original_path" snapshot_zig_build_archive \
        "$published" "$output" "$source_root" "$local_cache" "$global_cache" \
        >/dev/null 2>&1; then
        die "snapshot retained output after a post-publication stat failure"
        return 1
    fi
    assert_source_archive_failure_clean "$output" "$root" || return 1

    for failure in fail noop move_then_fail; do
        clear_shims
        case "$failure" in
            fail) system_command="$(make_failure_shim mv)" ;;
            noop)
                system_command="$(make_delegating_shim mv 'exit 0')"
                ;;
            move_then_fail)
                system_command="$(make_delegating_shim mv \
                    '"$GRAITH_TEST_SYSTEM_COMMAND" "$@"; exit 1')"
                ;;
        esac
        output="$output_root/snapshot-mv-$failure.a"
        if GRAITH_TEST_SYSTEM_COMMAND="$system_command" PATH="$shim_dir:$original_path" \
            snapshot_zig_build_archive "$published" "$output" \
            "$source_root" "$local_cache" "$global_cache" >/dev/null 2>&1; then
            die "snapshot accepted injected final move $failure"
            return 1
        fi
        assert_source_archive_failure_clean "$output" "$root" || return 1
    done

    for command_name in file mktemp cp; do
        clear_shims
        system_command="$(make_failure_shim "$command_name")"
        output="$output_root/materialize-$command_name.a"
        if GRAITH_TEST_SYSTEM_COMMAND="$system_command" PATH="$shim_dir:$original_path" \
            materialize_static_archive "$snapshot" "$output" \
            "$source_root" "$local_cache" "$global_cache" >/dev/null 2>&1; then
            die "materializer accepted injected $command_name failure"
            return 1
        fi
        assert_source_archive_failure_clean "$output" "$root" || return 1
    done

    for failure in t rcsD p; do
        clear_shims
        system_command="$(make_delegating_shim zig \
            'if [[ "${1:-}" == "ar" && "${2:-}" == "$GRAITH_TEST_ZIG_OPERATION" ]]; then exit 1; fi')"
        output="$output_root/materialize-zig-$failure.a"
        if GRAITH_TEST_ZIG_OPERATION="$failure" GRAITH_TEST_SYSTEM_COMMAND="$system_command" \
            PATH="$shim_dir:$original_path" materialize_static_archive "$snapshot" \
            "$output" "$source_root" "$local_cache" "$global_cache" \
            >/dev/null 2>&1; then
            die "materializer accepted injected Zig ar $failure failure"
            return 1
        fi
        assert_source_archive_failure_clean "$output" "$root" || return 1
    done

    clear_shims
    system_command="$(make_failure_shim nm)"
    output="$output_root/materialize-verifier.a"
    if GRAITH_TEST_SYSTEM_COMMAND="$system_command" PATH="$shim_dir:$original_path" \
        materialize_static_archive "$snapshot" "$output" \
        "$source_root" "$local_cache" "$global_cache" >/dev/null 2>&1; then
        die "materializer accepted injected final verifier failure"
        return 1
    fi
    assert_source_archive_failure_clean "$output" "$root" || return 1

    for failure in fail noop move_then_fail; do
        clear_shims
        case "$failure" in
            fail) system_command="$(make_failure_shim mv)" ;;
            noop) system_command="$(make_delegating_shim mv 'exit 0')" ;;
            move_then_fail)
                system_command="$(make_delegating_shim mv \
                    '"$GRAITH_TEST_SYSTEM_COMMAND" "$@"; exit 1')"
                ;;
        esac
        output="$output_root/materialize-mv-$failure.a"
        if GRAITH_TEST_SYSTEM_COMMAND="$system_command" PATH="$shim_dir:$original_path" \
            materialize_static_archive "$snapshot" "$output" \
            "$source_root" "$local_cache" "$global_cache" >/dev/null 2>&1; then
            die "materializer accepted injected final move $failure"
            return 1
        fi
        assert_source_archive_failure_clean "$output" "$root" || return 1
    done

    if ! rm -f "$NATIVE_WORK/pkgconfig/libghostty-vt-static.pc"; then return 1; fi
    clear_shims
    system_command="$(make_delegating_shim sha256sum \
        'last="${!#}"; if [[ "$last" == "$GRAITH_TEST_FAIL_PATH" ]]; then exit 1; fi')"
    local pc="$NATIVE_WORK/pkgconfig/libghostty-vt-static.pc"
    if GRAITH_TEST_FAIL_PATH="$pc" GRAITH_TEST_SYSTEM_COMMAND="$system_command" \
        PATH="$shim_dir:$original_path" write_pkg_config "$regular" >/dev/null 2>&1; then
        die "pkg-config writer accepted a post-publication hash failure"
        return 1
    fi
    [[ ! -e "$pc" && ! -L "$pc" ]] || {
        die "failed pkg-config publication retained its output"
        return 1
    }
    assert_source_archive_failure_clean "$output_root/never-created.a" "$root" || return 1

    for failure in fail noop move_then_fail; do
        clear_shims
        case "$failure" in
            fail) system_command="$(make_failure_shim mv)" ;;
            noop) system_command="$(make_delegating_shim mv 'exit 0')" ;;
            move_then_fail)
                system_command="$(make_delegating_shim mv \
                    '"$GRAITH_TEST_SYSTEM_COMMAND" "$@"; exit 1')"
                ;;
        esac
        if GRAITH_TEST_SYSTEM_COMMAND="$system_command" PATH="$shim_dir:$original_path" \
            write_pkg_config "$regular" >/dev/null 2>&1; then
            die "pkg-config writer accepted injected final move $failure"
            return 1
        fi
        [[ ! -e "$pc" && ! -L "$pc" ]] || {
            die "failed pkg-config final move retained its output"
            return 1
        }
        assert_source_archive_failure_clean \
            "$output_root/never-created.a" "$root" || return 1
    done

    clear_shims
    system_command="$(make_delegating_shim stat \
        'last="${!#}"; if [[ "$last" == "$GRAITH_TEST_FAIL_PATH"* ]]; then printf "0\n"; exit 0; fi')"
    output="$output_root/finalizer-private-dir.a"
    if GRAITH_TEST_FAIL_PATH="$NATIVE_WORK/source-archive." \
        GRAITH_TEST_SYSTEM_COMMAND="$system_command" PATH="$shim_dir:$original_path" \
        finalize_source_build_archive "$published" "$output" "$source_root" \
        "$local_cache" "$global_cache" >/dev/null 2>&1; then
        die "finalizer accepted invalid private-directory evidence"
        return 1
    fi
    assert_source_archive_failure_clean "$output" "$root" || return 1

    clear_shims
    system_command="$(make_delegating_shim sha256sum \
        'last="${!#}"; if [[ "$last" == "$GRAITH_TEST_FAIL_PATH" && -f "$GRAITH_TEST_PC" ]]; then exit 1; fi')"
    output="$output_root/finalizer-after-pc.a"
    if GRAITH_TEST_FAIL_PATH="$output" GRAITH_TEST_PC="$pc" \
        GRAITH_TEST_SYSTEM_COMMAND="$system_command" PATH="$shim_dir:$original_path" \
        finalize_source_build_archive "$published" "$output" "$source_root" \
        "$local_cache" "$global_cache" >/dev/null 2>&1; then
        die "finalizer accepted failure after pkg-config publication"
        return 1
    fi
    [[ ! -e "$pc" && ! -L "$pc" ]] || {
        die "finalizer retained pkg-config metadata after later failure"
        return 1
    }
    assert_source_archive_failure_clean "$output" "$root" || return 1

    clear_shims
    printf '%s\n' \
        'void *ghostty_terminal_new(void);' \
        'int main(void) { return ghostty_terminal_new() == 0 ? 0 : 1; }' \
        >"$source_root/link.c"
    if ! mv "$local_cache" "$root/removed-zig-local"; then return 1; fi
    if cc -o "$output_root/linked" "$source_root/link.c" "$regular"; then
        "$output_root/linked"
    else
        die "materialized archive did not link after deleting thin members"
        return 1
    fi
}

source_build() {
    local target="${1:-}"
    local output="${2:-}"
    if [[ -z "$target" || -z "$output" ]]; then
        echo "usage: $0 source-build <zig-target> <output-library>" >&2
        return 2
    fi
    if ! require_exact_zig; then
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
    local checkout_sha
    if ! checkout_sha="$(git -C "$source" rev-parse HEAD)" ||
        [[ -z "$checkout_sha" || "$checkout_sha" != "$GHOSTTY_SHA" ]]; then
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
    if [[ ! -f "$library" && ! -L "$library" ]]; then
        echo "error: Ghostty build did not produce $library" >&2
        return 1
    fi
    local output_parent
    if ! output_parent="$(dirname -- "$output")" || [[ -z "$output_parent" ]]; then
        die "could not determine the source build output parent"
        return 1
    fi
    if ! mkdir -p "$output_parent" || [[ ! -d "$output_parent" || -L "$output_parent" ]]; then
        die "could not create a regular source build output parent"
        return 1
    fi
    finalize_source_build_archive "$library" "$output" \
        "$source" "$NATIVE_WORK/zig-local" "$NATIVE_WORK/zig-global"
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

verify_default_binary() {
    local binary="${1:-}"
    local build_info

    if [[ ! -f "$binary" ]]; then
        echo "usage: $0 verify-default-binary <binary>" >&2
        return 2
    fi

    build_info="$(go version -m "$binary")"
    if grep -Fq 'go.mitchellh.com/libghostty' <<<"$build_info"; then
        echo "error: ordinary rollback binary contains go-libghostty" >&2
        return 1
    fi
    if grep -Fq 'tags=libghostty' <<<"$build_info"; then
        echo "error: ordinary rollback binary contains the libghostty build tag" >&2
        return 1
    fi
    if grep -Fq 'ghostty_terminal_new' < <(strings "$binary"); then
        echo "error: ordinary rollback binary contains a native Ghostty symbol" >&2
        return 1
    fi
}

candidate_revision() {
    local revision

    cd "$REPO_DIR"
    if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
        echo "error: candidate packaging requires a clean Git worktree" >&2
        return 1
    fi

    revision="$(git rev-parse HEAD)"
    if [[ ! "$revision" =~ ^[0-9a-f]{40}$ ]]; then
        echo "error: candidate source is not at a full Git revision" >&2
        return 1
    fi
    printf '%s\n' "$revision"
}

verify_darwin_native_linkage() {
    local binary="${1:-}"

    if [[ ! -f "$binary" ]]; then
        echo "usage: $0 verify-darwin-native-linkage <binary>" >&2
        return 2
    fi
    if ! lipo "$binary" -verify_arch arm64; then
        echo "error: native candidate is not a macOS arm64 binary" >&2
        return 1
    fi
    if ! nm "$binary" | awk '
        NF >= 3 && $NF == "_ghostty_terminal_new" && $(NF - 1) !~ /^[Uu]$/ { found = 1 }
        END { exit !found }
    '; then
        echo "error: candidate does not define ghostty_terminal_new" >&2
        return 1
    fi
    if grep -Eqi '(lib)?ghostty[^[:space:]]*\.dylib' < <(otool -L "$binary"); then
        echo "error: candidate dynamically links a Ghostty library" >&2
        return 1
    fi
}

test_darwin_linkage_policy() {
    local root

    if [[ "$(uname -s)-$(uname -m)" != "Darwin-arm64" ]]; then
        echo "error: Darwin linkage policy tests require macOS arm64" >&2
        return 1
    fi

    root="$(mktemp -d "$NATIVE_WORK/linkage-policy.XXXXXX")"
    cat >"$root/defined.c" <<'EOF'
void *ghostty_terminal_new(void) { return 0; }
int main(void) { return ghostty_terminal_new() != 0; }
EOF
    clang -arch arm64 -o "$root/defined" "$root/defined.c"
    verify_darwin_native_linkage "$root/defined"

    cat >"$root/undefined.c" <<'EOF'
extern void *ghostty_terminal_new(void);
void *use_ghostty(void) { return ghostty_terminal_new(); }
EOF
    clang -arch arm64 -dynamiclib -undefined dynamic_lookup \
        -o "$root/undefined.dylib" "$root/undefined.c"
    if verify_darwin_native_linkage "$root/undefined.dylib" >/dev/null 2>&1; then
        echo "error: linkage policy accepted an undefined Ghostty symbol" >&2
        return 1
    fi

    cat >"$root/dependency.c" <<'EOF'
int ghostty_fixture_dependency(void) { return 0; }
EOF
    clang -arch arm64 -dynamiclib \
        -install_name @rpath/libghostty-vt.dylib \
        -o "$root/libghostty-vt.dylib" "$root/dependency.c"
    cat >"$root/dynamic.c" <<'EOF'
extern int ghostty_fixture_dependency(void);
void *ghostty_terminal_new(void) { return 0; }
int main(void) { return ghostty_fixture_dependency(); }
EOF
    clang -arch arm64 -L"$root" -lghostty-vt \
        -o "$root/dynamic" "$root/dynamic.c"
    if verify_darwin_native_linkage "$root/dynamic" >/dev/null 2>&1; then
        echo "error: linkage policy accepted a Ghostty dylib dependency" >&2
        return 1
    fi

    rm -rf "$root"
}

test_metadata_policy() {
    local root
    local invalid

    root="$(mktemp -d "$NATIVE_WORK/metadata-policy.XXXXXX")"
    invalid="$root/invalid.spdx.json"
    verify_metadata
    jq '
        (.packages[] | select(.SPDXID == "SPDXRef-Package-Ghostty") | .sourceInfo) |=
            sub("-Demit-xcframework=true"; "-Demit-xcframework=false")
        ' "$REPO_DIR/libghostty-native.spdx.json" >"$invalid"
    if verify_metadata "" "$invalid" \
        "$REPO_DIR/THIRD_PARTY_NOTICES.libghostty.md" >/dev/null 2>&1; then
        echo "error: metadata policy accepted the wrong Apple build configuration" >&2
        return 1
    fi
    rm -rf "$root"
}

candidate_identity() {
    local binary="${1:-}"
    local expected_revision="${2:-}"
    local build_info
    local runtime_revision

    if [[ ! -f "$binary" || ! "$expected_revision" =~ ^[0-9a-f]{40}$ ]]; then
        echo "usage: $0 verify-darwin-arm64-candidate <binary> <revision>" >&2
        return 2
    fi

    build_info="$(go version -m "$binary")"
    for required in \
        $'\tdep\tgo.mitchellh.com/libghostty\t'"$GO_LIBGHOSTTY_VERSION"$'\t'"$GO_LIBGHOSTTY_SUM" \
        $'\tbuild\t-tags=libghostty' \
        $'\tbuild\tCGO_ENABLED=1' \
        $'\tbuild\tGOARCH=arm64' \
        $'\tbuild\tGOOS=darwin'; do
        if ! grep -Fq "$required" <<<"$build_info"; then
            echo "error: candidate build metadata is missing $required" >&2
            return 1
        fi
    done
    if grep -Fq $'\tdep\tgithub.com/charmbracelet/x/vt\t' <<<"$build_info"; then
        echo "error: native candidate contains the rollback terminal dependency" >&2
        return 1
    fi
    if ! verify_darwin_native_linkage "$binary"; then
        return 1
    fi

    runtime_revision="$("$binary" --json version | jq -r .commit)"
    if [[ "$runtime_revision" != "$expected_revision" ]]; then
        echo "error: candidate runtime revision is $runtime_revision; want $expected_revision" >&2
        return 1
    fi
    if grep -Eq '/home/runner|/Users/|/private/var/folders/|/runner/work/' \
        < <(strings "$binary"); then
        echo "error: candidate contains a local or CI build path" >&2
        return 1
    fi
}

materialize_candidate_spdx() {
    local binary="${1:-}"
    local revision="${2:-}"
    local output="${3:-}"
    local package_filename="${4:-gr}"
    local binary_sha
    local namespace

    if [[ ! -f "$binary" || ! "$revision" =~ ^[0-9a-f]{40}$ || -z "$output" ||
        ! "$package_filename" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
        echo "usage: $0 materialize-spdx <binary> <revision> <output> [package-filename]" >&2
        return 2
    fi

    binary_sha="$(sha256_value "$binary")"
    namespace="$SPDX_NAMESPACE/candidate/$revision/darwin/arm64/$binary_sha"
    jq \
        --arg binary_sha "$binary_sha" \
        --arg namespace "$namespace" \
        --arg package_filename "$package_filename" \
        --arg revision "$revision" '
        .name = ("graith-libghostty-darwin-arm64-" + $revision) |
        .documentNamespace = $namespace |
        .packages = ([.packages[] | select(.SPDXID != "SPDXRef-Package-GraithNativeCandidate")] + [{
            "SPDXID": "SPDXRef-Package-GraithNativeCandidate",
            "checksums": [{"algorithm": "SHA256", "checksumValue": $binary_sha}],
            "copyrightText": "Copyright (c) 2025 Dougal Matthews",
            "downloadLocation": "NOASSERTION",
            "externalRefs": [{
                "referenceCategory": "PACKAGE-MANAGER",
                "referenceLocator": ("pkg:github/d0ugal/graith@" + $revision),
                "referenceType": "purl"
            }],
            "filesAnalyzed": false,
            "licenseConcluded": "MIT",
            "licenseDeclared": "MIT",
            "name": "graith-libghostty-darwin-arm64",
            "packageFileName": $package_filename,
            "sourceInfo": ("Graith revision " + $revision + "; target GOOS=darwin GOARCH=arm64; packaged binary " + $package_filename + " SHA-256 " + $binary_sha + "."),
            "supplier": "Person: Dougal Matthews",
            "versionInfo": $revision
        }]) |
        .relationships = ([.relationships[] | select(
            .spdxElementId != "SPDXRef-DOCUMENT" and
            .spdxElementId != "SPDXRef-Package-GraithNativeCandidate"
        )] + [
            {
                "relatedSpdxElement": "SPDXRef-Package-GraithNativeCandidate",
                "relationshipType": "DESCRIBES",
                "spdxElementId": "SPDXRef-DOCUMENT"
            },
            {
                "relatedSpdxElement": "SPDXRef-Package-GoLibghostty",
                "relationshipType": "STATIC_LINK",
                "spdxElementId": "SPDXRef-Package-GraithNativeCandidate"
            },
            {
                "relatedSpdxElement": "SPDXRef-Package-Ghostty",
                "relationshipType": "STATIC_LINK",
                "spdxElementId": "SPDXRef-Package-GraithNativeCandidate"
            }
        ])
        ' "$REPO_DIR/libghostty-native.spdx.json" >"$output"
}

verify_candidate_spdx() {
    local binary="${1:-}"
    local revision="${2:-}"
    local document="${3:-}"
    local package_filename="${4:-gr}"
    local binary_sha
    local namespace

    if [[ ! -f "$binary" || ! -f "$document" || ! "$revision" =~ ^[0-9a-f]{40}$ ||
        ! "$package_filename" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
        echo "usage: $0 verify-candidate-spdx <binary> <revision> <document> [package-filename]" >&2
        return 2
    fi

    binary_sha="$(sha256_value "$binary")"
    namespace="$SPDX_NAMESPACE/candidate/$revision/darwin/arm64/$binary_sha"
    jq -e \
        --arg binary_sha "$binary_sha" \
        --arg namespace "$namespace" \
        --arg package_filename "$package_filename" \
        --arg revision "$revision" '
        def package($id): first(.packages[] | select(.SPDXID == $id));
        def relates($from; $type; $to): any(.relationships[];
            .spdxElementId == $from and .relationshipType == $type and .relatedSpdxElement == $to);
        .spdxVersion == "SPDX-2.3" and
        .documentNamespace == $namespace and
        ([.packages[] | select(.SPDXID == "SPDXRef-Package-GraithNativeCandidate")] | length) == 1 and
        package("SPDXRef-Package-GraithNativeCandidate").versionInfo == $revision and
        package("SPDXRef-Package-GraithNativeCandidate").packageFileName == $package_filename and
        package("SPDXRef-Package-GraithNativeCandidate").checksums ==
            [{"algorithm": "SHA256", "checksumValue": $binary_sha}] and
        relates("SPDXRef-DOCUMENT"; "DESCRIBES"; "SPDXRef-Package-GraithNativeCandidate") and
        relates("SPDXRef-Package-GraithNativeCandidate"; "STATIC_LINK"; "SPDXRef-Package-GoLibghostty") and
        relates("SPDXRef-Package-GraithNativeCandidate"; "STATIC_LINK"; "SPDXRef-Package-Ghostty")
        ' "$document" >/dev/null
}

install_spdx_validator() {
    local destination="${1:-}"
    local archive
    local jar

    if [[ -z "$destination" ]]; then
        echo "usage: $0 install-spdx-validator <empty-directory>" >&2
        return 2
    fi
    mkdir -p "$destination"
    if find "$destination" -mindepth 1 -print -quit | grep -q .; then
        echo "error: SPDX validator destination is not empty" >&2
        return 1
    fi

    archive="$NATIVE_WORK/tools-java-$SPDX_TOOLS_VERSION.zip"
    curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
        "$SPDX_TOOLS_URL" --output "$archive"
    if ! sha256_check "$SPDX_TOOLS_SHA256" "$archive"; then
        echo "error: SPDX validator checksum mismatch" >&2
        return 1
    fi
    unzip -q "$archive" -d "$destination"
    jar="$destination/tools-java-$SPDX_TOOLS_VERSION-jar-with-dependencies.jar"
    if [[ ! -f "$jar" ]]; then
        echo "error: SPDX archive did not contain the expected validator" >&2
        return 1
    fi
    printf '%s\n' "$jar"
}

validate_spdx() {
    local jar="${1:-}"
    local document="${2:-$REPO_DIR/libghostty-native.spdx.json}"
    local output

    if [[ ! -f "$jar" || ! -f "$document" ]]; then
        echo "usage: $0 validate-spdx <tools-java-jar> [document]" >&2
        return 2
    fi
    output="$(java -jar "$jar" Verify "$document")"
    printf '%s\n' "$output"
    if ! grep -Fq 'This SPDX Document is valid.' <<<"$output"; then
        echo "error: official SPDX validator rejected $document" >&2
        return 1
    fi
}

publish_directory_exclusive() {
    local source="${1:-}"
    local destination="${2:-}"
    local helper="$NATIVE_WORK/rename-excl"

    if [[ ! -d "$source" || -z "$destination" ]]; then
        echo "usage: publish_directory_exclusive <source> <destination>" >&2
        return 2
    fi
    if [[ ! -x "$helper" ]]; then
        go build -buildvcs=false -trimpath -o "$helper" \
            "$REPO_DIR/internal/pty/testdata/renameexcl"
    fi
    if ! "$helper" "$source" "$destination"; then
        echo "error: candidate destination appeared before atomic publication" >&2
        return 1
    fi
}

test_exclusive_publication() {
    local root
    local source
    local destination

    root="$(mktemp -d "$NATIVE_WORK/exclusive-publish.XXXXXX")"
    source="$root/source-success"
    destination="$root/candidate-success"
    mkdir "$source"
    printf 'braw\n' >"$source/payload"
    publish_directory_exclusive "$source" "$destination"
    if [[ -e "$source" || "$(cat "$destination/payload")" != "braw" ]]; then
        echo "error: exclusive publication did not move the complete directory" >&2
        return 1
    fi

    source="$root/source-collision"
    destination="$root/candidate-collision"
    mkdir "$source" "$destination"
    printf 'canny\n' >"$source/payload"
    printf 'dreich\n' >"$destination/sentinel"
    if publish_directory_exclusive "$source" "$destination" >/dev/null 2>&1; then
        echo "error: exclusive publication accepted an existing destination" >&2
        return 1
    fi
    if [[ ! -d "$source" || "$(cat "$destination/sentinel")" != "dreich" || \
        -e "$destination/source-collision" ]]; then
        echo "error: failed exclusive publication changed the source or destination" >&2
        return 1
    fi
    rm -rf "$root"
}

package_darwin_arm64_candidate() (
    local binary="${1:-}"
    local destination="${2:-}"
    local spdx_jar="${3:-}"
    local package_filename="${4:-gr}"
    local destination_parent
    local revision
    local staging=""
    local tampered

    trap 'cleanup_candidate_staging "$staging"' EXIT

    if [[ ! -f "$binary" || -z "$destination" || ! -f "$spdx_jar" ||
        ! "$package_filename" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
        echo "usage: $0 package-darwin-arm64 <binary> <destination> <spdx-jar> [package-filename]" >&2
        return 2
    fi
    if [[ -e "$destination" ]]; then
        echo "error: candidate destination already exists" >&2
        return 1
    fi

    revision="$(candidate_revision)"
    candidate_identity "$binary" "$revision"
    destination_parent="$(dirname "$destination")"
    mkdir -p "$destination_parent"
    staging="$(mktemp -d "$destination_parent/.graith-native-candidate.XXXXXX")"

    cp "$binary" "$staging/$package_filename"
    cp "$REPO_DIR/THIRD_PARTY_NOTICES.libghostty.md" "$staging/"
    candidate_identity "$staging/$package_filename" "$revision"
    materialize_candidate_spdx \
        "$staging/$package_filename" "$revision" \
        "$staging/libghostty-native.spdx.json" "$package_filename"
    verify_candidate_spdx \
        "$staging/$package_filename" "$revision" \
        "$staging/libghostty-native.spdx.json" "$package_filename"
    validate_spdx "$spdx_jar" "$staging/libghostty-native.spdx.json"

    tampered="$staging/$package_filename.tampered"
    cp "$staging/$package_filename" "$tampered"
    printf '\0' >>"$tampered"
    if verify_candidate_spdx \
        "$tampered" "$revision" "$staging/libghostty-native.spdx.json" \
        "$package_filename"; then
        echo "error: candidate SPDX accepted changed binary bytes" >&2
        return 1
    fi
    rm "$tampered"

    if grep -Eq '/home/runner|/Users/|/private/var/folders/|/runner/work/' \
        "$staging/libghostty-native.spdx.json" \
        "$staging/THIRD_PARTY_NOTICES.libghostty.md"; then
        echo "error: candidate metadata contains a local or CI path" >&2
        return 1
    fi
    if [[ "$(find "$staging" -mindepth 1 -maxdepth 1 -type f -exec basename {} \; | sort)" != \
        "$(printf '%s\n' "$package_filename" libghostty-native.spdx.json THIRD_PARTY_NOTICES.libghostty.md | sort)" ]]; then
        echo "error: candidate artifact contents are incomplete or unexpected" >&2
        return 1
    fi

    if ! publish_directory_exclusive "$staging" "$destination"; then
        return 1
    fi
    staging=""
)

usage() {
    cat <<EOF
usage: $0 test|race|fuzz|bench|memory|daemon-test|soak [cycles [timeout]]|all
       $0 source-build <zig-target> <output-library>
       $0 source-test <zig-target>
       $0 test-source-archive-policy
       $0 verify-static-archive <library>
       $0 verify-dependency-unit
       $0 verify-generated-dependency-unit
       $0 generate-dependency-unit
       $0 accept-license-reviews
       $0 verify-metadata [ghostty-source]
       $0 verify-default-binary <binary>
       $0 verify-darwin-arm64-candidate <binary> <revision>
       $0 verify-candidate-spdx <binary> <revision> <document> [package-filename]
       $0 test-darwin-linkage-policy
       $0 test-metadata-policy
       $0 install-spdx-validator <empty-directory>
       $0 validate-spdx <tools-java-jar> [document]
       $0 test-exclusive-publish
       $0 package-darwin-arm64 <binary> <destination> <tools-java-jar> [package-filename]

test/bench/memory use the checksum-pinned Apple artifact on macOS arm64.
daemon-test runs the external daemon lifecycle and bounded 12-cycle soak.
soak defaults to 1,000 cycles bounded by one hour.
source-build checks out Ghostty $GHOSTTY_SHA and requires Zig $REQUIRED_ZIG.
generate-dependency-unit rotates the complete lock, Go module, headers, SPDX,
notice inventory, and Apple artifact metadata; verify-dependency-unit is offline.
accept-license-reviews explicitly binds reviewed conclusions to current license
and embedded-notice hashes; run it only after inspecting the changed evidence.
EOF
}

case "${1:-}" in
    test|race|fuzz|bench|memory)
        run_go "$1"
        ;;
    daemon-test)
        run_daemon_validation 12 \
            '^(TestLibghosttyDaemonLifecycle|TestLibghosttyDaemonSoak|TestNativeProcessObservation|TestNativeRestartDiagnostics|TestDaemonFDGrowthExceeded|TestIsolatedNativeEnvironmentAllowlist)$' \
            '3m' '5m' '0'
        ;;
    soak)
        run_daemon_validation "${2:-1000}" '^TestLibghosttyDaemonSoak$' \
            "${3:-1h}" '65m' '1'
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
    test-source-archive-policy)
        test_source_archive_policy
        ;;
    verify-static-archive)
        verify_static_archive "${2:-}"
        ;;
    verify-dependency-unit)
        verify_dependency_unit
        ;;
    verify-generated-dependency-unit)
        verify_generated_dependency_unit
        ;;
    generate-dependency-unit)
        generate_dependency_unit
        ;;
    accept-license-reviews)
        accept_license_reviews
        ;;
    verify-metadata)
        verify_metadata "${2:-}"
        ;;
    verify-default-binary)
        verify_default_binary "${2:-}"
        ;;
    verify-darwin-arm64-candidate)
        candidate_identity "${2:-}" "${3:-}"
        ;;
    verify-candidate-spdx)
        verify_candidate_spdx "${2:-}" "${3:-}" "${4:-}" "${5:-gr}"
        ;;
    test-darwin-linkage-policy)
        test_darwin_linkage_policy
        ;;
    test-metadata-policy)
        test_metadata_policy
        ;;
    install-spdx-validator)
        install_spdx_validator "${2:-}"
        ;;
    validate-spdx)
        validate_spdx "${2:-}" "${3:-$REPO_DIR/libghostty-native.spdx.json}"
        ;;
    test-exclusive-publish)
        test_exclusive_publication
        ;;
    package-darwin-arm64)
        package_darwin_arm64_candidate "${2:-}" "${3:-}" "${4:-}" "${5:-gr}"
        ;;
    *)
        usage >&2
        exit 2
        ;;
esac
