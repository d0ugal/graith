#!/usr/bin/env bash
set -euo pipefail

packages="${1:-}"
repository="${2:-}"
retain_releases="${3:-}"

if [[ ! -d "$packages" || ! -d "$repository/.git" ||
    ! "$retain_releases" =~ ^[1-9][0-9]*$ || -z "${GNUPGHOME:-}" ||
    -z "${GPG_KEY_ID:-}" || -z "${GPG_PASSPHRASE:-}" ]]; then
    echo "usage: GNUPGHOME=... GPG_KEY_ID=... GPG_PASSPHRASE=... $0 <packages> <repository> <retain-releases>" >&2
    exit 2
fi

shopt -s nullglob
debs=("$packages"/*.deb)
rpms=("$packages"/*.rpm)
if [[ "${#debs[@]}" -ne 2 || "${#rpms[@]}" -ne 2 ]]; then
    echo "error: repository publication requires exactly two debs and two rpms" >&2
    exit 1
fi

mkdir -p "$repository/gpg"
gpg --armor --export "$GPG_KEY_ID" >"$repository/gpg/graith.asc"
gpg --export "$GPG_KEY_ID" >"$repository/gpg/graith.gpg"
cp "$repository/gpg/graith.gpg" "$repository/gpg/graith-archive-keyring.gpg"

rpm --import "$repository/gpg/graith.asc"
for package in "${rpms[@]}"; do
    signature="$(rpm --checksig "$package")"
    if [[ "$signature" != *"signatures OK"* ]]; then
        echo "error: repository RPM is not already signature-verified: $package" >&2
        exit 1
    fi
done

prune_dir() {
    local directory="$1"
    local extension="$2"
    local architecture versions version total
    local -a architectures=()

    [[ -d "$directory" ]] || return 0
    mapfile -t architectures < <(
        for package in "$directory"/graith_*_linux_*."$extension"; do
            [[ -e "$package" ]] || continue
            architecture="${package##*_linux_}"
            printf '%s\n' "${architecture%."$extension"}"
        done | LC_ALL=C sort -u
    )
    for architecture in "${architectures[@]}"; do
        versions="$({
            for package in "$directory"/graith_*_linux_"$architecture"."$extension"; do
                [[ -e "$package" ]] || continue
                version="${package##*/graith_}"
                printf '%s\n' "${version%%_linux_*}"
            done
        } | sort -u -V -r)"
        total="$(grep -c . <<<"$versions" || true)"
        if ((total > retain_releases)); then
            while IFS= read -r version; do
                [[ -n "$version" ]] || continue
                rm -f "$directory/graith_${version}_linux_${architecture}.${extension}"
            done < <(tail -n "+$((retain_releases + 1))" <<<"$versions")
        fi
    done
}

rpm_root="$repository/rpm"
mkdir -p "$rpm_root"
cp "${rpms[@]}" "$rpm_root/"
prune_dir "$rpm_root" rpm
rm -rf "$rpm_root/repodata"
createrepo_c "$rpm_root"
printf '%s' "$GPG_PASSPHRASE" | gpg --batch --yes --pinentry-mode loopback \
    --passphrase-fd 0 --local-user "$GPG_KEY_ID" --detach-sign --armor \
    --output "$rpm_root/repodata/repomd.xml.asc" \
    "$rpm_root/repodata/repomd.xml"

aptly_root="$(mktemp -d)"
stage="$(mktemp -d)"
trap 'rm -rf "$aptly_root" "$stage"' EXIT
cat >"$HOME/.aptly.conf" <<EOF
{"rootDir":"${aptly_root}","architectures":["amd64","arm64"]}
EOF

old_pool="$repository/deb/pool/main/g/graith"
if [[ -d "$old_pool" ]]; then
    find "$old_pool" -maxdepth 1 -name 'graith_*_linux_*.deb' \
        -exec cp -n {} "$stage/" \;
fi
cp "${debs[@]}" "$stage/"
prune_dir "$stage" deb
aptly repo create -distribution=stable -component=main graith
aptly repo add graith "$stage/"
aptly publish repo -gpg-key="$GPG_KEY_ID" -passphrase="$GPG_PASSPHRASE" \
    -batch graith
rm -rf "$repository/deb"
mkdir -p "$repository/deb"
cp -a "$aptly_root/public/." "$repository/deb/"
