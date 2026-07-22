#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
checksums="${2:-}"
output="${3:-}"

if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ||
    ! -f "$checksums" || -z "$output" ]]; then
    echo "usage: $0 <version> <checksums> <output>" >&2
    exit 2
fi

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

darwin_arm64="$(checksum_for "graith_${version}_darwin_arm64.tar.gz")"
linux_amd64="$(checksum_for "graith_${version}_linux_amd64.tar.gz")"
linux_arm64="$(checksum_for "graith_${version}_linux_arm64.tar.gz")"
temporary="$(mktemp "$(dirname "$output")/.graith-formula.XXXXXX")"
trap 'rm -f "$temporary"' EXIT

cat >"$temporary" <<EOF
class Graith < Formula
  desc "Terminal session manager for AI coding agents"
  homepage "https://github.com/d0ugal/graith"
  version "${version}"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/d0ugal/graith/releases/download/v${version}/graith_${version}_darwin_arm64.tar.gz"
      sha256 "${darwin_arm64}"
    else
      odie "graith supports only Apple Silicon on macOS"
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/d0ugal/graith/releases/download/v${version}/graith_${version}_linux_amd64.tar.gz"
      sha256 "${linux_amd64}"
    elsif Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/d0ugal/graith/releases/download/v${version}/graith_${version}_linux_arm64.tar.gz"
      sha256 "${linux_arm64}"
    else
      odie "graith supports only Linux amd64/arm64"
    end
  end

  def install
    bin.install "gr"
    if OS.mac?
      (libexec/"graith").install "GraithNotifier.app"
      (libexec/"graith").install "Graith.app"
    end
  end

  def caveats
    <<~EOS
      To restart the graith daemon after upgrading:
        gr daemon restart

      Before uninstalling on macOS, remove every registered Graith user service:
        gr daemon service remove --all-profiles
    EOS
  end

  test do
    system "#{bin}/gr", "version"
  end
end
EOF

chmod 644 "$temporary"
mv "$temporary" "$output"
trap - EXIT
