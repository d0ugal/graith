#!/usr/bin/env bash
# Build, package, and validate the non-production libghostty-vt candidate.
# All generated source, archives, caches, and binaries stay below a temporary
# work directory. Pin rotation is documented in the libghostty design record.
set -euo pipefail

readonly GHOSTTY_SHA="91f66da24527fa02d92b5fd0b41cd020f553a64c"
readonly GHOSTTY_VERSION="1.3.2-dev"
readonly GHOSTTY_REPO="https://github.com/ghostty-org/ghostty.git"
readonly GHOSTTY_LICENSE_SHA256="386211873e5b7a02f663ae4d7adf96285999f91608f8f9f31fecfd0f4095e6f1"
readonly GHOSTTY_ARTIFACT_URL="https://github.com/d0ugal/graith/releases/download/libghostty-vt-91f66da/libghostty-vt.xcframework.zip"
readonly GHOSTTY_ARTIFACT_SHA256="25c1620e63311cc687637c8e3bdfae1fe3e070892966c07d0d91065ccda541c0"

readonly GO_LIBGHOSTTY_SHA="e9e1010f80b1ced0b7efcdb300f4838513c0816e"
readonly GO_LIBGHOSTTY_VERSION="v0.0.0-20260527181217-e9e1010f80b1"
readonly GO_LIBGHOSTTY_SUM="h1:XAiToY/9BPUvzfTHSmhHRjPprV5JfwjWE6BGT7ojEQ8="
readonly GO_LIBGHOSTTY_LICENSE_SHA256="fdf9b4ad7b61687fd3d4b1e3efa63cbc10743e6b733a62669b53a324251357b9"

readonly REQUIRED_ZIG="0.15.2"
readonly ZIG_SOURCE_URL="https://ziglang.org/download/0.15.2/zig-0.15.2.tar.xz"
readonly ZIG_SOURCE_SHA256="d9b30c7aa983fcff5eed2084d54ae83eaafe7ff3a84d8fb754d854165a6e521c"
readonly ZIG_LINUX_X86_64_URL="https://ziglang.org/download/0.15.2/zig-x86_64-linux-0.15.2.tar.xz"
readonly ZIG_LINUX_X86_64_SHA256="02aa270f183da276e5b5920b1dac44a63f1a49e55050ebde3aecc9eb82f93239"
readonly ZIG_LICENSE_SHA256="5c537d6853e005298a285d508cff9ac7192cea23576c840d485b2b586a7ff177"

readonly UUCODE_VERSION="0.2.0"
readonly UUCODE_URL="https://deps.files.ghostty.org/uucode-0.2.0-ZZjBPqZVVABQepOqZHR7vV_NcaN-wats0IB6o-Exj6m9.tar.gz"
readonly UUCODE_HASH="uucode-0.2.0-ZZjBPqZVVABQepOqZHR7vV_NcaN-wats0IB6o-Exj6m9"
readonly UUCODE_SHA256="d0abee0f4f8bd6eae3c051777e16e7c42d8964aaaa015591c4e565703f465f95"
readonly UUCODE_LICENSE_SHA256="312e901e142be2477b4ca859e9311f9e3f80d33372991759b7921c1893605f33"
readonly UUCODE_HOEHRMANN_LICENSE_SHA256="de219cece932aad5a817bf763393d8d149d378a15d2ad5320e3331eac07626dd"
readonly UUCODE_UNICODE_LICENSE_SHA256="1eda5a3b026870c737b22e8bcd4954338612c790db688242e003f41a4fa95175"

readonly HIGHWAY_VERSION="1.2.0"
readonly HIGHWAY_SHA="66486a10623fa0d72fe91260f96c892e41aceb06"
readonly HIGHWAY_URL="https://deps.files.ghostty.org/highway-66486a10623fa0d72fe91260f96c892e41aceb06.tar.gz"
readonly HIGHWAY_HASH="N-V-__8AAGmZhABbsPJLfbqrh6JTHsXhY6qCaLAQyx25e0XE"
readonly HIGHWAY_SHA256="87d4f8893ef4e08f224973608ffebf94268a81380ba79c12e8841968c80aa212"
readonly HIGHWAY_BSD_LICENSE_SHA256="d25e82e26acd42ca3ccc9993622631163425b869b9e16284226d534cff6470f2"

# Ghostty's package manifest was not bumped when its vendored amalgamation was
# upgraded. The compiled headers identify v9.0.0; the two source hashes below
# make that conclusion independent of the stale 5.2.8 package metadata.
readonly SIMDUTF_VERSION="9.0.0"
readonly SIMDUTF_UPSTREAM_SHA="ca7acbcea967b5dcbab490066e99e3a6e6925539"
readonly SIMDUTF_REPO="https://github.com/simdutf/simdutf.git"
readonly SIMDUTF_MANIFEST_VERSION="5.2.8"
readonly SIMDUTF_CPP_SHA256="38dc5481dc4b7eef95cea8056b84d940419288100f317ecaff683bd89f163263"
readonly SIMDUTF_HEADER_SHA256="d3501fc2143f0edc5c84e6bb013a7fdb8ccd95514f7fd0816669248e62676301"
readonly SIMDUTF_MIT_LICENSE_URL="https://raw.githubusercontent.com/simdutf/simdutf/ca7acbcea967b5dcbab490066e99e3a6e6925539/LICENSE-MIT"
readonly SIMDUTF_MIT_LICENSE_SHA256="fc8dbc04e03ad4efc08a647ffe7f995b811a95bc04c0e85a56d5277c6593fa5f"

readonly SPDX_TOOLS_VERSION="2.0.7"
readonly SPDX_TOOLS_URL="https://github.com/spdx/tools-java/releases/download/v2.0.7/tools-java-2.0.7.zip"
readonly SPDX_TOOLS_SHA256="2dc63c3399c5178058b1be8a3de6f13b9f24981cd86c4292ef98f4a7e90de36d"

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
readonly REPO_DIR
readonly COMMITTED_HEADERS="$REPO_DIR/gui/shared/Sources/CGhosttyVT/include/ghostty"
readonly SPDX_DOCUMENT="$REPO_DIR/libghostty-native.spdx.json"
readonly NOTICE_DOCUMENT="$REPO_DIR/THIRD_PARTY_NOTICES.libghostty.md"

NATIVE_WORK="${GRAITH_LIBGHOSTTY_WORK:-}"
OWN_WORK=0
if [[ -z "$NATIVE_WORK" ]]; then
    NATIVE_WORK="$(mktemp -d)"
    OWN_WORK=1
fi
KEEP_WORK="${GRAITH_LIBGHOSTTY_KEEP_WORK:-0}"
mkdir -p "$NATIVE_WORK"
NATIVE_WORK="$(cd "$NATIVE_WORK" && pwd)"
if [[ "$NATIVE_WORK" == "/" ]]; then
    echo "error: refusing to use the filesystem root as native work directory" >&2
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

ensure_empty_directory() {
    local directory="$1"
    mkdir -p "$directory"
    if [[ -n "$(find "$directory" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
        die "destination directory is not empty: $directory"
    fi
}

sha256_value() {
    local file_name="$1"
    if [[ "$(uname -s)" == "Darwin" ]]; then
        shasum -a 256 "$file_name" | awk '{print $1}'
    else
        sha256sum "$file_name" | awk '{print $1}'
    fi
}

sha256_check() {
    local expected="$1"
    local file_name="$2"
    [[ "$(sha256_value "$file_name")" == "$expected" ]]
}

assert_sha256() {
    local expected="$1"
    local file_name="$2"
    local label="$3"
    local actual
    actual="$(sha256_value "$file_name")"
    [[ "$actual" == "$expected" ]] ||
        die "$label checksum is $actual; want $expected"
}

assert_tar_member_sha256() {
    local expected="$1"
    local archive="$2"
    local member="$3"
    local label="$4"
    local actual
    if [[ "$(uname -s)" == "Darwin" ]]; then
        actual="$(tar -xOf "$archive" "$member" | shasum -a 256 | awk '{print $1}')"
    else
        actual="$(tar -xOf "$archive" "$member" | sha256sum | awk '{print $1}')"
    fi
    [[ "$actual" == "$expected" ]] ||
        die "$label checksum is $actual; want $expected"
}

download_checked() {
    local url="$1"
    local expected="$2"
    local output="$3"
    local partial="${output}.partial"

    if [[ -f "$output" ]] && sha256_check "$expected" "$output"; then
        return
    fi

    curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
        "$url" --output "$partial"
    if ! sha256_check "$expected" "$partial"; then
        die "download checksum mismatch for $url"
    fi
    mv "$partial" "$output"
}

write_pkg_config() {
    local library="$1"
    local directory="$NATIVE_WORK/pkgconfig"
    mkdir -p "$directory"
    cat > "$directory/libghostty-vt-static.pc" <<EOF
Name: libghostty-vt-static
Description: pinned static libghostty-vt for Graith
Version: $GHOSTTY_SHA
Cflags: -I$COMMITTED_HEADERS/.. -DGHOSTTY_STATIC
Libs: $library
EOF
    printf '%s\n' "$directory"
}

fetch_source() {
    local source="$NATIVE_WORK/ghostty"
    if [[ ! -d "$source/.git" ]]; then
        if [[ -e "$source" && -n "$(find "$source" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]]; then
            die "$source exists but is not a Git checkout"
        fi
        git init -q "$source"
        git -C "$source" remote add origin "$GHOSTTY_REPO"
    fi

    # Inspect the configured URL rather than Git's transport-rewritten value;
    # developer SSH insteadOf rules must not change the recorded provenance.
    if [[ "$(git -C "$source" config --get remote.origin.url)" != "$GHOSTTY_REPO" ]]; then
        die "existing Ghostty checkout has an unexpected origin"
    fi
    git -C "$source" fetch --depth 1 origin "$GHOSTTY_SHA"
    git -C "$source" checkout -q --detach FETCH_HEAD
    if [[ "$(git -C "$source" rev-parse HEAD)" != "$GHOSTTY_SHA" ]]; then
        die "Ghostty checkout did not resolve to the required SHA"
    fi
    if ! git -C "$source" diff --quiet || ! git -C "$source" diff --cached --quiet; then
        die "Ghostty checkout contains tracked modifications"
    fi
    printf '%s\n' "$source"
}

apple_library() {
    if [[ "$(uname -s)" != "Darwin" ]]; then
        die "the pinned GUI artifact contains Apple slices only; use source-build on Linux"
    fi

    local archive="$NATIVE_WORK/libghostty-vt.xcframework.zip"
    local framework="$NATIVE_WORK/libghostty-vt.xcframework"
    local extract_dir="$NATIVE_WORK/apple-extract"
    local library="$framework/macos-arm64_x86_64/libghostty-vt.a"

    download_checked "$GHOSTTY_ARTIFACT_URL" "$GHOSTTY_ARTIFACT_SHA256" "$archive"
    if [[ -e "$extract_dir" ]]; then
        [[ "$extract_dir" == "$NATIVE_WORK/"* ]] || die "unsafe Apple extraction path"
        rm -rf -- "$extract_dir"
    fi
    mkdir -p "$extract_dir"
    unzip -q "$archive" -d "$extract_dir"
    if [[ ! -f "$extract_dir/libghostty-vt.xcframework/macos-arm64_x86_64/libghostty-vt.a" ]]; then
        die "pinned artifact does not contain the universal macOS library"
    fi
    if [[ -e "$framework" ]]; then
        [[ "$framework" == "$NATIVE_WORK/"* ]] || die "unsafe Apple framework path"
        rm -rf -- "$framework"
    fi
    mv "$extract_dir/libghostty-vt.xcframework" "$framework"
    printf '%s\n' "$library"
}

verify_headers() {
    local source="${1:-}"
    local framework="${2:-}"
    [[ -n "$source" && -d "$source/include/ghostty" ]] ||
        die "usage: $0 verify-headers <ghostty-source> [xcframework]"

    diff -ru "$source/include/ghostty" "$COMMITTED_HEADERS"

    if [[ -n "$framework" ]]; then
        local slice
        for slice in macos-arm64_x86_64 ios-arm64 ios-arm64-simulator; do
            local artifact_headers="$framework/$slice/Headers/ghostty"
            [[ -d "$artifact_headers" ]] || die "Apple artifact is missing $slice headers"
            diff -ru "$source/include/ghostty" "$artifact_headers"
        done
    fi
}

verify_metadata() {
    local source="${1:-}"
    local actual_version module_json module_dir

    require_command jq
    cd "$REPO_DIR"
    actual_version="$(go list -mod=readonly -m -f '{{.Version}}' go.mitchellh.com/libghostty)"
    [[ "$actual_version" == "$GO_LIBGHOSTTY_VERSION" ]] ||
        die "go-libghostty version is $actual_version; want $GO_LIBGHOSTTY_VERSION"
    grep -Fqx "go.mitchellh.com/libghostty $GO_LIBGHOSTTY_VERSION $GO_LIBGHOSTTY_SUM" go.sum ||
        die "go.sum does not contain the exact go-libghostty module checksum"

    module_json="$(go mod download -json "go.mitchellh.com/libghostty@$GO_LIBGHOSTTY_VERSION")"
    module_dir="$(jq -r '.Dir' <<<"$module_json")"
    [[ -d "$module_dir" ]] || die "go-libghostty module source was not downloaded"
    assert_sha256 "$GO_LIBGHOSTTY_LICENSE_SHA256" "$module_dir/LICENSE" "go-libghostty license"

    jq -e \
        --arg ghostty "$GHOSTTY_SHA" \
        --arg ghostty_version "$GHOSTTY_VERSION+$GHOSTTY_SHA" \
        --arg go_libghostty "$GO_LIBGHOSTTY_VERSION" \
        --arg uucode "$UUCODE_VERSION" \
        --arg uucode_sha "$UUCODE_SHA256" \
        --arg highway "$HIGHWAY_VERSION+$HIGHWAY_SHA" \
        --arg highway_sha "$HIGHWAY_SHA256" \
        --arg simdutf "$SIMDUTF_VERSION+$SIMDUTF_UPSTREAM_SHA" \
        --arg simdutf_cpp "$SIMDUTF_CPP_SHA256" \
        --arg simdutf_h "$SIMDUTF_HEADER_SHA256" \
        --arg zig "$REQUIRED_ZIG" \
        --arg zig_sha "$ZIG_SOURCE_SHA256" '
        def package($id): first(.packages[] | select(.SPDXID == $id));
        def has_sha($package; $sha): any($package.checksums[]?; .algorithm == "SHA256" and .checksumValue == $sha);
        def relates($from; $type; $to): any(.relationships[];
            .spdxElementId == $from and .relationshipType == $type and .relatedSpdxElement == $to);
        .spdxVersion == "SPDX-2.3" and
        .dataLicense == "CC0-1.0" and
        (.packages | length) == 7 and
        (package("SPDXRef-Package-GoLibghostty").versionInfo == $go_libghostty) and
        (package("SPDXRef-Package-GoLibghostty").licenseConcluded == "MIT") and
        (package("SPDXRef-Package-Ghostty").versionInfo == $ghostty_version) and
        any(package("SPDXRef-Package-Ghostty").externalRefs[]; .referenceLocator | contains($ghostty)) and
        (package("SPDXRef-Package-Uucode").versionInfo == $uucode) and
        has_sha(package("SPDXRef-Package-Uucode"); $uucode_sha) and
        (package("SPDXRef-Package-Uucode").licenseConcluded == "MIT AND Unicode-3.0") and
        (package("SPDXRef-Package-Highway").versionInfo == $highway) and
        has_sha(package("SPDXRef-Package-Highway"); $highway_sha) and
        (package("SPDXRef-Package-Highway").licenseConcluded == "BSD-3-Clause") and
        (package("SPDXRef-Package-Simdutf").versionInfo == $simdutf) and
        (package("SPDXRef-Package-Simdutf").sourceInfo | contains($simdutf_cpp)) and
        (package("SPDXRef-Package-Simdutf").sourceInfo | contains($simdutf_h)) and
        (package("SPDXRef-Package-Simdutf").licenseConcluded == "MIT AND BSD-3-Clause AND Apache-2.0") and
        (package("SPDXRef-Package-ZigRuntime").versionInfo == $zig) and
        has_sha(package("SPDXRef-Package-ZigRuntime"); $zig_sha) and
        (package("SPDXRef-Package-ZigRuntime").licenseConcluded == "MIT AND (Apache-2.0 WITH LLVM-exception)") and
        (.relationships | length) == 7 and
        relates("SPDXRef-DOCUMENT"; "DESCRIBES"; "SPDXRef-Package-GraithNativeCandidate") and
        relates("SPDXRef-Package-GraithNativeCandidate"; "STATIC_LINK"; "SPDXRef-Package-GoLibghostty") and
        relates("SPDXRef-Package-GraithNativeCandidate"; "STATIC_LINK"; "SPDXRef-Package-Ghostty") and
        relates("SPDXRef-Package-Ghostty"; "STATIC_LINK"; "SPDXRef-Package-Uucode") and
        relates("SPDXRef-Package-Ghostty"; "STATIC_LINK"; "SPDXRef-Package-Highway") and
        relates("SPDXRef-Package-Ghostty"; "STATIC_LINK"; "SPDXRef-Package-Simdutf") and
        relates("SPDXRef-Package-Ghostty"; "STATIC_LINK"; "SPDXRef-Package-ZigRuntime")
        ' "$SPDX_DOCUMENT" >/dev/null

    local required
    for required in \
        "$GHOSTTY_SHA" "$GHOSTTY_ARTIFACT_URL" "$GHOSTTY_ARTIFACT_SHA256" \
        "$GHOSTTY_LICENSE_SHA256" \
        "$GO_LIBGHOSTTY_SHA" "$GO_LIBGHOSTTY_SUM" "$GO_LIBGHOSTTY_LICENSE_SHA256" \
        "$REQUIRED_ZIG" "$ZIG_SOURCE_URL" "$ZIG_SOURCE_SHA256" "$ZIG_LICENSE_SHA256" \
        "$UUCODE_HASH" "$UUCODE_SHA256" "$UUCODE_LICENSE_SHA256" \
        "$UUCODE_HOEHRMANN_LICENSE_SHA256" "$UUCODE_UNICODE_LICENSE_SHA256" \
        "$HIGHWAY_SHA" "$HIGHWAY_HASH" "$HIGHWAY_SHA256" "$HIGHWAY_BSD_LICENSE_SHA256" \
        "$SIMDUTF_VERSION" "$SIMDUTF_UPSTREAM_SHA" \
        "$SIMDUTF_CPP_SHA256" "$SIMDUTF_HEADER_SHA256" "$SIMDUTF_MIT_LICENSE_SHA256"; do
        grep -Fq "$required" "$NOTICE_DOCUMENT" ||
            die "native notice inventory is missing $required"
    done

    if [[ -z "$source" ]]; then
        return
    fi
    [[ "$(git -C "$source" rev-parse HEAD)" == "$GHOSTTY_SHA" ]] ||
        die "native metadata source is not the required Ghostty commit"
    assert_sha256 "$GHOSTTY_LICENSE_SHA256" "$source/LICENSE" "Ghostty license"

    grep -Fq ".version = \"$GHOSTTY_VERSION\"" "$source/build.zig.zon" ||
        die "Ghostty source version does not match metadata"
    grep -Fq ".minimum_zig_version = \"$REQUIRED_ZIG\"" "$source/build.zig.zon" ||
        die "Ghostty minimum Zig version does not match metadata"
    grep -Fq ".url = \"$UUCODE_URL\"" "$source/build.zig.zon" ||
        die "Ghostty uucode URL does not match the atomic pin"
    grep -Fq ".hash = \"$UUCODE_HASH\"" "$source/build.zig.zon" ||
        die "Ghostty uucode content hash does not match the atomic pin"
    grep -Fq ".version = \"$HIGHWAY_VERSION\"" "$source/pkg/highway/build.zig.zon" ||
        die "Ghostty Highway version does not match metadata"
    grep -Fq ".url = \"$HIGHWAY_URL\"" "$source/pkg/highway/build.zig.zon" ||
        die "Ghostty Highway URL does not match the atomic pin"
    grep -Fq ".hash = \"$HIGHWAY_HASH\"" "$source/pkg/highway/build.zig.zon" ||
        die "Ghostty Highway content hash does not match the atomic pin"

    # Preserve the known upstream manifest mismatch as an asserted fact while
    # deriving the SBOM version from the exact vendored code below.
    grep -Fq ".version = \"$SIMDUTF_MANIFEST_VERSION\"" "$source/pkg/simdutf/build.zig.zon" ||
        die "Ghostty's recorded simdutf manifest version changed; re-audit the vendored code"
    grep -Fq "#define SIMDUTF_VERSION \"$SIMDUTF_VERSION\"" \
        "$source/pkg/simdutf/vendor/simdutf.h" ||
        die "vendored simdutf code does not identify the audited version"
    assert_sha256 "$SIMDUTF_CPP_SHA256" "$source/pkg/simdutf/vendor/simdutf.cpp" "simdutf.cpp"
    assert_sha256 "$SIMDUTF_HEADER_SHA256" "$source/pkg/simdutf/vendor/simdutf.h" "simdutf.h"

    grep -Fq 'lib.bundle_compiler_rt = true;' "$source/src/build/GhosttyLibVt.zig" ||
        die "Ghostty no longer bundles the audited Zig compiler runtime"
    grep -Fq 'lib.bundle_ubsan_rt = true;' "$source/src/build/GhosttyLibVt.zig" ||
        die "Ghostty no longer bundles the audited Zig UBSan runtime"
    verify_headers "$source"
}

verify_provenance() {
    local source
    source="$(fetch_source)"
    verify_metadata "$source"

    local simdutf_tag_sha
    simdutf_tag_sha="$(git ls-remote "$SIMDUTF_REPO" "refs/tags/v$SIMDUTF_VERSION" | awk '{print $1}')"
    [[ "$simdutf_tag_sha" == "$SIMDUTF_UPSTREAM_SHA" ]] ||
        die "simdutf v$SIMDUTF_VERSION does not resolve to the audited upstream commit"

    local apple_archive="$NATIVE_WORK/libghostty-vt.xcframework.zip"
    local uucode_archive="$NATIVE_WORK/uucode-$UUCODE_VERSION.tar.gz"
    local highway_archive="$NATIVE_WORK/highway-$HIGHWAY_SHA.tar.gz"
    local zig_archive="$NATIVE_WORK/zig-$REQUIRED_ZIG.tar.xz"
    local simdutf_license="$NATIVE_WORK/simdutf-$SIMDUTF_UPSTREAM_SHA-LICENSE-MIT"

    download_checked "$GHOSTTY_ARTIFACT_URL" "$GHOSTTY_ARTIFACT_SHA256" "$apple_archive"
    download_checked "$UUCODE_URL" "$UUCODE_SHA256" "$uucode_archive"
    download_checked "$HIGHWAY_URL" "$HIGHWAY_SHA256" "$highway_archive"
    download_checked "$ZIG_SOURCE_URL" "$ZIG_SOURCE_SHA256" "$zig_archive"
    download_checked "$SIMDUTF_MIT_LICENSE_URL" "$SIMDUTF_MIT_LICENSE_SHA256" "$simdutf_license"

    assert_tar_member_sha256 "$UUCODE_LICENSE_SHA256" "$uucode_archive" \
        "uucode-$UUCODE_VERSION/LICENSE.md" "uucode license"
    assert_tar_member_sha256 "$UUCODE_HOEHRMANN_LICENSE_SHA256" "$uucode_archive" \
        "uucode-$UUCODE_VERSION/licenses/LICENSE_Bjoern_Hoehrmann" "uucode decoder notice"
    assert_tar_member_sha256 "$UUCODE_UNICODE_LICENSE_SHA256" "$uucode_archive" \
        "uucode-$UUCODE_VERSION/licenses/LICENSE_unicode" "uucode Unicode notice"
    assert_tar_member_sha256 "$HIGHWAY_BSD_LICENSE_SHA256" "$highway_archive" \
        "highway-$HIGHWAY_SHA/LICENSE-BSD3" "Highway BSD license"
    assert_tar_member_sha256 "$ZIG_LICENSE_SHA256" "$zig_archive" \
        "zig-$REQUIRED_ZIG/LICENSE" "Zig license"
}

verify_static_archive() {
    local library="${1:-}"
    [[ -f "$library" ]] || die "usage: $0 verify-static-archive <library>"
    require_command ar
    require_command nm
    require_command strings

    local -a archives=("$library")
    if [[ "$(uname -s)" == "Darwin" ]] && lipo -info "$library" 2>/dev/null | grep -Fq ' are: '; then
        archives=()
        local arch
        for arch in arm64 x86_64; do
            local thin="$NATIVE_WORK/libghostty-vt-$arch.a"
            lipo "$library" -thin "$arch" -output "$thin"
            archives+=("$thin")
        done
    fi

    local expected_members
    expected_members="$(printf '%s\n' \
        abort.o base64.o codepoint_width.o compiler_rt.o index_of.o \
        libghostty-vt-static_zcu.o libhighway_zcu.o per_target.o \
        simdutf.o targets.o vt.o | sort)"

    local archive
    for archive in "${archives[@]}"; do
        local actual_members symbols
        actual_members="$(ar -t "$archive" | grep -v '^__.SYMDEF' | sort)"
        [[ "$actual_members" == "$expected_members" ]] || {
            printf 'expected archive members:\n%s\nactual archive members:\n%s\n' \
                "$expected_members" "$actual_members" >&2
            die "static archive contents do not match the audited dependency closure"
        }
        symbols="$(nm -g "$archive" 2>/dev/null)"
        grep -Eq '[[:space:]][Tt][[:space:]]_?ghostty_terminal_new$' <<<"$symbols" ||
            die "static archive does not define ghostty_terminal_new"
        grep -Eq '[[:space:]][Tt][[:space:]]_?__ZN7simdutf' <<<"$symbols" ||
            die "static archive does not contain simdutf"
        grep -Eq '[[:space:]][Tt][[:space:]]_?__ZN3hwy' <<<"$symbols" ||
            die "static archive does not contain Highway"
        grep -Eq '[[:space:]][Tt][[:space:]]_?__ubsan_handle_' <<<"$symbols" ||
            die "static archive does not contain the audited Zig UBSan runtime"
        grep -Fq "zig $REQUIRED_ZIG" < <(strings "$archive") ||
            die "static archive does not identify the required Zig toolchain"
        file "$archive"
    done
}

prepare_apple() {
    local source library framework
    source="$(fetch_source)"
    verify_metadata "$source"
    library="$(apple_library)"
    framework="$NATIVE_WORK/libghostty-vt.xcframework"
    verify_headers "$source" "$framework"
    verify_static_archive "$library"
    write_pkg_config "$library" >/dev/null
}

require_exact_zig() {
    require_command zig
    local actual
    actual="$(zig version)"
    [[ "$actual" == "$REQUIRED_ZIG" ]] ||
        die "Zig $REQUIRED_ZIG is required; found $actual"
}

source_build() {
    local target="${1:-}"
    local output="${2:-}"
    [[ -n "$target" && -n "$output" ]] ||
        die "usage: $0 source-build <zig-target> <output-library>"
    require_exact_zig

    local source
    source="$(fetch_source)"
    verify_metadata "$source"
    (
        cd "$source"
        zig build \
            --global-cache-dir "$NATIVE_WORK/zig-global" \
            --cache-dir "$NATIVE_WORK/zig-local" \
            -Demit-lib-vt=true \
            -Demit-xcframework=false \
            -Doptimize=ReleaseFast \
            -Dsimd=true \
            -Dtarget="$target"
    )

    local built_library="$source/zig-out/lib/libghostty-vt.a"
    [[ -f "$built_library" ]] || die "Ghostty build did not produce $built_library"
    mkdir -p "$(dirname "$output")"
    cp "$built_library" "$output"
    verify_static_archive "$output"
    write_pkg_config "$output" >/dev/null
}

source_test() {
    local target="${1:-}"
    [[ -n "$target" ]] || die "usage: $0 source-test <native-zig-target>"
    require_exact_zig

    local source
    source="$(fetch_source)"
    verify_metadata "$source"
    (
        cd "$source"
        # Ghostty enables the slow runtime safety asserted by test-lib-vt only
        # in Debug builds; release artifacts remain ReleaseFast above.
        zig build test-lib-vt \
            --global-cache-dir "$NATIVE_WORK/zig-global" \
            --cache-dir "$NATIVE_WORK/zig-local" \
            -Demit-lib-vt=true \
            -Demit-xcframework=false \
            -Doptimize=Debug \
            -Dsimd=true \
            -Dtarget="$target"
    )
}

run_upstream_tests() {
    cd "$REPO_DIR"
    verify_metadata
    go test -count=1 go.mitchellh.com/libghostty/...
}

run_go() {
    local mode="$1"
    prepare_apple
    local library="$NATIVE_WORK/libghostty-vt.xcframework/macos-arm64_x86_64/libghostty-vt.a"
    PKG_CONFIG_PATH="$(write_pkg_config "$library")${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
    export PKG_CONFIG_PATH

    cd "$REPO_DIR"
    case "$mode" in
        test)
            run_upstream_tests
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
            go test -run '^$' -tags=libghostty,libghostty_compare ./internal/pty \
                -bench '^BenchmarkTerminalBackends$' -benchmem -benchtime=3x -count=5
            ;;
        memory)
            local charm_test="$NATIVE_WORK/pty-charm.test"
            local ghostty_test="$NATIVE_WORK/pty-libghostty.test"
            go test -c -o "$charm_test" ./internal/pty
            go test -c -tags=libghostty,libghostty_compare \
                -o "$ghostty_test" ./internal/pty

            local backend
            for backend in charm libghostty-helper; do
                local test_binary="$charm_test"
                if [[ "$backend" == "libghostty-helper" ]]; then
                    test_binary="$ghostty_test"
                fi
                /usr/bin/time -l env GRAITH_TERMINAL_MEMORY_BACKEND="$backend" \
                    "$test_binary" -test.run '^TestTerminalBackendPeakMemoryWorkload$' -test.v
            done
            ;;
    esac
}

inspect_linkage() {
    local binary="${1:-}"
    local require_symbols="${2:-true}"
    [[ -f "$binary" ]] || die "usage: $0 inspect-linkage <binary> [true|false]"

    local build_info
    build_info="$(go version -m "$binary")"
    grep -Fq "go.mitchellh.com/libghostty" <<<"$build_info" ||
        die "candidate does not contain go-libghostty module metadata"
    grep -Fq "$GO_LIBGHOSTTY_VERSION" <<<"$build_info" ||
        die "candidate contains the wrong go-libghostty version"
    grep -Fq "$GO_LIBGHOSTTY_SUM" <<<"$build_info" ||
        die "candidate contains the wrong go-libghostty module checksum"
    grep -Fq 'tags=libghostty' <<<"$build_info" ||
        die "candidate was not built with the libghostty tag"

    local format dependencies
    format="$(file -b "$binary")"
    case "$format" in
        *Mach-O*)
            require_command otool
            dependencies="$(otool -L "$binary")"
            ;;
        *ELF*)
            require_command objdump
            dependencies="$(objdump -p "$binary" | grep -E 'NEEDED|RPATH|RUNPATH|interpreter' || true)"
            ;;
        *)
            die "unsupported executable format: $format"
            ;;
    esac
    printf 'executable: %s\ndynamic dependencies:\n%s\n' "$format" "$dependencies"
    if grep -Eiq 'libghostty|libsimdutf|libhwy|libhighway|libc\+\+|libstdc\+\+' <<<"$dependencies"; then
        die "candidate has an unexpected dynamic native dependency"
    fi

    if [[ "$require_symbols" == "true" ]]; then
        grep -Eq '[[:space:]][Tt][[:space:]]_?ghostty_terminal_new$' \
            < <(nm -g "$binary" 2>/dev/null) ||
            die "candidate does not define ghostty_terminal_new; static linkage is unproven"
    elif [[ "$require_symbols" != "false" ]]; then
        die "inspect-linkage symbol mode must be true or false"
    fi
}

verify_default_binary() {
    local binary="${1:-}"
    [[ -f "$binary" ]] || die "usage: $0 verify-default-binary <binary>"
    local build_info
    build_info="$(go version -m "$binary")"
    if grep -Fq 'go.mitchellh.com/libghostty' <<<"$build_info"; then
        die "ordinary rollback binary contains go-libghostty"
    fi
    if grep -Fq 'tags=libghostty' <<<"$build_info"; then
        die "ordinary rollback binary contains the libghostty build tag"
    fi
    if grep -Fq 'ghostty_terminal_new' < <(strings "$binary"); then
        die "ordinary rollback binary contains a native Ghostty symbol"
    fi
    printf '%s\n' "$build_info"
}

verify_binary_privacy() {
    local binary="${1:-}"
    [[ -f "$binary" ]] || die "usage: $0 verify-binary-privacy <binary>"
    if grep -Eq '/home/runner|/Users/|/private/var/folders/|/runner/work/' \
        < <(strings "$binary"); then
        die "candidate contains a local or CI build path; package a stripped binary"
    fi
}

verify_selectors() {
    cd "$REPO_DIR"
    local selected

    selected="$(CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go list -f '{{join .GoFiles "\n"}}' ./internal/pty)"
    grep -Fqx 'terminal_backend_charm.go' <<<"$selected" || die "default selector is not Charm"
    if grep -Fq 'terminal_backend_ghostty' <<<"$selected"; then
        die "ordinary no-cgo build selected a libghostty backend file"
    fi

    selected="$(CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go list -tags=libghostty -f '{{join .GoFiles "\n"}}' ./internal/pty)"
    grep -Fqx 'terminal_backend_ghostty_nocgo.go' <<<"$selected" ||
        die "explicit no-cgo build did not select the fail-closed selector"
    if grep -Eq '^terminal_backend_ghostty\.go$' <<<"$selected"; then
        die "explicit no-cgo build selected the native implementation"
    fi

    selected="$(CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go list -tags=libghostty -f '{{join .GoFiles "\n"}}' ./internal/pty)"
    grep -Fqx 'terminal_backend_ghostty.go' <<<"$selected" ||
        die "supported cgo build did not select the native implementation"

    selected="$(CGO_ENABLED=1 GOOS=freebsd GOARCH=amd64 go list -tags=libghostty -f '{{join .GoFiles "\n"}}{{join .TestGoFiles "\n"}}' ./internal/pty)"
    grep -Fqx 'terminal_backend_ghostty_unsupported.go' <<<"$selected" ||
        die "unsupported OS did not select the fail-closed selector"
    grep -Fqx 'terminal_backend_ghostty_unsupported_test.go' <<<"$selected" ||
        die "unsupported OS regression test is not selected"
    if grep -Eq '^terminal_backend_ghostty\.go$' <<<"$selected"; then
        die "unsupported OS selected the native implementation"
    fi

    CGO_ENABLED=0 go test -count=1 -tags=libghostty ./internal/pty \
        -run '^TestLibghosttyRequiresCGO$'
    CGO_ENABLED=0 go test -count=1 -tags='libghostty libghostty_test_unsupported' \
        ./internal/pty -run '^TestLibghosttyRejectsUnsupportedOS$'
}

package_candidate() {
    local binary="${1:-}"
    local destination="${2:-}"
    [[ -f "$binary" && -n "$destination" ]] ||
        die "usage: $0 package-candidate <binary> <empty-directory>"
    mkdir -p "$destination"
    local -a existing
    shopt -s nullglob dotglob
    existing=("$destination"/*)
    shopt -u nullglob dotglob
    ((${#existing[@]} == 0)) || die "candidate destination is not empty: $destination"

    verify_metadata
    verify_binary_privacy "$binary"
    cp "$binary" "$destination/gr"
    cp "$SPDX_DOCUMENT" "$NOTICE_DOCUMENT" "$destination/"
    if grep -Eq '/home/runner|/Users/|/private/var/folders/|/runner/work/' \
        "$destination/libghostty-native.spdx.json" \
        "$destination/THIRD_PARTY_NOTICES.libghostty.md"; then
        die "candidate provenance metadata contains a local or CI build path"
    fi

    local actual expected
    actual="$(find "$destination" -mindepth 1 -maxdepth 1 -type f -exec basename {} \; | sort)"
    expected="$(printf '%s\n' gr libghostty-native.spdx.json THIRD_PARTY_NOTICES.libghostty.md | sort)"
    [[ "$actual" == "$expected" ]] || die "candidate artifact contents are incomplete or unexpected"
}

install_zig() {
    local destination="${1:-}"
    [[ -n "$destination" ]] || die "usage: $0 install-zig <empty-directory>"
    [[ "$(uname -s)-$(uname -m)" == "Linux-x86_64" ]] ||
        die "install-zig currently pins the Linux x86_64 CI host distribution only"
    ensure_empty_directory "$destination"
    local archive="$NATIVE_WORK/zig-x86_64-linux-$REQUIRED_ZIG.tar.xz"
    download_checked "$ZIG_LINUX_X86_64_URL" "$ZIG_LINUX_X86_64_SHA256" "$archive"
    tar -xf "$archive" -C "$destination" --strip-components=1
    [[ "$("$destination/zig" version)" == "$REQUIRED_ZIG" ]] ||
        die "installed Zig executable has an unexpected version"
    printf '%s\n' "$destination/zig"
}

install_spdx_validator() {
    local destination="${1:-}"
    [[ -n "$destination" ]] || die "usage: $0 install-spdx-validator <empty-directory>"
    ensure_empty_directory "$destination"
    local archive="$NATIVE_WORK/tools-java-$SPDX_TOOLS_VERSION.zip"
    download_checked "$SPDX_TOOLS_URL" "$SPDX_TOOLS_SHA256" "$archive"
    unzip -q "$archive" -d "$destination"
    local jar="$destination/tools-java-$SPDX_TOOLS_VERSION-jar-with-dependencies.jar"
    [[ -f "$jar" ]] || die "SPDX tools archive did not contain the expected validator"
    printf '%s\n' "$jar"
}

validate_spdx() {
    local jar="${1:-}"
    [[ -f "$jar" ]] || die "usage: $0 validate-spdx <tools-java-jar>"
    require_command java
    local output
    output="$(java -jar "$jar" Verify "$SPDX_DOCUMENT")"
    printf '%s\n' "$output"
    grep -Fq 'This SPDX Document is valid.' <<<"$output" ||
        die "official SPDX validator did not accept $SPDX_DOCUMENT"
}

usage() {
    cat <<EOF
usage: $0 test|race|fuzz|bench|memory|all
       $0 fetch-source
       $0 prepare-apple
       $0 source-build <zig-target> <output-library>
       $0 source-test <native-zig-target>
       $0 upstream-test
       $0 verify-metadata [ghostty-source]
       $0 verify-provenance
       $0 verify-headers <ghostty-source> [xcframework]
       $0 verify-static-archive <library>
       $0 inspect-linkage <binary> [true|false]
       $0 verify-default-binary <binary>
       $0 verify-binary-privacy <binary>
       $0 verify-selectors
       $0 package-candidate <binary> <empty-directory>
       $0 install-zig <empty-directory>
       $0 install-spdx-validator <empty-directory>
       $0 validate-spdx <tools-java-jar>

test/bench/memory use the checksum-pinned universal Apple artifact.
source-build checks out Ghostty $GHOSTTY_SHA and requires Zig $REQUIRED_ZIG.
These commands produce testing candidates only; production GoReleaser builds
remain unchanged and pure Go.
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
    fetch-source)
        fetch_source
        ;;
    prepare-apple)
        prepare_apple
        ;;
    source-build)
        source_build "${2:-}" "${3:-}"
        ;;
    source-test)
        source_test "${2:-}"
        ;;
    upstream-test)
        run_upstream_tests
        ;;
    verify-metadata)
        verify_metadata "${2:-}"
        ;;
    verify-provenance)
        verify_provenance
        ;;
    verify-headers)
        verify_headers "${2:-}" "${3:-}"
        ;;
    verify-static-archive)
        verify_static_archive "${2:-}"
        ;;
    inspect-linkage)
        inspect_linkage "${2:-}" "${3:-true}"
        ;;
    verify-default-binary)
        verify_default_binary "${2:-}"
        ;;
    verify-binary-privacy)
        verify_binary_privacy "${2:-}"
        ;;
    verify-selectors)
        verify_selectors
        ;;
    package-candidate)
        package_candidate "${2:-}" "${3:-}"
        ;;
    install-zig)
        install_zig "${2:-}"
        ;;
    install-spdx-validator)
        install_spdx_validator "${2:-}"
        ;;
    validate-spdx)
        validate_spdx "${2:-}"
        ;;
    *)
        usage >&2
        exit 2
        ;;
esac
