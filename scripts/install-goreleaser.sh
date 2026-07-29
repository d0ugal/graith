#!/usr/bin/env bash
# Install an exact GoReleaser release after fail-closed checksum verification.
set -euo pipefail

version="${1:-}"
dest="${2:-}"

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
    echo "error: GoReleaser version must be exact semver with v prefix, got: ${version:-<empty>}" >&2
    exit 1
fi

if [ -z "$dest" ]; then
    echo "error: destination directory is required" >&2
    exit 1
fi

case "$(uname -s)" in
    Darwin) os="Darwin" ;;
    Linux) os="Linux" ;;
    *)
        echo "error: unsupported GoReleaser install OS: $(uname -s)" >&2
        exit 1
        ;;
esac

case "$(uname -m)" in
    x86_64 | amd64) arch="x86_64" ;;
    arm64 | aarch64) arch="arm64" ;;
    *)
        echo "error: unsupported GoReleaser install architecture: $(uname -m)" >&2
        exit 1
        ;;
esac

asset="goreleaser_${os}_${arch}.tar.gz"
base_url="https://github.com/goreleaser/goreleaser/releases/download/${version}"
tmp="$(mktemp -d)"
cleanup() {
    rm -rf "$tmp"
}
trap cleanup EXIT

archive="${tmp}/${asset}"
checksums="${tmp}/checksums.txt"
extract_dir="${tmp}/extract"
mkdir -p "$extract_dir" "$dest"

curl_args=(
    --proto '=https'
    --tlsv1.2
    --fail
    --location
    --silent
    --show-error
    --retry 4
    --retry-delay 2
    --retry-max-time 120
)

curl "${curl_args[@]}" \
    "${base_url}/${asset}" --output "$archive"
curl "${curl_args[@]}" \
    "${base_url}/checksums.txt" --output "$checksums"

expected="$(awk -v asset="$asset" '$2 == asset { print }' "$checksums")"
if [ -z "$expected" ] || [ "$(printf '%s\n' "$expected" | wc -l | tr -d '[:space:]')" != "1" ]; then
    echo "error: checksums.txt does not contain exactly one checksum for ${asset}" >&2
    exit 1
fi

case "$os" in
    Darwin)
        printf '%s\n' "$expected" | (cd "$tmp" && shasum -a 256 -c -)
        ;;
    Linux)
        printf '%s\n' "$expected" | (cd "$tmp" && sha256sum -c -)
        ;;
esac

tar -xzf "$archive" -C "$extract_dir" goreleaser
install -m 0755 "${extract_dir}/goreleaser" "${dest}/goreleaser"
"${dest}/goreleaser" --version | grep -F "${version#v}" >/dev/null

if [ -n "${GITHUB_PATH:-}" ]; then
    echo "$dest" >>"$GITHUB_PATH"
fi
