"""hyctl — Python launcher for the Hydra CLI.

Pure-Python wrapper: on first run it downloads the prebuilt `hyctl` binary
matching this package's version from the GitHub release, verifies its
checksum, caches it, and then execs it. No Go toolchain required.
"""
from __future__ import annotations

import hashlib
import os
import platform
import stat
import sys
import tarfile
import tempfile
import urllib.request
import zipfile
from pathlib import Path

__version__ = "1.0.0"

REPO = "ankit373/hydra"
PROJECT = "hydra"  # goreleaser archive filename prefix
TAG = f"v{__version__}"
BASE = f"https://github.com/{REPO}/releases/download/{TAG}"

_OS = {"darwin": "darwin", "linux": "linux", "windows": "windows"}
_ARCH = {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}


def _target() -> tuple[str, str]:
    osname = _OS.get(platform.system().lower())
    arch = _ARCH.get(platform.machine().lower())
    if not osname or not arch:
        _die(f"unsupported platform {platform.system()}/{platform.machine()}")
    return osname, arch


def _die(msg: str) -> None:
    sys.stderr.write(f"\n  hyctl: {msg}\n")
    sys.stderr.write(f"  Install manually from https://github.com/{REPO}/releases/tag/{TAG}\n\n")
    raise SystemExit(1)


def _cache_dir() -> Path:
    root = os.environ.get("XDG_CACHE_HOME") or str(Path.home() / ".cache")
    d = Path(root) / "hyctl" / __version__
    d.mkdir(parents=True, exist_ok=True)
    return d


def _fetch(url: str) -> bytes:
    req = urllib.request.Request(url, headers={"User-Agent": "hyctl-pip-installer"})
    with urllib.request.urlopen(req) as resp:  # noqa: S310 (trusted GitHub host)
        return resp.read()


def _verify(archive: bytes, name: str) -> None:
    try:
        sums = _fetch(f"{BASE}/checksums.txt").decode("utf-8")
    except Exception:
        sys.stderr.write("hyctl: checksums.txt unavailable — skipping verification\n")
        return
    for line in sums.splitlines():
        if line.strip().endswith(name):
            expected = line.split()[0]
            actual = hashlib.sha256(archive).hexdigest()
            if expected != actual:
                _die(f"checksum mismatch (expected {expected}, got {actual})")
            return


def _ensure_binary() -> Path:
    osname, arch = _target()
    exe = "hyctl.exe" if osname == "windows" else "hyctl"
    dest = _cache_dir() / exe
    if dest.exists():
        return dest

    ext = "zip" if osname == "windows" else "tar.gz"
    archive_name = f"{PROJECT}_{__version__}_{osname}_{arch}.{ext}"
    sys.stderr.write(f"hyctl: downloading {archive_name} ({TAG})…\n")
    try:
        blob = _fetch(f"{BASE}/{archive_name}")
    except Exception as e:  # noqa: BLE001
        _die(f"download failed — {e}")

    _verify(blob, archive_name)

    with tempfile.TemporaryDirectory() as tmp:
        apath = Path(tmp) / archive_name
        apath.write_bytes(blob)
        if ext == "zip":
            with zipfile.ZipFile(apath) as z:
                z.extract(exe, tmp)
        else:
            with tarfile.open(apath) as t:
                t.extract(exe, tmp)  # noqa: S202 (single known member)
        (Path(tmp) / exe).replace(dest)

    if osname != "windows":
        dest.chmod(dest.stat().st_mode | stat.S_IEXEC | stat.S_IXGRP | stat.S_IXOTH)
    return dest


def main() -> None:
    binary = _ensure_binary()
    args = [str(binary), *sys.argv[1:]]
    if os.name == "nt":
        import subprocess  # noqa: PLC0415

        raise SystemExit(subprocess.run(args).returncode)
    os.execv(str(binary), args)


if __name__ == "__main__":
    main()
