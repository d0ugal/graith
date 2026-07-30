#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
    echo "usage: $0 <version> <linux-x64-sha256> <install-dir>" >&2
    exit 2
fi

version="$1"
checksum="$2"
dest="$3"

if [[ ! "$version" =~ ^[0-9]+[.][0-9]+[.][0-9]+$ ]]; then
    echo "error: invalid Dart Sass version: $version" >&2
    exit 1
fi

if [[ ! "$checksum" =~ ^[a-f0-9]{64}$ ]]; then
    echo "error: invalid Dart Sass checksum for $version" >&2
    exit 1
fi

tmpdir="$(mktemp -d)"
archive="$tmpdir/dart-sass.tar.gz"
extract="$tmpdir/extract"
asset="dart-sass-${version}-linux-x64.tar.gz"
url="https://github.com/sass/dart-sass/releases/download/${version}/${asset}"

curl_args=(
    --proto '=https'
    --tlsv1.2
    --fail
    --location
    --retry 4
    --retry-max-time 120
    --show-error
    --silent
)

curl "${curl_args[@]}" "$url" --output "$archive"

if command -v sha256sum >/dev/null 2>&1; then
    printf '%s  %s\n' "$checksum" "$archive" | sha256sum -c -
else
    printf '%s  %s\n' "$checksum" "$archive" | shasum -a 256 -c -
fi

mkdir -p "$extract"
tar -xzf "$archive" -C "$extract"

if [[ ! -x "$extract/dart-sass/sass" || ! -x "$extract/dart-sass/src/dart" ]]; then
    echo "error: Dart Sass archive did not contain the expected executables" >&2
    exit 1
fi

mkdir -p "$dest"
cp -R "$extract/dart-sass/." "$dest/"

"$dest/sass" --version | grep -F "$version"

if [[ -n "${GITHUB_PATH:-}" ]]; then
    printf '%s\n' "$dest" >> "$GITHUB_PATH"
fi
