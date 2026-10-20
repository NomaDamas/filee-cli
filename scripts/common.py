"""Shared helpers for the build scripts. Standard library only."""

import hashlib
import json
import os
import shutil
import stat
import subprocess
import sys
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
UPSTREAM_DIR = ROOT / ".upstream" / "Filee"
DATA = ROOT / "data"

RIDS = ("win-x64", "linux-x64", "linux-arm64", "osx-x64", "osx-arm64")


class BuildError(Exception):
    pass


def read_upstream() -> dict:
    """The pinned upstream release from the UPSTREAM file (key=value lines)."""
    values = {}
    for line in (ROOT / "UPSTREAM").read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            key, _, value = line.partition("=")
            values[key.strip()] = value.strip()
    for key in ("repository", "tag", "commit", "revision"):
        if not values.get(key):
            raise BuildError(f"UPSTREAM has no '{key}'")
    values["upstream_version"] = values["tag"].lstrip("v")
    values["version"] = f"{values['upstream_version']}-{values['revision']}"
    return values


def package_name(rid: str) -> str:
    return f"filee-cli-{read_upstream()['version']}-{rid}"


def archive_name(rid: str) -> str:
    return package_name(rid) + (".zip" if rid.startswith("win-") else ".tar.gz")


def run(command, cwd=None, env=None, capture=False, check=True):
    print("+ " + " ".join(str(c) for c in command), flush=True)
    result = subprocess.run([str(c) for c in command], cwd=cwd, env=env, text=True,
                            stdout=subprocess.PIPE if capture else None)
    if check and result.returncode != 0:
        raise BuildError(f"{command[0]} failed with exit code {result.returncode}")
    return result.stdout if capture else result.returncode


def sha256_file(path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def download(url: str, sha256: str, cache: Path) -> Path:
    """Downloads url into cache (named by its hash) and verifies the SHA-256."""
    sha256 = sha256.lower()
    cache.mkdir(parents=True, exist_ok=True)
    target = cache / sha256
    if target.exists() and sha256_file(target) == sha256:
        return target
    partial = cache / (sha256 + ".partial")
    print(f"get {url}", flush=True)
    request = urllib.request.Request(url, headers={"User-Agent": "filee-cli-build"})
    with urllib.request.urlopen(request, timeout=300) as response, open(partial, "wb") as stream:
        shutil.copyfileobj(response, stream)
    actual = sha256_file(partial)
    if actual != sha256:
        partial.unlink()
        raise BuildError(f"Checksum mismatch for {url}: expected {sha256}, got {actual}")
    partial.replace(target)
    return target


def rmtree(path) -> None:
    def onerror(function, name, _exc):
        os.chmod(name, stat.S_IWRITE)
        function(name)
    if os.path.lexists(path):
        shutil.rmtree(path, onerror=onerror)


def load_json(path):
    with open(path, encoding="utf-8") as stream:
        return json.load(stream)


def fail(error: Exception) -> int:
    message = f"{error}"
    print(f"::error::{message}" if os.environ.get("GITHUB_ACTIONS") else f"error: {message}", file=sys.stderr)
    return 1


def extract_tar(archive, destination) -> None:
    """Extracts a tar archive. Callers validate member names first; the 'data' filter is used where Python has it."""
    import tarfile
    with tarfile.open(archive) as tar:
        if hasattr(tarfile, "data_filter"):
            tar.extractall(destination, filter="data")
        else:
            tar.extractall(destination)
