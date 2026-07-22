#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
checksums="${2:-}"
output_directory="${3:-}"

if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ||
    ! -f "$checksums" || ! -d "$output_directory" ]]; then
    echo "usage: $0 <version> <checksums> <output-directory>" >&2
    exit 2
fi

# Arch's pkgver field does not permit hyphens. Keep the upstream release
# version intact for GitHub tag, filename, and checksum lookup purposes, while
# using Arch's prescribed underscore representation in package metadata.
pkgver="${version//-/_}"

checksum_for() {
    local filename="$1"
    local matches
    matches="$(awk -v filename="$filename" '$2 == filename {print $1}' "$checksums")"
    if [[ ! "$matches" =~ ^[0-9a-f]{64}$ ]]; then
        echo "error: expected exactly one SHA-256 for $filename" >&2
        return 1
    fi
    printf '%s' "$matches"
}

amd64="$(checksum_for "graith_${version}_linux_amd64.tar.gz")"
arm64="$(checksum_for "graith_${version}_linux_arm64.tar.gz")"
pkgbuild="$(mktemp "$output_directory/.PKGBUILD.XXXXXX")"
srcinfo="$(mktemp "$output_directory/..SRCINFO.XXXXXX")"
trap 'rm -f "$pkgbuild" "$srcinfo"' EXIT

cat >"$pkgbuild" <<EOF
pkgname=graith-bin
pkgver=${pkgver}
pkgrel=1
pkgdesc='Terminal session manager for AI coding agents'
arch=('x86_64' 'aarch64')
url='https://github.com/d0ugal/graith'
license=('MIT')
provides=('graith')
conflicts=('graith')
options=('!strip')
source_x86_64=("graith_${version}_linux_amd64.tar.gz::https://github.com/d0ugal/graith/releases/download/v${version}/graith_${version}_linux_amd64.tar.gz")
source_aarch64=("graith_${version}_linux_arm64.tar.gz::https://github.com/d0ugal/graith/releases/download/v${version}/graith_${version}_linux_arm64.tar.gz")
sha256sums_x86_64=('${amd64}')
sha256sums_aarch64=('${arm64}')

package() {
  install -Dm755 "\${srcdir}/gr" "\${pkgdir}/usr/bin/gr"
  install -Dm644 "\${srcdir}/LICENSE" "\${pkgdir}/usr/share/licenses/graith-bin/LICENSE"
  install -Dm644 "\${srcdir}/README.md" "\${pkgdir}/usr/share/doc/graith/README.md"
  install -Dm644 "\${srcdir}/libghostty-native.spdx.json" "\${pkgdir}/usr/share/doc/graith/libghostty-native.spdx.json"
  install -Dm644 "\${srcdir}/THIRD_PARTY_NOTICES.libghostty.md" "\${pkgdir}/usr/share/doc/graith/THIRD_PARTY_NOTICES.libghostty.md"
  install -Dm644 "\${srcdir}/completions/gr.bash" "\${pkgdir}/usr/share/bash-completion/completions/gr"
  install -Dm644 "\${srcdir}/completions/gr.zsh" "\${pkgdir}/usr/share/zsh/site-functions/_gr"
  install -Dm644 "\${srcdir}/completions/gr.fish" "\${pkgdir}/usr/share/fish/vendor_completions.d/gr.fish"
  install -dm755 "\${pkgdir}/usr/share/man/man1"
  install -m644 "\${srcdir}"/man/*.1.gz "\${pkgdir}/usr/share/man/man1/"
}
EOF

cat >"$srcinfo" <<EOF
pkgbase = graith-bin
	pkgdesc = Terminal session manager for AI coding agents
	pkgver = ${pkgver}
	pkgrel = 1
	url = https://github.com/d0ugal/graith
	arch = x86_64
	arch = aarch64
	license = MIT
	provides = graith
	conflicts = graith
	options = !strip
	source_x86_64 = graith_${version}_linux_amd64.tar.gz::https://github.com/d0ugal/graith/releases/download/v${version}/graith_${version}_linux_amd64.tar.gz
	sha256sums_x86_64 = ${amd64}
	source_aarch64 = graith_${version}_linux_arm64.tar.gz::https://github.com/d0ugal/graith/releases/download/v${version}/graith_${version}_linux_arm64.tar.gz
	sha256sums_aarch64 = ${arm64}

pkgname = graith-bin
EOF

chmod 644 "$pkgbuild" "$srcinfo"
mv "$pkgbuild" "$output_directory/PKGBUILD"
mv "$srcinfo" "$output_directory/.SRCINFO"
trap - EXIT
