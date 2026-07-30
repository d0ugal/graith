#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
    echo "usage: $0 <version> <linux-amd64-deb-sha256>" >&2
    exit 2
fi

version="$1"
checksum="$2"

if [[ ! "$version" =~ ^[0-9]+[.][0-9]+[.][0-9]+$ ]]; then
    echo "error: invalid Hugo version: $version" >&2
    exit 1
fi

if [[ ! "$checksum" =~ ^[a-f0-9]{64}$ ]]; then
    echo "error: invalid Hugo checksum for $version" >&2
    exit 1
fi

tmpdir="$(mktemp -d)"
archive="$tmpdir/hugo.deb"
asset="hugo_extended_${version}_linux-amd64.deb"
url="https://github.com/gohugoio/hugo/releases/download/v${version}/${asset}"

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

sudo dpkg -i "$archive"
hugo version | grep -F "v${version}"
