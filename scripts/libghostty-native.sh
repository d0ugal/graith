#!/usr/bin/env bash
# Build, package, and validate the non-production libghostty-vt candidate.
# All generated source, archives, caches, and binaries stay below a temporary
# work directory. Pin rotation is documented in the libghostty design record.
set -euo pipefail
export LC_ALL=C

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
readonly SPDX_NAMESPACE="https://github.com/d0ugal/graith/sbom/libghostty-native/91f66da24527fa02d92b5fd0b41cd020f553a64c/e9e1010f80b1ced0b7efcdb300f4838513c0816e"

# Graith supports the latest stable macOS release and the immediately previous
# major, currently macOS 26 and 15. The archive's macOS 13 objects are compatible
# inputs, but native candidates follow product policy and declare 15.0.
readonly DARWIN_ARCHIVE_DEPLOYMENT_TARGET="13.0"
readonly DARWIN_CANDIDATE_DEPLOYMENT_TARGET="15.0"

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

new_source_materialization() {
    local work_directory="${1:-$NATIVE_WORK}"
    [[ -d "$work_directory" && "$work_directory" != "/" ]] || {
        die "source materialization requires a safe work directory"
        return 1
    }
    mktemp -d "$work_directory/ghostty-source.XXXXXX"
}

fetch_source() {
    # Each operation gets a fresh script-owned materialization. Reusing a
    # checkout would either admit ignored build outputs or require deleting
    # arbitrary content from a caller-selected work directory.
    local source
    source="$(new_source_materialization)"
    git init -q "$source"
    git -C "$source" remote add origin "$GHOSTTY_REPO"

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
    verify_clean_checkout "$source" "Ghostty checkout"
    printf '%s\n' "$source"
}

verify_clean_checkout() {
    local repository="${1:-}"
    local label="${2:-Git checkout}"
    [[ -d "$repository" ]] || die "checkout does not exist: $repository"

    local status
    status="$(git -C "$repository" status --porcelain=v1 \
        --untracked-files=all --ignored=matching)"
    [[ -z "$status" ]] ||
        die "$label contains tracked, untracked, or ignored content"
}

test_source_cleanliness() {
    local checkout="$NATIVE_WORK/source-cleanliness-test"
    ensure_empty_directory "$checkout"
    git init -q "$checkout"
    git -C "$checkout" -c user.name=Graith -c user.email=graith@example.invalid \
        commit -q --allow-empty -m braw
    verify_clean_checkout "$checkout" "cleanliness fixture"

    printf 'dreich untracked content\n' >"$checkout/dreich"
    if verify_clean_checkout "$checkout" "cleanliness fixture" >/dev/null 2>&1; then
        die "source cleanliness check accepted untracked content"
        return 1
    fi
    printf 'dreich\n' >"$checkout/.git/info/exclude"
    if verify_clean_checkout "$checkout" "cleanliness fixture" >/dev/null 2>&1; then
        die "source cleanliness check accepted ignored content"
        return 1
    fi
    [[ "$(<"$checkout/dreich")" == "dreich untracked content" ]] || {
        die "source cleanliness check changed rejected ignored content"
        return 1
    }

    local materialization_root="$NATIVE_WORK/source-materialization-test"
    ensure_empty_directory "$materialization_root"
    local unowned="$materialization_root/ghostty"
    mkdir -p "$unowned"
    git init -q "$unowned"
    git -C "$unowned" -c user.name=Graith -c user.email=graith@example.invalid \
        commit -q --allow-empty -m bothy
    printf 'result*\n' >"$unowned/.git/info/exclude"
    printf 'thrawn developer content\n' >"$unowned/result-local"
    git -C "$unowned" status --porcelain=v1 --ignored=matching | \
        grep -Fqx '!! result-local' || {
        die "unowned materialization fixture is not ignored"
        return 1
    }
    local fresh
    fresh="$(new_source_materialization "$materialization_root")"
    [[ "$fresh" != "$unowned" && -z "$(find "$fresh" -mindepth 1 -print -quit)" ]] || {
        die "source materialization reused an unowned checkout"
        return 1
    }
    [[ "$(<"$unowned/result-local")" == "thrawn developer content" ]] || {
        die "source materialization changed an unowned checkout"
        return 1
    }
    printf 'source cleanliness checks passed\n'
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
        --arg zig_sha "$ZIG_SOURCE_SHA256" \
        --arg namespace "$SPDX_NAMESPACE" '
        def package($id): first(.packages[] | select(.SPDXID == $id));
        def has_sha($package; $sha): any($package.checksums[]?; .algorithm == "SHA256" and .checksumValue == $sha);
        def relates($from; $type; $to): any(.relationships[];
            .spdxElementId == $from and .relationshipType == $type and .relatedSpdxElement == $to);
        .spdxVersion == "SPDX-2.3" and
        .dataLicense == "CC0-1.0" and
        (.documentNamespace == $namespace) and
        (.packages | length) == 6 and
        ([.packages[] | select(.SPDXID == "SPDXRef-Package-GraithNativeCandidate")] | length) == 0 and
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
        (.relationships | length) == 6 and
        relates("SPDXRef-DOCUMENT"; "DESCRIBES"; "SPDXRef-Package-GoLibghostty") and
        relates("SPDXRef-DOCUMENT"; "DESCRIBES"; "SPDXRef-Package-Ghostty") and
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

verify_macho_archive_deployment_target() {
    local archive="$1"
    local expected_members="$2"
    require_command otool

    local load_commands build_version_count minos minos_count
    if ! load_commands="$(otool -l "$archive")"; then
        die "cannot inspect Mach-O archive deployment targets"
        return 1
    fi
    build_version_count="$(awk '
        $1 == "cmd" && $2 == "LC_BUILD_VERSION" { count++ }
        END { print count + 0 }
    ' <<<"$load_commands")"
    minos="$(awk '$1 == "minos" { print $2 }' <<<"$load_commands")"
    minos_count="$(awk 'NF { count++ } END { print count + 0 }' <<<"$minos")"

    [[ "$build_version_count" == "$expected_members" &&
        "$minos_count" == "$expected_members" ]] || {
        die "each Mach-O archive member must contain one LC_BUILD_VERSION minimum"
        return 1
    }
    if awk -v expected="$DARWIN_ARCHIVE_DEPLOYMENT_TARGET" '
        NF && $0 != expected { exit 1 }
    ' <<<"$minos"; then
        printf 'Mach-O archive members target macOS %s (compatible with candidate floor %s)\n' \
            "$DARWIN_ARCHIVE_DEPLOYMENT_TARGET" "$DARWIN_CANDIDATE_DEPLOYMENT_TARGET"
    else
        die "Mach-O archive members do not match the audited deployment target"
        return 1
    fi
    if grep -Eq 'LC_VERSION_MIN_[A-Z0-9_]+' <<<"$load_commands"; then
        die "Mach-O archive contains an unaccounted legacy deployment-target command"
        return 1
    fi
}

verify_static_archive() {
    local library="${1:-}"
    [[ -f "$library" ]] || die "usage: $0 verify-static-archive <library>"
    require_command ar
    require_command file
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
        local actual_members member symbols member_count
        actual_members="$({
            while IFS= read -r member; do
                printf '%s\n' "${member##*/}"
            done < <(ar -t "$archive")
        } | grep -v '^__.SYMDEF' | sort)"
        [[ "$actual_members" == "$expected_members" ]] || {
            printf 'expected archive members:\n%s\nactual archive members:\n%s\n' \
                "$expected_members" "$actual_members" >&2
            die "static archive contents do not match the audited dependency closure"
        }
        member_count="$(awk 'NF { count++ } END { print count + 0 }' \
            <<<"$actual_members")"
        local object_member="" object_format
        while IFS= read -r object_member; do
            [[ "${object_member##*/}" == "__.SYMDEF"* ]] || break
        done < <(ar -t "$archive")
        [[ -n "$object_member" ]] || die "static archive does not contain an object"
        object_format="$(ar -p "$archive" "$object_member" | file -b -)"

        local ghostty_pattern simdutf_pattern highway_pattern ubsan_pattern
        case "$object_format" in
            *Mach-O*)
                if ! verify_macho_archive_deployment_target \
                    "$archive" "$member_count"; then
                    return 1
                fi
                ghostty_pattern='[[:space:]][Tt][[:space:]]_ghostty_terminal_new$'
                simdutf_pattern='[[:space:]][Tt][[:space:]]__ZN7simdutf'
                highway_pattern='[[:space:]][Tt][[:space:]]__ZN3hwy'
                ubsan_pattern='[[:space:]][Tt][[:space:]]___ubsan_handle_[[:alnum:]_]+$'
                ;;
            *ELF*)
                ghostty_pattern='[[:space:]][Tt][[:space:]]ghostty_terminal_new$'
                simdutf_pattern='[[:space:]][Tt][[:space:]]_ZN7simdutf'
                highway_pattern='[[:space:]][Tt][[:space:]]_ZN3hwy'
                # Zig emits defined weak UBSan handlers in ELF compiler_rt;
                # lowercase weak/undefined symbols are intentionally rejected.
                ubsan_pattern='[[:space:]][TtW][[:space:]]__ubsan_handle_[[:alnum:]_]+$'
                ;;
            *)
                die "unsupported static archive object format: $object_format"
                ;;
        esac

        symbols="$(nm -g "$archive" 2>/dev/null)"
        grep -Eq "$ghostty_pattern" <<<"$symbols" ||
            die "static archive does not define ghostty_terminal_new"
        grep -Eq "$simdutf_pattern" <<<"$symbols" ||
            die "static archive does not contain simdutf"
        grep -Eq "$highway_pattern" <<<"$symbols" ||
            die "static archive does not contain Highway"
        grep -Eq "$ubsan_pattern" <<<"$symbols" ||
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

parse_macho_dependencies() {
    local binary="$1"
    local libraries="$2"
    local line dependency
    local saw_header=0
    local dependency_count=0

    while IFS= read -r line || [[ -n "$line" ]]; do
        if ((saw_header == 0)); then
            [[ "$line" == "$binary:" ]] || {
                die "otool -L output has an unexpected candidate header"
                return 1
            }
            saw_header=1
            continue
        fi
        if [[ ! "$line" =~ ^[[:space:]]+(.+)[[:space:]]+\(compatibility[[:space:]]version[[:space:]][0-9]+(\.[0-9]+){1,2},[[:space:]]current[[:space:]]version[[:space:]][0-9]+(\.[0-9]+){1,2}\)$ ]]; then
            die "otool -L output contains a malformed dependency record"
            return 1
        fi
        dependency="${BASH_REMATCH[1]}"
        [[ -n "$dependency" ]] || {
            die "otool -L output contains an empty dependency name"
            return 1
        }
        printf '%s\n' "$dependency"
        ((dependency_count += 1))
    done <<<"$libraries"

    ((saw_header == 1 && dependency_count > 0)) || {
        die "otool -L output does not contain dependencies"
        return 1
    }
}

parse_elf_dependencies() {
    local dynamic="$1"
    local line dependency
    local dependency_count=0

    while IFS= read -r line || [[ -n "$line" ]]; do
        [[ "$line" == *"(NEEDED)"* ]] || continue
        if [[ ! "$line" =~ ^[[:space:]]*0x[0-9a-fA-F]+[[:space:]]+\(NEEDED\)[[:space:]]+Shared[[:space:]]library:[[:space:]]\[(.*)\][[:space:]]*$ ]]; then
            die "readelf output contains a malformed NEEDED record"
            return 1
        fi
        dependency="${BASH_REMATCH[1]}"
        [[ -n "$dependency" ]] || {
            die "readelf output contains an empty NEEDED name"
            return 1
        }
        printf '%s\n' "$dependency"
        ((dependency_count += 1))
    done <<<"$dynamic"

    ((dependency_count > 0)) || {
        die "readelf output does not contain NEEDED dependencies"
        return 1
    }
}

parse_elf_interpreter() {
    local program_headers="$1"
    local line interpreter=""
    local interpreter_count=0

    while IFS= read -r line || [[ -n "$line" ]]; do
        [[ "$line" == *"Requesting program interpreter:"* ]] || continue
        if [[ ! "$line" =~ ^[[:space:]]*\[Requesting[[:space:]]program[[:space:]]interpreter:[[:space:]](.*)\][[:space:]]*$ ]]; then
            die "readelf output contains a malformed PT_INTERP record"
            return 1
        fi
        interpreter="${BASH_REMATCH[1]}"
        [[ -n "$interpreter" ]] || {
            die "readelf output contains an empty PT_INTERP value"
            return 1
        }
        ((interpreter_count += 1))
    done <<<"$program_headers"

    ((interpreter_count == 1)) || {
        die "readelf output must contain exactly one PT_INTERP value"
        return 1
    }
    printf '%s\n' "$interpreter"
}

parse_elf_forbidden_dynamic_records() {
    local dynamic="$1"
    local line
    while IFS= read -r line || [[ -n "$line" ]]; do
        if [[ "$line" =~ \((RPATH|RUNPATH|AUDIT|DEPAUDIT|FILTER|AUXILIARY)\) ]]; then
            printf '%s\n' "$line"
        fi
    done <<<"$dynamic"
}

verify_macho_load_policy() {
    local load_commands="$1"
    local expected_minos="$2"
    local line trimmed command=""
    local dyld_info_count=0
    local build_version_count=0
    local dylinker_count=0
    local dylinker_name_count=0
    local platform_count=0
    local minos_count=0
    local ntools_count=0
    local linker_tool_count=0
    local linker_version_count=0
    local forbidden_count=0

    while IFS= read -r line || [[ -n "$line" ]]; do
        trimmed="${line#"${line%%[![:space:]]*}"}"
        if [[ "$trimmed" == cmd\ * ]]; then
            if [[ ! "$trimmed" =~ ^cmd[[:space:]]+(LC_[A-Z0-9_]+)$ ]]; then
                die "otool -l output contains a malformed load-command name"
                return 1
            fi
            command="${BASH_REMATCH[1]}"
            case "$command" in
                LC_DYLD_INFO_ONLY) ((dyld_info_count += 1)) ;;
                LC_BUILD_VERSION) ((build_version_count += 1)) ;;
                LC_LOAD_DYLINKER) ((dylinker_count += 1)) ;;
                LC_DYLD_INFO|LC_DYLD_CHAINED_FIXUPS|LC_RPATH|LC_DYLD_ENVIRONMENT|LC_VERSION_MIN_*)
                    ((forbidden_count += 1))
                    ;;
            esac
            continue
        fi

        case "$trimmed" in
            name\ *)
                if [[ "$command" == "LC_LOAD_DYLINKER" ]]; then
                    if [[ ! "$trimmed" =~ ^name[[:space:]]+(.+)[[:space:]]+\(offset[[:space:]][0-9]+\)$ ]]; then
                        die "otool -l output contains a malformed LC_LOAD_DYLINKER name"
                        return 1
                    fi
                    [[ "${BASH_REMATCH[1]}" == "/usr/lib/dyld" ]] || {
                        die "Darwin candidate has an unexpected dynamic linker: ${BASH_REMATCH[1]}"
                        return 1
                    }
                    ((dylinker_name_count += 1))
                fi
                ;;
            platform\ *)
                if [[ "$command" == "LC_BUILD_VERSION" ]]; then
                    [[ "$trimmed" == "platform 1" ]] || {
                        die "Darwin candidate LC_BUILD_VERSION is not for macOS"
                        return 1
                    }
                    ((platform_count += 1))
                fi
                ;;
            minos\ *)
                [[ "$command" == "LC_BUILD_VERSION" &&
                    "$trimmed" == "minos $expected_minos" ]] || {
                    die "Darwin candidate has an unexpected minimum OS: $trimmed"
                    return 1
                }
                ((minos_count += 1))
                ;;
            ntools\ *)
                if [[ "$command" == "LC_BUILD_VERSION" ]]; then
                    [[ "$trimmed" == "ntools 1" ]] || {
                        die "Darwin candidate must record exactly one external build tool"
                        return 1
                    }
                    ((ntools_count += 1))
                fi
                ;;
            tool\ *)
                if [[ "$command" == "LC_BUILD_VERSION" ]]; then
                    [[ "$trimmed" == "tool 3" ]] || {
                        die "Darwin candidate was not stamped by the external Apple linker"
                        return 1
                    }
                    ((linker_tool_count += 1))
                fi
                ;;
            version\ *)
                if [[ "$command" == "LC_BUILD_VERSION" ]]; then
                    [[ "$trimmed" =~ ^version[[:space:]][0-9]+(\.[0-9]+){1,2}$ ]] || {
                        die "Darwin candidate has a malformed external linker version"
                        return 1
                    }
                    ((linker_version_count += 1))
                fi
                ;;
        esac
    done <<<"$load_commands"

    ((forbidden_count == 0)) || {
        die "Darwin candidate contains a forbidden loader or search-path command"
        return 1
    }
    ((dyld_info_count == 1)) || {
        die "Darwin candidate must contain exactly one LC_DYLD_INFO_ONLY command"
        return 1
    }
    ((build_version_count == 1 && platform_count == 1 && minos_count == 1)) || {
        die "Darwin candidate must contain one exact macOS LC_BUILD_VERSION command"
        return 1
    }
    ((dylinker_count == 1 && dylinker_name_count == 1)) || {
        die "Darwin candidate must use exactly one canonical LC_LOAD_DYLINKER"
        return 1
    }
    ((ntools_count == 1 && linker_tool_count == 1 && linker_version_count == 1)) || {
        die "Darwin candidate does not prove an external Apple linker invocation"
        return 1
    }
}

inspect_linkage() {
    local binary="${1:-}"
    local require_symbols="${2:-true}"
    [[ -f "$binary" ]] || die "usage: $0 inspect-linkage <binary> [true|false]"

    local build_info
    if ! build_info="$(go version -m "$binary")"; then
        die "candidate does not contain readable Go build metadata"
        return 1
    fi
    grep -Fq "go.mitchellh.com/libghostty" <<<"$build_info" || {
        die "candidate does not contain go-libghostty module metadata"
        return 1
    }
    grep -Fq "$GO_LIBGHOSTTY_VERSION" <<<"$build_info" || {
        die "candidate contains the wrong go-libghostty version"
        return 1
    }
    grep -Fq "$GO_LIBGHOSTTY_SUM" <<<"$build_info" || {
        die "candidate contains the wrong go-libghostty module checksum"
        return 1
    }
    grep -Fq 'tags=libghostty' <<<"$build_info" || {
        die "candidate was not built with the libghostty tag"
        return 1
    }
    if grep -Eq '(^|[,[:space:]])libghostty_compare($|[,[:space:]])' \
        <<<"$build_info"; then
        die "candidate was built with the test-only libghostty_compare tag"
        return 1
    fi
    if grep -Fq 'github.com/charmbracelet/x/vt' <<<"$build_info"; then
        die "production candidate contains the excluded x/vt module"
        return 1
    fi

    local cgo_enabled actual_goos actual_goarch
    if ! cgo_enabled="$(build_setting "$build_info" CGO_ENABLED)" ||
        [[ "$cgo_enabled" != "1" ]]; then
        die "native candidate build metadata does not require cgo"
        return 1
    fi
    if ! actual_goos="$(build_setting "$build_info" GOOS)" ||
        ! actual_goarch="$(build_setting "$build_info" GOARCH)"; then
        return 1
    fi

    local format dependencies interpreter forbidden_records
    format="$(file -b "$binary")"
    case "$format" in
        *Mach-O*)
            [[ "$actual_goos" == "darwin" ]] || {
                die "Mach-O candidate build metadata is not for Darwin"
                return 1
            }
            require_command otool
            local libraries load_commands
            if ! libraries="$(otool -L "$binary")" ||
                ! dependencies="$(parse_macho_dependencies "$binary" "$libraries")" ||
                ! load_commands="$(otool -l "$binary")" ||
                ! verify_macho_load_policy "$load_commands" \
                    "$DARWIN_CANDIDATE_DEPLOYMENT_TARGET"; then
                return 1
            fi
            interpreter=""
            forbidden_records=""
            if ! verify_dynamic_dependency_policy darwin "$actual_goarch" "$dependencies" \
                "$interpreter" "$forbidden_records"; then
                return 1
            fi
            ;;
        *ELF*)
            [[ "$actual_goos" == "linux" ]] || {
                die "ELF candidate build metadata is not for Linux"
                return 1
            }
            require_command readelf
            local dynamic program_headers
            dynamic="$(readelf -dW "$binary")"
            program_headers="$(readelf -lW "$binary")"
            if ! dependencies="$(parse_elf_dependencies "$dynamic")" ||
                ! interpreter="$(parse_elf_interpreter "$program_headers")"; then
                return 1
            fi
            forbidden_records="$(parse_elf_forbidden_dynamic_records "$dynamic")"
            if ! verify_dynamic_dependency_policy linux "$actual_goarch" "$dependencies" \
                "$interpreter" "$forbidden_records"; then
                return 1
            fi
            ;;
        *)
            die "unsupported executable format: $format"
            return 1
            ;;
    esac
    printf 'executable: %s\ndynamic dependencies:\n%s\n' "$format" "$dependencies"
    if [[ -n "$interpreter" ]]; then
        printf 'program interpreter: %s\n' "$interpreter"
    fi

    if [[ "$require_symbols" == "true" ]]; then
        local go_symbols
        if ! go_symbols="$(go tool nm "$binary" 2>/dev/null)"; then
            die "candidate Go symbols are unreadable"
            return 1
        fi
        if grep -Fq 'github.com/charmbracelet/x/vt' <<<"$go_symbols"; then
            die "production candidate contains excluded x/vt symbols"
            return 1
        fi
        grep -Eq '[[:space:]][Tt][[:space:]]_?ghostty_terminal_new$' \
            < <(nm -g "$binary" 2>/dev/null) || {
            die "candidate does not define ghostty_terminal_new; static linkage is unproven"
            return 1
        }
    elif [[ "$require_symbols" != "false" ]]; then
        die "inspect-linkage symbol mode must be true or false"
        return 1
    fi
}

verify_dynamic_dependency_policy() {
    local goos="${1:-}"
    local goarch="${2:-}"
    local dependencies="${3:-}"
    local interpreter="${4:-}"
    local forbidden_records="${5:-}"
    case "$goos/$goarch" in
        darwin/amd64|darwin/arm64|linux/amd64|linux/arm64) ;;
        *)
            die "unsupported dynamic dependency policy target: $goos/$goarch"
            return 1
            ;;
    esac
    [[ -z "$forbidden_records" ]] || {
        die "$goos candidate contains a forbidden runtime loader record"
        return 1
    }

    local dependency saw_system_runtime=0
    while IFS= read -r dependency; do
        [[ -n "$dependency" ]] || continue
        case "$goos:$dependency" in
            darwin:/usr/lib/libSystem.B.dylib)
                saw_system_runtime=1
                ;;
            linux:libc.so.6)
                saw_system_runtime=1
                ;;
            linux:libpthread.so.0|linux:libresolv.so.2|linux:libdl.so.2) ;;
            *)
                if [[ "$goos" == "darwin" ]] && {
                    [[ "$dependency" =~ ^/usr/lib/libresolv\.[0-9]+\.dylib$ ]] ||
                        [[ "$dependency" =~ ^/System/Library/Frameworks/CoreFoundation\.framework/Versions/[A-Za-z0-9.]+/CoreFoundation$ ]] ||
                        [[ "$dependency" =~ ^/System/Library/Frameworks/Security\.framework/Versions/[A-Za-z0-9.]+/Security$ ]]
                }; then
                    continue
                fi
                die "$goos candidate has unexpected dynamic dependency: $dependency"
                return 1
                ;;
        esac
    done <<<"$dependencies"
    ((saw_system_runtime == 1)) || {
        die "$goos candidate does not declare its expected system runtime"
        return 1
    }

    case "$goos/$goarch:$interpreter" in
        darwin/amd64:|darwin/arm64:) ;;
        linux/amd64:/lib64/ld-linux-x86-64.so.2) ;;
        linux/arm64:/lib/ld-linux-aarch64.so.1) ;;
        *)
            die "$goos/$goarch candidate has an unexpected program interpreter: $interpreter"
            return 1
            ;;
    esac
}

test_linkage_policy() {
    local darwin_dependencies linux_dependencies parsed
    darwin_dependencies="$(printf '%s\n' \
        /usr/lib/libresolv.9.dylib \
        /System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation \
        /System/Library/Frameworks/Security.framework/Versions/A/Security \
        /usr/lib/libSystem.B.dylib)"
    verify_dynamic_dependency_policy darwin arm64 "$darwin_dependencies" "" ""
    if verify_dynamic_dependency_policy darwin arm64 \
        "$darwin_dependencies"$'\n/opt/dreich/libunknown.dylib' "" "" \
        >/dev/null 2>&1; then
        die "Darwin dependency policy accepted an unknown library"
        return 1
    fi
    if verify_dynamic_dependency_policy darwin arm64 "$darwin_dependencies" "" \
        "LC_RPATH" >/dev/null 2>&1; then
        die "Darwin dependency policy accepted a runtime search path"
        return 1
    fi
    if verify_dynamic_dependency_policy darwin arm64 "$darwin_dependencies" "" \
        "LC_DYLD_CHAINED_FIXUPS" >/dev/null 2>&1; then
        die "Darwin dependency policy accepted chained fixups"
        return 1
    fi

    local macho_libraries
    macho_libraries=$'/opt/braw/gr:\n\t/usr/lib/libresolv.9.dylib (compatibility version 1.0.0, current version 1.0.0)\n\t/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation (compatibility version 150.0.0, current version 4000.1.0)\n\t/System/Library/Frameworks/Security.framework/Versions/A/Security (compatibility version 1.0.0, current version 61439.140.12)\n\t/usr/lib/libSystem.B.dylib (compatibility version 1.0.0, current version 1345.120.2)'
    parsed="$(parse_macho_dependencies /opt/braw/gr "$macho_libraries")"
    [[ "$parsed" == "$darwin_dependencies" ]] ||
        die "Mach-O dependency parser changed canonical install names"

    local macho_spoofed
    macho_spoofed=$'/opt/braw/gr:\n\t/usr/lib/libresolv.9.dylib (compatibility version 1.0.0, current version 1.0.0)\n\t/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation (compatibility version 150.0.0, current version 4000.1.0)\n\t/System/Library/Frameworks/Security.framework/Versions/A/Security (compatibility version 1.0.0, current version 61439.140.12)\n\t/usr/lib/libSystem.B.dylib evil (compatibility version 1.0.0, current version 1345.120.2)'
    parsed="$(parse_macho_dependencies /opt/braw/gr "$macho_spoofed")"
    if verify_dynamic_dependency_policy darwin arm64 "$parsed" "" "" \
        >/dev/null 2>&1; then
        die "Mach-O parser truncated an adversarial install-name suffix"
        return 1
    fi
    if parse_macho_dependencies /opt/braw/gr \
        $'/opt/braw/gr:\n\t/usr/lib/libSystem.B.dylib' >/dev/null 2>&1; then
        die "Mach-O parser accepted a malformed dependency record"
        return 1
    fi

    local macho_load_commands
    macho_load_commands=$'Load command 0\n      cmd LC_DYLD_INFO_ONLY\n  cmdsize 48\nLoad command 1\n      cmd LC_LOAD_DYLINKER\n  cmdsize 32\n     name /usr/lib/dyld (offset 12)\nLoad command 2\n      cmd LC_BUILD_VERSION\n  cmdsize 32\n platform 1\n    minos 15.0\n      sdk 26.5\n   ntools 1\n     tool 3\n  version 1267.1'
    verify_macho_load_policy "$macho_load_commands" \
        "$DARWIN_CANDIDATE_DEPLOYMENT_TARGET"
    if verify_macho_load_policy \
        "${macho_load_commands/cmd LC_DYLD_INFO_ONLY/cmd LC_UUID}" \
        "$DARWIN_CANDIDATE_DEPLOYMENT_TARGET" >/dev/null 2>&1; then
        die "Mach-O load policy accepted missing dyld-info"
        return 1
    fi
    if verify_macho_load_policy \
        "$macho_load_commands"$'\nLoad command 3\n      cmd LC_DYLD_INFO_ONLY' \
        "$DARWIN_CANDIDATE_DEPLOYMENT_TARGET" >/dev/null 2>&1; then
        die "Mach-O load policy accepted duplicate dyld-info"
        return 1
    fi
    if verify_macho_load_policy "${macho_load_commands/minos 15.0/minos 14.0}" \
        "$DARWIN_CANDIDATE_DEPLOYMENT_TARGET" >/dev/null 2>&1; then
        die "Mach-O load policy accepted the wrong deployment target"
        return 1
    fi
    if verify_macho_load_policy \
        "${macho_load_commands/\/usr\/lib\/dyld/\/usr\/lib\/dyld evil}" \
        "$DARWIN_CANDIDATE_DEPLOYMENT_TARGET" >/dev/null 2>&1; then
        die "Mach-O load policy truncated an adversarial dynamic-linker suffix"
        return 1
    fi
    if verify_macho_load_policy \
        "${macho_load_commands/$'     tool 3\n'/}" \
        "$DARWIN_CANDIDATE_DEPLOYMENT_TARGET" >/dev/null 2>&1; then
        die "Mach-O load policy accepted a binary without external-link proof"
        return 1
    fi
    local forbidden_command
    for forbidden_command in \
        LC_DYLD_INFO LC_DYLD_CHAINED_FIXUPS LC_RPATH LC_DYLD_ENVIRONMENT \
        LC_VERSION_MIN_MACOSX LC_VERSION_MIN_IPHONEOS; do
        if verify_macho_load_policy \
            "$macho_load_commands"$'\nLoad command 3\n      cmd '"$forbidden_command" \
            "$DARWIN_CANDIDATE_DEPLOYMENT_TARGET" >/dev/null 2>&1; then
            die "Mach-O load policy accepted $forbidden_command"
            return 1
        fi
    done

    linux_dependencies="$(printf '%s\n' libc.so.6 libresolv.so.2)"
    verify_dynamic_dependency_policy linux amd64 "$linux_dependencies" \
        /lib64/ld-linux-x86-64.so.2 ""
    verify_dynamic_dependency_policy linux arm64 "$linux_dependencies" \
        /lib/ld-linux-aarch64.so.1 ""
    if verify_dynamic_dependency_policy linux amd64 \
        "$linux_dependencies"$'\nlibdreich.so.1' \
        /lib64/ld-linux-x86-64.so.2 "" >/dev/null 2>&1; then
        die "Linux dependency policy accepted an unknown library"
        return 1
    fi
    if verify_dynamic_dependency_policy linux amd64 "$linux_dependencies" \
        /lib64/ld-linux-x86-64.so.2 "RUNPATH /opt/dreich" \
        >/dev/null 2>&1; then
        die "Linux dependency policy accepted a runtime search path"
        return 1
    fi

    local elf_dynamic elf_program_headers forbidden_records
    elf_dynamic=$'Dynamic section at offset 0xbraw contains 3 entries:\n 0x0000000000000001 (NEEDED)             Shared library: [libc.so.6]\n 0x0000000000000001 (NEEDED)             Shared library: [libresolv.so.2]\n 0x0000000000000000 (NULL)               0x0'
    parsed="$(parse_elf_dependencies "$elf_dynamic")"
    [[ "$parsed" == "$linux_dependencies" ]] ||
        die "ELF dependency parser changed canonical SONAMEs"
    parsed="$(parse_elf_dependencies \
        "${elf_dynamic/libc.so.6]/libc.so.6]evil]}")"
    if verify_dynamic_dependency_policy linux amd64 "$parsed" \
        /lib64/ld-linux-x86-64.so.2 "" >/dev/null 2>&1; then
        die "ELF parser truncated an adversarial SONAME suffix"
        return 1
    fi
    local elf_malformed_dynamic
    elf_malformed_dynamic=$'Dynamic section at offset 0xbraw contains 1 entry:\n 0x0000000000000001 (NEEDED)             Shared library: libc.so.6'
    if parse_elf_dependencies "$elf_malformed_dynamic" >/dev/null 2>&1; then
        die "ELF parser accepted a malformed NEEDED record"
        return 1
    fi

    elf_program_headers=$'Program Headers:\n  INTERP         0x00000000000002e0\n      [Requesting program interpreter: /lib64/ld-linux-x86-64.so.2]'
    parsed="$(parse_elf_interpreter "$elf_program_headers")"
    [[ "$parsed" == "/lib64/ld-linux-x86-64.so.2" ]] ||
        die "ELF parser changed the canonical PT_INTERP value"
    parsed="$(parse_elf_interpreter \
        "${elf_program_headers/ld-linux-x86-64.so.2]/ld-linux-x86-64.so.2]evil]}")"
    if verify_dynamic_dependency_policy linux amd64 "$linux_dependencies" \
        "$parsed" "" >/dev/null 2>&1; then
        die "ELF parser truncated an adversarial PT_INTERP suffix"
        return 1
    fi
    if verify_dynamic_dependency_policy linux amd64 "$linux_dependencies" \
        /lib/ld-linux-aarch64.so.1 "" >/dev/null 2>&1; then
        die "Linux amd64 policy accepted the arm64 program interpreter"
        return 1
    fi
    if verify_dynamic_dependency_policy linux arm64 "$linux_dependencies" \
        /lib64/ld-linux-x86-64.so.2 "" >/dev/null 2>&1; then
        die "Linux arm64 policy accepted the amd64 program interpreter"
        return 1
    fi
    local elf_malformed_program_headers
    elf_malformed_program_headers=$'Program Headers:\n  INTERP         0x00000000000002e0\n      [Requesting program interpreter: /lib64/ld-linux-x86-64.so.2'
    if parse_elf_interpreter "$elf_malformed_program_headers" >/dev/null 2>&1; then
        die "ELF parser accepted a malformed PT_INTERP record"
        return 1
    fi

    local forbidden_tag
    for forbidden_tag in RPATH RUNPATH AUDIT DEPAUDIT FILTER AUXILIARY; do
        forbidden_records="$(parse_elf_forbidden_dynamic_records \
            "$elf_dynamic"$'\n 0x000000000000001d ('"$forbidden_tag"$') loader value')"
        [[ -n "$forbidden_records" ]] ||
            die "ELF parser did not preserve $forbidden_tag"
        if verify_dynamic_dependency_policy linux amd64 "$linux_dependencies" \
            /lib64/ld-linux-x86-64.so.2 "$forbidden_records" \
            >/dev/null 2>&1; then
            die "ELF load policy accepted $forbidden_tag"
            return 1
        fi
    done
    printf 'dynamic dependency policy checks passed\n'
}

verify_candidate_for_packaging() {
    local binary="${1:-}"
    local goos="${2:-}"
    local goarch="${3:-}"
    [[ -f "$binary" ]] || {
        die "usage: verify_candidate_for_packaging <binary> <goos> <goarch>"
        return 1
    }

    if ! candidate_identity "$binary" "$goos" "$goarch" >/dev/null; then
        return 1
    fi
    if ! inspect_linkage "$binary" true; then
        return 1
    fi
    verify_binary_privacy "$binary"
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
        die "candidate contains a local or CI build path; package a trimpath binary"
    fi
}

build_setting() {
    local build_info="$1"
    local key="$2"
    local value
    if ! value="$(optional_build_setting "$build_info" "$key")"; then
        return 1
    fi
    if [[ -z "$value" ]]; then
        die "candidate build metadata does not contain $key"
        return 1
    fi
    printf '%s\n' "$value"
}

optional_build_setting() {
    local build_info="$1"
    local key="$2"
    local value
    value="$(awk -v prefix="$key=" \
        '$1 == "build" && index($2, prefix) == 1 { print substr($2, length(prefix) + 1) }' \
        <<<"$build_info")"
    if [[ "$value" == *$'\n'* ]]; then
        die "candidate build metadata contains multiple $key settings"
        return 1
    fi
    printf '%s\n' "$value"
}

candidate_git_revision() {
    local revision status
    if ! revision="$(git -C "$REPO_DIR" rev-parse --verify HEAD)" ||
        [[ ! "$revision" =~ ^[0-9a-f]{40}$ ]]; then
        die "candidate source is not at a full Git revision"
        return 1
    fi
    status="$(git -C "$REPO_DIR" status --porcelain=v1 --untracked-files=all)"
    if [[ -n "$status" ]]; then
        die "candidate source Git worktree is modified"
        return 1
    fi
    printf '%s\n' "$revision"
}

is_linked_git_worktree() {
    local repository="${1:-$REPO_DIR}"
    local git_dir common_dir
    if ! git_dir="$(git -C "$repository" rev-parse --absolute-git-dir)" ||
        ! common_dir="$(git -C "$repository" rev-parse \
            --path-format=absolute --git-common-dir)"; then
        die "cannot resolve Git directories for candidate source"
        return 1
    fi
    if ! git_dir="$(cd "$git_dir" && pwd -P)" ||
        ! common_dir="$(cd "$common_dir" && pwd -P)"; then
        die "cannot canonicalize Git directories for candidate source"
        return 1
    fi
    [[ "$git_dir" != "$common_dir" ]]
}

host_go_target() {
    case "$(uname -s)-$(uname -m)" in
        Darwin-arm64) printf 'darwin\tarm64\n' ;;
        Darwin-x86_64) printf 'darwin\tamd64\n' ;;
        Linux-x86_64) printf 'linux\tamd64\n' ;;
        Linux-aarch64) printf 'linux\tarm64\n' ;;
        *) return 1 ;;
    esac
}

verify_candidate_commit() {
    local binary="$1"
    local revision="$2"
    local goos="$3"
    local goarch="$4"
    local symbols
    if ! symbols="$(go tool nm -size -type "$binary" 2>/dev/null)"; then
        die "candidate does not retain the symbols required for provenance verification"
        return 1
    fi
    grep -Eq '[[:space:]][Dd][[:space:]]github\.com/d0ugal/graith/internal/version\.CommitSHA$' \
        <<<"$symbols" || {
        die "candidate does not define internal/version.CommitSHA"
        return 1
    }
    grep -Eq '[[:space:]][Rr][[:space:]]github\.com/d0ugal/graith/internal/version\.CommitSHA\.str$' \
        <<<"$symbols" || {
        die "candidate does not retain the injected CommitSHA data symbol"
        return 1
    }

    if ! (
        cd "$REPO_DIR"
        go run ./internal/cmd/read-go-string "$binary" \
            github.com/d0ugal/graith/internal/version.CommitSHA "$revision"
    ); then
        die "candidate CommitSHA data does not equal its build ID revision"
        return 1
    fi

    local host_target host_goos host_goarch runtime_revision
    if host_target="$(host_go_target)"; then
        IFS=$'\t' read -r host_goos host_goarch <<<"$host_target"
        if [[ "$host_goos/$host_goarch" == "$goos/$goarch" ]]; then
            if ! runtime_revision="$("$binary" --json version | jq -er '.commit')"; then
                die "candidate runtime version identity is unreadable"
                return 1
            fi
            if [[ "$runtime_revision" != "$revision" ]]; then
                die "candidate runtime CommitSHA does not match its build ID revision"
                return 1
            fi
        fi
    fi
}

candidate_identity_ldflags() {
    local goos="${1:-}"
    local goarch="${2:-}"
    local strip="${3:-true}"
    case "$goos/$goarch" in
        darwin/amd64|darwin/arm64|linux/amd64|linux/arm64) ;;
        *)
            die "unsupported candidate target: $goos/$goarch"
            return 1
            ;;
    esac
    if [[ "$strip" != "true" && "$strip" != "false" ]]; then
        die "candidate ldflags strip mode must be true or false"
        return 1
    fi

    local revision
    if ! revision="$(candidate_git_revision)"; then
        return 1
    fi
    local flags="-buildid=graith-native/$revision/$goos/$goarch -X github.com/d0ugal/graith/internal/version.CommitSHA=$revision"
    if [[ "$strip" == "true" ]]; then
        # Omit DWARF while retaining the global symbols that let the packager
        # prove static Ghostty linkage and the injected revision on exact bytes.
        flags="-w $flags"
    fi
    printf '%s\n' "$flags"
}

verify_darwin_candidate_environment() {
    local goarch="$1"
    local clang_arch
    case "$goarch" in
        amd64) clang_arch="x86_64" ;;
        arm64) clang_arch="arm64" ;;
        *)
            die "unsupported Darwin candidate architecture: $goarch"
            return 1
            ;;
    esac

    [[ "${MACOSX_DEPLOYMENT_TARGET:-}" == "$DARWIN_CANDIDATE_DEPLOYMENT_TARGET" ]] || {
        die "Darwin candidates require MACOSX_DEPLOYMENT_TARGET=$DARWIN_CANDIDATE_DEPLOYMENT_TARGET"
        return 1
    }
    local cc=" ${CC:-} "
    [[ "$cc" == *" -arch $clang_arch "* ]] || {
        die "Darwin candidate CC must select -arch $clang_arch"
        return 1
    }
    [[ "$cc" == *" -mmacosx-version-min=$DARWIN_CANDIDATE_DEPLOYMENT_TARGET "* ]] || {
        die "Darwin candidate CC must include the exact deployment target"
        return 1
    }
}

candidate_ldflags() {
    local goos="${1:-}"
    local goarch="${2:-}"
    local strip="${3:-true}"
    local flags
    if ! flags="$(candidate_identity_ldflags "$goos" "$goarch" "$strip")"; then
        return 1
    fi
    if [[ "$goos" == "darwin" ]]; then
        if ! verify_darwin_candidate_environment "$goarch"; then
            return 1
        fi
        # Force Apple ld explicitly. Without legacy dyld info, the external
        # linker encodes CommitSHA's pointer as a chained fixup rather than the
        # direct on-disk address required by the exact-byte provenance reader.
        flags="$flags -linkmode=external -extldflags=-Wl,-no_fixup_chains"
    fi
    printf '%s\n' "$flags"
}

candidate_identity() {
    local binary="$1"
    local expected_goos="$2"
    local expected_goarch="$3"
    case "$expected_goos/$expected_goarch" in
        darwin/amd64|darwin/arm64|linux/amd64|linux/arm64) ;;
        *)
            die "unsupported candidate target: $expected_goos/$expected_goarch"
            return 1
            ;;
    esac

    local build_id
    if ! build_id="$(go tool buildid "$binary")"; then
        die "candidate does not contain a readable Go build ID"
        return 1
    fi
    local build_id_pattern='^graith-native/([0-9a-f]{40})/(darwin|linux)/(amd64|arm64)$'
    if [[ ! "$build_id" =~ $build_id_pattern ]]; then
        die "candidate does not contain the required native build ID"
        return 1
    fi
    local revision="${BASH_REMATCH[1]}"
    local build_id_goos="${BASH_REMATCH[2]}"
    local build_id_goarch="${BASH_REMATCH[3]}"
    if [[ "$build_id_goos" != "$expected_goos" || "$build_id_goarch" != "$expected_goarch" ]]; then
        die "candidate build ID target is $build_id_goos/$build_id_goarch; want $expected_goos/$expected_goarch"
        return 1
    fi

    local source_revision
    if ! source_revision="$(candidate_git_revision)"; then
        return 1
    fi
    if [[ "$revision" != "$source_revision" ]]; then
        die "candidate build ID revision does not match the clean Git HEAD"
        return 1
    fi

    local build_info
    if ! build_info="$(go version -m "$binary")"; then
        die "candidate does not contain readable Go build metadata"
        return 1
    fi

    local actual_goos actual_goarch
    if ! actual_goos="$(build_setting "$build_info" GOOS)" ||
        ! actual_goarch="$(build_setting "$build_info" GOARCH)"; then
        return 1
    fi
    if [[ "$actual_goos" != "$expected_goos" || "$actual_goarch" != "$expected_goarch" ]]; then
        die "candidate target is $actual_goos/$actual_goarch; want $expected_goos/$expected_goarch"
        return 1
    fi

    if ! verify_candidate_commit "$binary" "$revision" "$actual_goos" "$actual_goarch"; then
        return 1
    fi

    local vcs vcs_revision modified
    if ! vcs="$(optional_build_setting "$build_info" vcs)" ||
        ! vcs_revision="$(optional_build_setting "$build_info" vcs.revision)" ||
        ! modified="$(optional_build_setting "$build_info" vcs.modified)"; then
        return 1
    fi
    local vcs_fields=0
    [[ -n "$vcs" ]] && ((vcs_fields += 1))
    [[ -n "$vcs_revision" ]] && ((vcs_fields += 1))
    [[ -n "$modified" ]] && ((vcs_fields += 1))
    if ((vcs_fields != 0 && vcs_fields != 3)); then
        die "candidate contains incomplete Go VCS build metadata"
        return 1
    fi
    if ((vcs_fields == 3)); then
        if [[ "$vcs" != "git" || "$vcs_revision" != "$revision" || "$modified" != "false" ]]; then
            die "candidate Go VCS metadata does not match its clean Git build ID"
            return 1
        fi
    elif [[ "${GRAITH_LIBGHOSTTY_REQUIRE_GO_VCS:-auto}" == "1" ]]; then
        die "candidate is missing required Go VCS build metadata"
        return 1
    elif [[ "${GRAITH_LIBGHOSTTY_REQUIRE_GO_VCS:-auto}" != "auto" ]]; then
        die "GRAITH_LIBGHOSTTY_REQUIRE_GO_VCS must be 1 or auto"
        return 1
    elif ! is_linked_git_worktree "$REPO_DIR"; then
        die "missing Go VCS metadata is allowed only in a linked Git worktree"
        return 1
    fi
    printf '%s\t%s\t%s\n' "$revision" "$actual_goos" "$actual_goarch"
}

test_candidate_build_identity() {
    local destination="${1:-}"
    [[ -n "$destination" ]] || {
        die "usage: $0 test-build-identity <empty-directory>"
        return 1
    }
    ensure_empty_directory "$destination"

    local host_target goos goarch
    if ! host_target="$(host_go_target)"; then
        die "unsupported build-identity test host: $(uname -s)-$(uname -m)"
        return 1
    fi
    IFS=$'\t' read -r goos goarch <<<"$host_target"

    local revision flags binary
    revision="$(candidate_git_revision)"
    # This intentionally invalid pure-Go fixture exercises identity and native
    # rejection only; external Darwin flags require cgo and belong exclusively
    # to real candidate builds.
    flags="$(candidate_identity_ldflags "$goos" "$goarch" true)"
    binary="$destination/gr"
    (
        cd "$REPO_DIR"
        CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
            go build -buildvcs=true -trimpath -ldflags="$flags" \
            -o "$binary" ./cmd/graith
    )
    candidate_identity "$binary" "$goos" "$goarch" >/dev/null
    if verify_candidate_for_packaging "$binary" "$goos" "$goarch" \
        >/dev/null 2>&1; then
        die "packaging input verification accepted a pure-Go binary"
        return 1
    fi
    local fake_validator="$destination/not-a-validator.jar"
    local pure_package="$destination/pure-package"
    printf 'braw validator sentinel\n' >"$fake_validator"
    if package_candidate "$binary" "$goos" "$goarch" "$pure_package" \
        "$fake_validator" >/dev/null 2>&1; then
        die "package-candidate accepted a pure-Go binary"
        return 1
    fi
    if [[ -n "$(find "$pure_package" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
        die "package-candidate wrote output before proving native linkage"
        return 1
    fi

    local wrong_goarch="arm64"
    if [[ "$goarch" == "arm64" ]]; then
        wrong_goarch="amd64"
    fi
    local wrong_flags="-s -w -buildid=graith-native/$revision/$goos/$wrong_goarch -X github.com/d0ugal/graith/internal/version.CommitSHA=$revision"
    local wrong_binary="$destination/gr-wrong-build-id"
    (
        cd "$REPO_DIR"
        CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
            go build -buildvcs=true -trimpath -ldflags="$wrong_flags" \
            -o "$wrong_binary" ./cmd/graith
    )
    if candidate_identity "$wrong_binary" "$goos" "$goarch" >/dev/null 2>&1; then
        die "candidate identity accepted a mismatched custom build ID"
        return 1
    fi

    local wrong_commit_binary="$destination/gr-wrong-commit"
    local wrong_commit="0000000000000000000000000000000000000000"
    local wrong_commit_flags="-w -buildid=graith-native/$revision/$goos/$goarch -X github.com/d0ugal/graith/internal/version.CommitSHA=$wrong_commit"
    (
        cd "$REPO_DIR"
        CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
            go build -buildvcs=true -trimpath -ldflags="$wrong_commit_flags" \
            -o "$wrong_commit_binary" ./cmd/graith
    )
    if candidate_identity "$wrong_commit_binary" "$goos" "$goarch" >/dev/null 2>&1; then
        die "candidate identity accepted a mismatched runtime CommitSHA"
        return 1
    fi

    local cross_binary="$destination/gr-cross-wrong-commit"
    local cross_flags="-w -buildid=graith-native/$revision/$goos/$wrong_goarch -X github.com/d0ugal/graith/internal/version.CommitSHA=$wrong_commit -X github.com/d0ugal/graith/internal/version.Version=$revision"
    (
        cd "$REPO_DIR"
        CGO_ENABLED=0 GOOS="$goos" GOARCH="$wrong_goarch" \
            go build -buildvcs=true -trimpath -ldflags="$cross_flags" \
            -o "$cross_binary" ./cmd/graith
    )
    if candidate_identity "$cross_binary" "$goos" "$wrong_goarch" \
        >/dev/null 2>&1; then
        die "candidate identity accepted a cross-target spoofed CommitSHA"
        return 1
    fi

    local template_sha hardlink output fixed_partial
    template_sha="$(sha256_value "$SPDX_DOCUMENT")"
    if materialize_candidate_spdx "$binary" "$goos" "$goarch" \
        "$REPO_DIR/./libghostty-native.spdx.json" >/dev/null 2>&1; then
        die "SPDX materialization accepted an equivalent committed-template path"
        return 1
    fi
    hardlink="$destination/committed-template-hardlink.json"
    ln "$SPDX_DOCUMENT" "$hardlink"
    if materialize_candidate_spdx "$binary" "$goos" "$goarch" "$hardlink" \
        >/dev/null 2>&1; then
        die "SPDX materialization accepted a hard link to the committed template"
        return 1
    fi
    if [[ "$(sha256_value "$SPDX_DOCUMENT")" != "$template_sha" ]]; then
        die "SPDX output safety test changed the committed dependency inventory"
        return 1
    fi

    output="$destination/candidate.spdx.json"
    fixed_partial="${output}.partial"
    printf 'braw sentinel\n' >"$fixed_partial"
    materialize_candidate_spdx "$binary" "$goos" "$goarch" "$output"
    grep -Fqx 'braw sentinel' "$fixed_partial" || {
        die "SPDX materialization reused a predictable partial path"
        return 1
    }

    local regular_repo linked_repo separate_work separate_git
    regular_repo="$destination/regular-repository"
    linked_repo="$destination/linked-worktree"
    separate_work="$destination/separate-worktree"
    separate_git="$destination/separate-git-dir"
    git init -q "$regular_repo"
    git -C "$regular_repo" -c user.name=Graith -c user.email=graith@example.invalid \
        commit -q --allow-empty -m braw
    git -C "$regular_repo" worktree add -q --detach "$linked_repo" HEAD
    git init -q --separate-git-dir="$separate_git" "$separate_work"
    if is_linked_git_worktree "$regular_repo"; then
        die "ordinary Git checkout was classified as a linked worktree"
        return 1
    fi
    is_linked_git_worktree "$linked_repo" || {
        die "linked Git worktree was not recognized"
        return 1
    }
    if is_linked_git_worktree "$separate_work"; then
        die "separate-git-dir checkout was classified as a linked worktree"
        return 1
    fi
    printf 'candidate build identity checks passed for %s/%s\n' "$goos" "$goarch"
}

materialize_candidate_spdx() {
    local binary="${1:-}"
    local goos="${2:-}"
    local goarch="${3:-}"
    local output="${4:-}"
    if [[ ! -f "$binary" || -z "$output" ]]; then
        die "usage: $0 materialize-spdx <binary> <goos> <goarch> <output>"
        return 1
    fi
    local output_parent output_name canonical_output canonical_template
    output_parent="$(dirname "$output")"
    output_name="$(basename "$output")"
    if [[ ! -d "$output_parent" ]]; then
        die "SPDX output directory does not exist: $output_parent"
        return 1
    fi
    if ! output_parent="$(cd "$output_parent" && pwd -P)"; then
        die "cannot canonicalize SPDX output directory"
        return 1
    fi
    canonical_output="$output_parent/$output_name"
    canonical_template="$(cd "$(dirname "$SPDX_DOCUMENT")" && pwd -P)/$(basename "$SPDX_DOCUMENT")"
    if [[ "$canonical_output" == "$canonical_template" ]] ||
        [[ -e "$output" && "$output" -ef "$SPDX_DOCUMENT" ]]; then
        die "refusing to overwrite the committed SPDX dependency inventory"
        return 1
    fi

    local identity revision actual_goos actual_goarch
    if ! identity="$(candidate_identity "$binary" "$goos" "$goarch")"; then
        return 1
    fi
    IFS=$'\t' read -r revision actual_goos actual_goarch <<<"$identity"

    local binary_sha namespace candidate_name candidate_purl source_info document_name
    binary_sha="$(sha256_value "$binary")"
    namespace="$SPDX_NAMESPACE/candidate/$revision/$actual_goos/$actual_goarch/$binary_sha"
    candidate_name="graith-libghostty-$actual_goos-$actual_goarch"
    candidate_purl="pkg:github/d0ugal/graith@$revision"
    source_info="Graith revision $revision; target GOOS=$actual_goos GOARCH=$actual_goarch; packaged binary SHA-256 $binary_sha."
    document_name="$candidate_name-$revision"

    local partial
    if ! partial="$(mktemp "$output_parent/.${output_name}.partial.XXXXXX")"; then
        die "cannot create a unique SPDX staging file"
        return 1
    fi
    if ! jq \
        --arg candidate_name "$candidate_name" \
        --arg candidate_purl "$candidate_purl" \
        --arg document_name "$document_name" \
        --arg namespace "$namespace" \
        --arg revision "$revision" \
        --arg source_info "$source_info" \
        --arg binary_sha "$binary_sha" '
        .name = $document_name |
        .documentNamespace = $namespace |
        .packages += [{
            "SPDXID": "SPDXRef-Package-GraithNativeCandidate",
            "checksums": [{"algorithm": "SHA256", "checksumValue": $binary_sha}],
            "copyrightText": "Copyright (c) 2025 Dougal Matthews",
            "downloadLocation": "NOASSERTION",
            "externalRefs": [{
                "referenceCategory": "PACKAGE-MANAGER",
                "referenceLocator": $candidate_purl,
                "referenceType": "purl"
            }],
            "filesAnalyzed": false,
            "licenseConcluded": "MIT",
            "licenseDeclared": "MIT",
            "name": $candidate_name,
            "packageFileName": "gr",
            "sourceInfo": $source_info,
            "supplier": "Person: Dougal Matthews",
            "versionInfo": $revision
        }] |
        .relationships = (
            [.relationships[] | select(
                .spdxElementId != "SPDXRef-DOCUMENT" or .relationshipType != "DESCRIBES"
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
            ]
        )
        ' "$SPDX_DOCUMENT" >"$partial"; then
        rm -f -- "$partial"
        die "cannot materialize candidate SPDX document"
        return 1
    fi
    if ! verify_candidate_binding "$binary" "$goos" "$goarch" "$partial"; then
        rm -f -- "$partial"
        return 1
    fi
    mv "$partial" "$canonical_output"
}

verify_candidate_binding() {
    local binary="${1:-}"
    local goos="${2:-}"
    local goarch="${3:-}"
    local document="${4:-}"
    if [[ ! -f "$binary" || ! -f "$document" ]]; then
        die "usage: $0 verify-candidate-binding <binary> <goos> <goarch> <spdx>"
        return 1
    fi

    local identity revision actual_goos actual_goarch
    if ! identity="$(candidate_identity "$binary" "$goos" "$goarch")"; then
        return 1
    fi
    IFS=$'\t' read -r revision actual_goos actual_goarch <<<"$identity"

    local binary_sha namespace candidate_name candidate_purl source_info document_name
    binary_sha="$(sha256_value "$binary")"
    namespace="$SPDX_NAMESPACE/candidate/$revision/$actual_goos/$actual_goarch/$binary_sha"
    candidate_name="graith-libghostty-$actual_goos-$actual_goarch"
    candidate_purl="pkg:github/d0ugal/graith@$revision"
    source_info="Graith revision $revision; target GOOS=$actual_goos GOARCH=$actual_goarch; packaged binary SHA-256 $binary_sha."
    document_name="$candidate_name-$revision"

    if ! jq -e \
        --arg binary_sha "$binary_sha" \
        --arg candidate_name "$candidate_name" \
        --arg candidate_purl "$candidate_purl" \
        --arg document_name "$document_name" \
        --arg namespace "$namespace" \
        --arg revision "$revision" \
        --arg source_info "$source_info" '
        def package($id): first(.packages[] | select(.SPDXID == $id));
        def relates($from; $type; $to): any(.relationships[];
            .spdxElementId == $from and .relationshipType == $type and .relatedSpdxElement == $to);
        .spdxVersion == "SPDX-2.3" and
        .dataLicense == "CC0-1.0" and
        .name == $document_name and
        .documentNamespace == $namespace and
        (.packages | length) == 7 and
        ([.packages[] | select(.SPDXID == "SPDXRef-Package-GraithNativeCandidate")] | length) == 1 and
        (package("SPDXRef-Package-GraithNativeCandidate").name == $candidate_name) and
        (package("SPDXRef-Package-GraithNativeCandidate").versionInfo == $revision) and
        (package("SPDXRef-Package-GraithNativeCandidate").packageFileName == "gr") and
        (package("SPDXRef-Package-GraithNativeCandidate").sourceInfo == $source_info) and
        (package("SPDXRef-Package-GraithNativeCandidate").filesAnalyzed == false) and
        (package("SPDXRef-Package-GraithNativeCandidate").licenseConcluded == "MIT") and
        (package("SPDXRef-Package-GraithNativeCandidate").checksums ==
            [{"algorithm": "SHA256", "checksumValue": $binary_sha}]) and
        (package("SPDXRef-Package-GraithNativeCandidate").externalRefs == [{
            "referenceCategory": "PACKAGE-MANAGER",
            "referenceLocator": $candidate_purl,
            "referenceType": "purl"
        }]) and
        (.relationships | length) == 7 and
        relates("SPDXRef-DOCUMENT"; "DESCRIBES"; "SPDXRef-Package-GraithNativeCandidate") and
        relates("SPDXRef-Package-GraithNativeCandidate"; "STATIC_LINK"; "SPDXRef-Package-GoLibghostty") and
        relates("SPDXRef-Package-GraithNativeCandidate"; "STATIC_LINK"; "SPDXRef-Package-Ghostty") and
        relates("SPDXRef-Package-Ghostty"; "STATIC_LINK"; "SPDXRef-Package-Uucode") and
        relates("SPDXRef-Package-Ghostty"; "STATIC_LINK"; "SPDXRef-Package-Highway") and
        relates("SPDXRef-Package-Ghostty"; "STATIC_LINK"; "SPDXRef-Package-Simdutf") and
        relates("SPDXRef-Package-Ghostty"; "STATIC_LINK"; "SPDXRef-Package-ZigRuntime")
        ' "$document" >/dev/null; then
        die "candidate SPDX document is not bound to the binary and target"
        return 1
    fi
}

test_candidate_binding() {
    local binary="$1"
    local goos="$2"
    local goarch="$3"
    local document="$4"
    verify_candidate_binding "$binary" "$goos" "$goarch" "$document"

    local tampered="$NATIVE_WORK/binding-tampered-$goos-$goarch"
    cp "$binary" "$tampered"
    printf '\0' >>"$tampered"
    if verify_candidate_binding "$tampered" "$goos" "$goarch" "$document" \
        >/dev/null 2>&1; then
        die "candidate binding accepted changed binary bytes"
        return 1
    fi

    local wrong_goarch="arm64"
    if [[ "$goarch" == "arm64" ]]; then
        wrong_goarch="amd64"
    fi
    if verify_candidate_binding "$binary" "$goos" "$wrong_goarch" "$document" \
        >/dev/null 2>&1; then
        die "candidate binding accepted the wrong target"
        return 1
    fi
    rm -f -- "$tampered"
    printf 'candidate binding checks passed for %s/%s\n' "$goos" "$goarch"
}

verify_selectors() {
    cd "$REPO_DIR"
    require_command jq
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
    grep -Fqx 'terminal_backend_ghostty_unsupported_error.go' <<<"$selected" ||
        die "unsupported OS did not select the shared fail-closed error helper"
    grep -Fqx 'terminal_backend_ghostty_unsupported_test.go' <<<"$selected" ||
        die "unsupported OS regression test is not selected"
    if grep -Eq '^terminal_backend_ghostty\.go$' <<<"$selected"; then
        die "unsupported OS selected the native implementation"
    fi

    local test_output
    test_output="$(CGO_ENABLED=0 go test -json -count=1 -tags=libghostty \
        ./internal/pty -run '^TestLibghosttyBackendRequiresCGO$')"
    jq -e 'select(.Action == "pass" and .Test == "TestLibghosttyBackendRequiresCGO")' \
        <<<"$test_output" >/dev/null || die "requires-cgo regression test did not execute and pass"

    test_output="$(CGO_ENABLED=0 go test -json -count=1 -tags=libghostty \
        ./internal/pty -run '^TestLibghosttyRejectsUnsupportedOS$')"
    jq -e 'select(.Action == "pass" and .Test == "TestLibghosttyRejectsUnsupportedOS")' \
        <<<"$test_output" >/dev/null || die "unsupported-OS regression test did not execute and pass"
}

package_candidate() {
    local binary="${1:-}"
    local goos="${2:-}"
    local goarch="${3:-}"
    local destination="${4:-}"
    local spdx_jar="${5:-}"
    [[ -f "$binary" && -n "$destination" && -f "$spdx_jar" ]] || {
        die "usage: $0 package-candidate <binary> <goos> <goarch> <empty-directory> <spdx-jar>"
        return 1
    }
    mkdir -p "$destination"
    local -a existing
    shopt -s nullglob dotglob
    existing=("$destination"/*)
    shopt -u nullglob dotglob
    ((${#existing[@]} == 0)) || {
        die "candidate destination is not empty: $destination"
        return 1
    }

    if ! verify_candidate_for_packaging "$binary" "$goos" "$goarch"; then
        return 1
    fi
    if ! verify_metadata; then
        return 1
    fi
    cp "$binary" "$destination/gr"
    cp "$NOTICE_DOCUMENT" "$destination/"
    if ! verify_candidate_for_packaging "$destination/gr" "$goos" "$goarch"; then
        return 1
    fi
    if ! materialize_candidate_spdx "$destination/gr" "$goos" "$goarch" \
        "$destination/libghostty-native.spdx.json"; then
        return 1
    fi
    if ! test_candidate_binding "$destination/gr" "$goos" "$goarch" \
        "$destination/libghostty-native.spdx.json"; then
        return 1
    fi
    if ! validate_spdx "$spdx_jar" \
        "$destination/libghostty-native.spdx.json"; then
        return 1
    fi
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
    local document="${2:-$SPDX_DOCUMENT}"
    [[ -f "$jar" && -f "$document" ]] ||
        die "usage: $0 validate-spdx <tools-java-jar> [spdx-document]"
    require_command java
    local output
    output="$(java -jar "$jar" Verify "$document")"
    printf '%s\n' "$output"
    grep -Fq 'This SPDX Document is valid.' <<<"$output" ||
        die "official SPDX validator did not accept $document"
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
       $0 candidate-ldflags <goos> <goarch> <true|false>
       $0 test-linkage-policy
       $0 test-source-cleanliness
       $0 test-build-identity <empty-directory>
       $0 materialize-spdx <binary> <goos> <goarch> <output>
       $0 verify-candidate-binding <binary> <goos> <goarch> <spdx>
       $0 verify-selectors
       $0 package-candidate <binary> <goos> <goarch> <empty-directory> <spdx-jar>
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
    candidate-ldflags)
        candidate_ldflags "${2:-}" "${3:-}" "${4:-true}"
        ;;
    test-linkage-policy)
        test_linkage_policy
        ;;
    test-source-cleanliness)
        test_source_cleanliness
        ;;
    test-build-identity)
        test_candidate_build_identity "${2:-}"
        ;;
    materialize-spdx)
        materialize_candidate_spdx "${2:-}" "${3:-}" "${4:-}" "${5:-}"
        ;;
    verify-candidate-binding)
        verify_candidate_binding "${2:-}" "${3:-}" "${4:-}" "${5:-}"
        ;;
    verify-selectors)
        verify_selectors
        ;;
    package-candidate)
        package_candidate "${2:-}" "${3:-}" "${4:-}" "${5:-}" "${6:-}"
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
