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

readonly GHOSTTY_SHA="$(jq -er '.ghostty.commit' "$DEPENDENCY_LOCK")"
readonly GHOSTTY_REPO="$(jq -er '.ghostty.repository' "$DEPENDENCY_LOCK")"
readonly GHOSTTY_ARTIFACT_URL="$(jq -er '.ghostty.appleArtifact.url' "$DEPENDENCY_LOCK")"
readonly GHOSTTY_ARTIFACT_SHA256="$(jq -er '.ghostty.appleArtifact.sha256' "$DEPENDENCY_LOCK")"
readonly REQUIRED_ZIG="$(jq -er '.zig.version' "$DEPENDENCY_LOCK")"
readonly GO_LIBGHOSTTY_SHA="$(jq -er '.goLibghostty.commit' "$DEPENDENCY_LOCK")"
readonly GO_LIBGHOSTTY_VERSION="$(jq -er '.goLibghostty.version' "$DEPENDENCY_LOCK")"
readonly GO_LIBGHOSTTY_SUM="$(jq -er '.goLibghostty.moduleSum' "$DEPENDENCY_LOCK")"
readonly UUCODE_VERSION="$(jq -er '.uucode.version' "$DEPENDENCY_LOCK")"
readonly UUCODE_HASH="$(jq -er '.uucode.zigHash' "$DEPENDENCY_LOCK")"
readonly HIGHWAY_VERSION="$(jq -er '.highway.version' "$DEPENDENCY_LOCK")"
readonly HIGHWAY_SHA="$(jq -er '.highway.commit' "$DEPENDENCY_LOCK")"
readonly SIMDUTF_VERSION="$(jq -er '.simdutf.version' "$DEPENDENCY_LOCK")"
readonly SIMDUTF_MANIFEST_VERSION="$(jq -er '.simdutf.manifestVersion' "$DEPENDENCY_LOCK")"
readonly SIMDUTF_UPSTREAM_SHA="$(jq -er '.simdutf.commit' "$DEPENDENCY_LOCK")"
readonly SIMDUTF_CPP_SHA256="$(jq -er '.simdutf.cppSHA256' "$DEPENDENCY_LOCK")"
readonly SIMDUTF_HEADER_SHA256="$(jq -er '.simdutf.headerSHA256' "$DEPENDENCY_LOCK")"
readonly ZIG_SOURCE_SHA256="$(jq -er '.zig.sourceSHA256' "$DEPENDENCY_LOCK")"
readonly SPDX_TOOLS_VERSION="$(jq -er '.spdxTools.version' "$DEPENDENCY_LOCK")"
readonly SPDX_TOOLS_URL="$(jq -er '.spdxTools.url' "$DEPENDENCY_LOCK")"
readonly SPDX_TOOLS_SHA256="$(jq -er '.spdxTools.sha256' "$DEPENDENCY_LOCK")"
readonly SPDX_NAMESPACE="https://github.com/d0ugal/graith/sbom/libghostty-native/$GHOSTTY_SHA/$GO_LIBGHOSTTY_SHA"
readonly BENCH_SAMPLES="5"
readonly BENCHTIME="1s"
readonly MEASUREMENT_GOMAXPROCS="10"
readonly MEMORY_SAMPLES="5"

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

    if [[ "$(uname -s)" == "Darwin" ]]; then
        shasum -a 256 "$path" | awk '{print $1}'
    else
        sha256sum "$path" | awk '{print $1}'
    fi
}

verify_dependency_unit() {
    cd "$REPO_DIR"
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
                -run 'TestLibghostty|TestProbeUpgrade|TestUpgradeHelperHandoff'
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
                -run 'TestLibghostty|TestProbeUpgrade|TestUpgradeHelperHandoff'
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
    local library
    local binary="$NATIVE_WORK/gr-libghostty-daemon-race"
    local daemon_gocache="${GRAITH_LIBGHOSTTY_GOCACHE:-$NATIVE_WORK/go-cache}"

    library="$(apple_library)"
    PKG_CONFIG_PATH="$(write_pkg_config "$library")${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
    export PKG_CONFIG_PATH
    mkdir -p "$daemon_gocache"

    verify_metadata
    cd "$REPO_DIR"
    GOCACHE="$daemon_gocache" CGO_ENABLED=1 \
        go build -race -trimpath -tags='libghostty' \
        -o "$binary" ./cmd/graith
    GRAITH_LIBGHOSTTY_DAEMON_BINARY="$binary" \
        GRAITH_LIBGHOSTTY_SOAK_CYCLES="$cycles" \
        GRAITH_LIBGHOSTTY_SOAK_TIMEOUT="$workload_timeout" \
        GRAITH_LIBGHOSTTY_LONG_SOAK="$long_soak" \
        GOCACHE="$daemon_gocache" \
        CGO_ENABLED=1 go test -v -race -count=1 -tags='integration libghostty' \
            -timeout="$go_timeout" -run "$test_pattern" ./internal/integration
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
