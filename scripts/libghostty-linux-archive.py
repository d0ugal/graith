#!/usr/bin/env python3
"""Create and inspect the exact Linux libghostty artifact contract."""

from __future__ import annotations

import argparse
import gzip
import io
import json
import stat
import tarfile
import tempfile
from pathlib import Path

ALLOWED = (
    "libghostty-vt.a",
    "pkgconfig/libghostty-vt-static.pc",
    "manifest.json",
    "libghostty-native.spdx.json",
    "THIRD_PARTY_NOTICES.libghostty.md",
)


def _members(archive: Path) -> list[tarfile.TarInfo]:
    with archive.open("rb") as stream, gzip.GzipFile(fileobj=stream) as compressed:
        with tarfile.open(fileobj=compressed, mode="r:") as tar:
            return tar.getmembers()


def inspect_archive(archive: Path) -> None:
    members = _members(archive)
    names = [member.name for member in members]
    if names != list(ALLOWED):
        raise SystemExit(
            "Linux artifact has unexpected or incomplete archive members: "
            + json.dumps(names)
        )
    for member in members:
        if member.pax_headers:
            raise SystemExit(f"Linux artifact member contains metadata: {member.name}")
        if not member.isreg() or member.islnk() or member.issym():
            raise SystemExit(f"Linux artifact member is not a regular file: {member.name}")


def pack(source: Path, archive: Path) -> None:
    if not source.is_dir():
        raise SystemExit(f"artifact source is not a directory: {source}")
    for name in ALLOWED:
        path = source / name
        if not path.is_file() or path.is_symlink():
            raise SystemExit(f"artifact source member is not a regular file: {name}")
    archive.parent.mkdir(parents=True, exist_ok=True)
    with archive.open("wb") as stream:
        with gzip.GzipFile(fileobj=stream, mode="wb", mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as tar:
                for name in ALLOWED:
                    data = (source / name).read_bytes()
                    info = tarfile.TarInfo(name)
                    info.size = len(data)
                    info.mode = stat.S_IFREG | 0o644
                    info.mtime = 0
                    info.uid = info.gid = 0
                    info.uname = info.gname = ""
                    tar.addfile(info, io.BytesIO(data))
    inspect_archive(archive)


def regression() -> None:
    with tempfile.TemporaryDirectory(prefix="libghostty-archive-") as directory:
        root = Path(directory)
        source = root / "source"
        source.mkdir()
        for name in ALLOWED:
            path = source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(name.encode())

        contaminated = root / "contaminated.tar.gz"
        with contaminated.open("wb") as stream, gzip.GzipFile(fileobj=stream, mode="wb", mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.PAX_FORMAT) as tar:
                for name in (
                    *ALLOWED,
                    "._libghostty-vt.a",
                    "pkgconfig/._libghostty-vt-static.pc",
                    "._manifest.json",
                    "._libghostty-native.spdx.json",
                    "._THIRD_PARTY_NOTICES.libghostty.md",
                ):
                    data = name.encode()
                    info = tarfile.TarInfo(name)
                    info.size = len(data)
                    info.pax_headers = {"SCHILY.xattr.com.apple.FinderInfo": "contaminated"}
                    tar.addfile(info, io.BytesIO(data))
        try:
            inspect_archive(contaminated)
        except SystemExit:
            pass
        else:
            raise AssertionError("contaminated archive unexpectedly passed inspection")

        corrected = root / "corrected.tar.gz"
        pack(source, corrected)
        inspect_archive(corrected)
        assert [member.name for member in _members(corrected)] == list(ALLOWED)


def main() -> None:
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)
    inspect_parser = subparsers.add_parser("inspect")
    inspect_parser.add_argument("archive", type=Path)
    pack_parser = subparsers.add_parser("pack")
    pack_parser.add_argument("source", type=Path)
    pack_parser.add_argument("archive", type=Path)
    subparsers.add_parser("test")
    args = parser.parse_args()
    if args.command == "inspect":
        inspect_archive(args.archive)
    elif args.command == "pack":
        pack(args.source, args.archive)
    else:
        regression()


if __name__ == "__main__":
    main()
